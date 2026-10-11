"""Launcher submit_outcome against the real Submit decision contract.

The launcher must pass the actually observed decision revision: adopted
assignments submit with it, holds are rejected/held, stale revisions fail
precisely, and legacy unadopted assignments succeed without one. The launcher
never auto-refreshes or auto-proceeds.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).parents[1]
sys.path.insert(0, str(ROOT))
import muse_worker_launcher as launcher
from muse_worker_tools import child_environment
from validate_context_package import ValidationError


class SubmitOutcomeDecisionTests(unittest.TestCase):
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
        settings = self.root / '.squad/config.yaml'
        settings.write_text(settings.read_text().replace('default_worktree_per_claim: true', 'default_worktree_per_claim: false'))
        self.squad('dispatcher', 'register', '--as', 'dispatcher')
        self.squad('dispatcher', 'dispatch', 'reserve', 'D-1', '--source', 'github:example/repo#1')
        self.squad('dispatcher', 'dispatch', 'controller-bind', '--native-session', 'dispatcher-native')
        self.squad('dispatcher', 'new', 'BUG', 'Isolated submit fixture', '--ready')
        self.squad('dispatcher', 'dispatch', 'attach', 'D-1', '--item', 'BUG-001', '--generation', '1')
        self.squad('dispatcher', 'dispatch', 'bind', 'D-1', '--thread-id', 'worker-native', '--generation', '1', '--supervised')
        self.squad('worker', 'register', '--as', 'worker')
        self.squad('worker', 'claim', 'BUG-001')
        self.c = {'coordination_executable': str(self.binary), 'ledger_directory': str(self.root),
                  'coordination_home': str(self.root / 'home'), 'agent_id': 'worker'}
        self.pin = {'reservation': 'D-1', 'generation': 1, 'native': 'worker-native'}
        self.body = self.root / 'outcome.txt'
        self.body.write_text('factual outcome')

    def squad(self, actor, *args):
        env = dict(self.env, SQUAD_AGENT=actor, SQUAD_SESSION_ID='test:' + actor)
        result = subprocess.run([str(self.binary), *args], cwd=self.root, env=env,
                                capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout

    def launcher_env(self):
        return child_environment(dict(self.c, native_session_id='worker-native'))

    def latest_message(self, agent, thread):
        import sqlite3 as dbapi
        with dbapi.connect(self.root / 'home/global.db') as db:
            return db.execute("SELECT max(id) FROM messages WHERE agent_id=? AND thread=?", (agent, thread)).fetchone()[0]

    def adopt(self, action, expected_revision=0):
        self.squad('dispatcher', 'milestone', '--to', 'BUG-001', 'Dispatcher ' + action + '; bounded fixture only.')
        outcome = self.latest_message('dispatcher', 'BUG-001')
        argv = ['dispatcher', 'terminal-events', 'decision-set', '--reservation', 'D-1', '--generation', '1',
               '--worker-session', 'worker-native', '--expected-revision', str(expected_revision), '--outcome', str(outcome),
               '--action', action]
        if action == 'hold':
            argv += ['--condition', 'fixture hold; bounded test only']
        self.squad(*argv)
        return json.loads(self.squad('dispatcher', 'terminal-events', 'decision-get', '--reservation', 'D-1',
                                     '--generation', '1', '--worker-session', 'worker-native'))

    def test_adopted_proceed_submits(self):
        decision = self.adopt('proceed')
        submitted = launcher.submit_outcome(self.c, self.launcher_env(), self.pin, 'handoff-complete', self.body,
                                            decision_revision=decision['revision'])
        self.assertTrue(submitted['message_id'] and submitted['event_id'])

    def test_adopted_hold_is_rejected(self):
        decision = self.adopt('hold')
        with self.assertRaises(ValidationError) as ctx:
            launcher.submit_outcome(self.c, self.launcher_env(), self.pin, 'handoff-complete', self.body,
                                    decision_revision=decision['revision'])
        self.assertIn('hold', str(ctx.exception))

    def test_stale_revision_is_rejected_precisely(self):
        decision = self.adopt('proceed')
        self.squad('dispatcher', 'milestone', '--to', 'BUG-001', 'Dispatcher revised proceed; bounded fixture only.')
        outcome = self.latest_message('dispatcher', 'BUG-001')
        self.squad('dispatcher', 'terminal-events', 'decision-set', '--reservation', 'D-1', '--generation', '1',
                   '--worker-session', 'worker-native', '--expected-revision', str(decision['revision']),
                   '--outcome', str(outcome), '--action', 'proceed')
        with self.assertRaises(ValidationError) as ctx:
            launcher.submit_outcome(self.c, self.launcher_env(), self.pin, 'handoff-complete', self.body,
                                    decision_revision=decision['revision'])
        self.assertIn('stale-decision', str(ctx.exception))

    def test_legacy_unadopted_succeeds_without_decision(self):
        submitted = launcher.submit_outcome(self.c, self.launcher_env(), self.pin, 'handoff-complete', self.body)
        self.assertTrue(submitted['message_id'] and submitted['event_id'])

    def test_missing_revision_on_adopted_is_rejected(self):
        self.adopt('proceed')
        with self.assertRaises(ValidationError) as ctx:
            launcher.submit_outcome(self.c, self.launcher_env(), self.pin, 'handoff-complete', self.body)
        self.assertIn('stale-decision', str(ctx.exception))

    def test_distinct_request_keys_record_separate_events(self):
        env = self.launcher_env()
        first = launcher.submit_outcome(self.c, env, self.pin, 'decision-request', self.body, request_key='phase-a')
        retry = launcher.submit_outcome(self.c, env, self.pin, 'decision-request', self.body, request_key='phase-a')
        self.assertEqual(retry, first)
        body_b = self.root / 'outcome-b.txt'
        body_b.write_text('phase B question')
        second = launcher.submit_outcome(self.c, env, self.pin, 'decision-request', body_b, request_key='phase-b')
        self.assertNotEqual(second, first)
        body_changed = self.root / 'outcome-changed.txt'
        body_changed.write_text('changed body')
        with self.assertRaises(ValidationError) as ctx:
            launcher.submit_outcome(self.c, env, self.pin, 'decision-request', body_changed, request_key='phase-a')
        self.assertIn('payload-conflict', str(ctx.exception))

    def test_resumed_reports_have_distinct_stable_execution_keys(self):
        self.adopt('proceed')
        def publish(execution, summary):
            state = self.root / execution
            state.mkdir(exist_ok=True)
            (state / 'report.json').write_text(json.dumps({'status': 'completed', 'summary': summary}))
            return launcher.publish_report(self.c, self.launcher_env(), dict(self.pin, id=execution), state)
        first = publish('muse-first', 'new Worker verified source')
        self.assertEqual(publish('muse-first', 'new Worker verified source'), first)
        second = publish('muse-resumed', 'same native verified preserved source')
        self.assertNotEqual(second['event_id'], first['event_id'])
        with self.assertRaisesRegex(ValidationError, 'payload-conflict'):
            publish('muse-first', 'changed first result')

    def test_blocked_report_publishes_under_hold_without_releasing_it(self):
        decision = self.adopt('hold')
        state = self.root / 'held-execution'
        state.mkdir()
        report = state / 'report.json'
        report.write_text(json.dumps({'status': 'blocked', 'summary': 'owned tools stopped and joined'}))
        result = launcher.publish_report(self.c, self.launcher_env(), dict(self.pin, id='muse-held'), state)
        self.assertTrue(result['event_id'])
        current = launcher.read_decision(self.c, self.launcher_env(), self.pin)
        self.assertEqual(current, decision)
        report.write_text(json.dumps({'status': 'completed', 'summary': 'must reject completion'}))
        with self.assertRaisesRegex(ValidationError, 'hold'):
            launcher.publish_report(self.c, self.launcher_env(), dict(self.pin, id='muse-held-new'), state)

    def test_report_replays_after_publisher_crashes_after_commit(self):
        import sqlite3
        proceed = self.adopt('proceed')
        state = self.root / 'crashed-execution'
        state.mkdir()
        (state / 'report.json').write_text(json.dumps({'status': 'completed', 'summary': 'joined source verification'}))
        request = self.root / 'crashed-request.json'
        request.write_text(json.dumps({'config': self.c, 'pin': dict(self.pin, id='muse-crashed'), 'state': str(state)}))
        # The real child commits the outcome, then exits before its caller gets
        # a receipt. Recovery must use retained execution identity, not rerun
        # the source task or create another business completion.
        code = ('import json,os,sys; from pathlib import Path; '
                'sys.path.insert(0,sys.argv[1]); import muse_worker_launcher as launcher; '
                'from muse_worker_tools import child_environment; '
                'q=json.loads(Path(sys.argv[2]).read_text()); '
                'launcher.publish_report(q["config"],child_environment(dict(q["config"],native_session_id="worker-native")),q["pin"],Path(q["state"])); '
                'os._exit(73)')
        crashed = subprocess.run([sys.executable, '-c', code, str(ROOT), str(request)],
                                 cwd=self.root, env=self.env, capture_output=True, text=True, timeout=20)
        self.assertEqual(crashed.returncode, 73, crashed.stderr)
        self.assertFalse((state / 'outcome.json').exists())
        with sqlite3.connect(self.root / 'home/global.db') as db:
            committed = db.execute("SELECT outcome_id,event_id FROM terminal_event_receipts WHERE kind='handoff-complete'").fetchall()
        self.assertEqual(len(committed), 1)
        # A controller may hold the next phase after the irreversible commit.
        # Identical publication replay must still return its original receipt.
        self.adopt('hold', expected_revision=proceed['revision'])
        replay = launcher.publish_report(self.c, self.launcher_env(), dict(self.pin, id='muse-crashed'), state)
        self.assertEqual((replay['message_id'], replay['event_id']), committed[0])
        with sqlite3.connect(self.root / 'home/global.db') as db:
            self.assertEqual(db.execute("SELECT count(*) FROM terminal_event_receipts WHERE kind='handoff-complete'").fetchone()[0], 1)
            self.assertEqual(db.execute("SELECT count(*) FROM messages WHERE agent_id='worker' AND body='joined source verification'").fetchone()[0], 1)


if __name__ == '__main__':
    unittest.main()
