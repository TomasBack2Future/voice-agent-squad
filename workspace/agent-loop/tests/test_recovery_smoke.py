"""The acceptance runner must never turn missing coverage into qualification."""
import io
import os
import sys
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parents[1]))
import recovery_smoke as smoke


class SmokeRunnerTests(unittest.TestCase):
    def result(self, body):
        case = type('Fixture', (unittest.TestCase,), {'test_fixture': body})
        return smoke.execute(unittest.defaultTestLoader.loadTestsFromTestCase(case), io.StringIO())

    def test_empty_suite_fails(self):
        self.assertEqual(smoke.execute(unittest.TestSuite(), io.StringIO())['status'], 'failed')

    def test_missing_suite_is_failure(self):
        suite = unittest.TestLoader().loadTestsFromName('missing_recovery_suite_xyz')
        result = smoke.execute(suite, io.StringIO())
        self.assertEqual(result['status'], 'failed')
        self.assertGreater(result['errors'], 0)

    def test_skip_is_not_pass(self):
        result = self.result(lambda case: case.skipTest('native executable absent'))
        self.assertEqual(result['status'], 'failed')
        self.assertEqual(result['skipped'], 1)

    def test_expected_failure_is_not_qualification(self):
        @unittest.expectedFailure
        def failure(case):
            case.fail('not implemented')
        self.assertEqual(self.result(failure)['status'], 'failed')

    def test_failure_and_success(self):
        self.assertEqual(self.result(lambda case: case.fail('broken'))['status'], 'failed')
        result = self.result(lambda case: case.assertTrue(True))
        self.assertEqual(result['status'], 'passed')
        self.assertEqual(result['tests_run'], 1)

    def test_suite_loading_and_execution_cannot_inherit_live_routing(self):
        inherited = {'SQUAD_REMOTE_URL': 'https://fixture.invalid',
                     'SQUAD_REMOTE_TOKEN': 'fixture-token', 'SQUAD_HOME': '/live',
                     'CODEX_THREAD_ID': 'parent', 'CLAUDE_SESSION_ID': 'parent',
                     'MUSE_NATIVE_EXECUTABLE': '/qualified/native'}
        observed = []
        def check_environment(*args):
            observed.append(True)
            self.assertFalse(any(k.startswith('SQUAD_') for k in os.environ))
            self.assertNotIn('CODEX_THREAD_ID', os.environ)
            self.assertNotIn('CLAUDE_SESSION_ID', os.environ)
            self.assertEqual(os.environ['MUSE_NATIVE_EXECUTABLE'], '/qualified/native')
            self.assertEqual(os.environ['PYTHONDONTWRITEBYTECODE'], '1')
            return unittest.TestSuite()
        def execute_fixture(suite):
            check_environment()
            return {'status': 'failed'}
        with patch.dict(os.environ, inherited), \
                patch.object(unittest.TestLoader, 'loadTestsFromName', side_effect=check_environment), \
                patch.object(smoke, 'execute', side_effect=execute_fixture):
            smoke.run_suites(('fixture',))
            self.assertEqual(os.environ['SQUAD_REMOTE_URL'], inherited['SQUAD_REMOTE_URL'])
            self.assertEqual(os.environ['CODEX_THREAD_ID'], 'parent')
        self.assertEqual(len(observed), 2)

    def test_all_runtime_role_cells_are_explicit(self):
        cells = smoke.native_coverage()
        self.assertEqual(len(cells), 15)
        self.assertEqual({c['role'] for c in cells}, set(smoke.ROLES))
        self.assertTrue(all(c['status'] in ('not_run', 'unavailable') for c in cells))
        self.assertTrue(all(c['native_qualified'] is False for c in cells))

    def test_missing_role_executor_blocks_before_test_execution(self):
        with patch.object(smoke, 'run_suites') as run:
            report = smoke.qualify('codex', 'deployer')
        run.assert_not_called()
        self.assertEqual(report['status'], 'blocked')
        self.assertFalse(report['native_qualified'])

    def test_required_native_suites_cannot_fall_back_to_simulation(self):
        with patch.object(smoke, 'run_suites', return_value=[{'status': 'failed', 'skipped': 1}]) as run:
            report = smoke.qualify('muse', 'worker')
        self.assertEqual(report['status'], 'failed')
        self.assertFalse(report['native_qualified'])
        self.assertEqual(run.call_args.args[0], smoke.NATIVE_SUITES[('muse', 'worker')])

    def test_worker_native_does_not_qualify_cross_runtime_or_other_roles(self):
        with patch.object(smoke, 'run_suites', return_value=[{'status': 'passed'}]):
            report = smoke.qualify('muse', 'worker')
        self.assertEqual(report['scope'], 'source-worker-new-resume-hold')
        self.assertNotIn('deployer', report['scope'])
        self.assertFalse(report['cross_runtime_migration_qualified'])


if __name__ == '__main__':
    unittest.main()
