import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
sys.path.insert(0, str(Path(__file__).parents[1]))
from claude_workspace_trust import workspace_trust


class ClaudeTrustTests(unittest.TestCase):
    def test_exact_worktree_trust_preserves_other_projects_and_permissions(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); work = root / 'owned-worktree'; work.mkdir()
            env = {'CLAUDE_CONFIG_DIR': str(root / 'config'), 'HOME': str(root / 'home')}
            config = root / 'config/.claude.json'; config.parent.mkdir()
            original = {'projects': {'/other': {'hasTrustDialogAccepted': False}}, 'unrelated': {'preserve': True}}
            config.write_text(json.dumps(original))
            self.assertEqual(workspace_trust(work, env)['status'], 'will-establish')
            self.assertEqual(json.loads(config.read_text()), original)
            self.assertEqual(workspace_trust(work, env, True)['status'], 'established')
            result = json.loads(config.read_text())
            self.assertEqual(result['projects'].pop(str(work.resolve())), {'hasTrustDialogAccepted': True})
            self.assertEqual(result, original)
            self.assertEqual(os.stat(config).st_mode & 0o777, 0o600)
            self.assertEqual(workspace_trust(work, env)['status'], 'established')

    def test_native_configuration_lock_blocks_without_overwriting(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); work = root / 'work'; work.mkdir()
            config = root / '.claude.json'; config.write_text('{"native_update": true}')
            lock = root / '.claude.json.lock'; lock.mkdir()
            with self.assertRaisesRegex(ValueError, 'configuration busy'):
                workspace_trust(work, {'HOME': str(root)}, True)
            self.assertEqual(config.read_text(), '{"native_update": true}')
            self.assertTrue(lock.is_dir())
            lock.rmdir()
            workspace_trust(work, {'HOME': str(root)}, True)
            self.assertTrue(json.loads(config.read_text())['native_update'])
            self.assertFalse(lock.exists())

    def test_invalid_config_and_symlink_never_overwritten(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); work = root / 'work'; work.mkdir(); config = root / '.claude.json'
            env = {'HOME': str(root)}
            for text in ('bad json', '[]', '{"projects": []}'):
                config.write_text(text)
                with self.assertRaises(ValueError): workspace_trust(work, env, True)
                self.assertEqual(config.read_text(), text)
            config.unlink(); config.symlink_to(root / 'outside')
            with self.assertRaises(ValueError): workspace_trust(work, env, True)
            with self.assertRaises(ValueError): workspace_trust(root, env, True)


@unittest.skipUnless(os.environ.get('SQUAD_CLAUDE_TRUST_BINARY'), 'explicit native Claude trust qualification required')
class ClaudeNativeTrustTests(unittest.TestCase):
    def test_trust_dialog_before_and_after_in_an_isolated_linked_worktree(self):
        import pty
        import re
        import select
        import signal
        import subprocess
        import time
        with tempfile.TemporaryDirectory(prefix='claude-trust-qualification-') as tmp:
            root = Path(tmp); repo = root / 'repo'; work = root / 'work'; home = root / 'home'; home.mkdir()
            def git(*args): subprocess.run(['git', *args], check=True, capture_output=True)
            git('init', '-b', 'main', str(repo))
            git('-C', str(repo), 'config', 'user.name', 'Test')
            git('-C', str(repo), 'config', 'user.email', 'test@example.invalid')
            git('-C', str(repo), 'commit', '--allow-empty', '-m', 'fixture')
            git('-C', str(repo), 'worktree', 'add', '-b', 'worker', str(work))
            def screen(trusted):
                config = root / ('trusted' if trusted else 'untrusted'); config.mkdir()
                key = 'sk-ant-isolated-test-not-a-real-key'
                path = config / '.claude.json'
                path.write_text(json.dumps({'hasCompletedOnboarding': True, 'theme': 'dark',
                    'customApiKeyResponses': {'approved': [key[-20:]], 'rejected': []},
                    'projects': {str(repo): {'hasTrustDialogAccepted': False}}}))
                env = {k: v for k, v in os.environ.items() if not any(token in k.upper() for token in
                       ('ANTHROPIC', 'CLAUDE', 'SQUAD', 'CODEX', 'MUSE', 'TOKEN', 'API_KEY'))}
                env.update(HOME=str(home), CLAUDE_CONFIG_DIR=str(config), ANTHROPIC_API_KEY=key,
                           CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC='1', DISABLE_AUTOUPDATER='1', TERM='xterm-256color')
                if trusted: workspace_trust(work, env, True)
                master, slave = pty.openpty()
                p = subprocess.Popen([os.environ['SQUAD_CLAUDE_TRUST_BINARY'], '--model', 'sonnet', '--permission-mode', 'default'],
                                     cwd=work, env=env, stdin=slave, stdout=slave, stderr=slave, start_new_session=True)
                os.close(slave); data = b''
                try:
                    deadline = time.monotonic() + 12
                    while time.monotonic() < deadline and p.poll() is None:
                        if select.select([master], [], [], .2)[0]:
                            try: data += os.read(master, 65536)
                            except OSError: break
                finally:
                    if p.poll() is None: os.killpg(p.pid, signal.SIGTERM)
                    try: p.wait(timeout=5)
                    except subprocess.TimeoutExpired: os.killpg(p.pid, signal.SIGKILL); p.wait()
                    os.close(master)
                text = re.sub(r'\x1b\[[0-?]*[ -/]*[@-~]', '', data.decode(errors='replace'))
                normalized = re.sub(r'\s+', '', text.lower())
                prompt = any(token in normalized for token in ('trustthisfolder', 'trustthefiles', 'projectyoucreated', 'quicksafetycheck'))
                if trusted:
                    # Never broaden the persisted trust to the main checkout.
                    self.assertFalse(json.loads(path.read_text())['projects'][str(repo)]['hasTrustDialogAccepted'])
                return prompt, normalized
            self.assertTrue(screen(False)[0], 'native baseline did not reproduce the trust prompt')
            prompt, text = screen(True)
            self.assertFalse(prompt)
            self.assertIn('claudecode', text)
            self.assertIn('manualmode', text)

if __name__ == '__main__': unittest.main()
