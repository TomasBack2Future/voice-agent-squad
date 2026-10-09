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
- If publication fails, the frozen episode stays in the outbox and is
  replayed for real on the next hook invocation or turn boundary. Delivery
  never opens or closes an episode; only native health signals do.
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

    def row(self):
        return json.loads((self.root / 'state' / 'failure-episodes.json').read_text())[
            'DISPATCH-1|1|worker-native']

    def outbox_ids(self):
        return [entry['episode_id'] for entry in self.row()['outbox']]

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
            hook.note_progress(payload(turn_id='turn-2', status='success', error=''), self.cfg)
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
                                       status='success', error=''), self.cfg)
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
        # Simulate a lost receipt: the frozen ep-1 payload is still in
        # the outbox while the episode stays allocated and open.
        lost = hook.observe(payload(), self.cfg)
        lost['episode_id'] = 'ep-1'
        data = json.loads((self.root / 'state' / 'failure-episodes.json').read_text())
        data['DISPATCH-1|1|worker-native']['outbox'] = [lost]
        (self.root / 'state' / 'failure-episodes.json').write_text(json.dumps(data))
        stub2 = ok_run({'event_id': 'e1', 'state': 'pending'})
        with mock.patch.object(hook.subprocess, 'run', side_effect=stub2):
            retry = hook.publish(payload(turn_id='turn-1b', request_id='req-1b'), self.cfg)
            self.assertEqual(retry['state'], 'pending')
            self.assertEqual(retry['episode_id'], 'ep-1')
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
            healthy = payload(turn_id='turn-2', status='success', error='')
            self.assertTrue(hook.note_progress(healthy, self.cfg, hook._hook_env(self.cfg)))
            second = hook.publish(payload(turn_id='turn-3', request_id='req-3'), self.cfg)
            self.assertEqual(second['state'], 'pending')
        self.assertEqual(len(submits), 3)
        # Flushed ep-1 kept its request key; new outage is ep-2.
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-1', 'ep-2'])

    def test_failed_replay_after_boundary_keeps_boundary_and_new_outage(self):
        # 7a01892 review finding 1: ep-1 undelivered, healthy t2 ends its
        # outage although the t2 flush fails, then the t3 replay ALSO
        # fails. The boundary is already durable, so t3 opened ep-2 with
        # its own frozen body (one key, one body: 11a67a2 review); t3/t4
        # are one continuous outage. Exactly ep-1 then ep-2 are used.
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
            healthy = payload(turn_id='t2', status='success', error='')
            self.assertTrue(hook.note_progress(healthy, self.cfg, hook._hook_env(self.cfg)))
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
            self.assertEqual(self.row()['closed_by_turn'], 't2')
            self.assertTrue(self.row()['episode_open'])
            self.assertEqual(self.outbox_ids(), ['ep-1', 'ep-2'])
            fourth = hook.publish(payload(turn_id='t4', request_id='req-4'), self.cfg, attempts=1)
            self.assertEqual(fourth['episode_id'], 'ep-2')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-1', 'ep-1', 'ep-1', 'ep-2'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertIn('turn=t1 request=req-1', bodies[3])
        self.assertIn('turn=t3 request=req-3', bodies[4])

    def test_post_boundary_outage_survives_offline_replay_and_healthy_recovery(self):
        # D84-13: t1 fails offline, healthy t2 ends that outage although
        # its flush fails, t3 fails while still offline and freezes ep-2.
        # Transport recovers at healthy t4, which also ends the t3
        # outage: ep-1 and ep-2 both commit, and t5 is a NEW ep-3.
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
            healthy2 = payload(turn_id='t2', status='success', error='')
            self.assertTrue(hook.note_progress(healthy2, self.cfg, hook._hook_env(self.cfg)))
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
            online['yes'] = True
            healthy4 = payload(turn_id='t4', status='success', error='')
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
        # 4f43571 review finding 1: t1 undelivered, healthy t2 ends the
        # outage although its flush fails, healthy t3 delivers ep-1. A
        # later t4 outage whose first submit fails then replays: it is
        # ep-2 and must NOT spawn ep-3 from the old boundary. t5 dedupes.
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
            self.assertTrue(hook.note_progress(
                payload(turn_id='t2', status='success', error=''), self.cfg, hook._hook_env(self.cfg)))
            self.assertFalse(hook.note_progress(
                payload(turn_id='t3', status='success', error=''), self.cfg, hook._hook_env(self.cfg)))
            self.assertEqual(self.outbox_ids(), [])
            self.assertFalse(self.row()['episode_open'])
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t4', request_id='req-4'), self.cfg, attempts=1)
            fifth = hook.publish(payload(turn_id='t5', request_id='req-5'), self.cfg, attempts=1)
            self.assertEqual(fifth['episode_id'], 'ep-2')
            sixth = hook.publish(payload(turn_id='t6', request_id='req-6'), self.cfg, attempts=1)
            self.assertEqual(sixth['state'], 'duplicate')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-1', 'ep-1', 'ep-2', 'ep-2'])

    def test_flush_close_saves_snapshot_before_dropping_payload(self):
        # 4f43571 review finding 2: a healthy flush whose Submit commits
        # but whose delivery record fails must keep the frozen payload,
        # so a later turn replays it — never a committed server row with
        # no local body. The outage end itself was recorded first.
        stub = ok_run({'event_id': 'e1', 'state': 'pending'})
        with mock.patch.object(hook.subprocess, 'run', side_effect=subprocess.TimeoutExpired('squad', 5)):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(), self.cfg, attempts=1)
        real_save = hook._save
        def failing_save(config, data):
            if not data['DISPATCH-1|1|worker-native']['outbox']:
                raise OSError('isolated snapshot failure fixture')
            return real_save(config, data)
        with mock.patch.object(hook.subprocess, 'run', side_effect=stub):
            with mock.patch.object(hook, '_save', side_effect=failing_save):
                healthy = payload(turn_id='turn-2', status='success', error='')
                with self.assertRaises(OSError):
                    hook.note_progress(healthy, self.cfg, hook._hook_env(self.cfg))
        self.assertEqual(len(stub.submits), 1)
        self.assertFalse(self.row()['episode_open'])
        self.assertEqual(self.outbox_ids(), ['ep-1'])
        self.assertEqual(self.row()['outbox'][0]['turn_id'], 'turn-1')
        with mock.patch.object(hook.subprocess, 'run', side_effect=stub):
            healthy = payload(turn_id='turn-3', status='success', error='')
            self.assertFalse(hook.note_progress(healthy, self.cfg, hook._hook_env(self.cfg)))
        self.assertEqual(self.outbox_ids(), [])
        self.assertEqual(submit_bodies(stub)[0], submit_bodies(stub)[1])

    def test_queued_outage_wins_over_live_body_on_same_key(self):
        # 11a67a2 review: t1 undelivered, healthy t2 ends it (flush
        # fails), t3 freezes ep-2 (replay fails). The t4 replay SUCCEEDS
        # and must submit the FROZEN t3 body under ep-2, never the live
        # t4 body — one key carries exactly one body, so a later
        # lost-reply recovery can never hit a payload-conflict.
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
            self.assertTrue(hook.note_progress(
                payload(turn_id='t2', status='success', error=''), self.cfg, hook._hook_env(self.cfg)))
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
            fourth = hook.publish(payload(turn_id='t4', request_id='req-4'), self.cfg, attempts=1)
            self.assertEqual(fourth['episode_id'], 'ep-2')
            # D84-16: t3-t6 are one continuous outage. Delivering ep-2
            # is transport recovery, not native health, so t5 dedupes.
            fifth = hook.publish(payload(turn_id='t5', request_id='req-5'), self.cfg, attempts=1)
            self.assertEqual(fifth['state'], 'duplicate')
            sixth = hook.publish(payload(turn_id='t6', request_id='req-6'), self.cfg, attempts=1)
            self.assertEqual(sixth['state'], 'duplicate')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-1', 'ep-1', 'ep-1', 'ep-2'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertIn('turn=t1 request=req-1', bodies[3])
        # ep-2 carries the frozen t3 body, not the live t4 body.
        self.assertIn('turn=t3 request=req-3', bodies[4])
        self.assertNotIn('turn=t4', bodies[4])

    def test_queued_flush_never_reopens_new_failure_publishes(self):
        # D84-15 seq 1: t1 fails offline, t2 healthy offline, t3 fails
        # offline. Online at t4 healthy: ep-1 and ep-2 both commit
        # and t4 ends the t3 outage, so the t5 failure publishes as a NEW
        # ep-3 outage. Three events.
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
            self.assertTrue(hook.note_progress(
                payload(turn_id='t2', status='success', error=''), self.cfg, hook._hook_env(self.cfg)))
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
            online['yes'] = True
            self.assertTrue(hook.note_progress(
                payload(turn_id='t4', status='success', error=''), self.cfg, hook._hook_env(self.cfg)))
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
            for turn, status in (('t1', 'failed'), ('t2', 'success'), ('t3', 'failed'),
                                 ('t4', 'success'), ('t5', 'failed')):
                if status == 'failed':
                    with self.assertRaises(subprocess.SubprocessError):
                        hook.publish(payload(turn_id=turn, request_id='req-' + turn), self.cfg, attempts=1)
                else:
                    hook.note_progress(payload(turn_id=turn, status='success', error=''),
                                       self.cfg, hook._hook_env(self.cfg))
            online['yes'] = True
            for turn in ('t6', 't7', 't8'):
                hook.note_progress(payload(turn_id=turn, status='success', error=''),
                                   self.cfg, hook._hook_env(self.cfg), attempts=1)
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-2', 'ep-3'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        for body, turn in zip(bodies, ('t1', 't3', 't5')):
            self.assertIn('turn=%s request=req-%s' % (turn, turn), body)

    def test_confirmed_replay_keeps_queued_tail(self):
        # 4c756a4 review finding 1: ep-1 undelivered with TWO later
        # outages (t3 as ep-2, t5 as ep-3 across two offline boundaries).
        # A later replay of ep-1 succeeds: ep-2 AND ep-3 both still
        # deliver. Exactly ep-1/2/3.
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
            for turn, status in (('t1', 'failed'), ('t2', 'success'), ('t3', 'failed'),
                                 ('t4', 'success'), ('t5', 'failed')):
                if status == 'failed':
                    with self.assertRaises(subprocess.SubprocessError):
                        hook.publish(payload(turn_id=turn, request_id='req-' + turn), self.cfg, attempts=1)
                else:
                    hook.note_progress(payload(turn_id=turn, status='success', error=''),
                                       self.cfg, hook._hook_env(self.cfg), attempts=1)
            online['yes'] = True
            # Replay of ep-1 succeeds on a failure turn (not healthy):
            # ep-1, ep-2 AND ep-3 drain on the same turn (9c583c4 review:
            # nothing strands while the outage continues). Healthy t7
            # only ends the t5-t6 outage; nothing is left to deliver.
            sixth = hook.publish(payload(turn_id='t6', request_id='req-6'), self.cfg, attempts=1)
            self.assertEqual(sixth['episode_id'], 'ep-3')
            seventh = hook.note_progress(payload(turn_id='t7', status='success', error=''),
                                         self.cfg, hook._hook_env(self.cfg), attempts=1)
            self.assertTrue(seventh)
            self.assertEqual(self.outbox_ids(), [])
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-2', 'ep-3'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertIn('turn=t1 request=req-t1', bodies[0])
        self.assertIn('turn=t3 request=req-t3', bodies[1])
        self.assertIn('turn=t5 request=req-t5', bodies[2])

    def test_closed_row_with_queued_keeps_queue_and_new_outage(self):
        # 4c756a4 review finding 2: ep-1 commits on a healthy flush but
        # ep-2 delivery fails (attempts=1). The healthy t4 ended the t3
        # outage with ep-2 still undelivered. The next failure is a new
        # ep-3 outage: ep-2 delivers first with its own frozen body,
        # then ep-3. t6 healthy ends the t5 outage.
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
            hook.note_progress(payload(turn_id='t2', status='success', error=''),
                               self.cfg, hook._hook_env(self.cfg), attempts=1)
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
            online['yes'] = True
            # t4 healthy commits ep-1; queued ep-2 fails its single shot.
            self.assertTrue(hook.note_progress(
                payload(turn_id='t4', status='success', error=''), self.cfg,
                hook._hook_env(self.cfg), attempts=1))
            self.assertFalse(self.row()['episode_open'])
            self.assertEqual(self.outbox_ids(), ['ep-2'])
            fail_queued_once['yes'] = False
            # t5 failure: ep-2 survives, live outage frozen as ep-3.
            fifth = hook.publish(payload(turn_id='t5', request_id='req-5'), self.cfg, attempts=1)
            self.assertEqual(fifth['episode_id'], 'ep-3')
            sixth = hook.note_progress(payload(turn_id='t6', status='success', error=''),
                                       self.cfg, hook._hook_env(self.cfg), attempts=1)
            self.assertTrue(sixth)
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-2', 'ep-3'])

    def test_failure_turn_recovery_delivers_whole_tail(self):
        # 9c583c4 review: ep-1 undelivered with ep-2/ep-3 across two
        # offline boundaries. Transport recovers ON A FAILURE TURN: t6
        # delivers ep-1, ep-2 AND ep-3 — nothing strands behind the open
        # outage. Exactly ep-1, ep-2, ep-3.
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
            for turn, status in (('t1', 'failed'), ('t2', 'success'), ('t3', 'failed'),
                                 ('t4', 'success'), ('t5', 'failed')):
                if status == 'failed':
                    with self.assertRaises(subprocess.SubprocessError):
                        hook.publish(payload(turn_id=turn, request_id='req-' + turn), self.cfg, attempts=1)
                else:
                    hook.note_progress(payload(turn_id=turn, status='success', error=''),
                                       self.cfg, hook._hook_env(self.cfg), attempts=1)
            online['yes'] = True
            sixth = hook.publish(payload(turn_id='t6', request_id='req-6'), self.cfg, attempts=1)
            self.assertEqual(sixth['episode_id'], 'ep-3')
            # All three outages drained on t6. The still-down model keeps
            # failing with no healthy turn since t5, so t7 and t8 belong
            # to the ep-3 outage (D84-16) and dedupe.
            seventh = hook.publish(payload(turn_id='t7', request_id='req-7'), self.cfg, attempts=1)
            self.assertEqual(seventh['state'], 'duplicate')
            eighth = hook.publish(payload(turn_id='t8', request_id='req-8'), self.cfg, attempts=1)
            self.assertEqual(eighth['state'], 'duplicate')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-2', 'ep-3'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertIn('turn=t1 request=req-t1', bodies[0])
        self.assertIn('turn=t3 request=req-t3', bodies[1])
        self.assertIn('turn=t5 request=req-t5', bodies[2])

    def test_replay_success_after_failed_head_drains_tail(self):
        # c3b0d81 review: ep-1 undelivered with ep-2/ep-3 behind it. The
        # t6 failure turn delivers ep-1 but ALL ep-2 submits fail. The t7
        # replay of ep-2 succeeds and ep-3 drains on that same failure
        # turn. Exactly ep-1/2/3.
        submits = []
        online = {'yes': False}
        fail_ep2 = {'yes': True}
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            if not online['yes']:
                raise subprocess.TimeoutExpired(argv, 5)
            key = argv[argv.index('--request-key') + 1]
            if fail_ep2['yes'] and key == 'ep-2' and len(submits) == 1:
                raise subprocess.TimeoutExpired(argv, 5)
            submits.append(argv)
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            for turn, status in (('t1', 'failed'), ('t2', 'success'), ('t3', 'failed'),
                                 ('t4', 'success'), ('t5', 'failed')):
                if status == 'failed':
                    with self.assertRaises(subprocess.SubprocessError):
                        hook.publish(payload(turn_id=turn, request_id='req-' + turn), self.cfg, attempts=1)
                else:
                    hook.note_progress(payload(turn_id=turn, status='success', error=''),
                                       self.cfg, hook._hook_env(self.cfg), attempts=1)
            online['yes'] = True
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t6', request_id='req-6'), self.cfg, attempts=1)
            fail_ep2['yes'] = False
            seventh = hook.publish(payload(turn_id='t7', request_id='req-7'), self.cfg, attempts=1)
            self.assertEqual(seventh['episode_id'], 'ep-3')
            # D84-18: t6-t9 are one outage that began at t5; replay
            # success is transport recovery and never opens ep-4.
            eighth = hook.publish(payload(turn_id='t8', request_id='req-8'), self.cfg, attempts=1)
            self.assertEqual(eighth['state'], 'duplicate')
            ninth = hook.publish(payload(turn_id='t9', request_id='req-9'), self.cfg, attempts=1)
            self.assertEqual(ninth['state'], 'duplicate')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-2', 'ep-3'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertIn('turn=t1 request=req-t1', bodies[0])
        self.assertIn('turn=t3 request=req-t3', bodies[1])
        self.assertIn('turn=t5 request=req-t5', bodies[2])

    def test_interrupted_boundary_confirm_recovers_live_outage(self):
        # 1b417f3 review finding 1: the replay of ep-1 commits, but the
        # process dies (OSError on the delivery record) before ep-2 is
        # submitted. Recovery must NOT strand the post-boundary outage:
        # the next failure turn replays ep-1 identically and delivers
        # ep-2 with its first actual t3 observation. t4-t6 stay one
        # outage, so nothing new is allocated.
        submits = []
        online = {'yes': False}
        kill_once = {'yes': True}
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            if not online['yes']:
                raise subprocess.TimeoutExpired(argv, 5)
            submits.append(argv)
            key = argv[argv.index('--request-key') + 1]
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        real_save = hook._save
        def dying_save(config, data):
            if kill_once['yes'] and 'ep-1' not in [entry['episode_id'] for entry in
                                                   data['DISPATCH-1|1|worker-native']['outbox']]:
                kill_once['yes'] = False
                raise OSError('isolated crash between commit and delivery record')
            return real_save(config, data)
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            for turn, status in (('t1', 'failed'), ('t2', 'success'), ('t3', 'failed')):
                if status == 'failed':
                    with self.assertRaises(subprocess.SubprocessError):
                        hook.publish(payload(turn_id=turn, request_id='req-' + turn), self.cfg, attempts=1)
                else:
                    hook.note_progress(payload(turn_id=turn, status='success', error=''),
                                       self.cfg, hook._hook_env(self.cfg), attempts=1)
            online['yes'] = True
            with mock.patch.object(hook, '_save', side_effect=dying_save):
                with self.assertRaises(OSError):
                    hook.publish(payload(turn_id='t4', request_id='req-4'), self.cfg, attempts=1)
            self.assertEqual(self.outbox_ids(), ['ep-1', 'ep-2'])
            fifth = hook.publish(payload(turn_id='t5', request_id='req-5'), self.cfg, attempts=1)
            self.assertEqual(fifth['episode_id'], 'ep-2')
            sixth = hook.publish(payload(turn_id='t6', request_id='req-6'), self.cfg, attempts=1)
            self.assertEqual(sixth['state'], 'duplicate')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-1', 'ep-2'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertEqual(bodies[0], bodies[1])
        self.assertIn('turn=t1 request=req-t1', bodies[0])
        self.assertIn('turn=t3 request=req-t3', bodies[2])

    def test_interrupted_boundary_confirm_without_queue_recovers_live_outage(self):
        # d714909 review: t1 fails offline, healthy t2 ends that outage
        # with nothing else frozen yet (the common D84-11 shape). The t3
        # failure freezes ep-2 and its replay of ep-1 commits, but the
        # process dies before recording that delivery. The t4 failure
        # must NOT strand ep-2: it replays ep-1 identically and delivers
        # ep-2 with the first post-boundary t3 observation. t5 dedupes.
        submits = []
        online = {'yes': False}
        kill_once = {'yes': True}
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            if not online['yes']:
                raise subprocess.TimeoutExpired(argv, 5)
            submits.append(argv)
            key = argv[argv.index('--request-key') + 1]
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        real_save = hook._save
        def dying_save(config, data):
            if kill_once['yes'] and 'ep-1' not in [entry['episode_id'] for entry in
                                                   data['DISPATCH-1|1|worker-native']['outbox']]:
                kill_once['yes'] = False
                raise OSError('isolated crash between commit and delivery record')
            return real_save(config, data)
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t1', request_id='req-1'), self.cfg, attempts=1)
            hook.note_progress(payload(turn_id='t2', status='success', error=''),
                               self.cfg, hook._hook_env(self.cfg), attempts=1)
            online['yes'] = True
            with mock.patch.object(hook, '_save', side_effect=dying_save):
                with self.assertRaises(OSError):
                    hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
            self.assertTrue(self.row()['episode_open'])
            self.assertEqual(self.outbox_ids(), ['ep-1', 'ep-2'])
            fourth = hook.publish(payload(turn_id='t4', request_id='req-4'), self.cfg, attempts=1)
            self.assertEqual(fourth['episode_id'], 'ep-2')
            fifth = hook.publish(payload(turn_id='t5', request_id='req-5'), self.cfg, attempts=1)
            self.assertEqual(fifth['state'], 'duplicate')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-1', 'ep-2'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertEqual(bodies[0], bodies[1])
        self.assertIn('turn=t1 request=req-1', bodies[0])
        # The first actual post-boundary failure, never a later turn.
        self.assertIn('turn=t3 request=req-3', bodies[2])

    def test_open_row_close_clears_stale_boundary(self):
        # 1b417f3 review finding 1 (second half): only the open flag
        # decides allocation, so a stale field left by an older head can
        # never make the next episode republish spuriously.
        stub = ok_run({'event_id': 'e1', 'state': 'pending'})
        with mock.patch.object(hook.subprocess, 'run', side_effect=stub):
            hook.publish(payload(), self.cfg, attempts=1)
            data = json.loads((self.root / 'state' / 'failure-episodes.json').read_text())
            key = 'DISPATCH-1|1|worker-native'
            data[key]['healthy_boundary'] = 'stale-turn'
            (self.root / 'state' / 'failure-episodes.json').write_text(json.dumps(data))
            self.assertTrue(hook.note_progress(
                payload(turn_id='t2', status='success', error=''), self.cfg, hook._hook_env(self.cfg)))
            self.assertEqual(self.row()['closed_by_turn'], 't2')
            next_outage = hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
            self.assertEqual(next_outage['episode_id'], 'ep-2')
            repeat = hook.publish(payload(turn_id='t4', request_id='req-4'), self.cfg, attempts=1)
            self.assertEqual(repeat['state'], 'duplicate')
        self.assertEqual(len(stub.submits), 2)

    def test_open_row_with_queued_drains_on_failure_turn(self):
        # 2813cac review, D84-18: ep-1 undelivered with ep-2/ep-3 behind
        # it. t6 delivers ep-1 and ep-2; ep-3 fails at t6, t7 and t7b.
        # Every failure turn retries it instead of short-circuiting to
        # duplicate, and t8 delivers it. t6-t9 are one outage that began
        # at t5, so no ep-4/ep-5 is ever allocated.
        submits = []
        online = {'yes': False}
        fail_keys = {'ep-3': 1}
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            if not online['yes']:
                raise subprocess.TimeoutExpired(argv, 5)
            key = argv[argv.index('--request-key') + 1]
            if fail_keys.get(key, 0) > 0:
                fail_keys[key] -= 1
                raise subprocess.TimeoutExpired(argv, 5)
            submits.append(argv)
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            for turn, status in (('t1', 'failed'), ('t2', 'success'), ('t3', 'failed'),
                                 ('t4', 'success'), ('t5', 'failed')):
                if status == 'failed':
                    with self.assertRaises(subprocess.SubprocessError):
                        hook.publish(payload(turn_id=turn, request_id='req-' + turn), self.cfg, attempts=1)
                else:
                    hook.note_progress(payload(turn_id=turn, status='success', error=''),
                                       self.cfg, hook._hook_env(self.cfg), attempts=1)
            online['yes'] = True
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t6', request_id='req-6'), self.cfg, attempts=1)
            self.assertEqual(self.outbox_ids(), ['ep-3'])
            for turn in ('t7', 't7b'):
                fail_keys['ep-3'] = 1
                with self.assertRaises(subprocess.SubprocessError):
                    hook.publish(payload(turn_id=turn, request_id='req-' + turn), self.cfg, attempts=1)
            self.assertEqual(self.outbox_ids(), ['ep-3'])
            self.assertTrue(self.row()['episode_open'])
            eighth = hook.publish(payload(turn_id='t8', request_id='req-8'), self.cfg, attempts=1)
            self.assertEqual(eighth['episode_id'], 'ep-3')
            ninth = hook.publish(payload(turn_id='t9', request_id='req-9'), self.cfg, attempts=1)
            self.assertEqual(ninth['state'], 'duplicate')
        keys = [argv[argv.index('--request-key') + 1] for argv in submits]
        self.assertEqual(keys, ['ep-1', 'ep-2', 'ep-3'])
        bodies = submit_bodies(type('S', (), {'submits': submits})())
        self.assertIn('turn=t1 request=req-t1', bodies[0])
        self.assertIn('turn=t3 request=req-t3', bodies[1])
        self.assertIn('turn=t5 request=req-t5', bodies[2])

    def test_pending_flush_failure_keeps_pending(self):
        stub = ok_run({'event_id': 'e1', 'state': 'pending'})
        with mock.patch.object(hook.subprocess, 'run', side_effect=subprocess.TimeoutExpired('squad', 5)):
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(), self.cfg, attempts=1)
        with mock.patch.object(hook.subprocess, 'run', side_effect=subprocess.TimeoutExpired('squad', 5)):
            healthy = payload(turn_id='turn-2', status='success', error='')
            # The healthy turn ends the outage even though its flush
            # fails; the frozen ep-1 payload stays undelivered.
            self.assertTrue(hook.note_progress(healthy, self.cfg, hook._hook_env(self.cfg)))
        self.assertEqual(self.outbox_ids(), ['ep-1'])
        with mock.patch.object(hook.subprocess, 'run', side_effect=stub):
            replayed = hook.publish(payload(turn_id='turn-3', request_id='req-3'), self.cfg, attempts=1)
            self.assertEqual(replayed['state'], 'pending')
            self.assertEqual(replayed['episode_id'], 'ep-2')
        self.assertEqual([argv[argv.index('--request-key') + 1] for argv in stub.submits],
                         ['ep-1', 'ep-2'])

    def test_failed_flush_records_boundary_replay_closes_and_new_outage_republishes(self):
        # D84-11 finding 1: ep-1 stays undelivered and the healthy flush
        # at t2 fails, but t2 still ends the outage durably. The t3
        # failure is a NEW ep-2 episode, delivered after the ep-1 replay.
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
            healthy = payload(turn_id='t2', status='success', error='')
            self.assertTrue(hook.note_progress(healthy, self.cfg, hook._hook_env(self.cfg)))
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
        # D84-12: t1 fails (undelivered), healthy t2 ends it although its
        # flush fails, t3 opens ep-2 and delivers ep-1 but ALL ep-2
        # submits fail (lost replies), t4/t5 fail with no further healthy
        # turn. The t2 boundary ended only the t1 outage: t4 delivers ep-2
        # (no ep-3), t5 dedupes. Exactly the ep-1 and ep-2 keys are used.
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
            healthy = payload(turn_id='t2', status='success', error='')
            self.assertTrue(hook.note_progress(healthy, self.cfg, hook._hook_env(self.cfg)))
            with self.assertRaises(subprocess.SubprocessError):
                hook.publish(payload(turn_id='t3', request_id='req-3'), self.cfg, attempts=1)
            self.assertTrue(self.row()['episode_open'])
            self.assertEqual(self.row()['episode_id'], 'ep-2')
            self.assertEqual(self.outbox_ids(), ['ep-2'])
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
        # D84-11 finding 2: the process dies after the allocation save
        # and before any Submit. The frozen payload must replay
        # identically on the next publish, and — after a healthy turn
        # lands — the next outage must be a new episode.
        first = hook.observe(payload(turn_id='t1', request_id='req-1'), self.cfg)
        first['episode_id'] = 'ep-1'
        hook._save(self.cfg, {'DISPATCH-1|1|worker-native': {
            'sequence': 1, 'episode_id': 'ep-1', 'episode_open': True, 'outbox': [first]}})
        submits = []
        def script(argv, **kwargs):
            if argv[2] == 'decision-get':
                return subprocess.CompletedProcess(argv, 0, '{}\n', '')
            self.assertEqual(argv[2], 'submit')
            submits.append(argv)
            key = argv[argv.index('--request-key') + 1]
            return subprocess.CompletedProcess(argv, 0, json.dumps({'event_id': key, 'state': 'pending'}), '')
        with mock.patch.object(hook.subprocess, 'run', side_effect=script):
            # Same outage: no new ep-1 body from the live turn; the frozen
            # payload replays under its own key.
            receipt = hook.publish(payload(turn_id='t2x', request_id='req-2x'), self.cfg, attempts=1)
            self.assertEqual(receipt['episode_id'], 'ep-1')
            healthy = payload(turn_id='t2', status='success', error='')
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
