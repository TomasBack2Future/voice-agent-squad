import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parents[1]))
import claude_native_fence as fence
import terminal_receiver


class NativeFenceTests(unittest.TestCase):
    def setUp(self):
        self.config = dict(native_session_id='old-native', agent_id='old-actor',
                           ledger_directory='/fixture', squad_executable='/fixture/squad')

    def test_native_readback_and_sanitized_identity(self):
        result = subprocess.CompletedProcess([], 0, json.dumps({'state': 'eligible', 'native_session': 'old-native'}), '')
        with patch('claude_native_fence.subprocess.run', return_value=result) as run:
            fence.check(self.config, 'old-native')
        args, kwargs = run.call_args
        self.assertEqual(args[0][-2:], ['--native-session', 'old-native'])
        self.assertEqual(kwargs['env']['SQUAD_AGENT'], 'old-actor')
        self.assertEqual(kwargs['env']['SQUAD_NATIVE_SESSION_ID'], 'old-native')
        self.assertEqual(kwargs['env']['PYTHONDONTWRITEBYTECODE'], '1')
        self.assertNotIn('CODEX_THREAD_ID', kwargs['env'])

    def test_fenced_unavailable_and_changed_identity_fail_closed(self):
        for native, status, body in [('other', 0, '{}'), ('old-native', 1, ''),
                                     ('old-native', 0, '{"state":"eligible","native_session":"other"}')]:
            with self.subTest(native=native, status=status), patch('claude_native_fence.subprocess.run',
                    return_value=subprocess.CompletedProcess([], status, body, '')):
                with self.assertRaises(ValueError):
                    fence.check(self.config, native)

    def test_worker_settings_install_blocking_pre_tool_hook(self):
        with tempfile.TemporaryDirectory(prefix='guard with spaces ') as directory:
            path = Path(directory) / 'config.json'
            path.write_text(json.dumps(dict(self.config, role='worker', legacy_native_fence=True)))
            hooks = terminal_receiver.settings(path)['hooks']
            guard = hooks['PreToolUse'][0]['hooks'][0]
            self.assertNotIn('asyncRewake', guard)
            self.assertIn('claude_native_fence.py', guard['command'])
            self.assertEqual(guard['timeout'], 15)

    def test_invalid_real_hook_input_blocks_with_exit_two(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'config.json'
            path.write_text(json.dumps(self.config))
            result = subprocess.run([sys.executable, fence.__file__, '--config', str(path)],
                                    input=json.dumps({'hook_event_name': 'PostToolUse', 'session_id': 'old-native'}),
                                    capture_output=True, text=True, timeout=5,
                                    env=dict(os.environ, PYTHONDONTWRITEBYTECODE='1'))
            self.assertEqual(result.returncode, 2)
            self.assertEqual(result.stdout, '')
            self.assertIn('blocked', result.stderr)
