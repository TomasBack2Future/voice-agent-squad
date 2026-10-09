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

    def test_failed_replay_after_boundary_keeps_boundary_and_new_outage(self):
        # 7a01892 review finding 1: ep-1 pending, t2 healthy flush fails
        # (boundary recorded), then the t3 replay ALSO fails. The pending
        # rewrite must keep the boundary and episode identity, so the t4
        # replay still closes ep-1 at t2. The t3 post-boundary outage was
        # preserved first, so ep-2 carries the frozen t3 body (one key,
        # one body: 11a67a2 review); t3/t4 are one continuous outage.
        # Exactly ep-1 then ep-2 are used.
        submits = []
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            submits.append(argv)
            if len(submits) <= 3:
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
            row = json.loads((self.root / 'state' / 'failure-episodes.json').read_text())
            pending = row['DISPATCH-1|1|worker-native']
            self.assertTrue(pending['pending'])
            self.assertEqual(pending['episode_id'], 'ep-1')
            self.assertEqual(pending.get('healthy_boundary'), 't2')
            fourth = hook.publish(payload(turn_id='t4', request_id='req-4'), self.cfg, attempts=1)
            self.assertEqual(fourth['episode_id'], 'ep-2')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-1', 'ep-1', 'ep-1', 'ep-2'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertIn('turn=t1 request=req-1', bodies[3])
        self.assertIn('turn=t3 request=req-3', bodies[4])

    def test_post_boundary_outage_survives_offline_replay_and_healthy_recovery(self):
        # D84-13: t1 fails (offline, pending), t2 healthy flush fails
        # (boundary recorded), t3 fails while still offline (replay of
        # ep-1 fails too). Transport recovers at t4 healthy: the old
        # episode commits AND the post-boundary t3 outage is preserved
        # and committed as ep-2. D84-15: ep-2 is closed-delivered, never
        # reopened, so the t5 failure is a NEW ep-3 outage.
        submits = []
        online = {'yes': False}
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            if not online['yes']:
                raise subprocess.TimeoutExpired(argv, 5)
            submits.append(argv)
            key = argv[argv.index('--request-key') + 1]
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t1', request_id='req-1'), self.cfg, attempts=1)
            healthy2 = payload(turn_id='t2', status='completed', error='')
            self.assertFalse(hook.note_progress(healthy2, self.cfg, hook._hook_env(self.cfg)))
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
            online['yes'] = True
            healthy4 = payload(turn_id='t4', status='completed', error='')
            self.assertTrue(hook.note_progress(healthy4, self.cfg, hook._hook_env(self.cfg)))
            fifth = hook.publish(payload(turn_id='t5', request_id='req-5'), self.cfg, attempts=1)
            self.assertEqual(fifth['episode_id'], 'ep-3')
            sixth = hook.publish(payload(turn_id='t6', request_id='req-6'), self.cfg, attempts=1)
            self.assertEqual(sixth['state'], 'duplicate')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-2', 'ep-3'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertIn('turn=t1 request=req-1', bodies[0])
        self.assertIn('turn=t3 request=req-3', bodies[1])
        self.assertIn('turn=t5 request=req-5', bodies[2])

    def test_flush_close_clears_boundary_later_outage_stays_single(self):
        # 4f43571 review finding 1: t1 pending, t2 healthy flush fails
        # (boundary recorded), t3 healthy flush SUCCEEDS and closes ep-1.
        # The close must clear the boundary. A later t4 outage whose
        # first submit fails then replays: it confirms ep-2 open and
        # must NOT spawn ep-3 from a stale boundary. t5 dedupes.
        submits = []
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            submits.append(argv)
            n = len(submits)
            if n <= 2 or n == 4:
                raise subprocess.TimeoutExpired(argv, 5)
            key = argv[argv.index('--request-key') + 1]
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t1', request_id='req-1'), self.cfg, attempts=1)
            self.assertFalse(hook.note_progress(
                payload(turn_id='t2', status='completed', error=''), self.cfg, hook._hook_env(self.cfg)))
            self.assertTrue(hook.note_progress(
                payload(turn_id='t3', status='completed', error=''), self.cfg, hook._hook_env(self.cfg)))
            closed = json.loads((self.root / 'state' / 'failure-episodes.json').read_text())
            self.assertNotIn('healthy_boundary', closed['DISPATCH-1|1|worker-native'])
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t4', request_id='req-4'), self.cfg, attempts=1)
            fifth = hook.publish(payload(turn_id='t5', request_id='req-5'), self.cfg, attempts=1)
            self.assertEqual(fifth['episode_id'], 'ep-2')
            sixth = hook.publish(payload(turn_id='t6', request_id='req-6'), self.cfg, attempts=1)
            self.assertEqual(sixth['state'], 'duplicate')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-1', 'ep-1', 'ep-2', 'ep-2'])

    def test_flush_close_saves_snapshot_before_dropping_payload(self):
        # 4f43571 review finding 2: the healthy flush must record the
        # snapshot close BEFORE dropping the frozen payload. If the save
        # fails after a successful submit, the payload must still be in
        # the store so a later turn can replay it — never a committed
        # server row with no local body and a still-pending row.
        stub = ok_run({'event_id': 'e1', 'state': 'pending'})
        with mock.patch.object(hook.subprocess, 'run', side_effect=subprocess.TimeoutExpired('squad', 5)):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(), self.cfg, attempts=1)
        def failing_save(config, data):
            raise OSError('isolated snapshot failure fixture')
        with mock.patch.object(hook.subprocess, 'run', side_effect=stub):
            with mock.patch.object(hook, '_save', side_effect=failing_save):
                healthy = payload(turn_id='turn-2', status='completed', error='')
                with self.assertRaises(OSError):
                    hook.note_progress(healthy, self.cfg, hook._hook_env(self.cfg))
        pending = json.loads((self.root / 'state' / 'pending.json').read_text())
        self.assertIn('ep-1', pending)
        self.assertEqual(pending['ep-1']['turn_id'], 'turn-1')
        with mock.patch.object(hook.subprocess, 'run', side_effect=stub):
            healthy = payload(turn_id='turn-3', status='completed', error='')
            self.assertTrue(hook.note_progress(healthy, self.cfg, hook._hook_env(self.cfg)))

    def test_queued_outage_wins_over_live_body_on_same_key(self):
        # 11a67a2 review: t1 pending, t2 healthy flush fails (boundary),
        # t3 replay fails (t3 preserved as queued ep-2). t4 replay
        # SUCCEEDS: the boundary branch must submit the FROZEN t3 body
        # under ep-2, never the live t4 body — one key carries exactly
        # one body, so a later lost-reply recovery can never hit a
        # payload-conflict. t5 dedupes.
        submits = []
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            submits.append(argv)
            if len(submits) <= 3:
                raise subprocess.TimeoutExpired(argv, 5)
            key = argv[argv.index('--request-key') + 1]
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t1', request_id='req-1'), self.cfg, attempts=1)
            self.assertFalse(hook.note_progress(
                payload(turn_id='t2', status='completed', error=''), self.cfg, hook._hook_env(self.cfg)))
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
            fourth = hook.publish(payload(turn_id='t4', request_id='req-4'), self.cfg, attempts=1)
            self.assertEqual(fourth['episode_id'], 'ep-2')
            fifth = hook.publish(payload(turn_id='t5', request_id='req-5'), self.cfg, attempts=1)
            self.assertEqual(fifth['state'], 'duplicate')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-1', 'ep-1', 'ep-1', 'ep-2'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertIn('turn=t1 request=req-1', bodies[3])
        # ep-2 carries the frozen t3 body, not the live t4 body.
        self.assertIn('turn=t3 request=req-3', bodies[4])
        self.assertNotIn('turn=t4', bodies[4])

    def test_queued_flush_never_reopens_new_failure_publishes(self):
        # D84-15 seq 1: t1 fails offline, t2 healthy offline, t3 fails
        # offline. Online at t4 healthy: ep-1 and queued ep-2 both
        # commit, and ep-2 is CLOSED-delivered (never reopened), so the
        # t5 failure publishes as a NEW ep-3 outage. Three events.
        submits = []
        online = {'yes': False}
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            if not online['yes']:
                raise subprocess.TimeoutExpired(argv, 5)
            submits.append(argv)
            key = argv[argv.index('--request-key') + 1]
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t1', request_id='req-1'), self.cfg, attempts=1)
            self.assertFalse(hook.note_progress(
                payload(turn_id='t2', status='completed', error=''), self.cfg, hook._hook_env(self.cfg)))
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
            online['yes'] = True
            self.assertTrue(hook.note_progress(
                payload(turn_id='t4', status='completed', error=''), self.cfg, hook._hook_env(self.cfg)))
            fifth = hook.publish(payload(turn_id='t5', request_id='req-5'), self.cfg, attempts=1)
            self.assertEqual(fifth['episode_id'], 'ep-3')
            sixth = hook.publish(payload(turn_id='t6', request_id='req-6'), self.cfg, attempts=1)
            self.assertEqual(sixth['state'], 'duplicate')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-2', 'ep-3'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertIn('turn=t1 request=req-1', bodies[0])
        self.assertIn('turn=t3 request=req-3', bodies[1])
        self.assertIn('turn=t5 request=req-5', bodies[2])

    def test_two_offline_boundaries_keep_three_independent_outages(self):
        # D84-15 seq 2: t1/t3/t5 fail offline with healthy t2/t4 between
        # them. Online drains at t6/t7/t8 deliver all three as ep-1/2/3
        # — no single queued slot may merge outages across the two real
        # healthy boundaries.
        submits = []
        online = {'yes': False}
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            if not online['yes']:
                raise subprocess.TimeoutExpired(argv, 5)
            submits.append(argv)
            key = argv[argv.index('--request-key') + 1]
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            for turn, status in (('t1', 'failed'), ('t2', 'completed'), ('t3', 'failed'),
                                 ('t4', 'completed'), ('t5', 'failed')):
                if status == 'failed':
                    with self.assertRaises(subprocess.SubprocessError):
                        hook.publish(payload(turn_id=turn, request_id='req-' + turn), self.cfg, attempts=1)
                else:
                    hook.note_progress(payload(turn_id=turn, status='completed', error=''),
                                       self.cfg, hook._hook_env(self.cfg))
            online['yes'] = True
            for turn in ('t6', 't7', 't8'):
                hook.note_progress(payload(turn_id=turn, status='completed', error=''),
                                   self.cfg, hook._hook_env(self.cfg), attempts=1)
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-2', 'ep-3'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        for body, turn in zip(bodies, ('t1', 't3', 't5')):
            self.assertIn('turn=%s request=req-%s' % (turn, turn), body)

    def test_confirmed_replay_keeps_queued_tail(self):
        # 4c756a4 review finding 1: ep-1 pending with TWO queued outages
        # (t3 as ep-2, t5 as ep-3 across two offline boundaries). A later
        # replay of ep-1 succeeds: the open rewrite must keep the queue
        # tail, so ep-2 AND ep-3 both still deliver. Exactly ep-1/2/3.
        submits = []
        online = {'yes': False}
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            if not online['yes']:
                raise subprocess.TimeoutExpired(argv, 5)
            submits.append(argv)
            key = argv[argv.index('--request-key') + 1]
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            for turn, status in (('t1', 'failed'), ('t2', 'completed'), ('t3', 'failed'),
                                 ('t4', 'completed'), ('t5', 'failed')):
                if status == 'failed':
                    with self.assertRaises(subprocess.SubprocessError):
                        hook.publish(payload(turn_id=turn, request_id='req-' + turn), self.cfg, attempts=1)
                else:
                    hook.note_progress(payload(turn_id=turn, status='completed', error=''),
                                       self.cfg, hook._hook_env(self.cfg), attempts=1)
            online['yes'] = True
            # Replay of ep-1 succeeds on a failure turn (not healthy):
            # ep-1 confirms, queued ep-2 publishes, queued ep-3 stays.
            sixth = hook.publish(payload(turn_id='t6', request_id='req-6'), self.cfg, attempts=1)
            self.assertEqual(sixth['episode_id'], 'ep-2')
            seventh = hook.note_progress(payload(turn_id='t7', status='completed', error=''),
                                         self.cfg, hook._hook_env(self.cfg), attempts=1)
            self.assertTrue(seventh)
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-2', 'ep-3'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertIn('turn=t1 request=req-t1', bodies[0])
        self.assertIn('turn=t3 request=req-t3', bodies[1])
        self.assertIn('turn=t5 request=req-t5', bodies[2])

    def test_closed_row_with_queued_keeps_queue_and_new_outage(self):
        # 4c756a4 review finding 2: ep-1 commits on a healthy flush but
        # its queued ep-2 delivery fails (attempts=1). The row is closed
        # with queued ep-2 still named. The next failure must NOT adopt
        # the queued payload as an orphan: the queue survives and the
        # live failure preserves as the next outage. t6 healthy then
        # delivers queued ep-2; the live outage follows under ep-3.
        submits = []
        online = {'yes': False}
        fail_queued_once = {'yes': True}
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            if not online['yes']:
                raise subprocess.TimeoutExpired(argv, 5)
            key = argv[argv.index('--request-key') + 1]
            if fail_queued_once['yes'] and key == 'ep-2' and len(submits) == 1:
                raise subprocess.TimeoutExpired(argv, 5)
            submits.append(argv)
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t1', request_id='req-1'), self.cfg, attempts=1)
            hook.note_progress(payload(turn_id='t2', status='completed', error=''),
                               self.cfg, hook._hook_env(self.cfg), attempts=1)
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
            online['yes'] = True
            # t4 healthy commits ep-1; queued ep-2 fails its single shot.
            self.assertTrue(hook.note_progress(
                payload(turn_id='t4', status='completed', error=''), self.cfg,
                hook._hook_env(self.cfg), attempts=1))
            closed = json.loads((self.root / 'state' / 'failure-episodes.json').read_text())
            self.assertEqual(closed['DISPATCH-1|1|worker-native'].get('queued_outage'), ['ep-2'])
            fail_queued_once['yes'] = False
            # t5 failure: queue survives, live outage preserved as ep-3.
            fifth = hook.publish(payload(turn_id='t5', request_id='req-5'), self.cfg, attempts=1)
            self.assertEqual(fifth['episode_id'], 'ep-3')
            sixth = hook.note_progress(payload(turn_id='t6', status='completed', error=''),
                                       self.cfg, hook._hook_env(self.cfg), attempts=1)
            self.assertTrue(sixth)
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-2', 'ep-3'])

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
