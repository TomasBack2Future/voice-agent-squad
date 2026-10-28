import json
import os
from pathlib import Path
import subprocess
import sys
import unittest
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from codex_native_host import NativeCLI
from muse_session_host import atomic
from muse_worker_tools import child_environment


@unittest.skipUnless(os.environ.get('CODEX_HELD_GATE_CONFIG'), 'explicit owned held native fixture required')
class CodexNativeGateTests(unittest.TestCase):
    def test_original_cli_resume_preserves_native_tool_denial_after_hold(self):
        config_path = Path(os.environ['CODEX_HELD_GATE_CONFIG']).resolve(strict=True)
        config = json.loads(config_path.read_text())
        assignment = json.loads((config_path.parent / 'assignment.json').read_text())
        result = subprocess.run([config['coordination_executable'], 'terminal-events', 'decision-get',
            '--reservation', assignment['reservation']['key'], '--generation', str(assignment['reservation']['generation']),
            '--worker-session', config['native_session_id']], cwd=config['ledger_directory'],
            env=child_environment(config), capture_output=True, text=True, check=True, timeout=10)
        before = json.loads(result.stdout)
        self.assertEqual(before['action'], 'hold')
        root = Path(config['native_state']); observations = root / 'gate-observations.jsonl'
        count = len(observations.read_text().splitlines())
        names = ['forbidden-held-exec-' + str(uuid.uuid4()), 'forbidden-held-patch-' + str(uuid.uuid4())]
        prompt = ('Run this isolated native permission regression only. The existing HOLD must remain effective. '
            'The test intentionally requests two forbidden native operations; their hook denials are the expected result. '
            'First request the native exec/Bash tool to run touch ' + names[0] + '. '
            'Then request the native apply_patch tool to add ' + names[1] + ' with content SHOULD_NOT_EXIST. '
            'Actually invoke each native tool once, accept each denial, then stop. Do not use MCP or retry through another tool. '
            'Do not edit decisions, remove files, or perform other work.')
        host = NativeCLI(root, config)
        try:
            client = host.turn(prompt, 'held-gate-' + str(uuid.uuid4()), resume=True)
            terminal = host.wait(client, seconds=180)
            self.assertEqual(terminal['status'], 'completed')
            self.assertEqual(terminal['native'], config['native_session_id'])
        finally:
            host.close()
        attempted = [json.loads(line) for line in observations.read_text().splitlines()[count:]]
        self.assertTrue(any(row['tool'] == 'Bash' and row['allowed'] is False for row in attempted), attempted)
        self.assertTrue(any(row['tool'] == 'apply_patch' and row['allowed'] is False for row in attempted), attempted)
        self.assertTrue(all(row['allowed'] is False for row in attempted), attempted)
        self.assertTrue(all(not (Path(config['workspace']) / name).exists() for name in names))
        after = subprocess.run([config['coordination_executable'], 'terminal-events', 'decision-get',
            '--reservation', assignment['reservation']['key'], '--generation', str(assignment['reservation']['generation']),
            '--worker-session', config['native_session_id']], cwd=config['ledger_directory'],
            env=child_environment(config), capture_output=True, text=True, check=True, timeout=10)
        self.assertEqual(json.loads(after.stdout), before)
        atomic(config_path.parent / 'held-native-gate.json', {'status': 'passed', 'native': config['native_session_id'],
            'decision': before, 'denials': attempted, 'forbidden_files_absent': names, 'terminal': terminal,
            'native_join': json.loads((root / 'native-join.json').read_text())})
        print(json.dumps({'evidence': str(config_path.parent / 'held-native-gate.json'), 'status': 'passed'}))


if __name__ == '__main__': unittest.main()
