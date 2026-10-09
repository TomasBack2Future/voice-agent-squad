"""RED for #84: Muse PostLLMCall(status=failed) hook adapter and Dispatcher handling.

The adapter (muse_failure_hook.py) validates the exact ledger-bound Worker
native, reservation generation and controller recipient, rejects unrelated /
reminder / subagent sessions, sanitizes the failure observation (no prompt,
body or credentials), dedupes by reservation/generation/native/turn/request
plus continuous episode, and retries publication a bounded number of times.

The handling module (muse_failure_handling.py) classifies the sanitized
observation, enforces D84-2 (an event early relative to the terminal state is
never acked/consumed/dropped), pauses/completion, auth/config exclusion, and
one continuation per continuous outage within the existing budget.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import muse_failure_hook as hook
import muse_failure_handling as handling


def payload(**overrides):
    base = {'hook_event_name': 'PostLLMCall', 'session_id': 'worker-native',
            'turn_id': 'turn-1', 'status': 'failed', 'attempt': 1,
            'provider': 'meta', 'request_id': 'req-1',
            'error': 'API error 503: isolated failure fixture (after 10 provider attempts)'}
    base.update(overrides)
    return base


def config(**overrides):
    base = {'native_session_id': 'worker-native', 'agent_id': 'worker',
            'reservation': 'DISPATCH-1', 'generation': 1, 'item': 'BUG-001',
            'controller_agent_id': 'dispatcher',
            'state_directory': '', 'ledger_directory': '',
            'squad_executable': '/nonexistent/squad'}
    base.update(overrides)
    return base


class HookAdapterTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='muse-failure-hook ')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.cfg = config(state_directory=str(self.root / 'state'),
                          ledger_directory=str(self.root))

    def test_unrelated_reminder_and_subagent_sessions_rejected(self):
        for event in (payload(session_id='other-native'),
                      payload(session_id='worker-native', agent_id='child-1', agent_type='subagent'),
                      {'hook_event_name': 'PostLLMCall', 'session_id': 'worker-native',
                       'turn_id': 'turn-9', 'status': 'completed'}):
            with self.subTest(event=event):
                self.assertFalse(hook.admits(event, self.cfg))

    def test_stop_failure_never_admitted_without_proof(self):
        event = {'hook_event_name': 'StopFailure', 'session_id': 'worker-native',
                 'turn_id': 'turn-1', 'error': 'model_error'}
        self.assertFalse(hook.admits(event, self.cfg))

    def test_sanitized_observation_has_no_prompt_body_or_credentials(self):
        event = payload(error='connection refused, token abc123, prompt leaked')
        observation = hook.observe(event, self.cfg)
        blob = json.dumps(observation)
        for leaked in ('abc123', 'leaked', 'prompt', 'refused'):
            self.assertNotIn(leaked, blob)
        self.assertEqual(observation['native_session_id'], 'worker-native')
        self.assertEqual(observation['turn_id'], 'turn-1')
        self.assertEqual(observation['error_class'], 'connection')

    def test_error_classification(self):
        cases = [
            ('API error 503: x (after 10 provider attempts)', 'exhausted'),
            ('connection refused calling provider', 'connection'),
            ('your API key was rejected', 'auth'),
            ('quota exceeded for model', 'quota'),
            ('unknown model selected', 'config'),
            ('weird new failure mode', 'unknown'),
        ]
        for error, want in cases:
            with self.subTest(error=error):
                self.assertEqual(hook.classify_error(error), want)

    def test_dedupe_by_generation_native_turn_and_episode(self):
        # D84-4: the open marker is written only after confirmed publication.
        # duplicate() is a read; publish() confirms then marks.
        first = hook.observe(payload(), self.cfg)
        self.assertFalse(hook.duplicate(first, self.cfg))
        def ok(argv, **kwargs):
            import subprocess as sp
            if argv[1] == 'terminal-events' and argv[2] == 'decision-get':
                return sp.CompletedProcess(argv, 0, '{}\n', '')
            assert argv[2] == 'submit', argv
            return sp.CompletedProcess(argv, 0, '{"event_id":"e1","state":"pending"}', '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=ok):
            self.assertEqual(hook.publish(payload(), self.cfg)['state'], 'pending')
        dup = hook.observe(payload(), self.cfg)
        self.assertTrue(hook.duplicate(dup, self.cfg))
        other_turn = hook.observe(payload(turn_id='turn-2'), self.cfg)
        # A new turn in the same continuous episode still dedupes.
        self.assertTrue(hook.duplicate(other_turn, self.cfg))

    def test_bounded_publish_retry_then_pending(self):
        calls = []
        def failing(argv, **kwargs):
            calls.append(argv)
            raise subprocess.TimeoutExpired(argv, 5)
        with mock.patch.object(hook.subprocess, 'run', side_effect=failing):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(), self.cfg, attempts=3)
        self.assertEqual(len(calls), 3)
        pending = json.loads((self.root / 'state' / 'pending.json').read_text())
        self.assertEqual(set(pending), {'ep-1'})
        self.assertEqual(pending['ep-1']['turn_id'], 'turn-1')

    def test_unsafe_identifiers_rejected_precisely_not_dropped(self):
        # D84-7(c) as corrected by D84-9: turn_id is always present in
        # real captures so it stays strict; present-but-unsafe request/
        # provider values are a precise durable rejection, never a
        # silent exit 0. Absent request/provider map to the sentinel.
        for bad in ({'turn_id': ''}, {'request_id': 'a/b'}, {'provider': 'meta ai'},
                    {'turn_id': 't:1'}, {'turn_id': 'a b'}):
            with self.subTest(bad=bad):
                with self.assertRaises(hook.UnsafeIdentifier):
                    hook.observe(payload(**bad), self.cfg)
        (self.root / 'config.json').write_text(json.dumps(self.cfg))
        code = hook.main(['--config', str(self.root / 'config.json'),
                          '--event', json.dumps(payload(turn_id='t/1'))])
        self.assertEqual(code, 2)
        rejected = json.loads((self.root / 'state' / 'rejected.json').read_text())
        self.assertEqual(len(rejected), 1)
        self.assertIn('turn_id', rejected[0]['reason'])
        self.assertFalse((self.root / 'state' / 'pending.json').exists())
        self.assertFalse((self.root / 'state' / 'failure-episodes.json').exists())

    def test_safe_identifiers_cover_real_hook_values(self):
        obs = hook.observe(payload(turn_id='48b8b788-8dc3-4656-b776-4995a5c105b9',
                                   request_id='req-1.2_3', provider='meta'), self.cfg)
        self.assertEqual(obs['turn_id'], '48b8b788-8dc3-4656-b776-4995a5c105b9')

    def test_missing_request_id_and_provider_map_to_sentinel(self):
        # D84-9: every real Muse 1.4.3 failure capture has no request_id
        # and no provider. Absent fields map to the documented sentinel,
        # never reject; present-but-unsafe values still reject precisely.
        event = {'hook_event_name': 'PostLLMCall',
                 'session_id': 'worker-native',
                 'turn_id': 'b311fc68-3cd3-4711-a545-1feaf6358654',
                 'error': 'API error 503: isolated failure fixture (fixture_error) '
                          '(after 10 provider attempts)',
                 'error_details': None, 'status': 'failed', 'attempt': 1}
        self.assertTrue(hook.admits(event, self.cfg))
        obs = hook.observe(event, self.cfg)
        self.assertEqual(obs['request_id'], 'unknown')
        self.assertEqual(obs['provider'], 'unknown')
        self.assertEqual(obs['error_class'], 'exhausted')
        body = hook._compose_body('ep-1', obs)
        self.assertEqual(body, 'runtime-failure ep-1 exhausted '
                               'turn=b311fc68-3cd3-4711-a545-1feaf6358654 '
                               'request=unknown attempt=1 provider=unknown')
        import re
        go_shape = re.compile(
            r'\Aruntime-failure ep-[1-9][0-9]* '
            r'(exhausted|connection|auth|quota|config|unknown) '
            r'turn=[A-Za-z0-9_.-]{1,128} request=[A-Za-z0-9_.-]{1,128} '
            r'attempt=[0-9]{1,10} provider=[A-Za-z0-9_.-]{1,64}\Z')
        self.assertTrue(go_shape.match(body), body)
        for bad in ({'request_id': 'a/b'}, {'provider': 'meta ai'}):
            with self.subTest(bad=bad):
                with self.assertRaises(hook.UnsafeIdentifier):
                    hook.observe(payload(**bad), self.cfg)

    def test_compound_request_id_normalizes_deterministically(self):
        # D84-10: real Muse 1.4.4 emits <uuid>:<n>:<m> request IDs. The
        # documented mapping is <a>.<n>.<m>; it is deterministic (same
        # input always yields the same value, so dedupe stays stable)
        # and stays inside the closed body alphabet. Non-compound
        # shapes with colons still reject precisely.
        event = {'hook_event_name': 'PostLLMCall', 'session_id': 'worker-native',
                 'turn_id': '3ff577ba-8b93-4d79-ad4b-5ea4d8d81999',
                 'status': 'failed', 'attempt': 1,
                 'error': 'your API key from META_API_KEY was rejected',
                 'request_id': '3ff577ba-8b93-4d79-ad4b-5ea4d8d81999:0:1',
                 'provider': 'model.meta.response'}
        self.assertTrue(hook.admits(event, self.cfg))
        first = hook.observe(event, self.cfg)
        self.assertEqual(first['request_id'], '3ff577ba-8b93-4d79-ad4b-5ea4d8d81999.0.1')
        self.assertEqual(first['provider'], 'model.meta.response')
        self.assertEqual(first['error_class'], 'auth')
        second = hook.observe(dict(event), self.cfg)
        self.assertEqual(second['request_id'], first['request_id'])
        body = hook._compose_body('ep-1', first)
        self.assertEqual(body, 'runtime-failure ep-1 auth '
                               'turn=3ff577ba-8b93-4d79-ad4b-5ea4d8d81999 '
                               'request=3ff577ba-8b93-4d79-ad4b-5ea4d8d81999.0.1 '
                               'attempt=1 provider=model.meta.response')
        import re
        go_shape = re.compile(
            r'\Aruntime-failure ep-[1-9][0-9]* '
            r'(exhausted|connection|auth|quota|config|unknown) '
            r'turn=[A-Za-z0-9_.-]{1,128} request=[A-Za-z0-9_.-]{1,128} '
            r'attempt=[0-9]{1,10} provider=[A-Za-z0-9_.-]{1,64}\Z')
        self.assertTrue(go_shape.match(body), body)
        for bad in ('a/b:0:1', 'x:1', 'x:1:2:3', 'x::1', ':0:1', 'x:one:1'):
            with self.subTest(bad=bad):
                with self.assertRaises(hook.UnsafeIdentifier):
                    hook.observe(payload(request_id=bad), self.cfg)

    def test_dedupe_works_without_request_id(self):
        # D84-9: outage dedupe derives identity from turn + episode, so
        # same-outage repeats without request_id still dedupe.
        real = {'hook_event_name': 'PostLLMCall', 'session_id': 'worker-native',
                'error': 'API error 503: x (after 10 provider attempts)',
                'error_details': None, 'status': 'failed', 'attempt': 1}
        first = hook.observe(dict(real, turn_id='t-a'), self.cfg)
        self.assertFalse(hook.duplicate(first, self.cfg))
        def ok(argv, **kwargs):
            import subprocess as sp
            if argv[2] == 'decision-get':
                return sp.CompletedProcess(argv, 0, '{}\n', '')
            return sp.CompletedProcess(argv, 0, '{"event_id":"e1","state":"pending"}', '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=ok):
            self.assertEqual(hook.publish(dict(real, turn_id='t-a'), self.cfg)['state'], 'pending')
        repeat = hook.observe(dict(real, turn_id='t-b'), self.cfg)
        self.assertTrue(hook.duplicate(repeat, self.cfg))

    def test_hook_returns_quickly_without_waiting_for_turn(self):
        binary = self.root / 'squad'
        binary.write_text('#!/usr/bin/env python3\nimport json,sys\n'
                          'if sys.argv[1] == "terminal-events" and sys.argv[2] == "decision-get": print("{}")\n'
                          'else: print(json.dumps({"message_id":7,"event_id":"e1","state":"pending"}))\n')
        binary.chmod(0o700)
        self.cfg['squad_executable'] = str(binary)
        (self.root / 'config.json').write_text(json.dumps(self.cfg))
        start = time.monotonic()
        code = hook.main(['--config', str(self.root / 'config.json'),
                          '--event', json.dumps(payload())])
        self.assertEqual(code, 0)
        self.assertLess(time.monotonic() - start, 5)


class HandlingTests(unittest.TestCase):
    def event(self, **overrides):
        base = {'event_id': 'worker-terminal-v1/DISPATCH-1/1/worker-native/runtime-failure/7',
                'item_id': 'BUG-001', 'kind': 'runtime-failure', 'outcome_id': 7,
                'source_message_id': 7, 'error_class': 'exhausted'}
        base.update(overrides)
        return base

    def test_early_event_is_awaiting_terminal_not_acked(self):
        action, reason = handling.decide(self.event(), terminal=False, seen=set())
        self.assertEqual(action, 'awaiting-terminal')
        self.assertIn('not acked', reason)

    def test_early_event_never_consumes_continuation_budget(self):
        action, _ = handling.decide(self.event(), terminal=False, seen=set())
        self.assertNotEqual(action, 'continue')
        action, _ = handling.decide(self.event(), terminal=True, seen=set())
        self.assertEqual(action, 'continue')

    def test_no_duplicate_continuation_per_outage(self):
        key = handling.episode_key(self.event())
        action, _ = handling.decide(self.event(), terminal=True, seen={key})
        self.assertEqual(action, 'stop')

    def test_auth_quota_config_and_pause_never_continue(self):
        for error_class in ('auth', 'quota', 'config'):
            action, _ = handling.decide(self.event(error_class=error_class), terminal=True, seen=set())
            self.assertEqual(action, 'stop')
        action, _ = handling.decide(self.event(), terminal=True, seen=set(), paused=True)
        self.assertEqual(action, 'stop')
        action, _ = handling.decide(self.event(), terminal=True, seen=set(), completed=True)
        self.assertEqual(action, 'stop')

    def test_episode_key_stable_per_outage(self):
        first = handling.episode_key(self.event())
        second = handling.episode_key(self.event(outcome_id=8, source_message_id=8))
        self.assertEqual(first, second)

    def test_episode_key_uses_persisted_episode_suffix_and_class(self):
        # 1b417f3 review finding 2: the durable episode identity is the
        # event-id suffix /ep-N and the class is the closed-enum token
        # in the message body. Receipts carrying only event_id (plus an
        # optional body) must still key distinct outages distinctly —
        # no shared |unknown|ep-? key that one budget suppresses all.
        one = {'event_id': 'worker-terminal-v1/D/1/n/runtime-failure/3/ep-1',
               'kind': 'runtime-failure', 'outcome_id': 3,
               'body': 'runtime-failure ep-1 exhausted turn=t1 request=r1 attempt=1 provider=meta'}
        two = {'event_id': 'worker-terminal-v1/D/1/n/runtime-failure/4/ep-2',
               'kind': 'runtime-failure', 'outcome_id': 4,
               'body': 'runtime-failure ep-2 auth turn=t5 request=r5 attempt=1 provider=meta'}
        key_one = handling.episode_key(one)
        key_two = handling.episode_key(two)
        self.assertEqual(key_one, 'D|1|n|exhausted|ep-1')
        self.assertEqual(key_two, 'D|1|n|auth|ep-2')
        self.assertNotEqual(key_one, key_two)
        bare = {'event_id': 'worker-terminal-v1/D/1/n/runtime-failure/9/ep-7',
                'kind': 'runtime-failure', 'outcome_id': 9}
        self.assertEqual(handling.episode_key(bare), 'D|1|n|unknown|ep-7')
        # Auth from the body token never continues once terminal.
        action, _ = handling.decide(two, terminal=True, seen=set())
        self.assertEqual(action, 'stop')
        # Distinct persisted outages get distinct budgets.
        action, _ = handling.decide(two, terminal=True, seen={key_one})
        self.assertEqual(action, 'stop')
        other = dict(two, event_id='worker-terminal-v1/D/1/n/runtime-failure/5/ep-3',
                     body='runtime-failure ep-3 exhausted turn=t6 request=r6 attempt=1 provider=meta')
        action, _ = handling.decide(other, terminal=True, seen={key_one})
        self.assertEqual(action, 'continue')


if __name__ == '__main__':
    unittest.main()
