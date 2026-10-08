"""Muse failure e2e: hook observation persisted, receiver accepted, controller acked.

Real squad binary plus isolated temp ledger only; no live state.
"""
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import time
import unittest

ROOT = Path(__file__).parents[1]


class MuseFailureE2ETests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build = tempfile.TemporaryDirectory()
        cls.binary = Path(cls.build.name) / 'squad'
        subprocess.run(['go', 'build', '-o', str(cls.binary), './cmd/squad'],
                       cwd=ROOT.parents[1], check=True, capture_output=True)

    @classmethod
    def tearDownClass(cls):
        cls.build.cleanup()

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.env = {k: v for k, v in os.environ.items() if k not in (
            'CODEX_THREAD_ID', 'CODEX_SESSION_ID', 'CLAUDE_SESSION_ID', 'MUSE_SESSION_ID', 'SQUAD_NATIVE_SESSION_ID')}
        self.env.update(SQUAD_HOME=str(self.root / 'home'), SQUAD_NO_AUTO_DAEMON='1',
                        SQUAD_NO_BROWSER='1', SQUAD_NO_HYGIENE='1')
        subprocess.run(['git', 'init', str(self.root)], check=True, capture_output=True)
        self.squad('dispatcher', 'init', '--yes')
        for actor in ('dispatcher', 'worker'):
            self.squad(actor, 'register', '--as', actor)
        settings = self.root / '.squad/config.yaml'
        settings.write_text(settings.read_text().replace('default_worktree_per_claim: true',
                                                         'default_worktree_per_claim: false'))
        self.squad('dispatcher', 'new', 'BUG', 'Isolated e2e failure fixture', '--ready')
        self.squad('dispatcher', 'dispatch', 'reserve', 'D-E2E', '--source', 'github:example/repo#1')
        self.squad('dispatcher', 'dispatch', 'attach', 'D-E2E', '--item', 'BUG-001', '--generation', '1')
        self.squad('dispatcher', 'dispatch', 'controller-bind', '--native-session', 'dispatcher-native')
        self.squad('dispatcher', 'dispatch', 'receiver-bind', '--native-session', 'dispatcher-native',
                   '--epoch', '1', '--incarnation', 'e2e-recv', '--owner-pid', str(os.getpid()),
                   '--wake-kind', 'asyncRewake')
        # Qualification runs supervised; unattended bind is receiver-gated.
        self.squad('dispatcher', 'dispatch', 'bind', 'D-E2E', '--thread-id', 'e2e-native', '--generation', '1', '--supervised')
        self.squad('worker', 'claim', 'BUG-001', '--long')
        self.config = self.root / 'hook.json'
        self.config.write_text(json.dumps({
            'native_session_id': 'e2e-native', 'agent_id': 'worker',
            'reservation': 'D-E2E', 'generation': 1, 'item': 'BUG-001',
            'controller_agent_id': 'dispatcher',
            'state_directory': str(self.root / 'hook-state'),
            'ledger_directory': str(self.root),
            'squad_executable': str(self.binary)}))

    def squad(self, actor, *args):
        env = dict(self.env, SQUAD_AGENT=actor, SQUAD_SESSION_ID='claude:' + actor)
        result = subprocess.run([str(self.binary), *args], cwd=self.root, env=env,
                                capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout

    def db(self):
        return sqlite3.connect(self.root / 'home/global.db')

    def test_failure_hook_persists_receiver_accepts_controller_acks(self):
        event = {'hook_event_name': 'PostLLMCall', 'session_id': 'e2e-native',
                 'turn_id': 't1', 'status': 'failed', 'attempt': 10,
                 'provider': 'meta', 'request_id': 'req-e2e',
                 'error': 'API error 503: x (after 10 provider attempts)'}
        start = time.monotonic()
        hook = subprocess.run([sys.executable, str(ROOT / 'muse_failure_hook.py'),
                               '--config', str(self.config), '--event', json.dumps(event)],
                              env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(hook.returncode, 0, hook.stderr)
        self.assertLess(time.monotonic() - start, 10)
        with self.db() as db:
            row = db.execute("SELECT recipient,event_id,item_id,outcome_id,source_message_id,delivered_at,processed_at"
                             " FROM terminal_event_receipts WHERE kind='runtime-failure'").fetchone()
        self.assertIsNotNone(row)
        recipient, event_id, item, outcome, source, delivered, processed = row
        self.assertEqual(recipient, 'dispatcher')
        self.assertEqual(item, 'BUG-001')
        self.assertEqual(delivered, 0)
        self.assertEqual(processed, 0)
        with self.db() as db:
            body = db.execute('SELECT body FROM messages WHERE id=?', (outcome,)).fetchone()[0]
        self.assertEqual(body, 'runtime-failure ep-1 exhausted turn=t1 request=req-e2e attempt=10 provider=meta')
        self.assertNotIn('503', body)
        self.assertNotIn('provider attempts', body)
        self.assertNotIn('API error', body)

        out = self.squad('dispatcher', 'terminal-events', 'listen', '--delivery-session', 'e2e-recv',
                         '--native-session', 'dispatcher-native', '--max', '5s')
        receipt = json.loads(out)
        self.assertEqual(receipt['type'], 'worker-terminal-delivery-v1')
        self.assertEqual(receipt['recipient'], 'dispatcher')
        self.assertEqual([e['event_id'] for e in receipt['events']], [event_id])
        self.assertEqual(receipt['events'][0]['kind'], 'runtime-failure')
        with self.db() as db:
            delivered, processed = db.execute(
                'SELECT delivered_at,processed_at FROM terminal_event_receipts WHERE event_id=?',
                (event_id,)).fetchone()
        self.assertGreater(delivered, 0)
        self.assertEqual(processed, 0)

        self.squad('dispatcher', 'terminal-events', 'delivered', event_id,
                   '--delivery-session', 'e2e-recv', '--native-session', 'dispatcher-native')
        self.squad('dispatcher', 'dispatch', 'receiver-release', '--native-session', 'dispatcher-native',
                   '--epoch', '1', '--incarnation', 'e2e-recv')
        self.squad('dispatcher', 'dispatch', 'receiver-bind', '--native-session', 'dispatcher-native',
                   '--epoch', '1', '--incarnation', 'e2e-recv2')
        out = self.squad('dispatcher', 'terminal-events', 'listen', '--delivery-session', 'e2e-recv2',
                         '--native-session', 'dispatcher-native', '--max', '5s')
        self.assertEqual([e['event_id'] for e in json.loads(out)['events']], [event_id])

        self.squad('dispatcher', 'terminal-events', 'ack', event_id,
                   '--note', 'e2e reconciled runtime-failure', '--native-session', 'dispatcher-native')
        with self.db() as db:
            processed, note = db.execute(
                'SELECT processed_at,processed_note FROM terminal_event_receipts WHERE event_id=?',
                (event_id,)).fetchone()
        self.assertGreater(processed, 0)
        self.assertEqual(note, 'e2e reconciled runtime-failure')


if __name__ == '__main__':
    unittest.main()
