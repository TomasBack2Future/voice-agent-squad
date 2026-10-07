import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).parents[1]))
import launch_selection as selection


class SurfaceVerificationTests(unittest.TestCase):
    TREE = {'windows': [{'workspaces': [{'id': 'WS-1', 'active': True, 'panes': [
        {'surfaces': [
            {'ref': 'surface:1', 'active': False, 'selected': False, 'title': 'old shell'},
            {'ref': 'surface:2', 'active': True, 'selected': True, 'title': 'new TUI'}]}]}]}]}

    def test_selected_visible_surface_is_verified(self):
        surface = selection.verify_surface(self.TREE, 'WS-1', 'surface:2')
        self.assertEqual((surface['visible'], surface['selected']), (True, True))

    def test_hidden_or_old_shell_surface_is_rejected(self):
        with self.assertRaises(ValueError):
            selection.verify_surface(self.TREE, 'WS-1', 'surface:1')
        with self.assertRaises(ValueError):
            selection.verify_surface(self.TREE, 'WS-1', 'surface:9')
        with self.assertRaises(ValueError):
            selection.verify_surface(self.TREE, 'WS-OLD', 'surface:2')


class ResumeReadbackTests(unittest.TestCase):
    def test_resume_rejects_effective_config_mismatch(self):
        selected = {'model': 'muse-spark-1.3-contributor', 'effort': 'max',
                    'permission_mode': 'yolo', 'ui_mode': 'headless'}
        self.assertEqual(selection.check_resume_readback(
            selected, dict(selected, native_session='native-1')), 'native-1')
        with self.assertRaises(ValueError):
            selection.check_resume_readback(
                selected, dict(selected, effort='low', native_session='native-1'))

    def test_resume_rejects_native_or_task_drift(self):
        selected = {'model': 'm', 'effort': 'e', 'permission_mode': 'p', 'ui_mode': 'h'}
        with self.assertRaises(ValueError):
            selection.check_resume_readback(
                selected, dict(selected, native_session='other', task_item='BUG-016'),
                expected_native='native-1')
        with self.assertRaises(ValueError):
            selection.check_resume_readback(
                selected, dict(selected, native_session='native-1', task_item='OTHER-1'),
                expected_item='BUG-016')


class CodexResumeReadbackTests(unittest.TestCase):
    def test_resume_rejects_stale_permission_readback(self):
        selected = {'sandbox': 'danger-full-access', 'approval_policy': 'never'}
        effective = selection.normalize_codex_effective(
            {'sandbox': {'type': 'workspaceWrite'}, 'approvalPolicy': 'never'})
        with self.assertRaises(ValueError):
            selection.check_codex_resume(selected, effective)

    def test_resume_accepts_matching_full_access_readback(self):
        selected = {'sandbox': 'danger-full-access', 'approval_policy': 'never'}
        effective = selection.normalize_codex_effective(
            {'sandbox': {'type': 'dangerFullAccess'}, 'approvalPolicy': 'never'})
        self.assertTrue(selection.check_codex_resume(selected, effective))


class LaunchSelectionTests(unittest.TestCase):
    def test_explicit_task_selection_beats_persisted_preference_and_default(self):
        with tempfile.TemporaryDirectory() as tmp:
            preferences = Path(tmp) / 'preferences.json'
            preferences.write_text(json.dumps({'ui_mode': 'live-view', 'permission_mode': 'auto'}))
            resolved = selection.resolve_launch({'model': 'task-model', 'effort': 'medium'},
                                                str(preferences), 'claude')
            self.assertEqual(resolved['model'], ('task-model', 'task'))
            self.assertEqual(resolved['effort'], ('medium', 'task'))
            self.assertEqual(resolved['ui_mode'], ('live-view', 'preference'))
            self.assertEqual(resolved['permission_mode'], ('auto', 'preference'))

    def test_stale_or_unknown_preference_falls_back_to_runtime_default(self):
        with tempfile.TemporaryDirectory() as tmp:
            preferences = Path(tmp) / 'preferences.json'
            preferences.write_text(json.dumps({'ui_mode': 'unknown-mode', 'stale': True}))
            resolved = selection.resolve_launch({}, str(preferences), 'claude')
            self.assertEqual(resolved['ui_mode'], ('headless', 'default'))
            self.assertEqual(resolved['model'], ('native', 'default'))

    def test_unknown_task_combination_is_rejected_not_silently_downgraded(self):
        with tempfile.TemporaryDirectory() as tmp:
            preferences = Path(tmp) / 'preferences.json'
            preferences.write_text('{}')
            with self.assertRaises(ValueError):
                selection.resolve_launch({'ui_mode': 'interactive', 'sandbox': 'read-only',
                                          'mediation': 'none'}, str(preferences), 'muse')


class UnifiedReceiptTests(unittest.TestCase):
    def test_receipt_distinguishes_config_process_runtime_surface_ownership(self):
        receipt = selection.launch_receipt(
            config_check={'status': 'ready'},
            process={'pid': 1234, 'argv': ['claude', '--resume', 'native-1']},
            runtime={'runtime': 'claude', 'native_session': 'native-1'},
            surface={'visible': True, 'selected': True, 'surface_id': 'surface:9'},
            task={'item': 'BUG-016', 'claim': 'held', 'heartbeat': 'verified'},
            outcome={'report_path': '/tmp/report.json'})
        self.assertEqual(receipt['config'], 'ready')
        self.assertEqual(receipt['runtime'], 'claude:native-1')
        self.assertEqual(receipt['surface'], 'visible-selected')
        self.assertEqual(receipt['task'], 'BUG-016:held:verified')
        self.assertEqual(receipt['status'], 'launched')

    def test_hidden_surface_or_old_shell_is_not_success(self):
        base = dict(config_check={'status': 'ready'},
                    process={'pid': 1234, 'argv': ['muse']},
                    runtime={'runtime': 'muse', 'native_session': 'native-1'},
                    task={'item': 'BUG-016', 'claim': 'held', 'heartbeat': 'verified'},
                    outcome={'report_path': '/tmp/report.json'})
        hidden = selection.launch_receipt(surface={'visible': False, 'selected': False}, **base)
        self.assertEqual(hidden['status'], 'blocked')
        self.assertIn('surface', hidden['reason'])
        stale = selection.launch_receipt(surface={'visible': True, 'selected': True, 'shell_pid': 9999},
                                         process={'pid': 1234, 'argv': ['muse']}, **{k: v for k, v in base.items() if k != 'process'})
        self.assertEqual(stale['status'], 'blocked')
        self.assertIn('surface', stale['reason'])


if __name__ == '__main__':
    unittest.main()
