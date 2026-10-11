"""Real isolated CLI/store recovery through portable identities, not model tests.

All nine controller runtime transitions retain a legacy Worker without inventing
an execution pin. This deliberately does NOT qualify Worker replacement, native
idle wake, or deployment; those need their own native executors and evidence.
"""
import itertools
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).parents[1]
sys.path.insert(0, str(ROOT))
import runtime_entry
from validate_context_package import validate, ValidationError

RUNTIMES = ('claude', 'codex', 'muse')


class RecoveryMatrixTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build = tempfile.TemporaryDirectory(prefix='squad-recovery-build-')
        cls.addClassCleanup(cls.build.cleanup)
        cls.binary = Path(cls.build.name) / 'squad'
        subprocess.run(['go', 'build', '-o', str(cls.binary), './cmd/squad'],
                       cwd=ROOT.parents[1], check=True, capture_output=True, timeout=120)

    def setUp(self):
        scratch = tempfile.TemporaryDirectory(prefix='squad-recovery-matrix-')
        self.addCleanup(scratch.cleanup)
        self.root = Path(scratch.name)
        # Never inherit a remote service, actor, ledger or daemon configuration.
        self.env = {k: v for k, v in os.environ.items()
                    if not k.startswith('SQUAD_') and k not in runtime_entry.IDENTITIES}
        self.env.update(SQUAD_HOME=str(self.root / 'home'), SQUAD_NO_AUTO_DAEMON='1',
                        SQUAD_NO_BROWSER='1', SQUAD_NO_HYGIENE='1', PYTHONDONTWRITEBYTECODE='1')
        subprocess.run(['git', '-c', 'core.hooksPath=/dev/null', 'init', str(self.root)],
                       check=True, capture_output=True, timeout=15)
        self.clients = {}

    def call(self, actor, *args, reject=False):
        runtime, native = self.clients[actor]
        with patch.dict(os.environ, self.env, clear=True):
            env = runtime_entry.environment(runtime, native, actor, self.root)
        result = subprocess.run([str(self.binary), *args], cwd=self.root, env=env,
                                capture_output=True, text=True, timeout=15)
        if reject:
            self.assertNotEqual(result.returncode, 0, (args, result.stdout))
        else:
            self.assertEqual(result.returncode, 0, (args, result.stderr))
        return result.stdout

    def rows(self, sql, parameters=()):
        with sqlite3.connect(self.root / 'home/global.db') as db:
            return db.execute(sql, parameters).fetchall()

    def decision(self, actor, revision, action):
        self.call(actor, 'milestone', '--to', 'BUG-001', 'Fixture decision: ' + action)
        outcome = self.rows('SELECT max(id) FROM messages WHERE agent_id=?', (actor,))[0][0]
        args = ['terminal-events', 'decision-set', '--reservation', 'D-1', '--generation', '1',
                '--worker-session', 'worker-native', '--expected-revision', str(revision),
                '--outcome', str(outcome), '--action', action]
        if action == 'hold':
            args += ['--condition', 'explicit fixture pause']
        return json.loads(self.call(actor, *args))

    def get_decision(self):
        return json.loads(self.call('worker', 'terminal-events', 'decision-get',
                         '--reservation', 'D-1', '--generation', '1', '--worker-session', 'worker-native'))

    def submit(self, revision, key):
        return json.loads(self.call('worker', 'terminal-events', 'submit', '--reservation', 'D-1',
                         '--generation', '1', '--worker-session', 'worker-native',
                         '--kind', 'decision-request', '--body', 'fixture next phase ' + key,
                         '--expected-decision', str(revision), '--request-key', key))

    def handle(self, actor, event_id):
        runtime, native = self.clients[actor]
        delivery = json.loads(self.call(actor, 'terminal-events', 'listen', '--defer-delivery', '--max', '1s',
                             '--native-session', native, '--delivery-session', 'resume-1'))
        self.assertIn(event_id, [e['event_id'] for e in delivery['events']])
        self.assertEqual(self.rows('SELECT processed_at FROM terminal_event_receipts WHERE event_id=? AND recipient=?',
                                  (event_id, actor)), [(0,)])
        receipt = dict(schema_version='squad.handled-events.v1', runtime=runtime,
                       native_session=native, agent=actor, delivery_session='resume-1',
                       handled=[dict(event_id=event_id, note='fixture readback verified')])
        runtime_entry.handled_events(receipt, delivery, runtime, native, actor)
        self.call(actor, 'terminal-events', 'delivered', event_id, '--native-session', native,
                  '--delivery-session', 'resume-1')
        self.call(actor, 'terminal-events', 'ack', event_id, '--native-session', native,
                  '--note', 'fixture readback verified')
        # A resumed handler repeats the same receipt without duplicate effects.
        self.call(actor, 'terminal-events', 'ack', event_id, '--native-session', native,
                  '--note', 'fixture readback verified')
        self.assertGreater(self.rows('SELECT processed_at FROM terminal_event_receipts WHERE event_id=? AND recipient=?',
                                     (event_id, actor))[0][0], 0)

    def exercise(self, source, target):
        self.clients = dict(old=(source, 'old-native'), new=(target, 'new-native'),
                            worker=(source, 'worker-native'))
        self.call('old', 'init', '--yes')
        settings = self.root / '.squad/config.yaml'
        settings.write_text(settings.read_text().replace('default_worktree_per_claim: true',
                                                        'default_worktree_per_claim: false'))
        for actor in self.clients:
            self.call(actor, 'register', '--as', actor)
        self.call('old', 'dispatch', 'reserve', 'D-1', '--source', 'github:example/repo#1')
        self.call('old', 'dispatch', 'controller-bind', '--native-session', 'old-native')
        self.call('old', 'new', 'BUG', 'Isolated recovery fixture', '--ready')
        self.call('old', 'dispatch', 'attach', 'D-1', '--item', 'BUG-001', '--generation', '1')
        self.call('old', 'dispatch', 'bind', 'D-1', '--thread-id', 'worker-native',
                  '--generation', '1', '--supervised')
        self.call('worker', 'claim', 'BUG-001')
        self.decision('old', 0, 'hold')
        held = self.get_decision()
        pending = self.submit(held['revision'], 'before-transfer')
        claim = self.rows('SELECT * FROM claims')
        # Represent retained uncommitted work; transfer must not rewrite it.
        artifact = self.root / 'unfinished.txt'
        artifact.write_text('retained source and completed operation\n')
        cohort = json.loads(self.call('old', 'dispatch', 'list', '--json'))
        request = self.root / 'handoff.json'
        request.write_text(json.dumps(dict(request_id='matrix-transfer', expected_epoch=1,
                                          old_native='old-native', new_actor='new', new_native='new-native',
                                          reservations=cohort)))
        first = json.loads(self.call('old', 'dispatch', 'handoff', '--request', str(request)))
        replay = json.loads(self.call('old', 'dispatch', 'handoff', '--request', str(request)))
        self.assertEqual(first, replay)
        after = json.loads(self.call('new', 'dispatch', 'list', '--json'))
        self.assertEqual(after[0]['worker_thread_id'], 'worker-native')
        self.assertEqual(after[0]['generation'], 1)
        self.assertEqual(after[0]['reserved_by'], 'new')
        self.assertEqual(self.rows('SELECT * FROM claims'), claim)
        self.assertEqual(self.get_decision(), held)
        self.assertEqual(artifact.read_text(), 'retained source and completed operation\n')
        self.call('old', 'dispatch', 'controller-bind', '--native-session', 'old-native', reject=True)
        self.call('new', 'dispatch', 'receiver-bind', '--native-session', 'new-native',
                  '--epoch', '2', '--incarnation', 'resume-1')
        self.handle('new', pending['event_id'])
        self.decision('new', held['revision'], 'proceed')
        resumed = self.get_decision()
        self.assertEqual(resumed['action'], 'proceed')
        self.assertGreater(resumed['revision'], held['revision'])
        second = self.submit(resumed['revision'], 'after-transfer')
        self.assertNotEqual(second['event_id'], pending['event_id'])
        self.assertEqual(self.submit(resumed['revision'], 'after-transfer'), second)
        self.handle('new', second['event_id'])
        self.call('new', 'dispatch', 'receiver-release', '--native-session', 'new-native',
                  '--epoch', '2', '--incarnation', 'resume-1')
        self.call('worker', 'release', 'BUG-001')


def transition(source, target):
    def test(self):
        self.exercise(source, target)
    return test


for _source, _target in itertools.product(RUNTIMES, repeat=2):
    setattr(RecoveryMatrixTests, f'test_{_source}_to_{_target}', transition(_source, _target))


class RoleBoundaryTests(unittest.TestCase):
    def test_worker_schema_cannot_impersonate_other_roles(self):
        schema = json.loads((ROOT / 'schemas/assignment-envelope.schema.json').read_text())
        assignment = json.loads((ROOT / 'examples/assignment.studio-worker.json').read_text())
        validate(assignment, schema)
        for role in ('dispatcher', 'deployer', 'reviewer', 'investigator'):
            with self.subTest(role=role):
                assignment['role']['id'] = role
                with self.assertRaisesRegex(ValidationError, 'role.id'):
                    validate(assignment, schema)


if __name__ == '__main__':
    unittest.main()
