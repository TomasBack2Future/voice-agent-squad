import json
import os
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch

import test_worker_preflight as fixtures

ROOT = Path(__file__).parents[1]
sys.path.insert(0, str(ROOT))
import claude_worker_launcher as launcher
import worker_preflight as preflight


class LauncherTests(unittest.TestCase):
    setUp = fixtures.WorkerPreflightTests.setUp
    git = fixtures.WorkerPreflightTests.git

    def prepare(self, mode='native', behavior='ok'):
        self.session = '00000000-0000-4000-8000-000000000123'
        self.ledger = self.root / 'ledger'
        (self.ledger / '.squad').mkdir(parents=True)
        self.row = {'reservation_key': self.assignment['reservation']['key'],
                    'generation': self.assignment['reservation']['generation'],
                    'source_ref': 'github:' + self.assignment['issue'],
                    'canonical_item_id': self.assignment['item'],
                    'reserved_by': 'dispatcher-test', 'state': 'reserved'}
        self.rows = self.ledger / 'rows.json'
        self.save_rows()
        script = self.root / 'squad-stub'
        script.write_text('#!' + sys.executable + '\n' + '''import json, os, pathlib, sys
p=pathlib.Path.cwd()
with (p/'calls').open('a') as f:f.write('read\\n')
assert sys.argv[1:] == ['dispatch','list','--json']
assert os.environ['SQUAD_AGENT']=='worker-test'
assert os.environ['SQUAD_SESSION_ID'].startswith('claude:00000000-0000-4000-8000-000000000123:')
''' + ('''assert os.environ['CODEX_THREAD_ID']=='00000000-0000-4000-8000-000000000123'
assert os.environ['CODEX_SESSION_ID']==os.environ['CODEX_THREAD_ID']
''' if mode == 'codex-wrapper-compat' else '''assert 'CODEX_THREAD_ID' not in os.environ
assert 'CODEX_SESSION_ID' not in os.environ
''') + {
            'ok': "print((p/'rows.json').read_text())\n",
            'exit2': "print('squad-codex: CODEX_THREAD_ID/CODEX_SESSION_ID is unavailable',file=sys.stderr);sys.exit(2)\n",
            'unknown': "print('unexpected configuration error',file=sys.stderr);sys.exit(9)\n",
            'malformed': "print('not json')\n",
            'secret': "print('ACCESS_TOKEN=TOP_SECRET',file=sys.stderr);sys.exit(2)\n",
        }[behavior])
        script.chmod(0o700)
        self.client = self.root / 'client-stub'
        self.started = self.root / 'started.json'
        self.client.write_text('#!' + sys.executable + '\nimport json,os,pathlib,sys\n'
            + f'pathlib.Path({str(self.started)!r}).write_text(json.dumps({{"args":sys.argv[1:],"identity":os.environ["SQUAD_SESSION_ID"]}}))\n')
        self.client.chmod(0o700)
        prompt = self.root / 'prompt.txt'; prompt.write_text('Assigned work only')
        self.config = {'schema_version': 'agent-loop.claude-launch.v1',
                       'native_session_id': self.session, 'agent_id': 'worker-test',
                       'dispatcher_agent_id': 'dispatcher-test',
                       'coordination_executable': str(script), 'coordination_mode': mode,
                       'ledger_directory': str(self.ledger), 'client_executable': str(self.client),
                       'permission_mode': 'auto', 'prompt_file': str(prompt)}
        self.config_path = self.root / 'launch.json'
        self.config_path.write_text(json.dumps(self.config))
        self.path.write_text(json.dumps(self.assignment))

    def save_rows(self):
        self.rows.write_text(json.dumps([self.row]))

    def run_launcher(self, check=False):
        env = dict(os.environ, CODEX_THREAD_ID='PARENT', CODEX_SESSION_ID='PARENT',
                   SQUAD_SESSION_ID='PARENT', SQUAD_AGENT='PARENT')
        argv = [sys.executable, str(ROOT/'claude_worker_launcher.py'),
                '--assignment', str(self.path), '--config', str(self.config_path), '--wait-seconds', '0']
        if check: argv.append('--check')
        return subprocess.run(argv, env=env, text=True, capture_output=True, timeout=10)

    def test_native_check_clears_parent_identity_and_never_starts_client(self):
        self.prepare()
        result = self.run_launcher(check=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)['binding'], 'pending')
        self.assertFalse(self.started.exists())
        self.assertEqual((self.ledger/'calls').read_text(), 'read\n')

    def test_compat_environment_is_derived_from_child(self):
        self.prepare('codex-wrapper-compat')
        for clean in (False, True):
            with patch.dict(os.environ, {}, clear=clean):
                receipt = launcher.check_launch(self.assignment, self.config_path)
                self.assertEqual(receipt['binding'], 'pending')

    def test_all_command_errors_fail_once_with_diagnostic(self):
        self.prepare(behavior='exit2')
        result = self.run_launcher()
        self.assertEqual(result.returncode, 2)
        self.assertIn('CODEX_THREAD_ID/CODEX_SESSION_ID is unavailable', result.stderr)
        self.assertEqual((self.ledger/'calls').read_text(), 'read\n')
        self.assertFalse(self.started.exists())

    def test_unknown_nonzero_is_not_assumed_transient(self):
        self.prepare(behavior='unknown')
        result = self.run_launcher()
        self.assertIn('exit 9', result.stderr)
        self.assertEqual((self.ledger/'calls').read_text(), 'read\n')

    def test_malformed_output_never_enters_wait(self):
        self.prepare(behavior='malformed')
        result = self.run_launcher()
        self.assertIn('invalid JSON', result.stderr)
        self.assertEqual((self.ledger/'calls').read_text(), 'read\n')

    def test_credential_fields_are_not_echoed(self):
        self.prepare(behavior='secret')
        result = self.run_launcher()
        self.assertNotIn('TOP_SECRET', result.stdout + result.stderr)
        self.assertIn('redacted', result.stderr)

    def test_changed_generation_owner_source_item_and_binding_block(self):
        self.prepare()
        for field, value in [('generation', 42), ('reserved_by', 'other'),
                ('source_ref', 'github:other/repo#1'), ('canonical_item_id', 'OTHER-1'),
                ('state', 'completed'), ('worker_thread_id', 'OTHER')]:
            original = self.row.copy(); self.row[field] = value; self.save_rows()
            self.assertEqual(self.run_launcher().returncode, 2, field)
            self.assertFalse(self.started.exists())
            self.row = original

    def test_bound_launch_executes_once_with_selected_mode_and_prompt(self):
        self.prepare()
        self.row.update(state='dispatched', worker_thread_id=self.session);self.save_rows()
        result = self.run_launcher()
        self.assertEqual(result.returncode, 0, result.stderr)
        data=json.loads(self.started.read_text())
        self.assertEqual(data['args'], ['--session-id', self.session, '--permission-mode', 'auto', 'Assigned work only'])

    def test_only_successful_pending_binding_can_wait(self):
        self.prepare()
        with patch.object(launcher, 'binding', side_effect=['pending','bound']) as read:
            with patch.object(launcher.time, 'sleep') as sleep:
                launcher.wait_for_binding(self.assignment, self.config, {}, 3)
                self.assertEqual(read.call_count, 2);sleep.assert_called_once()
        self.assertEqual(self.run_launcher().returncode, 2)
        self.assertFalse(self.started.exists())

    def test_preflight_exercises_the_same_child_path(self):
        self.prepare(behavior='exit2')
        with patch('worker_preflight.shutil.which', return_value='/tool'):
            with self.assertRaisesRegex(preflight.ValidationError, 'is unavailable'):
                preflight.check(self.path,self.profile,'claude',[self.skill],[],self.config_path)

    def test_branch_drift_and_wrong_config_keys_are_rejected(self):
        self.prepare()
        self.git('checkout','-b','changed')
        self.assertIn('branch mismatch', self.run_launcher(check=True).stderr)
        self.git('checkout','work')
        self.config['env_claim']=True;self.config_path.write_text(json.dumps(self.config))
        self.assertIn('unexpected keys',self.run_launcher(check=True).stderr)


if __name__ == '__main__': unittest.main()
