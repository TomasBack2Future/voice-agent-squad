"""#84 recovery executor admission: preconditions before any same-native return.

Isolated temp session logs and throwaway child processes only; no Muse, model,
ledger or live session is touched.
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

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import muse_recovery_executor as executor

NATIVE = '01a11c1a-4859-7000-8000-000000000001'
EVENT_ID = 'worker-terminal-v1/DISPATCH-1/1/%s/runtime-failure/42/ep-1' % NATIVE


def record(record_kind, **event):
    return {'payload_type': 'runtime.session', 'payload': {'kind': record_kind, 'event': event}}


class RecoveryFixture(unittest.TestCase):
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


class RecoveryAdmissionTests(RecoveryFixture):
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


FAKE_SERVE = r"""
import json, os, sys
state = json.load(open(os.environ['FAKE_MUSE_STATE']))
log = open(os.environ['FAKE_MUSE_LOG'], 'a')
log.write(json.dumps({'argv': sys.argv[1:]}) + '\n'); log.flush()
def send(message):
    sys.stdout.write(json.dumps(message) + '\n'); sys.stdout.flush()
for line in sys.stdin:
    message = json.loads(line)
    method, params = message.get('method'), message.get('params') or {}
    log.write(json.dumps({'method': method, 'params': params}) + '\n'); log.flush()
    if 'id' not in message:
        continue
    reply = {'jsonrpc': '2.0', 'id': message['id']}
    if method == 'initialize':
        reply['result'] = {'serverInfo': {'name': 'muse', 'version': state['version']},
                           'schema': {'version': 1, 'fingerprint': state['fingerprint']}}
    elif method == 'session/resume':
        if state.get('flip_claim_on_resume'):
            squad = json.load(open(os.environ['FAKE_SQUAD_STATE']))
            squad['claim'] = 'OTHER-ITEM'
            json.dump(squad, open(os.environ['FAKE_SQUAD_STATE'], 'w'))
        reply['result'] = {'session': {'sessionId': params['sessionId'], 'modelId': state['model'],
                                       'providerId': 'meta', 'status': 'idle', 'activeTurnId': None},
                           'pendingRequests': [], 'lastTurn': {'terminal': state['last_terminal']}}
    elif method == 'goal/resume':
        reply['result'] = {'commandId': params['commandId'], 'status': 'accepted', 'turnId': 'goal-turn'}
    elif method == 'turn/start':
        reply['result'] = {'commandId': params['commandId'], 'disposition': 'started', 'turnId': 'task-turn'}
    else:
        reply['result'] = {}
    send(reply)
    if 'turnId' in reply.get('result', {}):
        turns = [reply['result']['turnId']] + (['goal-turn-2'] if method == 'goal/resume' else [])
        for turn in turns:
            send({'jsonrpc': '2.0', 'method': 'turn/started', 'params': {'turnId': turn}})
            send({'jsonrpc': '2.0', 'method': 'turn/completed', 'params': {'turnId': turn, 'terminal': 'completed'}})
"""

FAKE_SQUAD = r"""
import json, os, sys, time
state = json.load(open(os.environ['FAKE_SQUAD_STATE']))
args = sys.argv[1:]
if args[:1] == ['claim-inspect']:
    state['who_calls'] = state.get('who_calls', 0) + 1
    json.dump(state, open(os.environ['FAKE_SQUAD_STATE'], 'w'))
    if state.get('append_on_first_who') and state['who_calls'] == 1:
        with open(state['session_log'], 'a') as log:
            log.write(json.dumps(state['append_on_first_who']) + '\n')
    if state.get('hang_on_who_call') == state['who_calls']:
        time.sleep(5)
if args[:2] == ['dispatch', 'list']:
    print(json.dumps([{'reservation_key': 'DISPATCH-1', 'generation': 1, 'state': state.get('reservation_state', 'dispatched'),
                       'worker_thread_id': state['native'], 'reserved_by': 'dispatcher'}]))
elif args[:1] == ['who']:
    # Registration is not custody: an unregistered Muse Worker still holds claims.
    print(json.dumps([]))
elif args[:1] == ['claim-inspect']:
    claim = state.get('claim_row', {'item': args[1], 'holder': 'worker', 'generation': 1, 'state': 'held'})
    if state['claim'] != args[1]:
        claim = None
    print(json.dumps({'env_claim': claim}))
elif args[:2] == ['terminal-events', 'decision-get']:
    # The real Go Decision has no omitempty: "no adopted decision" is
    # revision 0 with empty strings, never an empty object.
    print(json.dumps(state.get('decision') or
                     {'revision': 0, 'outcome_id': 0, 'action': '', 'condition': '', 'worker_agent': ''}))
else:
    sys.exit(9)
"""


class RecoveryBackendTests(RecoveryFixture):
    """Option B continuation against a fake MSP host and fake coordination reads."""

    def setUp(self):
        super().setUp()
        self.serve = self.root / 'fake-muse'
        self.serve.write_text('#!%s\n%s' % (sys.executable, FAKE_SERVE))
        self.serve.chmod(0o755)
        squad = self.root / 'fake-squad'
        squad.write_text('#!%s\n%s' % (sys.executable, FAKE_SQUAD))
        squad.chmod(0o755)
        self.muse_state, self.muse_log = self.root / 'muse-state.json', self.root / 'muse-log.jsonl'
        self.squad_state = self.root / 'squad-state.json'
        self.set_muse()
        self.set_squad()
        for key, value in (('FAKE_MUSE_STATE', self.muse_state), ('FAKE_MUSE_LOG', self.muse_log),
                           ('FAKE_SQUAD_STATE', self.squad_state)):
            old = os.environ.get(key)
            os.environ[key] = str(value)
            self.addCleanup(lambda k=key, o=old: os.environ.pop(k) if o is None else os.environ.__setitem__(k, o))
        self.cfg.update(agent_id='worker', item='BUG-001', client_executable=str(self.serve),
                        client_marker=str(self.client_marker), coordination_executable=str(squad),
                        ledger_directory=str(self.root), provider='meta', model='muse-spark-1.3-contributor',
                        reasoning_effort='max', hooks={'PostLLMCall': []}, claim_generation=1,
                        expected_server_version='1.4.4', expected_schema_fingerprint='sha256:qualified',
                        continuation_prompt='Continue the assigned task from its last checkpoint.',
                        max_seconds=20, quiet_seconds=1)

    def set_muse(self, **overrides):
        state = {'version': '1.4.4', 'fingerprint': 'sha256:qualified', 'model': 'muse-spark-1.3-contributor',
                 'last_terminal': 'failed'}
        state.update(overrides)
        self.muse_state.write_text(json.dumps(state))

    def set_squad(self, **overrides):
        state = {'native': NATIVE, 'claim': 'BUG-001', 'decision': {'revision': 3, 'action': 'proceed'}}
        state.update(overrides)
        self.squad_state.write_text(json.dumps(state))

    def set_goal(self, status):
        # The native Muse 1.4.4 goals.db schema: one goal row per session.
        with sqlite3.connect(self.session_dir / 'goals.db') as db:
            db.execute('CREATE TABLE IF NOT EXISTS goals (session_id TEXT PRIMARY KEY, goal_id TEXT NOT NULL, '
                       'revision TEXT NOT NULL, objective TEXT NOT NULL, status TEXT NOT NULL, '
                       'percent_complete INTEGER NOT NULL)')
            db.execute('INSERT OR REPLACE INTO goals VALUES (?, ?, ?, ?, ?, 0)',
                       (NATIVE, 'goal-1', '1', 'fixture', status))

    def squad_state(self):
        return json.loads(self.squad_state_path().read_text())

    def squad_state_path(self):
        return self.squad_state

    def calls(self):
        if not self.muse_log.exists():
            return []
        return [json.loads(line) for line in self.muse_log.read_text().splitlines()]

    def methods(self):
        return [c['method'] for c in self.calls() if 'method' in c]

    def test_turn_continuation_resumes_same_native_with_original_settings(self):
        self.set_goal('active')
        child = self.client()
        self.failed_log(pid=child.pid)
        evidence = executor.recover(self.event, self.cfg)
        self.assertEqual(evidence['terminal'], 'completed', evidence)
        self.assertEqual((evidence['mode'], evidence['turn_id']), ('turn', 'task-turn'))
        self.assertEqual(evidence['client'], {'pid': child.pid, 'state': 'terminated'})
        self.assertEqual(self.methods(), ['initialize', 'initialized', 'session/resume',
                                          'session/setReasoningEffort', 'session/setApprovalMode', 'turn/start'])
        calls = {c['method']: c['params'] for c in self.calls() if 'method' in c}
        self.assertEqual(calls['session/resume']['sessionId'], NATIVE)
        self.assertEqual(calls['session/setReasoningEffort']['reasoningEffort'], 'max')
        self.assertEqual(calls['session/setApprovalMode']['mode'], 'allowAll')
        argv = self.calls()[0]['argv']
        self.assertEqual(argv[:5], ['serve', '--provider', 'meta', '--model', 'muse-spark-1.3-contributor'])
        self.assertIn('hooks={"PostLLMCall":[]}', argv)
        with self.assertRaisesRegex(executor.NotAdmitted, 'budget'):
            executor.recover(self.event, self.cfg)

    def test_blocked_goal_resumes_and_waits_for_its_chained_turns(self):
        self.set_goal('blocked')
        self.failed_log()
        evidence = executor.recover(self.event, self.cfg)
        self.assertEqual((evidence['mode'], evidence['turn_id']), ('goal', 'goal-turn'))
        self.assertEqual(evidence['turn_terminals'], ['completed', 'completed'])
        self.assertNotIn('turn/start', self.methods())

    def test_goal_state_matrix(self):
        # D84-34: active or no goal -> one turn/start (no goal created or
        # touched); fault-blocked -> goal/resume; user-paused/unknown -> stop;
        # completed -> stop with a recorded unsupported gap. Nothing runs on a stop.
        child = self.client()
        self.failed_log(pid=child.pid)
        self.assertEqual(executor.continuation_mode(self.cfg), 'turn')
        self.set_goal('active')
        self.assertEqual(executor.continuation_mode(self.cfg), 'turn')
        self.set_goal('blocked')
        self.assertEqual(executor.continuation_mode(self.cfg), 'goal')
        for status in ('paused', 'unknown-future-status', 'completed'):
            with self.subTest(status=status):
                self.set_goal(status)
                with self.assertRaisesRegex(executor.NotAdmitted, 'goal is'):
                    executor.recover(self.event, self.cfg)
                self.assertIsNone(child.poll())
                self.assertEqual(self.calls(), [])
        # The native finished-goal status is `complete`.
        self.set_goal('complete')
        evidence = executor.recover(self.event, self.cfg)
        self.assertIn('unsupported', evidence['stopped'])
        self.assertIsNone(child.poll())
        self.assertEqual(self.calls(), [])
        recorded = json.loads((Path(self.cfg['state_directory']) / 'recovery-ep-1.json').read_text())
        self.assertIn('unsupported', recorded['stopped'])
        self.assertEqual(executor.admit(self.event, self.cfg)['action'], 'continue')

    def test_no_goal_gets_one_turn_without_creating_a_goal(self):
        child = self.client()
        self.failed_log(pid=child.pid)
        evidence = executor.recover(self.event, self.cfg)
        self.assertEqual((evidence['mode'], evidence['terminal']), ('turn', 'completed'))
        self.assertNotIn('goal/resume', self.methods())
        self.assertFalse(any(m.startswith('goal/') for m in self.methods()))
        self.assertFalse((self.session_dir / 'goals.db').exists())
        with self.assertRaisesRegex(executor.NotAdmitted, 'budget'):
            executor.recover(self.event, self.cfg)

    def test_custody_lost_before_turn_start_sends_no_turn(self):
        self.set_goal('active')
        self.set_muse(flip_claim_on_resume=True)
        self.failed_log()
        evidence = executor.recover(self.event, self.cfg)
        self.assertIn('no longer holds its claim', evidence['stopped'])
        self.assertNotIn('turn/start', self.methods())
        self.assertNotIn('goal/resume', self.methods())
        with self.assertRaisesRegex(executor.NotAdmitted, 'budget'):
            executor.recover(self.event, self.cfg)

    def test_hold_decision_or_closed_reservation_changes_nothing(self):
        self.set_goal('active')
        child = self.client()
        self.failed_log(pid=child.pid)
        for squad in ({'decision': {'revision': 4, 'action': 'hold'}}, {'reservation_state': 'completed'}):
            with self.subTest(squad=squad):
                self.set_squad(**squad)
                with self.assertRaises(executor.NotAdmitted):
                    executor.recover(self.event, self.cfg)
                self.assertIsNone(child.poll())
                self.assertEqual(self.calls(), [])
        self.set_squad()
        self.assertEqual(executor.admit(self.event, self.cfg)['action'], 'continue')

    def test_unqualified_host_or_unexpected_session_starts_no_turn(self):
        self.set_goal('active')
        for muse in ({'version': '1.4.3'}, {'last_terminal': 'completed'}, {'model': 'other-model'}):
            with self.subTest(muse=muse):
                self.muse_log.unlink(missing_ok=True)
                state = Path(self.cfg['state_directory']) / 'recovery-attempts.json'
                state.unlink(missing_ok=True)
                self.set_muse(**muse)
                self.failed_log()
                evidence = executor.recover(self.event, self.cfg)
                self.assertIn('stopped', evidence)
                self.assertNotIn('turn/start', self.methods())
                self.assertNotIn('goal/resume', self.methods())


    def test_newer_run_before_sigterm_leaves_client_alone(self):
        # 160e0e7 review finding 1: the client starts a newer run between
        # admission and termination; recheck right before SIGTERM refuses.
        self.set_goal('active')
        child = self.client()
        self.failed_log(pid=child.pid)
        for appended in (record('run', kind='model_request_configured'),
                         {'payload_type': 'runtime.session.route_facts',
                          'payload': {'kind': 'route_facts', 'record': {'cwd': str(self.workspace), 'pid': 4242}}}):
            with self.subTest(appended=appended['payload']['kind']):
                self.failed_log(pid=child.pid)
                (Path(self.cfg['state_directory']) / 'recovery-attempts.json').unlink(missing_ok=True)
                self.set_squad(append_on_first_who=appended,
                               session_log=str(self.session_dir / 'session.jsonl'))
                evidence = executor.recover(self.event, self.cfg)
                self.assertIn('stopped', evidence)
                self.assertIsNone(child.poll())
                self.assertEqual(self.calls(), [])

    def test_coordination_timeout_after_sigterm_still_records_evidence(self):
        # 160e0e7 review finding 2: a TimeoutExpired in the second custody
        # read must be handled fail-closed and still write recovery-ep-1.json.
        self.set_goal('active')
        child = self.client()
        self.failed_log(pid=child.pid)
        self.set_squad(hang_on_who_call=2)
        self.cfg['coordination_timeout_seconds'] = 1
        evidence = executor.recover(self.event, self.cfg)
        self.assertIn('timed out', evidence['stopped'])
        self.assertIsNotNone(child.poll())
        self.assertNotIn('turn/start', self.methods())
        recorded = json.loads((Path(self.cfg['state_directory']) / 'recovery-ep-1.json').read_text())
        self.assertEqual(recorded['stopped'], evidence['stopped'])
        attempts = json.loads((Path(self.cfg['state_directory']) / 'recovery-attempts.json').read_text())
        self.assertEqual([a['stage'] for a in attempts.values()], ['stopped'])

    def test_custody_is_the_exact_claim_not_registration(self):
        # D84-40 live: the unregistered real Muse Worker held its claim, yet
        # `who` custody refused. Only the exact item/holder/held/generation counts.
        self.set_goal('active')
        self.failed_log()
        env = executor.msp.child_environment(self.cfg)
        self.assertEqual(executor.check_custody(self.cfg, env), 3)
        for row in ({'item': 'BUG-001', 'holder': 'someone-else', 'generation': 1, 'state': 'held'},
                    {'item': 'BUG-001', 'holder': 'worker', 'generation': 2, 'state': 'held'},
                    {'item': 'BUG-001', 'holder': 'worker', 'generation': 1, 'state': 'recovering'}):
            with self.subTest(row=row):
                self.set_squad(claim_row=row)
                with self.assertRaisesRegex(executor.NotAdmitted, 'claim'):
                    executor.check_custody(self.cfg, env)
        self.set_squad(claim='OTHER-ITEM')
        with self.assertRaisesRegex(executor.NotAdmitted, 'claim'):
            executor.check_custody(self.cfg, env)

    def test_check_mode_runs_custody_without_changing_anything(self):
        self.set_goal('active')
        child = self.client()
        self.failed_log(pid=child.pid)
        event_file, config_file = self.root / 'event.json', self.root / 'config.json'
        event_file.write_text(json.dumps(self.event))
        config_file.write_text(json.dumps(self.cfg))
        argv = ['--config', str(config_file), '--event-json', str(event_file), '--check']
        self.assertEqual(executor.main(argv), 0)
        self.set_squad(claim='OTHER-ITEM')
        self.assertEqual(executor.main(argv), 3)
        self.assertIsNone(child.poll())
        self.assertEqual(self.calls(), [])
        self.assertFalse((Path(self.cfg['state_directory']) / 'recovery-attempts.json').exists())

    def test_real_no_decision_shape_is_absent_but_adopted_must_proceed(self):
        # D84-68 live canary: decision-get with no adopted decision returns
        # revision 0 and an empty action; that is absent, not a refusal. An
        # adopted revision (>0) must be exactly `proceed`.
        self.set_goal('active')
        self.failed_log()
        env = executor.msp.child_environment(self.cfg)
        self.set_squad(decision=None)
        self.assertEqual(executor.check_custody(self.cfg, env), 0)
        for decision in ({'revision': 4, 'action': 'hold'}, {'revision': 5, 'action': ''},
                         {'revision': 0, 'action': 'hold'}, {'revision': 6, 'action': 'stop'}):
            with self.subTest(decision=decision):
                self.set_squad(decision=decision)
                with self.assertRaisesRegex(executor.NotAdmitted, 'decision'):
                    executor.check_custody(self.cfg, env)
        self.set_squad(decision={'revision': 7, 'action': 'proceed'})
        self.assertEqual(executor.check_custody(self.cfg, env), 7)

if __name__ == '__main__':
    unittest.main()
