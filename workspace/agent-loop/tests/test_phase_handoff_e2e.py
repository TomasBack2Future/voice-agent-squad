"""Real binary, real receiver: a phase result moves the assignment to its next step.

Isolated ledger (temporary SQUAD_HOME and repository), no network, no Issue,
no deployment, no model call. Reproduces the 2026-11-07 shape: the Worker
merged under a Dispatcher merge release and reported it with a plain message.
The Dispatcher's real `terminal-events listen` must deliver a wake for that
report; the Dispatcher consumes it with a new decision; the Worker reads the
decision back and submits the next phase result through `submit`, which the
receiver delivers again. A receiver restart between delivery and handling
redelivers without duplicating the result.
"""
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
import unittest

ROOT = Path(__file__).parents[1]
REPO = ROOT.parents[1]


class PhaseHandoffEndToEnd(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build = tempfile.TemporaryDirectory()
        cls.binary = Path(cls.build.name) / 'squad'
        subprocess.run(['go', 'build', '-o', str(cls.binary), './cmd/squad'], cwd=REPO, check=True, capture_output=True)

    @classmethod
    def tearDownClass(cls):
        cls.build.cleanup()

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.env = {k: v for k, v in os.environ.items() if not k.startswith(('SQUAD_', 'CODEX_', 'CLAUDE_', 'MUSE_'))}
        self.env.update(SQUAD_HOME=str(self.root / 'home'), SQUAD_NO_AUTO_DAEMON='1', SQUAD_NO_BROWSER='1', SQUAD_NO_HYGIENE='1')
        subprocess.run(['git', 'init', str(self.root)], check=True, capture_output=True)
        self.squad('dispatcher', 'init', '--yes')
        settings = self.root / '.squad/config.yaml'
        settings.write_text(settings.read_text().replace('default_worktree_per_claim: true', 'default_worktree_per_claim: false'))
        self.squad('dispatcher', 'register', '--as', 'dispatcher')
        self.squad('dispatcher', 'dispatch', 'reserve', 'D-1', '--source', 'github:example/repo#1')
        self.squad('dispatcher', 'dispatch', 'controller-bind', '--native-session', 'dispatcher-native')
        self.squad('dispatcher', 'new', 'BUG', 'Isolated handoff fixture', '--ready')
        self.squad('dispatcher', 'dispatch', 'attach', 'D-1', '--item', 'BUG-001', '--generation', '1')
        self.squad('dispatcher', 'dispatch', 'bind', 'D-1', '--thread-id', 'worker-native', '--generation', '1', '--supervised')
        self.squad('worker', 'register', '--as', 'worker')
        self.squad('worker', 'claim', 'BUG-001')

    def squad(self, actor, *args, check=True):
        env = dict(self.env, SQUAD_AGENT=actor, SQUAD_SESSION_ID='test:' + actor)
        if actor == 'dispatcher':
            env['SQUAD_NATIVE_SESSION_ID'] = 'dispatcher-native'
        result = subprocess.run([str(self.binary), *args], cwd=self.root, env=env, capture_output=True, text=True, timeout=30)
        if check:
            self.assertEqual(result.returncode, 0, result.stderr)
        return result

    def db(self):
        return sqlite3.connect(self.root / 'home/global.db')

    def latest(self, agent):
        with self.db() as db:
            return db.execute("SELECT max(id) FROM messages WHERE agent_id=? AND thread='BUG-001'", (agent,)).fetchone()[0]

    def decide(self, action, expected, text):
        self.squad('dispatcher', 'milestone', '--to', 'BUG-001', text)
        argv = ['terminal-events', 'decision-set', '--reservation', 'D-1', '--generation', '1', '--worker-session', 'worker-native',
                '--expected-revision', str(expected), '--outcome', str(self.latest('dispatcher')), '--action', action]
        if action == 'hold':
            argv += ['--condition', text]
        self.squad('dispatcher', *argv)
        return json.loads(self.squad('worker', 'terminal-events', 'decision-get', '--reservation', 'D-1', '--generation', '1',
                                     '--worker-session', 'worker-native').stdout)

    def listen(self, incarnation, seconds=20):
        """Run the Dispatcher's real native receiver once; it exits 2 with the delivered event IDs."""
        config = self.root / f'receiver-{incarnation}.json'
        config.write_text(json.dumps({'native_session_id': 'dispatcher-native', 'agent_id': 'dispatcher', 'role': 'dispatcher',
                                      'controller_epoch': 1, 'state_directory': str(self.root / 'receiver-state'),
                                      'ledger_directory': str(self.root), 'squad_executable': str(self.binary),
                                      'incarnation': incarnation, 'owner_pid': os.getpid(), 'max_seconds': seconds}))
        p = subprocess.Popen([sys.executable, str(ROOT / 'terminal_receiver.py'), '--config', str(config)], env=self.env,
                             stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            _, err = p.communicate(json.dumps({'session_id': 'dispatcher-native'}), timeout=seconds + 15)
        finally:
            if p.poll() is None:
                p.kill()
                p.wait()
        with self.db() as db:
            rows = db.execute("SELECT event_id,kind,outcome_id FROM terminal_event_receipts WHERE recipient='dispatcher' AND processed_at=0").fetchall()
        delivered = [{'event_id': e, 'kind': k, 'outcome_id': o} for e, k, o in rows if e in err]
        return delivered, (p.returncode, err[-400:])

    def test_plain_post_merge_report_reaches_the_dispatcher_and_the_next_step_runs(self):
        released = self.decide('proceed', 0, 'Merge release for PR #1 (fixture).')
        # The release row decision-set writes after its live PR check (no network here).
        with self.db() as db:
            item = db.execute("SELECT canonical_item_id FROM dispatch_reservations WHERE item_id='D-1'").fetchone()[0]
            db.execute("INSERT INTO dispatch_merge_releases(repo_id,reservation_key,generation,item_id,decision_revision,pr_repo,pr_number,head_sha,base_sha,evidence_outcome,expires_at)"
                       " SELECT repo_id,'D-1',1,?,?,'example/repo',1,?,?,?,9999999999 FROM dispatch_reservations WHERE item_id='D-1'",
                       (item, released['revision'], 'a' * 40, 'b' * 40, released['outcome_id']))
        # 2026-11-07: the Worker merged and wrote a plain message.
        self.squad('worker', 'say', '--to', 'BUG-001', 'MERGED PR #1 at ' + 'c' * 40 + '; not deployed.')
        report = self.latest('worker')
        events, err = self.listen('dispatcher-incarnation-1')
        wake = [e for e in events if e['kind'] == 'reconcile-needed' and e['outcome_id'] == report]
        self.assertEqual(1, len(wake), (events, err))

        # Interrupted after delivery, before handling: a restarted receiver redelivers.
        events, _ = self.listen('dispatcher-incarnation-2')
        self.assertIn(wake[0]['event_id'], [e['event_id'] for e in events])

        # The Dispatcher consumes it: next decision, then acknowledgement.
        nxt = self.decide('proceed', released['revision'], 'Merged at cccc; next: acceptance on the deployed candidate.')
        self.squad('dispatcher', 'terminal-events', 'ack', wake[0]['event_id'], '--native-session', 'dispatcher-native', '--note', 'merge verified; revision %d published' % nxt['revision'])
        self.assertEqual(released['revision'] + 1, nxt['revision'])  # the Worker reads the new decision back

        # The Worker runs the next step and reports it formally in one call.
        body = self.root / 'acceptance.txt'
        body.write_text('acceptance: all checks ran and passed (fixture)')
        argv = ['terminal-events', 'submit', '--reservation', 'D-1', '--generation', '1', '--worker-session', 'worker-native',
                '--kind', 'phase-result', '--phase', 'acceptance:fixture', '--checks', 'all', '--verdict', 'passed',
                '--body-file', str(body), '--request-key', 'acceptance-fixture', '--expected-decision', str(nxt['revision'])]
        first = json.loads(self.squad('worker', *argv).stdout)
        retry = json.loads(self.squad('worker', *argv).stdout)  # lost response, same request
        self.assertEqual(first, retry)
        events, _ = self.listen('dispatcher-incarnation-3')
        self.assertEqual([first['event_id']], [e['event_id'] for e in events if e['kind'] == 'phase-result'])
        with self.db() as db:
            self.assertEqual(1, db.execute("SELECT count(*) FROM dispatch_phase_results").fetchone()[0])
        self.squad('dispatcher', 'terminal-events', 'ack', first['event_id'], '--native-session', 'dispatcher-native', '--note', 'acceptance result consumed')
            # Chatter after the answering decision does not wake again (release revoked).
        self.squad('worker', 'say', '--to', 'BUG-001', 'progress note')
        events, (code, err) = self.listen('dispatcher-incarnation-4', seconds=8)
        self.assertEqual([], events, err)
        self.assertIn('receiver stopped', err)  # lifetime ended with nothing to deliver
        self.assertNotIn('worker-terminal-v1/', err)


if __name__ == '__main__':
    unittest.main()
