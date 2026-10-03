import io
import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).parents[1]))
import muse_worker_launcher as launch


class MuseUsabilityTests(unittest.TestCase):
    def test_effort_precedence_and_invalid_preference(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'settings.json'
            path.write_text('{}')
            c = {'settings_file': str(path)}
            self.assertEqual(launch.resolve_effort(c), ('max', 'default'))
            path.write_text(json.dumps({'reasoning_effort': 'xhigh'}))
            self.assertEqual(launch.resolve_effort(c), ('xhigh', 'settings-file'))
            self.assertEqual(launch.resolve_effort(dict(c, reasoning_effort='low')), ('low', 'launch-config'))
            path.write_text(json.dumps({'reasoning_effort': 'unknown'}))
            with self.assertRaises(ValueError):
                launch.resolve_effort(c)
            path.write_text('[]')
            with self.assertRaisesRegex(ValueError, 'settings must be an object'):
                launch.resolve_effort(c)

    def test_view_streams_messages_and_tools_without_control_sequences(self):
        view = launch.LiveView('native', stream=io.StringIO())
        def event(method, **params):
            view.observe({'method': method, 'params': {'sessionId': 'native', **params}})
        event('item/started', item={'kind': 'agentMessage', 'itemId': 'a'})
        event('item/delta', itemId='a', delta='working now\x1b[2J', field='text')
        self.assertIn('working now', view.stream.getvalue())
        self.assertNotIn('\x1b', view.stream.getvalue())
        event('item/started', item={'kind': 'toolCall', 'itemId': 't', 'tool': 'run_command'})
        self.assertIn('run_command', view.stream.getvalue())
        event('item/completed', item={'kind': 'toolCall', 'itemId': 't', 'tool': 'run_command',
                                      'status': 'completed', 'visibleOutput': 'tests passed'})
        self.assertIn('tests passed', view.stream.getvalue())
        view.observe({'method': 'item/completed', 'params': {'sessionId': 'foreign', 'item': {'kind': 'agentMessage', 'text': 'FOREIGN'}}})
        event('item/started', item={'kind': 'reasoning', 'itemId': 'r'})
        event('item/delta', itemId='r', delta='PRIVATE', field='summary.0')
        self.assertNotIn('FOREIGN', view.stream.getvalue())
        self.assertNotIn('PRIVATE', view.stream.getvalue())

    def test_view_bounds_output_and_broken_view_does_not_stop_custody(self):
        view = launch.LiveView('native', stream=io.StringIO())
        view.observe({'method': 'item/completed', 'params': {'sessionId': 'native', 'item': {
            'kind': 'toolCall', 'itemId': 't', 'tool': 'read_file', 'status': 'completed', 'visibleOutput': 'x' * 100000}}})
        self.assertLess(len(view.stream.getvalue()), 18000)
        class Broken:
            def write(self, text): raise BrokenPipeError()
        view = launch.LiveView('native', stream=Broken())
        view.status('continues despite closed viewer')

if __name__ == '__main__':
    unittest.main()
