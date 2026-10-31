import hashlib
import os
from pathlib import Path
import stat
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parents[1]))
import claude_worker_launcher as launcher
import worker_preflight as preflight
from validate_context_package import ValidationError

OLD, NEW = 'a' * 40, 'b' * 40


def assignment(profile):
    return {'project_profile': {'path': str(profile), 'id': 'p', 'version': 1}, 'worktree': '/tmp', 'repository': 'o/r'}


class GenerationBindingTests(unittest.TestCase):
    def test_generation_is_read_from_the_resolved_path_only(self):
        self.assertEqual(preflight.generation_of(Path(f'/x/generations/{OLD}/workspace/a.py')), OLD)
        self.assertIsNone(preflight.generation_of(Path('/x/source/workspace/a.py')))
        self.assertIsNone(preflight.generation_of(Path('/x/generations/short/a.py')))

    def test_profile_from_another_generation_is_refused_before_schema_validation(self):
        helper = f'/x/generations/{OLD}/workspace/agent-loop/worker_preflight.py'
        profile = Path(f'/x/generations/{NEW}/workspace/agent-loop/projects/p/profile.json')
        with patch.object(preflight, '__file__', helper), patch.object(preflight, 'validate_file') as validate:
            with self.assertRaisesRegex(ValidationError, 'differs from this helper generation'):
                preflight.check_profile(assignment(profile))
            validate.assert_not_called()

    def test_same_generation_and_source_checkouts_proceed_to_the_schema(self):
        profile = Path(f'/x/generations/{OLD}/workspace/agent-loop/projects/p/profile.json')
        helper = f'/x/generations/{OLD}/workspace/agent-loop/worker_preflight.py'
        for h, p in [(helper, profile), ('/src/workspace/agent-loop/worker_preflight.py', profile), (helper, Path('/src/profile.json'))]:
            with patch.object(preflight, '__file__', h), patch.object(preflight, 'validate_file', side_effect=ValidationError('schema reached')):
                with self.assertRaisesRegex(ValidationError, 'schema reached'):
                    preflight.check_profile(assignment(p))


class SchemaCompatibleGenerationTests(unittest.TestCase):
    def test_other_generation_with_identical_schema_is_not_refused(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d).resolve()
            other = root / 'generations' / NEW / 'workspace/agent-loop'
            (other / 'schemas').mkdir(parents=True)
            (other / 'projects/p').mkdir(parents=True)
            schema = (Path(preflight.__file__).resolve().parent / 'schemas/project-profile.schema.json').read_bytes()
            (other / 'schemas/project-profile.schema.json').write_bytes(schema)
            profile = other / 'projects/p/profile.json'
            profile.write_text('{}')
            helper = f'/x/generations/{OLD}/workspace/agent-loop/worker_preflight.py'
            with patch.object(preflight, '__file__', helper), patch.object(preflight, 'validate_file', side_effect=ValidationError('schema reached')):
                with self.assertRaisesRegex(ValidationError, 'schema reached'):
                    preflight.check_profile(assignment(profile))
            (other / 'schemas/project-profile.schema.json').write_bytes(schema + b' ')
            with patch.object(preflight, '__file__', helper), patch.object(preflight, 'validate_file') as validate:
                with self.assertRaisesRegex(ValidationError, 'differs from this helper generation'):
                    preflight.check_profile(assignment(profile))
                validate.assert_not_called()


class CapabilityTests(unittest.TestCase):
    def fake(self, directory, retire):
        path = Path(directory) / 'squad'
        path.write_text('#!/bin/sh\n'
                        'case "$1 $2" in\n'
                        '"heartbeat --help") echo "--reservation --generation --worker-session --check";;\n'
                        '"dispatch retire-client") %s;;\n'
                        'version*) echo 0.0.0-squad.test;;\nesac\n' % ('echo "--worker-session"' if retire else 'exit 1'))
        path.chmod(path.stat().st_mode | stat.S_IXUSR)
        return str(path)

    def test_runtime_without_retire_client_fails_preflight(self):
        with tempfile.TemporaryDirectory() as d:
            c = {'coordination_executable': self.fake(d, False), 'ledger_directory': d}
            with self.assertRaisesRegex(ValidationError, 'retire-client'):
                launcher.check_heartbeat_runtime(c, dict(os.environ))
            c['coordination_executable'] = self.fake(d, True)
            launcher.check_heartbeat_runtime(c, dict(os.environ))

    def test_check_launch_receipt_carries_runtime_identity(self):
        with tempfile.TemporaryDirectory() as d:
            exe = self.fake(d, True)
            c = {'coordination_executable': exe, 'ledger_directory': d}
            self.assertEqual(launcher.runtime_identity(c, dict(os.environ))['version'], '0.0.0-squad.test')
        import inspect
        self.assertIn("'runtime_identity': runtime_identity(c, env)", inspect.getsource(launcher.check_launch))
        # And the stubbed full preflight receipt carries it (see test_claude_worker_launcher).

    def test_probe_timeout_is_a_validation_error(self):
        import subprocess as sp
        with patch.object(launcher.subprocess, 'run', side_effect=sp.TimeoutExpired('x', 10)):
            with self.assertRaisesRegex(ValidationError, 'timed out'):
                launcher.check_heartbeat_runtime({'coordination_executable': '/x', 'ledger_directory': '/tmp'}, {})

    def test_launch_receipt_names_the_binary_that_will_execute(self):
        with tempfile.TemporaryDirectory() as d:
            exe = self.fake(d, True)
            identity = launcher.runtime_identity({'coordination_executable': exe, 'ledger_directory': d}, dict(os.environ))
            self.assertEqual(identity['version'], '0.0.0-squad.test')
            self.assertEqual(identity['sha256'], hashlib.sha256(Path(exe).read_bytes()).hexdigest())
            self.assertEqual(identity['path'], str(Path(exe).resolve()))


if __name__ == '__main__':
    unittest.main()
