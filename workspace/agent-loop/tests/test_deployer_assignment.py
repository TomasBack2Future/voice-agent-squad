"""Claude Deployer launch: envelope, preflight, launcher binding and receiver.

The Worker envelope, preflight and launcher stay Worker-only; the Deployer has
its own envelope (release reference, no Issue) and an explicit receiver role.
Every test uses isolated temporary repositories, stub executables and no ledger.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).parents[1]
sys.path.insert(0, str(ROOT))
import claude_worker_launcher as launcher
import terminal_receiver as receiver
import worker_preflight as preflight
from validate_context_package import ValidationError, validate

SKILL_PATH = '.agents/skills/studio-deploy-production/SKILL.md'
SKILL_TEXT = b'---\nname: studio-deploy-production\ndescription: fixture release skill\n---\nRelease only the frozen candidate.\n'
SESSION = '00000000-0000-4000-8000-000000000456'


def example():
    return json.loads((ROOT / 'examples/assignment.studio-deployer.json').read_text())


class DeployerFixture(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.work = self.root / 'work'
        self.work.mkdir()
        self.git('init', '-b', 'release')
        self.git('config', 'user.name', 'Test')
        self.git('config', 'user.email', 'test@example.invalid')
        (self.work / 'AGENTS.md').write_text('Isolated test repository.\n')
        (self.work / SKILL_PATH).parent.mkdir(parents=True)
        (self.work / SKILL_PATH).write_bytes(SKILL_TEXT)
        self.git('add', '-A')
        self.git('commit', '-m', 'initial')
        self.git('remote', 'add', 'origin', 'git@github.com:TomasBack2Future/voice-agent-studio.git')
        self.sha = self.git('rev-parse', 'HEAD')
        self.assignment = example()
        self.assignment.update(worktree=str(self.work), branch='release', base_sha=self.sha)
        self.assignment['release']['repositories'][0]['sha'] = self.sha
        self.profile = ROOT / 'projects/studio/profile.json'
        self.assignment['project_profile']['path'] = str(self.profile)
        self.path = self.root / 'assignment.json'
        self.skill = self.root / '.claude/skills/studio-deploy-production/SKILL.md'
        self.skill.parent.mkdir(parents=True)
        self.skill.write_bytes(SKILL_TEXT)

    def git(self, *args):
        return subprocess.check_output(['git', '-C', str(self.work), *args], stderr=subprocess.DEVNULL, text=True).strip()

    def check(self, skills=None):
        self.path.write_text(json.dumps(self.assignment))
        with patch('worker_preflight.shutil.which', return_value='/qualified/tool'):
            return preflight.check(self.path, self.profile, 'claude', skills or [self.skill], ['squad'], context_only=True)


class EnvelopeTests(DeployerFixture):
    def load(self):
        self.path.write_text(json.dumps(self.assignment))
        return preflight.load_assignment(self.path)

    def test_example_is_a_valid_deployer_envelope(self):
        self.assertEqual(self.load()['role']['id'], 'deployer')

    def test_worker_envelope_loads_through_the_same_entry_unchanged(self):
        worker = json.loads((ROOT / 'examples/assignment.studio-worker.json').read_text())
        self.path.write_text(json.dumps(worker))
        self.assertEqual(preflight.load_assignment(self.path), worker)

    def test_unknown_schema_version_is_rejected(self):
        self.assignment['schema_version'] = 'agent-loop.deployer-assignment.v2'
        with self.assertRaisesRegex(ValidationError, 'schema'):
            self.load()

    def test_worker_schema_still_refuses_a_deployer_role(self):
        schema = json.loads((ROOT / 'schemas/assignment-envelope.schema.json').read_text())
        worker = json.loads((ROOT / 'examples/assignment.studio-worker.json').read_text())
        worker['role'].update(id='deployer', skill='studio-deploy-production')
        with self.assertRaises(ValidationError):
            validate(worker, schema)

    def test_deployer_envelope_requires_release_and_forbids_issue(self):
        for name, mutate in {
            'issue': lambda a: a.update(issue='o/r#1'),
            'no release': lambda a: a.pop('release'),
            'worker role': lambda a: a['role'].update(id='worker', skill='agent-loop-worker'),
            'worker skill': lambda a: a['role'].update(skill='agent-loop-worker'),
            'other role': lambda a: a['role'].update(id='reviewer'),
            'production false': lambda a: a['authorization'].update(production=False),
            'no production reference': lambda a: a['authorization'].pop('production_reference'),
            'bad target': lambda a: a['release'].update(target='staging'),
            'bad manifest digest': lambda a: a['release'].update(manifest_sha256='abc'),
            'bad release sha': lambda a: a['release']['repositories'][0].update(sha='abc'),
            'extra release key': lambda a: a['release'].update(extra=1),
        }.items():
            with self.subTest(name):
                self.assignment = example()
                mutate(self.assignment)
                with self.assertRaises(ValidationError):
                    self.load()

    def test_release_must_name_repositories_and_contain_the_checkout_revision(self):
        self.assignment['release']['repositories'] = []
        with self.assertRaisesRegex(ValidationError, 'repositories'):
            self.load()
        self.assignment = example()
        self.assignment['release']['repositories'][0]['sha'] = 'b' * 40
        with self.assertRaisesRegex(ValidationError, 'base_sha'):
            self.load()
        self.assignment = example()
        self.assignment['release']['repositories'] *= 2
        with self.assertRaisesRegex(ValidationError, 'duplicate'):
            self.load()


class PreflightTests(DeployerFixture):
    def test_matching_release_skill_is_checked_without_mutation(self):
        before = self.git('status', '--porcelain')
        receipt = self.check()
        self.assertEqual(receipt['status'], 'context-checked')
        self.assertEqual(receipt['role'], 'deployer')
        self.assertEqual([s['skill'] for s in receipt['skills']], ['studio-deploy-production'])
        self.assertEqual(receipt['skills'][0]['source_revision'], self.sha)
        self.assertEqual(before, self.git('status', '--porcelain'))

    def test_worker_skill_cannot_stand_in_for_the_release_skill(self):
        worker = self.root / '.claude/skills/agent-loop-worker/SKILL.md'
        worker.parent.mkdir(parents=True)
        worker.symlink_to(ROOT / 'roles/worker/SKILL.md')
        self.skill.unlink()
        with self.assertRaisesRegex(ValidationError, 'release skill'):
            self.check([worker])

    def test_client_skill_differing_from_the_committed_skill_is_rejected(self):
        self.skill.write_bytes(SKILL_TEXT + b'Extra instruction.\n')
        with self.assertRaisesRegex(ValidationError, 'differs'):
            self.check()

    def test_wrong_skill_identity_is_rejected(self):
        wrong = SKILL_TEXT.replace(b'studio-deploy-production', b'other-skill')
        (self.work / SKILL_PATH).write_bytes(wrong)
        self.git('commit', '-am', 'rename')
        self.assignment['base_sha'] = self.git('rev-parse', 'HEAD')
        self.assignment['release']['repositories'][0]['sha'] = self.assignment['base_sha']
        self.skill.write_bytes(wrong)
        with self.assertRaisesRegex(ValidationError, 'identity'):
            self.check()

    def test_missing_client_skill_is_rejected(self):
        self.skill.unlink()
        with self.assertRaises(ValidationError):
            self.check()

    def test_skill_absent_at_the_selected_revision_is_rejected(self):
        self.git('rm', '-q', SKILL_PATH)
        self.git('commit', '-m', 'remove')
        self.assignment['base_sha'] = self.git('rev-parse', 'HEAD')
        self.assignment['release']['repositories'][0]['sha'] = self.assignment['base_sha']
        with self.assertRaisesRegex(ValidationError, 'selected revision'):
            self.check()

    def test_profile_without_release_skill_is_rejected(self):
        profile = json.loads(self.profile.read_text())
        profile.pop('release_skill')
        self.profile = self.root / 'profile.json'
        self.profile.write_text(json.dumps(profile))
        self.assignment['project_profile']['path'] = str(self.profile)
        with self.assertRaisesRegex(ValidationError, 'release_skill'):
            self.check()

    def test_profile_pins_the_studio_release_skill(self):
        pinned = json.loads(self.profile.read_text())['release_skill']
        self.assertEqual(pinned, {'name': 'studio-deploy-production', 'path': SKILL_PATH})

    def test_dirty_or_drifted_checkout_is_rejected(self):
        (self.work / 'AGENTS.md').write_text('changed\n')
        with self.assertRaisesRegex(ValidationError, 'dirty'):
            self.check()


class RuntimeTests(DeployerFixture):
    def test_deployer_is_qualified_for_claude_only(self):
        self.path.write_text(json.dumps(self.assignment))
        for runtime in ('codex', 'muse'):
            with self.subTest(runtime):
                with patch('worker_preflight.shutil.which', return_value='/qualified/tool'):
                    with self.assertRaisesRegex(ValidationError, 'Claude only'):
                        preflight.check(self.path, self.profile, runtime, [self.skill], ['squad'], context_only=True)


class LauncherFixture(DeployerFixture):
    def prepare(self, rows=None):
        self.ledger = self.root / 'ledger'
        (self.ledger / '.squad').mkdir(parents=True, exist_ok=True)
        self.row = {'reservation_key': self.assignment['reservation']['key'],
                    'generation': self.assignment['reservation']['generation'],
                    'source_ref': 'release:' + self.assignment['release']['id'],
                    'canonical_item_id': self.assignment['item'],
                    'reserved_by': 'dispatcher-test', 'state': 'reserved'}
        self.rows = self.ledger / 'rows.json'
        self.rows.write_text(json.dumps(rows if rows is not None else [self.row]))
        self.calls = self.root / 'calls.jsonl'
        script = self.root / 'squad-stub'
        script.write_text('#!' + sys.executable + '\n' + f'''import json, pathlib, sys
pathlib.Path({str(self.calls)!r}).open('a').write(json.dumps(sys.argv[1:]) + '\\n')
if sys.argv[1] == 'heartbeat':
    print('--reservation --generation --worker-session --check'); sys.exit(0)
if sys.argv[1:4] == ['resources', 'check', 'ENV-002']:
    print(json.dumps({{'item': 'ENV-002', 'status': 'ready'}})); sys.exit(0)
assert sys.argv[1:] == ['dispatch', 'list', '--json'], sys.argv
print((pathlib.Path.cwd() / 'rows.json').read_text())
''')
        script.chmod(0o700)
        self.client = self.root / 'client-stub'
        self.started = self.root / 'started.json'
        self.client.write_text('#!' + sys.executable + '\nimport json,sys\n'
                               + f'open({str(self.started)!r}, "w").write(json.dumps(sys.argv[1:]))\n')
        self.client.chmod(0o700)
        prompt = self.root / 'prompt.txt'
        prompt.write_text('Assigned release only')
        self.config = {'schema_version': 'agent-loop.claude-launch.v1', 'native_session_id': SESSION,
                       'agent_id': 'deployer-test', 'dispatcher_agent_id': 'dispatcher-test',
                       'coordination_executable': str(script), 'coordination_mode': 'native',
                       'ledger_directory': str(self.ledger), 'client_executable': str(self.client),
                       'client_config_directory': str(self.root / 'claude-config'),
                       'permission_mode': 'bypassPermissions', 'model': 'claude-sonnet-5-5', 'effort': 'high',
                       'prompt_file': str(prompt)}
        self.config_path = self.root / 'launch.json'
        self.save_config()
        self.path.write_text(json.dumps(self.assignment))

    def save_config(self):
        self.config_path.write_text(json.dumps(self.config))

    def call_log(self):
        return [json.loads(line) for line in self.calls.read_text().splitlines()] if self.calls.exists() else []

    def run_launcher(self, check=False):
        argv = [sys.executable, str(ROOT / 'claude_worker_launcher.py'), '--assignment', str(self.path),
                '--config', str(self.config_path), '--wait-seconds', '0']
        if check:
            argv.append('--check')
        return subprocess.run(argv, env=dict(os.environ, SQUAD_AGENT='PARENT'), text=True, capture_output=True, timeout=20)


class LauncherTests(LauncherFixture):
    def test_receiver_config_carries_the_assigned_role_and_native_fence(self):
        self.prepare()
        config = dict(self.config, event_executable='/isolated/squad')
        args = launcher.receiver_arguments(config, self.config_path, role='deployer')
        written = json.loads(Path(args[1]).with_name('config.json').read_text())
        self.assertEqual((written['role'], written['runtime'], written['legacy_native_fence']), ('deployer', 'claude', True))
        self.assertEqual(written['native_session_id'], SESSION)

    def test_receiver_role_defaults_to_worker_for_existing_callers(self):
        self.prepare()
        args = launcher.receiver_arguments(dict(self.config, event_executable='/isolated/squad'), self.config_path)
        self.assertEqual(json.loads(Path(args[1]).with_name('config.json').read_text())['role'], 'worker')

    def test_release_binding_pending_then_bound(self):
        self.prepare()
        env = launcher.child_environment(self.config)
        self.assertEqual(launcher.binding(self.assignment, self.config, env), 'pending')
        self.row.update(state='dispatched', worker_thread_id=SESSION)
        self.rows.write_text(json.dumps([self.row]))
        self.assertEqual(launcher.binding(self.assignment, self.config, env), 'bound')

    def test_github_source_ref_cannot_bind_a_deployer(self):
        self.prepare()
        self.row['source_ref'] = 'github:TomasBack2Future/voice-agent-studio#1'
        self.rows.write_text(json.dumps([self.row]))
        with self.assertRaisesRegex(ValidationError, 'reservation identity'):
            launcher.binding(self.assignment, self.config, launcher.child_environment(self.config))

    def test_changed_release_generation_owner_or_item_blocks(self):
        for field, value in {'source_ref': 'release:other', 'generation': 2, 'reserved_by': 'intruder',
                             'canonical_item_id': 'OTHER-1'}.items():
            with self.subTest(field):
                self.prepare()
                self.row[field] = value
                self.rows.write_text(json.dumps([self.row]))
                with self.assertRaises(ValidationError):
                    launcher.binding(self.assignment, self.config, launcher.child_environment(self.config))

    def test_worker_binding_is_unchanged(self):
        worker = json.loads((ROOT / 'examples/assignment.studio-worker.json').read_text())
        self.prepare()
        self.row.update(source_ref='github:' + worker['issue'], canonical_item_id=worker['item'],
                        reservation_key=worker['reservation']['key'])
        self.rows.write_text(json.dumps([self.row]))
        self.assertEqual(launcher.binding(worker, self.config, launcher.child_environment(self.config)), 'pending')

    def test_release_target_is_checked_read_only_with_policy_and_never_claimed(self):
        self.prepare()
        receipts = launcher.check_resources(self.assignment, self.config, launcher.child_environment(self.config))
        self.assertEqual([(r['phase'], r['item'], r['status']) for r in receipts], [('production', 'ENV-002', 'ready')])
        self.assertIn(['resources', 'check', 'ENV-002', '--require-policy'], self.call_log())
        self.assertFalse([c for c in self.call_log() if c[0] == 'claim'])

    def test_target_outside_the_profile_production_environment_is_refused_before_any_call(self):
        self.prepare()
        self.assignment['release']['target'] = 'ENV-004'
        with self.assertRaisesRegex(ValidationError, 'profile production'):
            launcher.check_resources(self.assignment, self.config, launcher.child_environment(self.config))
        self.assertEqual(self.call_log(), [])

    def test_unavailable_release_target_blocks(self):
        self.prepare()
        Path(self.config['coordination_executable']).write_text('#!' + sys.executable + '\nimport sys\nsys.exit(3)\n')
        with self.assertRaisesRegex(ValidationError, 'ENV-002'):
            launcher.check_resources(self.assignment, self.config, launcher.child_environment(self.config))

    def test_worker_only_launch_inputs_are_rejected_for_a_deployer(self):
        self.prepare()
        for key, value in {'resource_overrides': {'production': {'item': 'ENV-009', 'admission_reference': 'x'}},
                           'delivery_readiness_file': '/readiness.json',
                           'human_authorization': {'assignment_id': 'a', 'repository': 'o/r', 'reference': 'x',
                                                   'operations': ['source'],
                                                   'managed_review': {'provider': 'grok', 'destination': 'x',
                                                                      'content': 'source_diff_and_issue_contract_only'}}}.items():
            with self.subTest(key):
                self.config = dict(self.config, **{key: value})
                with self.assertRaisesRegex(ValidationError, 'Deployer'):
                    launcher.check_resources(self.assignment, self.config, launcher.child_environment(self.config))
                with self.assertRaisesRegex(ValidationError, 'Deployer'):
                    launcher.check_deployer_launch_inputs(self.assignment, self.config)

    def test_check_launch_reports_pending_binding_and_release_target(self):
        self.prepare()
        receipt = launcher.check_launch(self.assignment, self.config_path)
        self.assertEqual((receipt['binding'], receipt['role']), ('pending', 'deployer'))
        self.assertEqual([r['item'] for r in receipt['resources']], ['ENV-002'])
        self.assertFalse(self.started.exists())

    def test_cli_check_never_starts_the_client(self):
        self.prepare()
        result = self.run_launcher(check=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)['binding'], 'pending')
        self.assertFalse(self.started.exists())

    def test_bound_launch_starts_once_with_explicit_mode_model_and_effort(self):
        self.prepare()
        self.row.update(state='dispatched', worker_thread_id=SESSION)
        self.rows.write_text(json.dumps([self.row]))
        result = self.run_launcher()
        self.assertEqual(result.returncode, 0, result.stderr)
        args = json.loads(self.started.read_text())
        self.assertEqual(args[:6], ['--session-id', SESSION, '--permission-mode', 'bypassPermissions',
                                    '--model', 'claude-sonnet-5-5'])
        self.assertIn('--effort', args)
        self.assertFalse([c for c in self.call_log() if c[0] == 'claim'])

    def test_unbound_reservation_starts_no_client(self):
        self.prepare()
        result = self.run_launcher()
        self.assertEqual(result.returncode, 2)
        self.assertIn('binding deadline expired', result.stderr)
        self.assertFalse(self.started.exists())


class ReceiverTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.binary = self.root / 'squad'
        self.binary.write_text('''#!/usr/bin/env python3
import json, pathlib, sys
pathlib.Path("calls").open("a").write("call\\n")
if "ack" in sys.argv: sys.exit(0)
print(json.dumps({"type":"worker-terminal-delivery-v1","events":[{"event_id":"test-event"}]}))
''')
        self.binary.chmod(0o700)
        self.config = self.root / 'config.json'
        self.data = dict(native_session_id='session-one', agent_id='deployer', role='deployer',
                         state_directory=str(self.root / 'state'), ledger_directory=str(self.root),
                         squad_executable=str(self.binary), incarnation='launch-one', owner_pid=os.getpid(),
                         max_seconds=60, legacy_native_fence=True)
        self.config.write_text(json.dumps(self.data))

    def deliver(self):
        p = subprocess.Popen([sys.executable, receiver.__file__, '--config', str(self.config)], cwd=self.root,
                             stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        out, err = p.communicate(json.dumps({'session_id': 'session-one'}), timeout=10)
        return p.returncode, out, err

    def test_deployer_wake_reads_the_decision_and_grants_no_authority(self):
        code, out, err = self.deliver()
        self.assertEqual(code, 2)
        self.assertIn('terminal-events decision-get', err)
        self.assertIn('execution binding', err)
        self.assertIn('ENV holder', err)
        self.assertIn('not production authority', err)
        self.assertIn('Delivery is not processing', err)
        self.assertIn('terminal-events ack', err)
        self.assertNotIn('Invoke $squad-dispatcher', err)

    def test_deployer_wake_never_dispatches_another_session(self):
        _, _, err = self.deliver()
        self.assertIn('Do not dispatch another session', err)

    def test_unknown_role_fails_closed_without_listening(self):
        for role in ('reviewer', 'investigator', 'probe', 'anything'):
            with self.subTest(role):
                self.data['role'] = role
                self.config.write_text(json.dumps(self.data))
                code, out, err = self.deliver()
                self.assertEqual((code, out), (2, ''))
                self.assertNotIn('test-event', err)
                self.assertFalse((self.root / 'calls').exists())

    def test_settings_for_deployer_have_wake_hooks_and_the_native_fence_only(self):
        hooks = receiver.settings(self.config)['hooks']
        self.assertEqual(set(hooks), {'SessionStart', 'PostToolUse', 'Stop', 'PreToolUse'})
        fence = hooks['PreToolUse'][0]['hooks']
        self.assertEqual(len(fence), 1)
        self.assertIn('claude_native_fence.py', fence[0]['command'])
        for event in ('SessionStart', 'PostToolUse', 'Stop'):
            self.assertTrue(hooks[event][0]['hooks'][0]['asyncRewake'])

    def test_settings_reject_unknown_roles(self):
        self.data['role'] = 'reviewer'
        self.config.write_text(json.dumps(self.data))
        with self.assertRaises(ValueError):
            receiver.settings(self.config)

    def test_worker_and_dispatcher_settings_are_unchanged(self):
        self.data['role'] = 'worker'
        self.config.write_text(json.dumps(self.data))
        self.assertEqual(set(receiver.settings(self.config)['hooks']), {'SessionStart', 'PostToolUse', 'Stop', 'PreToolUse'})
        self.data.update(role='dispatcher', legacy_native_fence=False)
        self.config.write_text(json.dumps(self.data))
        dispatcher = receiver.settings(self.config)['hooks']
        self.assertEqual(len(dispatcher['PreToolUse'][0]['hooks']), 2)


if __name__ == '__main__':
    unittest.main()
