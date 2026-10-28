import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import codex_native_tool_gate as gate
import codex_worker_tools as tools
from muse_worker_tools import child_environment


class CodexSourceToolsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve(); self.work = self.root / 'work'; self.work.mkdir()
        self.refs = {}
        for name in ('role', 'profile', 'lifecycle'):
            path = self.root / (name + '.txt'); path.write_text(name)
            self.refs[name] = {'path': str(path), 'sha256': hashlib.sha256(name.encode()).hexdigest()}
        self.c = {'runtime': 'codex', 'workspace': str(self.work), 'execution_binding': str(self.root / 'binding.json'),
                  'native_session_id': 'native', 'execution_id': 'execution', 'startup_references': self.refs}
        self.path = self.root / 'config.json'; self.path.write_text(json.dumps(self.c))
        with patch.dict(os.environ, {'CODEX_THREAD_ID': 'native'}), patch('muse_worker_tools.Runtime'):
            self.bridge = tools.CodexBridge(self.path)
        self.addCleanup(self.bridge.lock.close)

    def test_startup_reads_are_required_and_verified_before_mutation(self):
        with patch('muse_worker_tools.coordination'):
            for name, ref in self.refs.items():
                self.assertEqual(self.bridge.call('read_file', {'path': ref['path']})['sha256'], ref['sha256'])
                if name != 'lifecycle':
                    with self.assertRaisesRegex(ValueError, 'all exact startup'):
                        self.bridge.call('write_file', {'path': 'marker', 'content': 'BAD', 'expected_sha256': 'absent'})
            self.bridge.call('write_file', {'path': 'marker', 'content': 'OK', 'expected_sha256': 'absent'})
        self.assertEqual((self.work / 'marker').read_text(), 'OK')

    def test_changed_startup_reference_and_read_escape_are_rejected(self):
        Path(self.refs['role']['path']).write_text('changed')
        with patch('muse_worker_tools.coordination'):
            with self.assertRaisesRegex(ValueError, 'reference changed'):
                self.bridge.call('read_file', {'path': self.refs['role']['path']})
            outside = self.root / 'outside'; outside.write_text('private')
            with self.assertRaisesRegex(ValueError, 'assigned source'):
                self.bridge.call('read_file', {'path': str(outside)})

    def test_stale_custody_rejects_even_read_only_tools(self):
        with patch('muse_worker_tools.coordination', side_effect=ValueError('stale custody')):
            with self.assertRaisesRegex(ValueError, 'stale custody'):
                self.bridge.call('read_file', {'path': self.refs['role']['path']})

    def test_foreign_native_environment_is_not_a_codex_identity(self):
        self.bridge.lock.close()
        with patch.dict(os.environ, {'CODEX_THREAD_ID': 'foreign', 'MUSE_SESSION_ID': 'native'}), patch('muse_worker_tools.Runtime'):
            with self.assertRaisesRegex(ValueError, 'identity mismatch'):
                tools.CodexBridge(self.path)

    def test_gate_denies_all_direct_tools_and_foreign_native(self):
        config = {'role': 'worker', 'native_session_id': 'native'}
        event = {'session_id': 'native', 'hook_event_name': 'PreToolUse'}
        for tool in ('Bash', 'exec_command', 'write_stdin', 'apply_patch', 'spawn_agent', 'mcp__other__write_file'):
            self.assertFalse(gate.decision(config, dict(event, tool_name=tool)), tool)
        self.assertTrue(gate.decision(config, dict(event, tool_name='mcp__squad_worker__write_file')))
        self.assertFalse(gate.decision(config, dict(event, session_id='other', tool_name='mcp__squad_worker__write_file')))

    def test_codex_coordination_uses_its_own_runtime_identity(self):
        c = dict(self.c, ledger_directory=str(self.root), coordination_home=str(self.root / 'home'), agent_id='worker')
        env = child_environment(c)
        self.assertTrue(env['SQUAD_SESSION_ID'].startswith('codex:native:'))
        self.assertNotIn('MUSE_SESSION_ID', env)


if __name__ == '__main__': unittest.main()
