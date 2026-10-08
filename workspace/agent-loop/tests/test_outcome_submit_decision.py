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

    def adopt(self, action):
        self.squad('dispatcher', 'milestone', '--to', 'BUG-001', 'Dispatcher ' + action + '; bounded fixture only.')
        outcome = self.latest_message('dispatcher', 'BUG-001')
        argv = ['dispatcher', 'terminal-events', 'decision-set', '--reservation', 'D-1', '--generation', '1',
               '--worker-session', 'worker-native', '--expected-revision', '0', '--outcome', str(outcome),
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


if __name__ == '__main__':
    unittest.main()
