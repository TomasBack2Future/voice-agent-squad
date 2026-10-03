"""Real CLI/SQLite controller handoff and Claude receiver lifecycle, no live state."""
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock
sys.path.insert(0, str(Path(__file__).parents[1]))
import terminal_receiver

ROOT = Path(__file__).parents[1]


class ReceiverLedgerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build = tempfile.TemporaryDirectory()
        cls.binary = Path(cls.build.name) / 'squad'
        subprocess.run(['go', 'build', '-o', str(cls.binary), './cmd/squad'], cwd=ROOT.parents[1], check=True, capture_output=True)

    @classmethod
    def tearDownClass(cls):
        cls.build.cleanup()

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.env = {k: v for k, v in os.environ.items() if k not in (
            'CODEX_THREAD_ID', 'CODEX_SESSION_ID', 'CLAUDE_SESSION_ID', 'MUSE_SESSION_ID', 'SQUAD_NATIVE_SESSION_ID')}
        self.env.update(SQUAD_HOME=str(self.root / 'home'), SQUAD_NO_AUTO_DAEMON='1', SQUAD_NO_BROWSER='1', SQUAD_NO_HYGIENE='1')
        subprocess.run(['git', 'init', str(self.root)], check=True, capture_output=True)
        self.squad('old', 'init', '--yes')
        for actor in ('old', 'new'):
            self.squad(actor, 'register', '--as', actor)
        self.squad('old', 'dispatch', 'reserve', 'D-1', '--source', 'github:example/repo#1')
        self.squad('old', 'dispatch', 'controller-bind', '--native-session', 'old-native')
        rows = json.loads(self.squad('old', 'dispatch', 'list', '--json'))
        request = self.root / 'handoff.json'
        request.write_text(json.dumps({'request_id': 'handoff', 'expected_epoch': 1, 'old_native': 'old-native',
                                      'new_actor': 'new', 'new_native': 'new-native', 'reservations': rows}))
        self.squad('old', 'dispatch', 'handoff', '--request', str(request))
        self.config = self.root / 'receiver.json'
        self.c = {'native_session_id': 'new-native', 'agent_id': 'new', 'role': 'dispatcher',
                  'controller_epoch': 2, 'state_directory': str(self.root / 'receiver-state'),
                  'ledger_directory': str(self.root), 'squad_executable': str(self.binary),
                  'incarnation': 'first', 'owner_pid': os.getpid(), 'max_seconds': 20}
        self.config.write_text(json.dumps(self.c))

    def squad(self, actor, *args):
        env = dict(self.env, SQUAD_AGENT=actor, SQUAD_SESSION_ID='claude:' + actor)
        result = subprocess.run([str(self.binary), *args], cwd=self.root, env=env, capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout

    def receiver(self):
        p = subprocess.Popen([sys.executable, str(ROOT / 'terminal_receiver.py'), '--config', str(self.config)],
                             env=self.env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        p.stdin.write(json.dumps({'session_id': self.c['native_session_id']})); p.stdin.close(); p.stdin = None
        def cleanup():
            if p.poll() is None:
                p.kill()
            p.communicate(timeout=5)
        self.addCleanup(cleanup)
        return p

    def rows(self):
        with sqlite3.connect(self.root / 'home/global.db') as db:
            return db.execute('SELECT native_session,epoch,incarnation FROM dispatch_controller_receivers').fetchall()

    def wait_bound(self, p, incarnation):
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline and p.poll() is None:
            if self.rows() == [('new-native', 2, incarnation)]: return
            time.sleep(.05)
        self.fail('receiver failed to bind exact handed-off native/epoch: ' + str(self.rows()))

    def test_handoff_receiver_binds_and_releases_on_replacement(self):
        first = self.receiver(); self.wait_bound(first, 'first')
        self.c['incarnation'] = 'second'; self.config.write_text(json.dumps(self.c))
        first.communicate(timeout=5)
        self.assertEqual(first.returncode, 0)
        self.assertEqual(self.rows(), [])
        second = self.receiver(); self.wait_bound(second, 'second')
        self.c['incarnation'] = 'third'; self.config.write_text(json.dumps(self.c))
        second.communicate(timeout=5)
        self.assertEqual(self.rows(), [])

    def test_lost_bind_response_is_recovered_from_owned_intent(self):
        state = Path(self.c['state_directory']); state.mkdir()
        env = dict(self.env, SQUAD_AGENT='new', SQUAD_SESSION_ID='claude:new-native')
        run = subprocess.run
        def lost_response(argv, **kwargs):
            result = run(argv, **kwargs)
            if 'receiver-bind' in argv:
                raise subprocess.TimeoutExpired(argv, 10)
            return result
        with mock.patch.object(terminal_receiver.subprocess, 'run', side_effect=lost_response):
            with self.assertRaises(subprocess.TimeoutExpired):
                with terminal_receiver.controller_receiver(self.c, env, state): pass
        self.assertEqual(self.rows(), [('new-native', 2, 'first')])
        self.c['incarnation'] = 'second'; self.config.write_text(json.dumps(self.c))
        second = self.receiver(); self.wait_bound(second, 'second')
        self.c['incarnation'] = 'third'; self.config.write_text(json.dumps(self.c))
        second.communicate(timeout=5)
        self.assertEqual(self.rows(), [])

    def test_delivery_after_handoff_is_not_automatic_handling(self):
        settings = self.root / '.squad/config.yaml'
        settings.write_text(settings.read_text().replace('default_worktree_per_claim: true', 'default_worktree_per_claim: false'))
        self.squad('new', 'new', 'BUG', 'Isolated delivery fixture', '--ready')
        self.squad('new', 'dispatch', 'attach', 'D-1', '--item', 'BUG-001', '--generation', '1')
        self.squad('new', 'dispatch', 'bind', 'D-1', '--thread-id', 'worker-native', '--generation', '1')
        self.squad('worker', 'register', '--as', 'worker')
        self.squad('worker', 'claim', 'BUG-001', '--long')
        self.squad('worker', 'milestone', '--to', 'BUG-001', 'Isolated blocker evidence')
        with sqlite3.connect(self.root / 'home/global.db') as db:
            outcome = db.execute("SELECT max(id) FROM messages WHERE agent_id='worker' AND thread='BUG-001'").fetchone()[0]
        published = json.loads(self.squad('worker', 'terminal-events', 'publish', '--reservation', 'D-1',
                    '--generation', '1', '--worker-session', 'worker-native', '--kind', 'blocked', '--outcome', str(outcome)))
        p = self.receiver(); out, err = p.communicate(timeout=10)
        self.assertEqual(p.returncode, 2, err)
        self.assertIn(published['event_id'], err)
        self.assertIn('Invoke $squad-dispatcher', err)
        self.assertEqual(self.rows(), [])
        with sqlite3.connect(self.root / 'home/global.db') as db:
            delivered, processed = db.execute('SELECT delivered_at,processed_at FROM terminal_event_receipts WHERE event_id=?', (published['event_id'],)).fetchone()
        self.assertGreater(delivered, 0)
        self.assertEqual(processed, 0)

    def test_wrong_epoch_and_foreign_receiver_cannot_be_replaced(self):
        self.c['controller_epoch'] = 1; self.config.write_text(json.dumps(self.c))
        p = self.receiver(); p.communicate(timeout=5)
        self.assertEqual(p.returncode, 2); self.assertEqual(self.rows(), [])
        self.c['controller_epoch'] = 2; self.config.write_text(json.dumps(self.c))
        self.squad('new', 'dispatch', 'receiver-bind', '--native-session', 'new-native', '--epoch', '2', '--incarnation', 'foreign')
        p = self.receiver(); p.communicate(timeout=5)
        self.assertEqual(p.returncode, 2)
        self.assertEqual(self.rows(), [('new-native', 2, 'foreign')])

if __name__ == '__main__':
    unittest.main()
