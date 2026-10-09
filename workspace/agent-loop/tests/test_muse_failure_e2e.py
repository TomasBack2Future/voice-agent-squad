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

    def test_real_144_compound_payload_delivers_normalized(self):
        """D84-10 real path: exact real Muse 1.4.4 capture shape.

        request_id is compound <uuid>:<n>:<m> and provider is present.
        The hook must normalize deterministically, send through the
        real Go Submit, and the receiver must deliver — 1 durable
        event, sanitized, exit 0 (never exit 2 with 0 events).
        """
        sys.path.insert(0, str(ROOT))
        event = {'hook_event_name': 'PostLLMCall', 'session_id': 'e2e-native',
                 'turn_id': '3ff577ba-8b93-4d79-ad4b-5ea4d8d81999',
                 'status': 'failed', 'attempt': 1,
                 'error': 'your API key from META_API_KEY was rejected',
                 'request_id': '3ff577ba-8b93-4d79-ad4b-5ea4d8d81999:0:1',
                 'provider': 'model.meta.response'}
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
        self.assertEqual(body, 'runtime-failure ep-1 auth '
                               'turn=3ff577ba-8b93-4d79-ad4b-5ea4d8d81999 '
                               'request=3ff577ba-8b93-4d79-ad4b-5ea4d8d81999.0.1 '
                               'attempt=1 provider=model.meta.response')
        self.assertNotIn('META_API_KEY', body)
        self.assertNotIn(':', body)
        out = self.squad('dispatcher', 'terminal-events', 'listen', '--delivery-session', 'e2e-recv',
                         '--native-session', 'dispatcher-native', '--max', '5s')
        receipt = json.loads(out)
        self.assertEqual([e['event_id'] for e in receipt['events']], [event_id])
        self.assertEqual(receipt['events'][0]['kind'], 'runtime-failure')

    def _hook_event(self, turn, status='failed'):
        return {'hook_event_name': 'PostLLMCall', 'session_id': 'e2e-native',
                'turn_id': turn, 'status': status, 'attempt': 1,
                'request_id': 'request-' + turn, 'provider': 'meta',
                'error': 'connection reset by peer' if status == 'failed' else ''}

    def _run_hook(self, event):
        run = subprocess.run([sys.executable, str(ROOT / 'muse_failure_hook.py'),
                              '--config', str(self.config), '--event', json.dumps(event)],
                             env=self.env, capture_output=True, text=True, timeout=15)
        self.assertEqual(run.returncode, 0, run.stderr)
        return run

    def _failure_rows(self):
        with self.db() as db:
            return db.execute(
                "SELECT e.event_id,m.body FROM terminal_event_receipts e "
                "JOIN messages m ON m.id=e.outcome_id "
                "WHERE e.kind='runtime-failure' ORDER BY e.outcome_id").fetchall()

    def test_healthy_boundary_survives_failed_flush_real_backend(self):
        """D84-11 finding 1 on the real backend.

        t1 fails and stays pending (submit transport broken), the healthy
        t2 flush also fails, then transport recovers: the t3 failure must
        replay ep-1 identically, close it at the t2 boundary, and publish
        the live t3 failure as ep-2. Two distinct events result.
        """
        sys.path.insert(0, str(ROOT))
        import muse_failure_hook as hook
        phase = {'fail_all': True}
        original = hook._submit
        def script(body, config, env, observation):
            if phase['fail_all']:
                raise subprocess.CalledProcessError(7, ['isolated-submit-transport-fixture'])
            env = dict(env, SQUAD_HOME=self.env['SQUAD_HOME'])
            return original(body, config, env, observation)
        hook._submit = script
        try:
            self.assertEqual(hook.run(self.config, self._hook_event('t1')), 0)
            self.assertEqual(hook.run(self.config, self._hook_event('t2', 'completed')), 0)
            mid = json.loads((self.root / 'hook-state' / 'failure-episodes.json').read_text())
            mid_row = mid['D-E2E|1|e2e-native']
            self.assertTrue(mid_row['pending'])
            self.assertEqual(mid_row['episode_id'], 'ep-1')
            self.assertEqual(mid_row.get('healthy_boundary'), 't2')
            self.assertEqual(self._failure_rows(), [])
            phase['fail_all'] = False
            self.assertEqual(hook.run(self.config, self._hook_event('t3')), 0)
        finally:
            hook._submit = original
        rows = self._failure_rows()
        self.assertEqual(len(rows), 2)
        self.assertTrue(rows[0][0].endswith('/ep-1'), rows[0][0])
        self.assertTrue(rows[1][0].endswith('/ep-2'), rows[1][0])
        self.assertIn('turn=t1 request=request-t1', rows[0][1])
        self.assertIn('turn=t3 request=request-t3', rows[1][1])
        snaps = json.loads((self.root / 'hook-state' / 'failure-episodes.json').read_text())
        seen = snaps['D-E2E|1|e2e-native']
        self.assertEqual(seen['episode_id'], 'ep-2')
        self.assertEqual(seen['sequence'], 2)

    def test_boundary_consumed_once_t1_to_t5_real_backend(self):
        """D84-12 full sequence on the real backend: exactly 2 events.

        t1 fails (pending), t2 healthy flush fails, t3 replays ep-1 to
        commit but ALL ep-2 submits fail (lost replies), t4/t5 fail with
        no further healthy turn. The t2 boundary closes ep-1 once and is
        cleared; t4 confirms ep-2 open (no ep-3), t5 dedupes.
        """
        sys.path.insert(0, str(ROOT))
        import muse_failure_hook as hook
        phase = {'fail_all': True}
        original = hook._submit
        def script(body, config, env, observation):
            if phase['fail_all']:
                raise subprocess.CalledProcessError(7, ['isolated-submit-transport-fixture'])
            if phase.get('fail_ep2') and observation.get('episode_id') == 'ep-2':
                # All in-call ep-2 submits lose their replies at t3.
                raise subprocess.CalledProcessError(7, ['isolated-submit-transport-fixture'])
            env = dict(env, SQUAD_HOME=self.env['SQUAD_HOME'])
            return original(body, config, env, observation)
        hook._submit = script
        try:
            self.assertEqual(hook.run(self.config, self._hook_event('t1')), 0)
            self.assertEqual(hook.run(self.config, self._hook_event('t2', 'completed')), 0)
            phase['fail_all'] = False
            phase['fail_ep2'] = True
            self.assertEqual(hook.run(self.config, self._hook_event('t3')), 0)
            phase['fail_ep2'] = False
            pending = json.loads((self.root / 'hook-state' / 'failure-episodes.json').read_text())
            row = pending['D-E2E|1|e2e-native']
            self.assertTrue(row['pending'])
            self.assertEqual(row['episode_id'], 'ep-2')
            self.assertNotIn('healthy_boundary', row)
            self.assertEqual(hook.run(self.config, self._hook_event('t4')), 0)
            self.assertEqual(hook.run(self.config, self._hook_event('t5')), 0)
        finally:
            hook._submit = original
        rows = self._failure_rows()
        self.assertEqual(len(rows), 2)
        self.assertTrue(rows[0][0].endswith('/ep-1'), rows[0][0])
        self.assertTrue(rows[1][0].endswith('/ep-2'), rows[1][0])
        self.assertIn('turn=t1 request=request-t1', rows[0][1])
        self.assertIn('turn=t3 request=request-t3', rows[1][1])

    def test_post_boundary_outage_survives_healthy_replay_real_backend(self):
        """D84-13 sequence on the real backend: exactly 2 events.

        t1 fails (offline, pending), t2 healthy flush fails offline
        (boundary recorded), t3 fails while still offline (its replay
        fails too, so the post-boundary outage is preserved). Transport
        recovers at t4 healthy: ep-1 commits and closes, then the queued
        t3 outage commits as ep-2. t5 healthy changes nothing.
        """
        sys.path.insert(0, str(ROOT))
        import muse_failure_hook as hook
        phase = {'offline': True}
        original = hook._submit
        def script(body, config, env, observation):
            if phase['offline']:
                raise subprocess.CalledProcessError(7, ['isolated-submit-offline-until-healthy'])
            env = dict(env, SQUAD_HOME=self.env['SQUAD_HOME'])
            return original(body, config, env, observation)
        hook._submit = script
        try:
            self.assertEqual(hook.run(self.config, self._hook_event('t1')), 0)
            self.assertEqual(hook.run(self.config, self._hook_event('t2', 'completed')), 0)
            self.assertEqual(hook.run(self.config, self._hook_event('t3')), 0)
            mid = json.loads((self.root / 'hook-state' / 'failure-episodes.json').read_text())
            mid_row = mid['D-E2E|1|e2e-native']
            self.assertTrue(mid_row['pending'])
            self.assertEqual(mid_row['episode_id'], 'ep-1')
            self.assertEqual(mid_row.get('queued_outage'), ['ep-2'])
            self.assertEqual(self._failure_rows(), [])
            phase['offline'] = False
            self.assertEqual(hook.run(self.config, self._hook_event('t4', 'completed')), 0)
            self.assertEqual(hook.run(self.config, self._hook_event('t5', 'completed')), 0)
        finally:
            hook._submit = original
        rows = self._failure_rows()
        self.assertEqual(len(rows), 2)
        self.assertTrue(rows[0][0].endswith('/ep-1'), rows[0][0])
        self.assertTrue(rows[1][0].endswith('/ep-2'), rows[1][0])
        self.assertIn('turn=t1 request=request-t1', rows[0][1])
        self.assertIn('turn=t3 request=request-t3', rows[1][1])

    def test_queued_flush_closed_then_new_failure_real_backend(self):
        """D84-15 seq 1 on the real backend: exactly 3 events.

        t1 fails offline, t2 healthy offline, t3 fails offline. Online
        at t4 healthy: ep-1 and queued ep-2 commit with ep-2 closed
        (never reopened). The t5 failure is a NEW ep-3 outage; t6
        healthy closes it.
        """
        sys.path.insert(0, str(ROOT))
        import muse_failure_hook as hook
        phase = {'offline': True}
        original = hook._submit
        def script(body, config, env, observation):
            if phase['offline']:
                raise subprocess.CalledProcessError(7, ['isolated-queued-healthy-boundary'])
            env = dict(env, SQUAD_HOME=self.env['SQUAD_HOME'])
            return original(body, config, env, observation)
        hook._submit = script
        try:
            self.assertEqual(hook.run(self.config, self._hook_event('t1')), 0)
            self.assertEqual(hook.run(self.config, self._hook_event('t2', 'completed')), 0)
            self.assertEqual(hook.run(self.config, self._hook_event('t3')), 0)
            phase['offline'] = False
            self.assertEqual(hook.run(self.config, self._hook_event('t4', 'completed')), 0)
            self.assertEqual(hook.run(self.config, self._hook_event('t5')), 0)
            self.assertEqual(hook.run(self.config, self._hook_event('t6', 'completed')), 0)
        finally:
            hook._submit = original
        rows = self._failure_rows()
        self.assertEqual(len(rows), 3)
        for (event_id, body), turn, key in zip(rows, ('t1', 't3', 't5'), ('ep-1', 'ep-2', 'ep-3')):
            self.assertTrue(event_id.endswith('/' + key), event_id)
            self.assertIn('turn=%s request=request-%s' % (turn, turn), body)

    def test_two_offline_boundaries_deliver_three_real_backend(self):
        """D84-15 seq 2 on the real backend: exactly 3 events.

        t1/t3/t5 fail offline with healthy t2/t4 between them. Online
        drains at t6/t7/t8 deliver all three as ep-1/2/3 — no single
        queued slot merges outages across the two real boundaries.
        The first failures use the exact real capture shapes: no
        request_id/provider (1.4.3 style) and compound request_id
        (1.4.4 style).
        """
        sys.path.insert(0, str(ROOT))
        import muse_failure_hook as hook
        real_first = [
            {'hook_event_name': 'PostLLMCall', 'session_id': 'e2e-native',
             'turn_id': 'b311fc68-3cd3-4711-a545-1feaf6358654',
             'error': 'API error 503: isolated failure fixture (fixture_error) '
                      '(after 10 provider attempts)',
             'error_details': None, 'status': 'failed', 'attempt': 1},
            {'hook_event_name': 'PostLLMCall', 'session_id': 'e2e-native',
             'turn_id': '3ff577ba-8b93-4d79-ad4b-5ea4d8d81999',
             'status': 'failed', 'attempt': 1,
             'error': 'your API key from META_API_KEY was rejected',
             'request_id': '3ff577ba-8b93-4d79-ad4b-5ea4d8d81999:0:1',
             'provider': 'model.meta.response'},
        ]
        phase = {'offline': True}
        original = hook._submit
        def script(body, config, env, observation):
            if phase['offline']:
                raise subprocess.CalledProcessError(7, ['isolated-multiple-healthy-boundaries'])
            env = dict(env, SQUAD_HOME=self.env['SQUAD_HOME'])
            return original(body, config, env, observation)
        hook._submit = script
        try:
            self.assertEqual(hook.run(self.config, real_first[0]), 0)
            self.assertEqual(hook.run(self.config, self._hook_event('t2', 'completed')), 0)
            self.assertEqual(hook.run(self.config, real_first[1]), 0)
            for turn, status in (('t4', 'completed'), ('t5', 'failed')):
                self.assertEqual(hook.run(self.config, self._hook_event(turn, status)), 0)
            mid = json.loads((self.root / 'hook-state' / 'failure-episodes.json').read_text())
            mid_row = mid['D-E2E|1|e2e-native']
            self.assertEqual(mid_row.get('queued_outage'), ['ep-2', 'ep-3'])
            self.assertEqual(self._failure_rows(), [])
            phase['offline'] = False
            for turn in ('t6', 't7', 't8'):
                self.assertEqual(hook.run(self.config, self._hook_event(turn, 'completed')), 0)
        finally:
            hook._submit = original
        rows = self._failure_rows()
        self.assertEqual(len(rows), 3)
        for (event_id, _), key in zip(rows, ('ep-1', 'ep-2', 'ep-3')):
            self.assertTrue(event_id.endswith('/' + key), event_id)
        self.assertIn('turn=b311fc68-3cd3-4711-a545-1feaf6358654 request=unknown', rows[0][1])
        self.assertIn('request=3ff577ba-8b93-4d79-ad4b-5ea4d8d81999.0.1', rows[1][1])
        self.assertIn('turn=t5 request=request-t5', rows[2][1])
        self.assertNotIn('fixture_error', rows[0][1])
        self.assertNotIn('META_API_KEY', rows[1][1])

    def test_replay_confirms_then_tail_delivers_real_backend(self):
        """Finding 1 on the real backend: exactly 3 events.

        ep-1 pending with queued ep-2/ep-3 across two offline
        boundaries. A failure turn's replay of ep-1 succeeds: ep-1
        confirms and the whole queued tail drains on that same turn
        (9c583c4 review: no open row may strand queued outages while
        the outage continues). The next healthy turn finds nothing.
        """
        sys.path.insert(0, str(ROOT))
        import muse_failure_hook as hook
        phase = {'offline': True}
        original = hook._submit
        def script(body, config, env, observation):
            if phase['offline']:
                raise subprocess.CalledProcessError(7, ['isolated-replay-keeps-tail'])
            env = dict(env, SQUAD_HOME=self.env['SQUAD_HOME'])
            return original(body, config, env, observation)
        hook._submit = script
        try:
            for turn, status in (('t1', 'failed'), ('t2', 'completed'), ('t3', 'failed'),
                                 ('t4', 'completed'), ('t5', 'failed')):
                self.assertEqual(hook.run(self.config, self._hook_event(turn, status)), 0)
            phase['offline'] = False
            self.assertEqual(hook.run(self.config, self._hook_event('t6')), 0)
            mid = json.loads((self.root / 'hook-state' / 'failure-episodes.json').read_text())
            mid_row = mid['D-E2E|1|e2e-native']
            self.assertIsNone(mid_row.get('queued_outage'))
            self.assertFalse(mid_row['episode_open'])
            self.assertEqual(hook.run(self.config, self._hook_event('t7', 'completed')), 0)
        finally:
            hook._submit = original
        rows = self._failure_rows()
        self.assertEqual(len(rows), 3)
        for (event_id, body), turn, key in zip(rows, ('t1', 't3', 't5'), ('ep-1', 'ep-2', 'ep-3')):
            self.assertTrue(event_id.endswith('/' + key), event_id)
            self.assertIn('turn=%s request=request-%s' % (turn, turn), body)

    def test_closed_row_with_queued_delivers_then_new_outage_real_backend(self):
        """Finding 2 on the real backend: exactly 3 events.

        ep-1 commits on a healthy flush but queued ep-2 fails its
        single delivery. The next failure drains queued ep-2 first and
        publishes the live failure as ep-3 — the queue is never adopted
        as an orphan and the live outage is never swallowed.
        """
        sys.path.insert(0, str(ROOT))
        import muse_failure_hook as hook
        phase = {'offline': True, 'fail_queued_once': True}
        original = hook._submit
        seen_keys = []
        def script(body, config, env, observation):
            if phase['offline']:
                raise subprocess.CalledProcessError(7, ['isolated-closed-row-queued'])
            key = observation.get('episode_id', '')
            if phase['fail_queued_once'] and key == 'ep-2' and seen_keys == ['ep-1']:
                raise subprocess.CalledProcessError(7, ['isolated-queued-single-shot'])
            seen_keys.append(key)
            env = dict(env, SQUAD_HOME=self.env['SQUAD_HOME'])
            return original(body, config, env, observation)
        hook._submit = script
        try:
            self.assertEqual(hook.run(self.config, self._hook_event('t1')), 0)
            self.assertEqual(hook.run(self.config, self._hook_event('t2', 'completed')), 0)
            self.assertEqual(hook.run(self.config, self._hook_event('t3')), 0)
            phase['offline'] = False
            self.assertEqual(hook.run(self.config, self._hook_event('t4', 'completed')), 0)
            mid = json.loads((self.root / 'hook-state' / 'failure-episodes.json').read_text())
            mid_row = mid['D-E2E|1|e2e-native']
            self.assertFalse(mid_row['pending'])
            self.assertFalse(mid_row['episode_open'])
            self.assertEqual(mid_row.get('queued_outage'), ['ep-2'])
            phase['fail_queued_once'] = False
            self.assertEqual(hook.run(self.config, self._hook_event('t5')), 0)
            self.assertEqual(hook.run(self.config, self._hook_event('t6', 'completed')), 0)
        finally:
            hook._submit = original
        rows = self._failure_rows()
        self.assertEqual(len(rows), 3)
        for (event_id, body), turn, key in zip(rows, ('t1', 't3', 't5'), ('ep-1', 'ep-2', 'ep-3')):
            self.assertTrue(event_id.endswith('/' + key), event_id)
            self.assertIn('turn=%s request=request-%s' % (turn, turn), body)

    def test_commit_before_interrupt_keeps_boundary_real_backend(self):
        """D84-11 finding 2 on the real backend.

        A real Submit commits, then the hook process exits before writing
        any snapshot. The orphan frozen payload is adopted: t2 healthy
        flushes it, and the t3 failure publishes as a NEW ep-2 episode.
        """
        sys.path.insert(0, str(ROOT))
        code = ('import importlib.util,json,os,sys\n'
                's=importlib.util.spec_from_file_location("a",sys.argv[1])\n'
                'm=importlib.util.module_from_spec(s)\n'
                's.loader.exec_module(m)\n'
                'orig=m._submit\n'
                'def crash(*args):\n'
                ' orig(*args)\n'
                ' os._exit(99)\n'
                'm._submit=crash\n'
                'm.run(__import__("pathlib").Path(sys.argv[2]),json.loads(sys.argv[3]))\n')
        child = subprocess.run([sys.executable, '-c', code,
                                str(ROOT / 'muse_failure_hook.py'), str(self.config),
                                json.dumps(self._hook_event('t1'))],
                               env=self.env, capture_output=True, text=True, timeout=25)
        self.assertEqual(child.returncode, 99, child.stderr)
        self.assertEqual(len(self._failure_rows()), 1)
        self.assertFalse((self.root / 'hook-state' / 'failure-episodes.json').exists())
        self._run_hook(self._hook_event('t2', 'completed'))
        self._run_hook(self._hook_event('t3'))
        rows = self._failure_rows()
        self.assertEqual(len(rows), 2)
        self.assertTrue(rows[0][0].endswith('/ep-1'), rows[0][0])
        self.assertTrue(rows[1][0].endswith('/ep-2'), rows[1][0])
        self.assertIn('turn=t1 request=request-t1', rows[0][1])
        self.assertIn('turn=t3 request=request-t3', rows[1][1])


if __name__ == '__main__':
    unittest.main()
