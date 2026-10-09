"""#84 recovery executor admission: preconditions before any same-native return.

Isolated temp session logs and throwaway child processes only; no Muse, model,
ledger or live session is touched.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import muse_recovery_executor as executor

NATIVE = '01a11c1a-4859-7000-8000-000000000001'
EVENT_ID = 'worker-terminal-v1/DISPATCH-1/1/%s/runtime-failure/42/ep-1' % NATIVE


def record(record_kind, **event):
    return {'payload_type': 'runtime.session', 'payload': {'kind': record_kind, 'event': event}}


class RecoveryAdmissionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='muse-recovery ')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.workspace = self.root / 'workspace'
        self.workspace.mkdir()
        self.session_dir = self.root / 'data/muse/sessions/2026/10/09' / NATIVE
        self.session_dir.mkdir(parents=True)
        self.client_marker = self.root / 'muse-bin-fixture'
        self.client_marker.write_text('')
        self.cfg = {'native_session_id': NATIVE, 'reservation': 'DISPATCH-1', 'generation': 1,
                    'controller_agent_id': 'dispatcher', 'workspace': str(self.workspace),
                    'muse_data_directory': str(self.root / 'data/muse'),
                    'state_directory': str(self.root / 'state'), 'terminate_grace_seconds': 5}
        self.event = {'event_id': EVENT_ID, 'kind': 'runtime-failure',
                      'body': 'runtime-failure ep-1 exhausted turn=t1 request=r1 attempt=10 provider=meta'}

    def write_log(self, records, pid=None):
        lines = [{'payload_type': 'runtime.session.route_facts',
                  'payload': {'kind': 'route_facts', 'record': {'cwd': str(self.workspace), 'pid': pid or 999999}}}]
        lines += records
        (self.session_dir / 'session.jsonl').write_text(''.join(json.dumps(l) + '\n' for l in lines))

    def failed_log(self, pid=None):
        self.write_log([record('run', kind='model_request_configured'),
                        record('run', kind='terminal', terminal='failed', reason='503')], pid)

    def client(self):
        child = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(60)',
                                  str(self.client_marker), '--workspace', str(self.workspace)])
        self.addCleanup(lambda: child.poll() is None and child.kill())
        return child

    def test_admits_confirmed_terminal_failure_and_continue_decision(self):
        self.failed_log()
        result = executor.admit(self.event, self.cfg)
        self.assertEqual(result['action'], 'continue')
        self.assertEqual(result['episode'], 'ep-1')

    def test_rejects_event_for_another_native_or_reservation(self):
        self.failed_log()
        for event_id in (EVENT_ID.replace(NATIVE, '01a11c1a-0000-7000-8000-000000000009'),
                         EVENT_ID.replace('DISPATCH-1', 'DISPATCH-2'),
                         EVENT_ID.replace('/1/', '/2/', 1),
                         EVENT_ID.replace('runtime-failure', 'blocked')):
            with self.subTest(event_id=event_id):
                with self.assertRaises(executor.NotAdmitted):
                    executor.admit(dict(self.event, event_id=event_id), self.cfg)

    def test_rejects_until_terminal_failure_is_confirmed(self):
        self.write_log([record('run', kind='model_request_configured')])
        with self.assertRaisesRegex(executor.NotAdmitted, 'awaiting-terminal'):
            executor.admit(self.event, self.cfg)

    def test_rejects_when_a_later_run_started_after_the_failure(self):
        self.write_log([record('run', kind='terminal', terminal='failed'),
                        record('run', kind='model_request_configured')])
        with self.assertRaisesRegex(executor.NotAdmitted, 'awaiting-terminal'):
            executor.admit(self.event, self.cfg)

    def test_non_retryable_and_paused_never_continue(self):
        self.failed_log()
        auth = dict(self.event, body=self.event['body'].replace('exhausted', 'auth'))
        with self.assertRaisesRegex(executor.NotAdmitted, 'auth'):
            executor.admit(auth, self.cfg)
        with self.assertRaisesRegex(executor.NotAdmitted, 'paused'):
            executor.admit(self.event, self.cfg, paused=True)

    def test_one_attempt_per_episode_is_durable(self):
        self.failed_log()
        executor.record_attempt(self.event, self.cfg, 'terminated')
        with self.assertRaisesRegex(executor.NotAdmitted, 'budget'):
            executor.admit(self.event, self.cfg)
        later = dict(self.event, event_id=EVENT_ID.replace('/42/ep-1', '/57/ep-2'),
                     body=self.event['body'].replace('ep-1', 'ep-2'))
        self.assertEqual(executor.admit(later, self.cfg)['action'], 'continue')

    def test_terminates_only_the_recorded_failed_client(self):
        child = self.client()
        bystander = self.client()
        self.failed_log(pid=child.pid)
        result = executor.terminate_failed_client(self.cfg, client_marker=str(self.client_marker))
        self.assertEqual(result, {'pid': child.pid, 'state': 'terminated'})
        self.assertIsNotNone(child.poll())
        self.assertIsNone(bystander.poll())

    def test_refuses_pid_that_is_not_this_sessions_client(self):
        other = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(60)'])
        self.addCleanup(lambda: other.poll() is None and other.kill())
        self.failed_log(pid=other.pid)
        with self.assertRaisesRegex(executor.NotAdmitted, 'not the failed client'):
            executor.terminate_failed_client(self.cfg, client_marker=str(self.client_marker))
        self.assertIsNone(other.poll())

    def test_already_exited_client_is_not_an_error(self):
        child = self.client()
        self.failed_log(pid=child.pid)
        child.kill()
        child.wait()
        self.assertEqual(executor.terminate_failed_client(self.cfg, client_marker=str(self.client_marker)),
                         {'pid': child.pid, 'state': 'already-exited'})


if __name__ == '__main__':
    unittest.main()
