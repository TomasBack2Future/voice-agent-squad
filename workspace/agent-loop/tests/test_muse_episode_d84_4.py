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
    submits = []
    def run(argv, **kwargs):
        if argv[1] == 'terminal-events' and argv[2] == 'decision-get':
            if revision is None:
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            return subprocess.CompletedProcess(argv, 0, json.dumps({'revision': revision}), '')
        if argv[1] == 'terminal-events' and argv[2] == 'submit':
            submits.append(argv)
            return subprocess.CompletedProcess(argv, 0, json.dumps(result), '')
        raise AssertionError('unexpected squad call: %r' % (argv,))
    run.submits = submits
    return run


def submit_bodies(stub):
    return [argv[argv.index('--body') + 1] for argv in stub.submits]


class EpisodeLifecycleTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='muse-episode-d84-4 ')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.cfg = config(self.root)

    def test_failure_then_healthy_then_new_failure_publishes_again(self):
        submits = []
        def counting(argv, **kwargs):
            if argv[1] == 'terminal-events' and argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            submits.append(argv)
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': 'e%d' % len(submits), 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=counting):
            first = hook.publish(payload(turn_id='turn-1'), self.cfg)
            self.assertEqual(first['state'], 'pending')
            # Verified healthy progress on the same native closes the episode.
            hook.note_progress(payload(turn_id='turn-2', status='completed', error=''), self.cfg)
            second = hook.publish(payload(turn_id='turn-3', request_id='req-3'), self.cfg)
            self.assertEqual(second['state'], 'pending')
            self.assertNotEqual(second['event_id'], first['event_id'])
        self.assertEqual(len(submits), 2)
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-2'])

    def test_same_outage_retries_still_dedupe(self):
        with mock.patch.object(hook.subprocess, 'run', side_effect=ok_run({'event_id': 'e1', 'state': 'pending'})):
            first = hook.publish(payload(turn_id='turn-1'), self.cfg)
            self.assertEqual(first['state'], 'pending')
            retry = hook.publish(payload(turn_id='turn-1'), self.cfg)
            self.assertEqual(retry['state'], 'duplicate')
            later_turn = hook.publish(payload(turn_id='turn-1b', request_id='req-1b'), self.cfg)
            self.assertEqual(later_turn['state'], 'duplicate')

    def test_publish_failure_stays_pending_and_replays(self):
        submits = []
        failed_once = []
        def flaky(argv, **kwargs):
            if argv[1] == 'terminal-events' and argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            if not failed_once:
                failed_once.append(True)
                raise subprocess.TimeoutExpired(argv, 5)
            submits.append(argv)
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': 'e1', 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=flaky):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(), self.cfg, attempts=1)
            # The failed publish left a pending episode; the next hook
            # invocation replays it for real instead of a permanent duplicate.
            replayed = hook.publish(payload(), self.cfg, attempts=1)
            self.assertEqual(replayed['state'], 'pending')
        self.assertEqual(len(submits), 1)

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

    def test_retry_reuses_episode_request_key(self):
        # D84-5/D84-6: one Submit per attempt; the same episode reuses the
        # same request key AND the identical frozen body, so the backend
        # dedupes to the same IDs. A lost receipt replays the identical
        # key instead of opening a new one.
        stub = ok_run({'event_id': 'e1', 'state': 'pending'})
        with mock.patch.object(hook.subprocess, 'run', side_effect=stub):
            first = hook.publish(payload(), self.cfg)
            self.assertEqual(first['state'], 'pending')
        self.assertEqual(len(stub.submits), 1)
        # Simulate a lost receipt: clear the marker but keep the episode
        # identity AND the frozen payload, republish with the same key.
        lost = hook.observe(payload(), self.cfg)
        lost['episode_id'] = 'ep-1'
        hook._mark_pending_unlocked(self.cfg, lost)
        hook._store_pending(self.cfg, lost)
        stub2 = ok_run({'event_id': 'e1', 'state': 'pending'})
        with mock.patch.object(hook.subprocess, 'run', side_effect=stub2):
            retry = hook.publish(payload(), self.cfg)
            self.assertEqual(retry['state'], 'pending')
        self.assertEqual(len(stub2.submits), 1)
        for stubbed in (stub, stub2):
            argv = stubbed.submits[0]
            self.assertEqual(argv[argv.index('--request-key') + 1], 'ep-1')
            self.assertIn('ep-1', argv[argv.index('--body') + 1])
        # Same key AND identical frozen body: no payload-conflict.
        self.assertEqual(stub.submits[0][stub.submits[0].index('--body') + 1],
                         stub2.submits[0][stub2.submits[0].index('--body') + 1])

    def test_live_revision_passed_to_submit(self):
        # Grok finding 2: the hook reads decision-get fresh per submit
        # instead of relying on a static config value.
        publishes = []
        stub = ok_run({'event_id': 'e1', 'state': 'pending'}, revision=7)
        real = stub
        def spy(argv, **kwargs):
            if argv[1] == 'terminal-events' and argv[2] == 'submit':
                publishes.append(argv)
            return real(argv, **kwargs)
        with mock.patch.object(hook.subprocess, 'run', side_effect=spy):
            result = hook.publish(payload(), self.cfg)
            self.assertEqual(result['state'], 'pending')
        self.assertEqual(len(publishes), 1)
        self.assertIn('--expected-decision', publishes[0])
        self.assertEqual(publishes[0][publishes[0].index('--expected-decision') + 1], '7')
        self.assertIn('--request-key', publishes[0])

    def test_overlapping_publish_serializes(self):
        # Grok finding 3: one lock from episode check through open marker.
        # Two sequential publishes prove the second sees the first's mark.
        stub = ok_run({'event_id': 'e1', 'state': 'pending'})
        with mock.patch.object(hook.subprocess, 'run', side_effect=stub):
            first = hook.publish(payload(), self.cfg)
            second = hook.publish(payload(turn_id='turn-1'), self.cfg)
        self.assertEqual(first['state'], 'pending')
        self.assertEqual(second['state'], 'duplicate')
        self.assertEqual(len(stub.submits), 1)

    def test_pending_flushed_on_healthy_turn_then_new_episode(self):
        # Grok round 2: a pending episode must flush (not linger) across a
        # healthy PostLLMCall, and the next outage gets a NEW episode id.
        submits = []
        def script(argv, **kwargs):
            if argv[1] == 'terminal-events' and argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            submits.append(argv)
            if len(submits) == 1:
                raise subprocess.TimeoutExpired(argv, 5)
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': 'e%d' % len(submits), 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='turn-1'), self.cfg, attempts=1)
            healthy = payload(turn_id='turn-2', status='completed', error='')
            self.assertTrue(hook.note_progress(healthy, self.cfg, hook._hook_env(self.cfg)))
            second = hook.publish(payload(turn_id='turn-3', request_id='req-3'), self.cfg)
            self.assertEqual(second['state'], 'pending')
        self.assertEqual(len(submits), 3)
        # Flushed ep-1 kept its request key; new outage is ep-2.
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-1', 'ep-2'])

    def test_pending_flush_failure_keeps_pending(self):
        stub = ok_run({'event_id': 'e1', 'state': 'pending'})
        with mock.patch.object(hook.subprocess, 'run', side_effect=subprocess.TimeoutExpired('squad', 5)):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(), self.cfg, attempts=1)
        with mock.patch.object(hook.subprocess, 'run', side_effect=subprocess.TimeoutExpired('squad', 5)):
            healthy = payload(turn_id='turn-2', status='completed', error='')
            self.assertFalse(hook.note_progress(healthy, self.cfg, hook._hook_env(self.cfg)))
        with mock.patch.object(hook.subprocess, 'run', side_effect=stub):
            replayed = hook.publish(payload(), self.cfg, attempts=1)
            self.assertEqual(replayed['state'], 'pending')

    def test_failed_flush_records_boundary_replay_closes_and_new_outage_republishes(self):
        # D84-11 finding 1: ep-1 stays pending, the healthy flush at t2
        # fails, and a later publish confirms the replay. The healthy
        # boundary must survive: the replay confirmation closes ep-1,
        # and the live t3 failure publishes as a NEW ep-2 episode.
        submits = []
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            submits.append(argv)
            if len(submits) <= 2:
                raise subprocess.TimeoutExpired(argv, 5)
            key = argv[argv.index('--request-key') + 1]
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t1', request_id='req-1'), self.cfg, attempts=1)
            healthy = payload(turn_id='t2', status='completed', error='')
            self.assertFalse(hook.note_progress(healthy, self.cfg, hook._hook_env(self.cfg)))
            receipt = hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        # Replay of frozen ep-1 first, then the live t3 failure as ep-2.
        self.assertEqual(keys, ['ep-1', 'ep-1', 'ep-1', 'ep-2'])
        self.assertIn('turn=t1 request=req-1', bodies[2])
        self.assertIn('turn=t3 request=req-3', bodies[3])
        self.assertEqual(receipt['episode_id'], 'ep-2')
        data = json.loads((self.root / 'state' / 'failure-episodes.json').read_text())
        seen = data['DISPATCH-1|1|worker-native']
        self.assertEqual(seen['episode_id'], 'ep-2')
        self.assertEqual(seen['sequence'], 2)

    def test_boundary_consumed_once_never_inherited_by_new_pending(self):
        # D84-12: t1 fails (pending), t2 healthy flush fails, t3 replay
        # confirms ep-1 but ALL ep-2 submits fail (lost replies), t4/t5
        # fail with no further healthy turn. The t2 boundary belongs to
        # ep-1 only: it is consumed once to close ep-1 and must NOT be
        # inherited by the ep-2 pending row. t4 confirms ep-2 open (no
        # ep-3), t5 dedupes. Exactly the ep-1 and ep-2 keys are used.
        submits = []
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            submits.append(argv)
            n = len(submits)
            if n <= 2:
                raise subprocess.TimeoutExpired(argv, 5)
            if n == 4:
                # t3's in-call ep-2 submit: all replies lost.
                raise subprocess.TimeoutExpired(argv, 5)
            key = argv[argv.index('--request-key') + 1]
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t1', request_id='req-1'), self.cfg, attempts=1)
            healthy = payload(turn_id='t2', status='completed', error='')
            self.assertFalse(hook.note_progress(healthy, self.cfg, hook._hook_env(self.cfg)))
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
            data = json.loads((self.root / 'state' / 'failure-episodes.json').read_text())
            pending = data['DISPATCH-1|1|worker-native']
            self.assertTrue(pending['pending'])
            self.assertEqual(pending['episode_id'], 'ep-2')
            self.assertNotIn('healthy_boundary', pending)
            fourth = hook.publish(payload(turn_id='t4', request_id='req-4'), self.cfg, attempts=1)
            self.assertEqual(fourth['episode_id'], 'ep-2')
            fifth = hook.publish(payload(turn_id='t5', request_id='req-5'), self.cfg, attempts=1)
            self.assertEqual(fifth['state'], 'duplicate')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-1', 'ep-1', 'ep-2', 'ep-2'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertIn('turn=t1 request=req-1', bodies[2])
        self.assertIn('turn=t3 request=req-3', bodies[3])
        self.assertIn('turn=t3 request=req-3', bodies[4])

    def test_orphan_pending_without_snapshot_replays_and_keeps_boundary(self):
        # D84-11 finding 2: the process dies after _store_pending but
        # before any snapshot write. The orphan frozen payload must be
        # adopted on the next publish, replayed identically, and — after
        # a healthy turn lands — the next outage must be a new episode.
        import muse_failure_hook as hook_module
        cfg = self.cfg
        first = hook_module.observe(payload(turn_id='t1', request_id='req-1'), cfg)
        first['episode_id'] = 'ep-1'
        hook_module._store_pending(cfg, first)
        self.assertFalse((self.root / 'state' / 'failure-episodes.json').exists())
        submits = []
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            submits.append(argv)
            key = argv[argv.index('--request-key') + 1]
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            # Orphan adopted: no new ep-1 submit of the live body; the
            # frozen payload replays under its own key.
            receipt = hook.publish(payload(turn_id='t2x', request_id='req-2x'), self.cfg, attempts=1)
            self.assertEqual(receipt['episode_id'], 'ep-1')
            healthy = payload(turn_id='t2', status='completed', error='')
            self.assertTrue(hook.note_progress(healthy, self.cfg, hook._hook_env(self.cfg)))
            second = hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
            self.assertEqual(second['episode_id'], 'ep-2')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-2'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertIn('turn=t1 request=req-1', bodies[0])
        self.assertIn('turn=t3 request=req-3', bodies[1])


if __name__ == '__main__':
    unittest.main()
