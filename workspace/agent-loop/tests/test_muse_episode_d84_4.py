"""D84-4 RED: failure episode lifecycle must close on healthy progress.

Defect on 8ae5648: failed turn 1 publishes; a successful PostLLMCall on
turn 2 does not close the episode; a new failure on turn 3 returns
`duplicate`. `episode_open` never closes after healthy progress, and
`muse_failure_handling.episode_key` (reservation/gen/native/class) lets one
consumed budget suppress every later outage for the life of the assignment.

Required behavior:
- A failure episode closes on verified healthy progress (a successful
  PostLLMCall or model/tool progress on the same native).
- A later independent failure opens a NEW episode: published and delivered
  again with its own bounded budget.
- Within ONE continuous outage, internal retries and repeated hooks for the
  same failed turn/request still dedupe.
- If publication fails, the episode stays pending and is replayed for real
  on the next hook invocation or turn boundary; the open marker must not be
  set before publication is durably confirmed.
- D84-2 early-before-terminal pending still holds.
"""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
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


def config(root, **overrides):
    base = {'native_session_id': 'worker-native', 'agent_id': 'worker',
            'reservation': 'DISPATCH-1', 'generation': 1, 'item': 'BUG-001',
            'controller_agent_id': 'dispatcher',
            'state_directory': str(root / 'state'),
            'ledger_directory': str(root),
            'squad_executable': '/nonexistent/squad'}
    base.update(overrides)
    return base


def ok_run(result, revision=None):
    bodies = []
    def run(argv, **kwargs):
        if argv[1] == 'stuck':
            bodies.append(argv[-1])
            return subprocess.CompletedProcess(argv, 0, '[stuck -> #BUG-001] %s\n' % argv[-1], '')
        if argv[1] == 'history':
            lines = ['history for BUG-001:']
            lines += ['  #%d [2026-10-09 01:00] worker (stuck): %s' % (i + 1, b)
                      for i, b in enumerate(bodies)]
            return subprocess.CompletedProcess(argv, 0, '\n'.join(lines) + '\n', '')
        if argv[1] == 'terminal-events' and argv[2] == 'decision-get':
            if revision is None:
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            return subprocess.CompletedProcess(argv, 0, json.dumps({'revision': revision}), '')
        return subprocess.CompletedProcess(argv, 0, json.dumps(result), '')
    run.bodies = bodies
    return run


class EpisodeLifecycleTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='muse-episode-d84-4 ')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.cfg = config(self.root)

    def test_failure_then_healthy_then_new_failure_publishes_again(self):
        bodies = []
        publishes = []
        def counting(argv, **kwargs):
            if argv[1] == 'stuck':
                bodies.append(argv[-1])
                return subprocess.CompletedProcess(argv, 0, '[stuck -> #BUG-001] %s\n' % argv[-1], '')
            if argv[1] == 'history':
                lines = ['history for BUG-001:']
                lines += ['  #%d [2026-10-09 01:00] worker (stuck): %s' % (i + 1, b)
                          for i, b in enumerate(bodies)]
                return subprocess.CompletedProcess(argv, 0, '\n'.join(lines) + '\n', '')
            if argv[1] == 'terminal-events' and argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            publishes.append(argv)
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': 'e%d' % len(publishes), 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=counting):
            first = hook.publish(payload(turn_id='turn-1'), self.cfg)
            self.assertEqual(first['state'], 'pending')
            # Verified healthy progress on the same native closes the episode.
            hook.note_progress(payload(turn_id='turn-2', status='completed', error=''), self.cfg)
            second = hook.publish(payload(turn_id='turn-3', request_id='req-3'), self.cfg)
            self.assertEqual(second['state'], 'pending')
            self.assertNotEqual(second['event_id'], first['event_id'])
        self.assertEqual(len(publishes), 2)
        self.assertNotEqual(bodies[0], bodies[1])

    def test_same_outage_retries_still_dedupe(self):
        with mock.patch.object(hook.subprocess, 'run', side_effect=ok_run({'event_id': 'e1', 'state': 'pending'})):
            first = hook.publish(payload(turn_id='turn-1'), self.cfg)
            self.assertEqual(first['state'], 'pending')
            retry = hook.publish(payload(turn_id='turn-1'), self.cfg)
            self.assertEqual(retry['state'], 'duplicate')
            later_turn = hook.publish(payload(turn_id='turn-1b', request_id='req-1b'), self.cfg)
            self.assertEqual(later_turn['state'], 'duplicate')

    def test_publish_failure_stays_pending_and_replays(self):
        bodies = []
        publishes = []
        failed_once = []
        def flaky(argv, **kwargs):
            if argv[1] == 'stuck':
                if not failed_once:
                    failed_once.append(True)
                    raise subprocess.TimeoutExpired(argv, 5)
                bodies.append(argv[-1])
                return subprocess.CompletedProcess(argv, 0, '[stuck -> #BUG-001] %s\n' % argv[-1], '')
            if argv[1] == 'history':
                lines = ['history for BUG-001:']
                lines += ['  #%d [2026-10-09 01:00] worker (stuck): %s' % (i + 1, b)
                          for i, b in enumerate(bodies)]
                return subprocess.CompletedProcess(argv, 0, '\n'.join(lines) + '\n', '')
            if argv[1] == 'terminal-events' and argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            publishes.append(argv)
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': 'e1', 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=flaky):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(), self.cfg, attempts=1)
            # The failed publish left a pending episode; the next hook
            # invocation replays it for real instead of a permanent duplicate.
            replayed = hook.publish(payload(), self.cfg, attempts=1)
            self.assertEqual(replayed['state'], 'pending')
        self.assertEqual(len(publishes), 1)

    def test_failed_publish_does_not_burn_episode(self):
        with mock.patch.object(hook.subprocess, 'run', side_effect=subprocess.TimeoutExpired('squad', 5)):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(), self.cfg, attempts=1)
        with mock.patch.object(hook.subprocess, 'run', side_effect=ok_run({'event_id': 'e1', 'state': 'pending'})):
            replayed = hook.publish(payload(turn_id='turn-2', request_id='req-2'), self.cfg)
            self.assertEqual(replayed['state'], 'pending')

    def test_healthy_progress_on_other_native_does_not_close(self):
        with mock.patch.object(hook.subprocess, 'run', side_effect=ok_run({'event_id': 'e1', 'state': 'pending'})):
            hook.publish(payload(turn_id='turn-1'), self.cfg)
            hook.note_progress(payload(session_id='other-native', turn_id='turn-2',
                                       status='completed', error=''), self.cfg)
            retry = hook.publish(payload(turn_id='turn-1'), self.cfg)
            self.assertEqual(retry['state'], 'duplicate')

    def test_new_episode_gets_fresh_handling_budget(self):
        first = {'event_id': 'worker-terminal-v1/DISPATCH-1/1/worker-native/runtime-failure/7',
                 'item_id': 'BUG-001', 'kind': 'runtime-failure', 'outcome_id': 7,
                 'source_message_id': 7, 'error_class': 'exhausted', 'episode_id': 'ep-1'}
        second = dict(first, outcome_id=9, source_message_id=9, episode_id='ep-2',
                      event_id='worker-terminal-v1/DISPATCH-1/1/worker-native/runtime-failure/9')
        self.assertNotEqual(handling.episode_key(first), handling.episode_key(second))
        seen = {handling.episode_key(first)}
        action, _ = handling.decide(second, terminal=True, seen=seen)
        self.assertEqual(action, 'continue')

    def test_same_episode_key_stable_across_retries(self):
        first = {'event_id': 'worker-terminal-v1/DISPATCH-1/1/worker-native/runtime-failure/7',
                 'kind': 'runtime-failure', 'error_class': 'exhausted', 'episode_id': 'ep-1'}
        retry = dict(first, outcome_id=8, source_message_id=8)
        self.assertEqual(handling.episode_key(first), handling.episode_key(retry))

    def test_early_before_terminal_pending_still_holds(self):
        event = {'event_id': 'worker-terminal-v1/DISPATCH-1/1/worker-native/runtime-failure/7',
                 'item_id': 'BUG-001', 'kind': 'runtime-failure', 'outcome_id': 7,
                 'source_message_id': 7, 'error_class': 'exhausted', 'episode_id': 'ep-1'}
        action, reason = handling.decide(event, terminal=False, seen=set())
        self.assertEqual(action, 'awaiting-terminal')
        self.assertIn('not acked', reason)

    def test_pre_terminal_nothing_consumable(self):
        # Grok finding 4: auth/quota/config, spent budget, pause and
        # completion all stay awaiting-terminal until the terminal state
        # is confirmed; only then do they resolve to stop.
        base = {'event_id': 'worker-terminal-v1/DISPATCH-1/1/worker-native/runtime-failure/7',
                'item_id': 'BUG-001', 'kind': 'runtime-failure', 'outcome_id': 7,
                'source_message_id': 7, 'error_class': 'exhausted', 'episode_id': 'ep-1'}
        key = handling.episode_key(base)
        early_cases = [
            ('auth class', dict(base, error_class='auth'), {}, set()),
            ('quota class', dict(base, error_class='quota'), {}, set()),
            ('config class', dict(base, error_class='config'), {}, set()),
            ('spent budget', base, {}, {key}),
            ('paused', base, {'paused': True}, set()),
            ('completed', base, {'completed': True}, set()),
        ]
        for name, event, flags, seen in early_cases:
            with self.subTest(name=name):
                action, _ = handling.decide(event, terminal=False, seen=seen, **flags)
                self.assertEqual(action, 'awaiting-terminal')
        late_cases = [
            ('auth class', dict(base, error_class='auth'), {}, set()),
            ('spent budget', base, {}, {key}),
            ('paused', base, {'paused': True}, set()),
        ]
        for name, event, flags, seen in late_cases:
            with self.subTest(name='terminal-' + name):
                action, _ = handling.decide(event, terminal=True, seen=seen, **flags)
                self.assertEqual(action, 'stop')

    def test_retry_reuses_committed_stuck_row(self):
        # Grok finding 1: history is scanned BEFORE posting, so a retry
        # after a committed stuck reuses the existing row.
        stub = ok_run({'event_id': 'e1', 'state': 'pending'})
        with mock.patch.object(hook.subprocess, 'run', side_effect=stub):
            first = hook.publish(payload(), self.cfg)
            self.assertEqual(first['state'], 'pending')
        self.assertEqual(len(stub.bodies), 1)
        # Simulate a lost receipt: clear the marker but keep the episode
        # identity, republish, no new row.
        lost = hook.observe(payload(), self.cfg)
        lost['episode_id'] = 'ep-1'
        hook._mark_pending_unlocked(self.cfg, lost)
        stub2 = ok_run({'event_id': 'e1', 'state': 'pending'})
        stub2.bodies.extend(stub.bodies)
        with mock.patch.object(hook.subprocess, 'run', side_effect=stub2):
            retry = hook.publish(payload(), self.cfg)
            self.assertEqual(retry['state'], 'pending')
        self.assertEqual(len(stub2.bodies), 1)

    def test_live_revision_passed_to_publish(self):
        # Grok finding 2: the hook reads decision-get fresh per publish
        # instead of relying on a static config value.
        publishes = []
        stub = ok_run({'event_id': 'e1', 'state': 'pending'}, revision=7)
        real = stub
        def spy(argv, **kwargs):
            if argv[1] == 'terminal-events' and argv[2] == 'publish':
                publishes.append(argv)
            return real(argv, **kwargs)
        with mock.patch.object(hook.subprocess, 'run', side_effect=spy):
            result = hook.publish(payload(), self.cfg)
            self.assertEqual(result['state'], 'pending')
        self.assertEqual(len(publishes), 1)
        self.assertIn('--expected-decision', publishes[0])
        self.assertEqual(publishes[0][publishes[0].index('--expected-decision') + 1], '7')

    def test_overlapping_publish_serializes(self):
        # Grok finding 3: one lock from episode check through open marker.
        # Two sequential publishes prove the second sees the first's mark.
        stub = ok_run({'event_id': 'e1', 'state': 'pending'})
        with mock.patch.object(hook.subprocess, 'run', side_effect=stub):
            first = hook.publish(payload(), self.cfg)
            second = hook.publish(payload(turn_id='turn-1'), self.cfg)
        self.assertEqual(first['state'], 'pending')
        self.assertEqual(second['state'], 'duplicate')
        self.assertEqual(len(stub.bodies), 1)


if __name__ == '__main__':
    unittest.main()
