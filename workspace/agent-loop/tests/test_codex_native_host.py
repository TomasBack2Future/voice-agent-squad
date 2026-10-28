import json
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import codex_native_host as native
from validate_context_package import ValidationError


class NativeStartupTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        auth = self.root / 'auth.json'
        auth.write_text('{}')
        self.config = {'role': 'worker', 'workspace': str(self.root), 'client_executable': sys.executable,
                       'auth_file': str(auth), 'model': 'gpt-6.1-sol', 'provider': 'openai', 'effort': 'medium',
                       'native_session_id': 'fixture-native'}
        self.identity = patch.object(native, 'binary_identity', return_value={'path': sys.executable})
        self.identity.start()
        self.addCleanup(self.identity.stop)
        self.state = self.root / 'native'
        self.host = native.NativeCLI(self.state, self.config)
        self.addCleanup(self.host.close)
        self.popen = subprocess.Popen

    def receipt(self):
        return json.loads((self.state / 'input-intent.json').read_text())

    def assert_joined(self, child):
        self.assertIsNotNone(child.poll())
        proof = json.loads((self.state / 'native-join.json').read_text())
        self.assertEqual(proof['pid'], child.pid)
        self.assertTrue(proof['joined'])
        self.assertIsNone(self.host.child)
        self.assertIsNone(self.host.stderr)
        native.NativeCLI(self.state, self.config).close()

    def test_popen_failure_records_no_submission_and_closes_log(self):
        with patch.object(native.subprocess, 'Popen', side_effect=OSError('fixture launch failure')):
            with self.assertRaisesRegex(OSError, 'launch failure'):
                self.host.turn('private input', 'input-one')
        self.assertEqual(self.receipt()['state'], 'not-started')
        self.assertNotIn('pid', self.receipt())
        self.assertIsNone(self.host.stderr)
        self.assertFalse((self.state / 'native-join.json').exists())
        native.NativeCLI(self.state, self.config).close()

    def test_client_identity_is_never_replayed_even_after_another_input(self):
        for text, client in [('first', 'stable-one'), ('second', 'stable-two')]:
            with patch.object(native.subprocess, 'Popen', side_effect=OSError('fixture launch failure')):
                with self.assertRaises(OSError):
                    self.host.turn(text, client)
        prior = (self.state / 'input-intent.json').read_bytes()
        for text in ('first', 'changed content'):
            with self.subTest(text=text), patch.object(native.subprocess, 'Popen', side_effect=OSError('unexpected replay')) as launch:
                with self.assertRaises(ValidationError):
                    self.host.turn(text, 'stable-one')
                launch.assert_not_called()
                self.assertEqual((self.state / 'input-intent.json').read_bytes(), prior)

    def test_client_reservation_survives_a_new_adapter_instance(self):
        with patch.object(native.subprocess, 'Popen', side_effect=OSError('fixture launch failure')):
            with self.assertRaises(OSError):
                self.host.turn('first', 'stable-one')
        reopened = native.NativeCLI(self.state, self.config)
        self.addCleanup(reopened.close)
        with patch.object(native.subprocess, 'Popen', side_effect=OSError('unexpected replay')) as launch:
            with self.assertRaises(ValidationError):
                reopened.turn('first', 'stable-one')
            launch.assert_not_called()

    def test_concurrent_same_client_has_only_one_launch_attempt(self):
        other = native.NativeCLI(self.state, self.config)
        self.addCleanup(other.close)
        barrier = threading.Barrier(2)
        outcomes = []

        def submit(host):
            barrier.wait(timeout=5)
            try:
                host.turn('first', 'race-client')
            except (ValidationError, OSError) as error:
                outcomes.append(type(error).__name__)

        with patch.object(native.subprocess, 'Popen', side_effect=OSError('fixture launch failure')) as launch:
            threads = [threading.Thread(target=submit, args=(host,)) for host in (self.host, other)]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join(timeout=5)
                self.assertFalse(thread.is_alive())
            self.assertEqual(launch.call_count, 1)
        self.assertCountEqual(outcomes, ['ValidationError', 'OSError'])

    def test_failed_current_turn_cannot_reuse_previous_rollout_context(self):
        rollout = self.host.home / 'sessions' / 'fixture-native.jsonl'
        rollout.parent.mkdir()
        context = {'cwd': str(self.root), 'model': self.config['model'], 'effort': 'medium',
                   'approval_policy': 'never', 'sandbox_policy': {'type': 'danger-full-access'},
                   'turn_id': 'previous-completed-turn'}
        rollout.write_text(json.dumps({'type': 'turn_context', 'payload': context}) + '\n')
        frames = [{'type': 'thread.started', 'thread_id': 'fixture-native'}, {'type': 'turn.failed'}]

        def launch(argv, **kwargs):
            code = 'import sys;sys.stdin.read();print(' + repr('\n'.join(json.dumps(f) for f in frames)) + ')'
            return self.popen([sys.executable, '-c', code], **kwargs)

        with patch.object(native.subprocess, 'Popen', side_effect=launch):
            self.host.turn('current input', 'current-client')
            with self.assertRaisesRegex(ValidationError, 'current native rollout context'):
                self.host.wait('current-client', seconds=5)
        self.assertIsNone(self.host.effective)
        self.assertNotEqual(self.receipt()['state'], 'terminal')

    def test_successful_resume_uses_only_the_appended_current_context(self):
        rollout = self.host.home / 'sessions' / 'fixture-native.jsonl'
        rollout.parent.mkdir()
        rollout.write_text(json.dumps({'type': 'turn_context', 'payload': {'turn_id': 'old', 'model': 'old'}}) + '\n')
        context = {'cwd': str(self.root), 'model': self.config['model'], 'effort': 'medium',
                   'approval_policy': 'never', 'sandbox_policy': {'type': 'danger-full-access'},
                   'turn_id': 'current-turn'}

        def launch(argv, **kwargs):
            code = ('import sys;from pathlib import Path;sys.stdin.read();'
                    + 'p=Path(' + repr(str(rollout)) + ');p.open("a").write('
                    + repr(json.dumps({'type': 'turn_context', 'payload': context}) + '\n') + ');'
                    + 'print(\'{"type":"thread.started","thread_id":"fixture-native"}\');'
                    + 'print(\'{"type":"turn.completed"}\')')
            return self.popen([sys.executable, '-c', code], **kwargs)

        with patch.object(native.subprocess, 'Popen', side_effect=launch):
            self.host.turn('current input', 'current-client')
            result = self.host.wait('current-client', seconds=5)
        self.assertEqual(result['id'], 'current-turn')
        self.assertEqual(result['status'], 'completed')

    def test_pid_is_durable_before_stdin_failure_and_owned_process_is_joined(self):
        children = []
        test = self

        class BrokenInput:
            def __init__(self, pipe):
                self.pipe = pipe

            @property
            def closed(self):
                return self.pipe.closed

            def write(self, value):
                test.assertEqual(test.receipt()['pid'], children[0].pid)
                test.assertEqual(test.receipt()['state'], 'submitted')
                self.pipe.write(value)
                raise BrokenPipeError('fixture input failure')

            def close(self):
                self.pipe.close()

        def launch(argv, **kwargs):
            child = self.popen([sys.executable, '-c', 'import time; time.sleep(60)'], **kwargs)
            children.append(child)
            child.stdin = BrokenInput(child.stdin)
            return child

        with patch.object(native.subprocess, 'Popen', side_effect=launch):
            with self.assertRaisesRegex(BrokenPipeError, 'input failure'):
                self.host.turn('private input', 'input-one')
        self.assert_joined(children[0])
        self.assertEqual(self.receipt()['state'], 'submitted')
        self.assertNotIn('private input', (self.state / 'input-intent.json').read_text())

    def test_reader_start_failure_joins_process_without_joining_unstarted_thread(self):
        children = []

        def launch(argv, **kwargs):
            child = self.popen([sys.executable, '-c', 'import time; time.sleep(60)'], **kwargs)
            children.append(child)
            return child

        with patch.object(native.subprocess, 'Popen', side_effect=launch), patch.object(native.threading.Thread, 'start', side_effect=RuntimeError('fixture reader failure')):
            with self.assertRaisesRegex(RuntimeError, 'reader failure'):
                self.host.turn('private input', 'input-one')
        self.assert_joined(children[0])


if __name__ == '__main__':
    unittest.main()
