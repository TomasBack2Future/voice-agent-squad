import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).parents[1]))
import launch_selection as selection


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


if __name__ == '__main__':
    unittest.main()
