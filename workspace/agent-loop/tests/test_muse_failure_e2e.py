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

        # D84-5: same-episode retry dedupes to the same IDs via the
        # stable request key; the next outage after healthy progress
        # records a new event under a new key.
        hook = subprocess.run([sys.executable, str(ROOT / 'muse_failure_hook.py'),
                               '--config', str(self.config), '--event', json.dumps(event)],
                              env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(hook.returncode, 0, hook.stderr)
        with self.db() as db:
            count = db.execute("SELECT count(*) FROM terminal_event_receipts WHERE kind='runtime-failure'").fetchone()[0]
        self.assertEqual(count, 1)
        healthy = dict(event, turn_id='t2', status='completed', error='')
        hook = subprocess.run([sys.executable, str(ROOT / 'muse_failure_hook.py'),
                               '--config', str(self.config), '--event', json.dumps(healthy)],
                              env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(hook.returncode, 0, hook.stderr)
        second = dict(event, turn_id='t3', request_id='req-e2e-2')
        hook = subprocess.run([sys.executable, str(ROOT / 'muse_failure_hook.py'),
                               '--config', str(self.config), '--event', json.dumps(second)],
                              env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(hook.returncode, 0, hook.stderr)
        with self.db() as db:
            rows = db.execute("SELECT event_id FROM terminal_event_receipts WHERE kind='runtime-failure' ORDER BY outcome_id").fetchall()
        self.assertEqual(len(rows), 2)
        self.assertTrue(rows[0][0].endswith('/ep-1'))
        self.assertTrue(rows[1][0].endswith('/ep-2'))

        self.squad('dispatcher', 'terminal-events', 'ack', event_id,
                   '--note', 'e2e reconciled runtime-failure', '--native-session', 'dispatcher-native')
        with self.db() as db:
            processed, note = db.execute(
                'SELECT processed_at,processed_note FROM terminal_event_receipts WHERE event_id=?',
                (event_id,)).fetchone()
        self.assertGreater(processed, 0)
        self.assertEqual(note, 'e2e reconciled runtime-failure')


    def test_lost_reply_second_failure_healthy_flush_new_outage(self):
        """D84-6 full sequence on the real backend.

        1. First failure submits ep-1 (turn=t1, request=req-1).
        2. Simulate a lost reply: force the local episode back to pending
           while the server row stays committed.
        3. A second failure in the SAME outage (different turn/request)
           must NOT rewrite the ep-1 payload: the replay resends the
           identical body and reconciles to the stored IDs.
        4. Healthy progress flushes/closes the episode.
        5. A new independent outage records under a NEW key with a fresh
           budget.
        """
        sys.path.insert(0, str(ROOT))
        import muse_failure_hook as hook
        first = {'hook_event_name': 'PostLLMCall', 'session_id': 'e2e-native',
                 'turn_id': 't1', 'status': 'failed', 'attempt': 10,
                 'provider': 'meta', 'request_id': 'req-1',
                 'error': 'API error 503: x (after 10 provider attempts)'}
        run = subprocess.run([sys.executable, str(ROOT / 'muse_failure_hook.py'),
                              '--config', str(self.config), '--event', json.dumps(first)],
                             env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(run.returncode, 0, run.stderr)
        with self.db() as db:
            committed = db.execute(
                "SELECT event_id,outcome_id FROM terminal_event_receipts WHERE kind='runtime-failure'").fetchone()
        self.assertIsNotNone(committed)
        event_id, outcome = committed
        # Lost reply: server committed, local receipt lost -> pending.
        # Faithful corruption: rebuild the exact pre-reply local state —
        # frozen payload present, open marker absent, sequence still 0
        # (nothing confirmed yet). Only the reply is lost.
        cfg = json.loads(self.config.read_text())
        obs = hook.observe(first, cfg)
        obs['episode_id'] = 'ep-1'
        hook._store_pending(cfg, obs)
        state_dir = Path(cfg['state_directory'])
        snaps = json.loads((state_dir / 'failure-episodes.json').read_text())
        snaps['D-E2E|1|e2e-native'] = {'episode_open': False, 'pending': True,
                                       'episode_id': 'ep-1', 'turns': [],
                                       'first_observed_at': obs['observed_at'],
                                       'sequence': 0}
        (state_dir / 'failure-episodes.json').write_text(json.dumps(snaps))
        # Second failure, same outage, different turn/request.
        second = dict(first, turn_id='t2', request_id='req-2')
        run = subprocess.run([sys.executable, str(ROOT / 'muse_failure_hook.py'),
                              '--config', str(self.config), '--event', json.dumps(second)],
                             env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(run.returncode, 0, run.stderr)
        with self.db() as db:
            rows = db.execute(
                "SELECT event_id,outcome_id FROM terminal_event_receipts WHERE kind='runtime-failure'").fetchall()
            body = db.execute('SELECT body FROM messages WHERE id=?', (outcome,)).fetchone()[0]
        # Same key + identical payload reconciles to stored IDs: no new
        # event, no payload-conflict, original body intact.
        self.assertEqual(rows, [committed])
        self.assertEqual(body, 'runtime-failure ep-1 exhausted turn=t1 request=req-1 attempt=10 provider=meta')
        # Healthy progress closes the episode after reconciling pending.
        healthy = dict(first, turn_id='t3', status='completed', error='')
        run = subprocess.run([sys.executable, str(ROOT / 'muse_failure_hook.py'),
                              '--config', str(self.config), '--event', json.dumps(healthy)],
                             env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(run.returncode, 0, run.stderr)
        # New independent outage gets a new key and records again.
        third = dict(first, turn_id='t4', request_id='req-3')
        run = subprocess.run([sys.executable, str(ROOT / 'muse_failure_hook.py'),
                              '--config', str(self.config), '--event', json.dumps(third)],
                             env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(run.returncode, 0, run.stderr)
        with self.db() as db:
            rows = db.execute(
                "SELECT event_id FROM terminal_event_receipts WHERE kind='runtime-failure' ORDER BY outcome_id").fetchall()
        self.assertEqual(len(rows), 2)
        self.assertTrue(rows[0][0].endswith('/ep-1'))
        self.assertTrue(rows[1][0].endswith('/ep-2'))
        snaps = json.loads((Path(json.loads(self.config.read_text())['state_directory'])
                            / 'failure-episodes.json').read_text())
        self.assertEqual(snaps['D-E2E|1|e2e-native']['sequence'], 2)

    def test_real_native_payload_without_request_id_delivers(self):
        """D84-9 real path: exact real Muse 1.4.3 capture shape.

        The payload has no request_id and no provider. The hook must
        accept it, send it through the real Go Submit with the sentinel,
        and the receiver must deliver it — 1 durable event, sanitized.
        """
        sys.path.insert(0, str(ROOT))
        event = {'hook_event_name': 'PostLLMCall', 'session_id': 'e2e-native',
                 'turn_id': 'b311fc68-3cd3-4711-a545-1feaf6358654',
                 'error': 'API error 503: isolated failure fixture (fixture_error) '
                          '(after 10 provider attempts)',
                 'error_details': None, 'status': 'failed', 'attempt': 1}
        run = subprocess.run([sys.executable, str(ROOT / 'muse_failure_hook.py'),
                              '--config', str(self.config), '--event', json.dumps(event)],
                             env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(run.returncode, 0, run.stderr)
        with self.db() as db:
            rows = db.execute(
                "SELECT event_id,outcome_id FROM terminal_event_receipts WHERE kind='runtime-failure'").fetchall()
        self.assertEqual(len(rows), 1)
        event_id, outcome = rows[0]
        self.assertTrue(event_id.endswith('/ep-1'), event_id)
        with self.db() as db:
            body = db.execute('SELECT body FROM messages WHERE id=?', (outcome,)).fetchone()[0]
        self.assertEqual(body, 'runtime-failure ep-1 exhausted '
                               'turn=b311fc68-3cd3-4711-a545-1feaf6358654 '
                               'request=unknown attempt=1 provider=unknown')
        self.assertNotIn('fixture_error', body)
        self.assertNotIn('API error', body)
        out = self.squad('dispatcher', 'terminal-events', 'listen', '--delivery-session', 'e2e-recv',
                         '--native-session', 'dispatcher-native', '--max', '5s')
        receipt = json.loads(out)
        self.assertEqual([e['event_id'] for e in receipt['events']], [event_id])
        self.assertEqual(receipt['events'][0]['kind'], 'runtime-failure')


if __name__ == '__main__':
    unittest.main()
