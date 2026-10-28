import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import muse_current_native as current
from validate_context_package import ValidationError


class CurrentMuseContractTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.binary = self.root / 'native-muse'
        self.binary.write_text('not executed')
        self.binary.chmod(0o700)
        self.config = {'runtime': 'muse', 'role': 'worker', 'workspace': str(self.root),
                       'client_executable': str(self.binary), 'model': current.MODEL, 'effort': 'max'}

    def test_exact_binary_and_selection_are_checked_without_execution(self):
        with mock.patch.object(current, 'BINARY_SHA256', hashlib.sha256(self.binary.read_bytes()).hexdigest()), \
                mock.patch.object(current.subprocess, 'run') as run:
            result = current.validate_selection(self.config)
        run.assert_not_called()
        self.assertEqual(result['version'], current.VERSION)
        for field, value in [('model', 'muse-wonder-1.0'), ('effort', 'high'),
                             ('provider', 'echo'), ('permission_mode', 'on-request'),
                             ('contributor_mode', 'off')]:
            with self.subTest(field=field), self.assertRaises(ValidationError):
                current.validate_selection(dict(self.config, **{field: value}))

    def test_changed_native_binary_is_rejected(self):
        with self.assertRaises(ValidationError):
            current.validate_selection(self.config)

    def test_mcp_configuration_preserves_only_the_current_native_identity(self):
        mapped = current.mcp_config({'source': {'command': sys.executable, 'args': ['bridge.py'],
                                               'env': {'CODEX_THREAD_ID': 'foreign', 'MUSE_SESSION_ID': 'foreign'}}}, 'own-native')
        server = mapped['mcpServers']['source']
        self.assertEqual(server['framing'], 'lineDelimitedJson')
        self.assertEqual(server['env']['MUSE_SESSION_ID'], 'own-native')
        self.assertNotIn('CODEX_THREAD_ID', server['env'])

    def test_completed_input_replay_does_not_start_another_native_turn(self):
        settings = self.root / 'settings.json'
        settings.write_text(json.dumps({'schema_version': 1, 'endpoint_transport':
                                       {'base_url': 'https://example.invalid/v1', 'auth': 'bearer'}}))
        config = dict(self.config, settings_file=str(settings))
        with mock.patch.object(current, 'BINARY_SHA256', hashlib.sha256(self.binary.read_bytes()).hexdigest()):
            native = current.NativeCLI(self.root / 'state', config)
        text, client = 'fixture input', 'same-input'
        result = {'id': 'native-turn', 'status': 'completed', 'native': native.config['native_session_id'], 'exit_code': 0}
        current.atomic(native.input_path(client), {'client_id': client,
                       'sha256': hashlib.sha256(text.encode()).hexdigest(), 'state': 'terminal', 'terminal': result})
        self.assertEqual(native.turn(text, client, resume=False), client)
        self.assertEqual(native.wait(client), result)
        self.assertIsNone(native.child)
        with self.assertRaises(ValidationError):
            native.turn('changed input', client, resume=False)

    def test_durable_settings_require_own_max_and_direct_tool_boundary(self):
        path = self.root / 'native.jsonl'
        permission = {'approval': 'allow_all', 'filesystem': {'mode': 'unrestricted'},
                      'local_command_network': {'mode': 'enabled'}, 'reviewer': 'none'}

        def record(kind, payload, native='own'):
            return {'stream': {'kind': 'session', 'id': native}, 'payload_type': kind, 'payload': payload}

        def write(effort='max', tools=None, native='own'):
            records = [record('runtime.reasoning_effort_reconfigure', {'record': {'effective': effort}}, native),
                       record('runtime.session.permission_profile_committed', {'resolved_snapshot': permission}),
                       record('runtime.session', {'toolset': {'active_tools': tools or ['read_file', 'mcp__source__owned_probe']}})]
            path.write_text('\n'.join(json.dumps(r) for r in records) + '\n')

        write()
        self.assertEqual(current.native_settings(path, 'own')['effort'], 'max')
        for kwargs in ({'effort': 'high'}, {'native': 'foreign'}, {'tools': ['shell']}):
            with self.subTest(kwargs=kwargs), self.assertRaises(ValidationError):
                write(**kwargs)
                current.native_settings(path, 'own')

    def test_unsupported_mcp_configuration_is_not_silently_dropped(self):
        with self.assertRaises(ValidationError):
            current.mcp_config({'source': {'command': sys.executable, 'startup_timeout_sec': 30}}, 'own')

    def new_native(self, state):
        settings = self.root / 'settings.json'
        settings.write_text(json.dumps({'schema_version': 1, 'endpoint_transport':
                                       {'base_url': 'https://example.invalid/v1', 'auth': 'bearer'}}))
        config = dict(self.config, settings_file=str(settings))
        with mock.patch.object(current, 'BINARY_SHA256', hashlib.sha256(self.binary.read_bytes()).hexdigest()):
            return current.NativeCLI(state, config)

    def interrupted_input(self, native, pid):
        value = dict(client_id='interrupted', command_id='original-command', native=native.config['native_session_id'],
                     sha256=hashlib.sha256(b'original input').hexdigest(), state='submitted',
                     turn_id='original-turn', pid=pid)
        current.atomic(native.input_path('interrupted'), value)
        return value

    def test_joined_interrupted_input_reopens_for_new_input_without_replay(self):
        native = self.new_native(self.root / 'state')
        child = subprocess.Popen([sys.executable, '-c', 'pass'])
        child.wait(timeout=5)
        original = self.interrupted_input(native, child.pid)
        current.atomic(native.root / 'native-join.json', dict(pid=child.pid, exit_code=0, joined=True,
                       native=native.config['native_session_id'], command_id='original-command'))
        reopened = self.new_native(native.root)
        saved = json.loads(reopened.input_path('interrupted').read_text())
        self.assertEqual(saved['state'], 'joined-unresolved')
        self.assertEqual(saved['original_state'], 'submitted')
        for field in ('command_id', 'turn_id', 'sha256', 'pid'):
            self.assertEqual(saved[field], original[field])
        self.assertNotIn('terminal', saved)
        with mock.patch.object(current, 'CurrentHost', side_effect=OSError('new input reached launch')) as launch:
            with self.assertRaises(ValidationError):
                reopened.turn('original input', 'interrupted')
            launch.assert_not_called()
            with self.assertRaisesRegex(OSError, 'new input reached launch'):
                reopened.turn('new bounded continuation', 'new-input')
            self.assertEqual(launch.call_count, 1)

    def test_unjoined_live_or_foreign_input_cannot_reopen(self):
        child = subprocess.Popen([sys.executable, '-c', 'import sys;sys.stdin.read()'], stdin=subprocess.PIPE)
        self.addCleanup(lambda: (child.stdin.close(), child.wait(timeout=5)))
        for index, proof in enumerate((None, {'pid': child.pid + 1, 'joined': True, 'exit_code': 0},
                                      {'pid': child.pid, 'joined': True, 'exit_code': 0},
                                      {'pid': child.pid, 'joined': True, 'exit_code': 0, 'native': 'foreign'})):
            with self.subTest(proof=proof):
                native = self.new_native(self.root / ('state-' + str(index)))
                self.interrupted_input(native, child.pid)
                if proof:
                    current.atomic(native.root / 'native-join.json', proof)
                with self.assertRaises(ValidationError):
                    self.new_native(native.root)
                self.assertEqual(json.loads(native.input_path('interrupted').read_text())['state'], 'submitted')

    def test_close_retains_per_input_join_after_later_native_join_replaces_latest(self):
        native = self.new_native(self.root / 'state')
        child = subprocess.Popen([sys.executable, '-c', 'pass'])
        child.wait(timeout=5)
        self.interrupted_input(native, child.pid)
        host = mock.Mock()
        host.p.pid, host.p.returncode = child.pid, child.returncode
        host.p.poll.return_value = child.returncode
        host.reader.is_alive.return_value = False
        native.host, native.active = host, 'interrupted'
        native.close()
        saved = json.loads(native.input_path('interrupted').read_text())
        self.assertEqual(saved['state'], 'joined-unresolved')
        self.assertEqual(saved['join']['pid'], child.pid)
        current.atomic(native.root / 'native-join.json', dict(pid=child.pid + 1, exit_code=0, joined=True))
        reopened = self.new_native(native.root)
        self.assertEqual(json.loads(reopened.input_path('interrupted').read_text()), saved)

    def test_popen_refusal_allows_reopen_for_new_input_without_replaying_original(self):
        native = self.new_native(self.root / 'state')
        with mock.patch.object(current.subprocess, 'Popen', side_effect=OSError('fixture process refusal')):
            with self.assertRaisesRegex(OSError, 'fixture process refusal'):
                native.turn('original input', 'refused-input')
        saved = json.loads(native.input_path('refused-input').read_text())
        self.assertEqual(saved['state'], 'not-started')
        self.assertNotIn('pid', saved)
        reopened = self.new_native(native.root)
        with self.assertRaises(ValidationError):
            reopened.turn('original input', 'refused-input')

    def test_constructor_reader_failure_retains_per_input_join_for_reopen(self):
        native = self.new_native(self.root / 'state')
        children = []
        real = subprocess.Popen

        def launch(argv, **kwargs):
            child = real([sys.executable, '-c', 'import sys;sys.stdin.read()'], **kwargs)
            children.append(child)
            return child

        with mock.patch.object(current.subprocess, 'Popen', side_effect=launch), \
                mock.patch.object(current.threading.Thread, 'start', side_effect=RuntimeError('fixture reader failed')):
            with self.assertRaisesRegex(RuntimeError, 'fixture reader failed'):
                native.turn('original input', 'reader-failed-input')
        self.assertIsNotNone(children[0].poll())
        saved = json.loads(native.input_path('reader-failed-input').read_text())
        self.assertEqual(saved['state'], 'joined-unresolved')
        self.assertEqual(saved['join']['pid'], children[0].pid)
        self.new_native(native.root)

    def test_workspace_extensions_are_rejected_before_native_trust(self):
        for name in ('.mcp.json', '.muse/hooks.json'):
            with self.subTest(name=name):
                extension = self.root / name
                extension.parent.mkdir(exist_ok=True)
                extension.write_text('{}')
                try:
                    with self.assertRaisesRegex(ValidationError, 'extensions require separate qualification'):
                        self.new_native(self.root / 'state')
                finally:
                    extension.unlink()

    def test_workspace_extension_admission_is_rechecked_for_each_launch(self):
        for index, name in enumerate(('.mcp.json', '.muse/hooks.json')):
            with self.subTest(name=name):
                native = self.new_native(self.root / ('state-' + str(index)))
                extension = self.root / name
                extension.parent.mkdir(exist_ok=True)
                extension.write_text('{}')
                try:
                    with mock.patch.object(current, 'CurrentHost', side_effect=OSError('unqualified launch reached')) as launch:
                        with self.assertRaisesRegex(ValidationError, 'extensions require separate qualification'):
                            native.turn('next turn', 'next-input')
                        launch.assert_not_called()
                finally:
                    extension.unlink()

    def test_concurrent_same_client_reserves_only_one_native_launch(self):
        first = self.new_native(self.root / 'state')
        second = self.new_native(self.root / 'state')
        path = first.input_path('same-input')
        barrier = threading.Barrier(2)
        outcomes = []
        exists, atomic = Path.exists, current.atomic

        def forced_absence(value):
            if value == path:
                barrier.wait(timeout=5)
                return False
            return exists(value)

        def write(value, payload):
            if value != path:
                return atomic(value, payload)

        def submit(native):
            try:
                native.turn('same text', 'same-input')
            except (OSError, ValidationError) as error:
                outcomes.append(type(error).__name__)

        with mock.patch.object(Path, 'exists', forced_absence), mock.patch.object(current, 'atomic', write), \
                mock.patch.object(current, 'CurrentHost', side_effect=OSError('fixture launch rejected')) as launch:
            threads = [threading.Thread(target=submit, args=(native,)) for native in (first, second)]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join(timeout=5)
                self.assertFalse(thread.is_alive())
            self.assertEqual(launch.call_count, 1)
        self.assertCountEqual(outcomes, ['ValidationError', 'OSError'])

    def test_host_reader_start_failure_joins_original_child_and_records_pid(self):
        children = []
        real = subprocess.Popen

        def launch(argv, **kwargs):
            child = real([sys.executable, '-c', 'import sys;sys.stdin.read()'], **kwargs)
            children.append(child)
            return child

        state = self.root / 'host-state'
        state.mkdir()
        c = dict(self.config, native_session_id='fixture-native')

        def fail_reader():
            journal = json.loads((state / 'native-process.json').read_text())
            self.assertEqual(journal['pid'], children[0].pid)
            self.assertEqual(journal['native'], 'fixture-native')
            raise RuntimeError('fixture reader failed')

        try:
            with mock.patch.object(current.subprocess, 'Popen', side_effect=launch), \
                    mock.patch.object(current.threading.Thread, 'start', side_effect=fail_reader):
                with self.assertRaisesRegex(RuntimeError, 'fixture reader failed'):
                    current.CurrentHost(c, state, dict(os.environ))
            self.assertIsNotNone(children[0].poll())
            join = json.loads((state / 'native-join.json').read_text())
            self.assertEqual(join['pid'], children[0].pid)
            self.assertTrue(join['joined'])
        finally:
            for child in children:
                if child.poll() is None:
                    child.kill()
                child.wait(timeout=5)
                child.stdin.close()
                child.stdout.close()

    def test_host_popen_failure_closes_original_stderr(self):
        state = self.root / 'host-state'
        state.mkdir()
        opened = []
        original = Path.open

        def open_file(path, *args, **kwargs):
            value = original(path, *args, **kwargs)
            if path.name == 'muse-stderr.log':
                opened.append(value)
            return value

        with mock.patch.object(Path, 'open', open_file), \
                mock.patch.object(current.subprocess, 'Popen', side_effect=OSError('fixture launch rejected')):
            with self.assertRaises(OSError):
                current.CurrentHost(dict(self.config, native_session_id='fixture-native'), state, dict(os.environ))
        try:
            self.assertTrue(opened[0].closed)
        finally:
            for stream in opened:
                stream.close()


@unittest.skipUnless(os.environ.get('SQUAD_TEST_CURRENT_MUSE') == '1', 'explicit native qualification only')
class CurrentMuseNativeTests(unittest.TestCase):
    def test_actual_joined_interruption_resumes_same_native(self):
        root = Path(os.environ['SQUAD_MUSE_EVIDENCE']).resolve() / 'joined-interruption'
        root.mkdir(parents=True, exist_ok=True)
        workspace = Path(tempfile.mkdtemp(prefix='squad-muse-interruption-'))
        self.addCleanup(lambda: __import__('shutil').rmtree(workspace))
        config = dict(runtime='muse', role='worker', workspace=str(workspace),
                      client_executable=os.environ['SQUAD_MUSE_EXECUTABLE'], model=current.MODEL, effort='max')
        original = current.NativeCLI(root / 'state', config)
        self.addCleanup(original.close)
        original.turn('Reply exactly ORIGINAL_MUSE_TURN. Do not use tools.', 'interrupted', resume=False)
        before = json.loads(original.input_path('interrupted').read_text())
        original.close()
        retained = json.loads(original.input_path('interrupted').read_text())
        self.assertEqual(retained['state'], 'joined-unresolved')
        self.assertNotIn('terminal', retained)
        self.assertEqual(retained['command_id'], before['command_id'])
        resumed = current.NativeCLI(root / 'state', config)
        self.addCleanup(resumed.close)
        resumed.turn('Reply exactly JOINED_MUSE_RESUME_READY. Do not use tools.', 'fresh-continuation', resume=True)
        terminal = resumed.wait('fresh-continuation', seconds=180)
        self.assertEqual(terminal['native'], original.config['native_session_id'])
        self.assertEqual(terminal['status'], 'completed')
        self.assertEqual(terminal['exit_code'], 0)
        last = json.loads((root / 'state/last-turn.json').read_text())
        self.assertTrue(any('JOINED_MUSE_RESUME_READY' in value for value in last['assistant_texts']))
        current.atomic(root / 'result.json', dict(original=before, retained=retained, terminal=terminal,
                       effective=resumed.effective, join=json.loads((root / 'state/native-join.json').read_text())))

    def test_actual_native_tool_boundary_and_same_session_resume(self):
        root = Path(os.environ['SQUAD_MUSE_EVIDENCE']).resolve()
        root.mkdir(parents=True, exist_ok=True)
        workspace = Path(tempfile.mkdtemp(prefix='squad-muse-workspace-'))
        bridge = root / 'fixture_mcp.py'
        bridge.write_text('''import json,sys,os
from pathlib import Path
for line in sys.stdin:
 r=json.loads(line); method=r.get('method'); result={}
 if method=='initialize': result={'protocolVersion':'2024-11-05','serverInfo':{'name':'owned-fixture','version':'1'},'capabilities':{'tools':{}}}
 elif method=='tools/list': result={'tools':[{'name':'owned_probe','description':'Return the owned fixture secret. Call exactly once.','inputSchema':{'type':'object','properties':{},'additionalProperties':False}}]}
 elif method=='tools/call':
  Path(sys.argv[1]).write_text(json.dumps({'native':os.environ['MUSE_SESSION_ID'],'name':r['params']['name']}))
  result={'content':[{'type':'text','text':'OWNED_MCP_82A7'}]}
 if 'id' in r: print(json.dumps({'jsonrpc':'2.0','id':r['id'],'result':result}),flush=True)
''')
        marker = root / 'tool-call.json'
        config = dict(runtime='muse', role='worker', workspace=str(workspace),
                      client_executable=os.environ['SQUAD_MUSE_EXECUTABLE'],
                      model=current.MODEL, effort='max')
        native = current.NativeCLI(root / 'state', config,
                     {'source': {'command': sys.executable, 'args': [str(bridge), str(marker)]}})
        self.addCleanup(native.close)
        self.addCleanup(lambda: __import__('shutil').rmtree(workspace))
        native.turn('Call the owned_probe MCP tool exactly once. Report its returned token, then report whether direct shell execution and direct file writing tools are available. Do not use any other tools.', 'first', resume=False)
        first = native.wait('first', seconds=180)
        self.assertEqual(first['status'], 'completed')
        self.assertEqual(first['exit_code'], 0)
        self.assertEqual(json.loads(marker.read_text())['native'], first['native'])
        first_evidence = json.loads((root / 'state/last-turn.json').read_text())
        self.assertTrue(any('OWNED_MCP_82A7' in s for s in first_evidence['assistant_texts']))
        native.turn('Reply exactly SAME_NATIVE_MUSE_READY. Do not use tools.', 'second', resume=True)
        second = native.wait('second', seconds=180)
        self.assertEqual(first['native'], second['native'])
        self.assertEqual(second['status'], 'completed')
        self.assertEqual(second['exit_code'], 0)
        second_evidence = json.loads((root / 'state/last-turn.json').read_text())
        self.assertTrue(any('SAME_NATIVE_MUSE_READY' in s for s in second_evidence['assistant_texts']))
        current.atomic(root / 'result.json', dict(first=first, second=second, config=config,
                       first_evidence=first_evidence, second_evidence=second_evidence,
                       join=json.loads((root / 'state/native-join.json').read_text())))


if __name__ == '__main__':
    unittest.main()
