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


if __name__ == '__main__':
    unittest.main()
