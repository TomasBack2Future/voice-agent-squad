import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('skill_sync', Path(__file__).parents[1] / 'skill_sync.py')
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


class SkillSyncTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / 'source repo'
        self.target = self.root / 'workspace'
        self.repo.mkdir()
        self.git('init', '-b', 'main')
        self.git('config', 'user.email', 'test@example.invalid')
        self.git('config', 'user.name', 'Test')
        self.skill = self.repo / 'package' / 'tools' / 'example'
        self.skill.mkdir(parents=True)
        (self.skill / 'SKILL.md').write_text('---\nname: example\ndescription: Example\n---\nVersion one\n')
        (self.repo / 'package' / 'README.md').write_text('References stay in the package.\n')
        self.commit()
        self.hooks = self.repo / '.git' / 'hooks'
        self.config_path = self.hooks / 'squad-skill-sync.json'

    def git(self, *args, cwd=None):
        return subprocess.check_output(['git', '-C', str(cwd or self.repo), *args], stderr=subprocess.PIPE).decode().strip()

    def commit(self):
        self.git('add', '.')
        self.git('commit', '-m', 'test')

    def install(self):
        return sync.install(self.repo, self.target, 'package', 'main')

    def synchronize(self, **kwargs):
        return sync.sync(json.loads(self.config_path.read_text()), **kwargs)

    def test_install_update_delete_and_reference_tree(self):
        first = self.install()
        for client in sync.CLIENTS:
            path = self.target / client / 'skills' / 'example'
            self.assertIn('Version one', (path / 'SKILL.md').read_text())
            self.assertTrue((path.resolve().parents[1] / 'README.md').exists())
        self.assertEqual(first['commit'], self.git('rev-parse', 'HEAD'))
        (self.skill / 'SKILL.md').write_text('---\nname: renamed\n---\nVersion two\n')
        self.commit()
        updated = self.synchronize()
        self.assertNotEqual(first['commit'], updated['commit'])
        for client in sync.CLIENTS:
            self.assertFalse(os.path.lexists(self.target / client / 'skills' / 'example'))
            self.assertIn('Version two', (self.target / client / 'skills' / 'renamed' / 'SKILL.md').read_text())
        self.git('rm', 'package/tools/example/SKILL.md')
        self.commit()
        self.assertEqual(self.synchronize()['skills'], [])

    def test_unowned_conflict_causes_no_partial_install(self):
        conflict = self.target / '.claude' / 'skills' / 'example'
        conflict.mkdir(parents=True)
        (conflict / 'SKILL.md').write_text('local')
        with self.assertRaisesRegex(ValueError, 'conflict'):
            self.install()
        self.assertFalse(self.config_path.exists())
        self.assertFalse((self.target / '.codex' / 'skills' / 'example').exists())
        self.assertEqual((conflict / 'SKILL.md').read_text(), 'local')

    def test_local_link_edit_blocks_entire_update(self):
        self.install()
        path = self.target / '.claude' / 'skills' / 'example'
        path.unlink()
        path.symlink_to(self.skill)
        with self.assertRaisesRegex(ValueError, 'conflict'):
            self.synchronize()
        self.assertEqual(path.resolve(), self.skill.resolve())

    def test_dirty_source_not_published_and_check_does_not_switch(self):
        self.install()
        (self.skill / 'SKILL.md').write_text('---\nname: example\n---\nUncommitted\n')
        self.synchronize()
        installed = self.target / '.codex' / 'skills' / 'example' / 'SKILL.md'
        self.assertIn('Version one', installed.read_text())
        self.commit()
        self.assertEqual(self.synchronize(check=True)['status'], 'checked')
        self.assertIn('Version one', installed.read_text())
        self.synchronize()
        self.assertIn('Uncommitted', installed.read_text())

    def test_old_hook_preserved_and_git_checkout_triggers_sync(self):
        old = self.hooks / 'post-checkout'
        marker = self.root / 'old-hook-ran'
        old.write_text('#!/bin/sh\nprintf "%s" "$3" > ' + sync.shlex.quote(str(marker)) + '\n')
        old.chmod(0o755)
        self.install()
        self.install()  # Idempotent installation does not wrap the wrapper.
        self.git('checkout', '-b', 'feature')
        (self.skill / 'SKILL.md').write_text('---\nname: example\n---\nFeature\n')
        self.commit()
        self.git('checkout', 'main')
        self.assertEqual(marker.read_text(), '1')
        installed = self.target / '.codex' / 'skills' / 'example' / 'SKILL.md'
        self.assertIn('Version one', installed.read_text())
        self.git('merge', '--ff-only', 'feature')
        self.assertIn('Feature', installed.read_text())

    def test_other_worktree_does_not_publish(self):
        self.install()
        worktree = self.root / 'worker'
        self.git('worktree', 'add', '-b', 'worker', str(worktree))
        (worktree / 'package' / 'tools' / 'example' / 'SKILL.md').write_text('---\nname: example\n---\nWorker\n')
        self.git('add', '.', cwd=worktree)
        self.git('commit', '-m', 'worker', cwd=worktree)
        self.git('checkout', 'HEAD', '--', 'package', cwd=worktree)
        self.assertIn('Version one', (self.target / '.codex' / 'skills' / 'example' / 'SKILL.md').read_text())

    def test_symlink_source_and_duplicate_names_rejected(self):
        (self.skill / 'secret').symlink_to('/etc/passwd')
        self.commit()
        with self.assertRaisesRegex(ValueError, 'symlink'):
            self.install()
        self.git('rm', 'package/tools/example/secret')
        duplicate = self.repo / 'package' / 'duplicate'
        duplicate.mkdir()
        (duplicate / 'SKILL.md').write_text('---\nname: example\n---\n')
        self.commit()
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            self.install()

    def test_snapshot_tampering_is_not_overwritten(self):
        self.install()
        installed = self.target / '.codex' / 'skills' / 'example' / 'SKILL.md'
        installed.write_text('local change')
        with self.assertRaisesRegex(ValueError, 'snapshot modified'):
            self.synchronize()
        self.assertEqual(installed.read_text(), 'local change')

    def test_custom_hooks_path_untouched(self):
        self.git('config', 'core.hooksPath', str(self.root / 'custom'))
        with self.assertRaisesRegex(ValueError, 'core.hooksPath'):
            self.install()
        self.assertFalse(self.config_path.exists())

    def test_linked_client_directory_rejected(self):
        self.target.mkdir()
        elsewhere = self.root / 'elsewhere'
        elsewhere.mkdir()
        (self.target / '.claude').symlink_to(elsewhere)
        with self.assertRaisesRegex(ValueError, 'directory is a symlink'):
            self.install()
        self.assertEqual(list(elsewhere.iterdir()), [])

    def test_uninstall_restores_prior_hook_and_keeps_snapshot(self):
        hook = self.hooks / 'post-merge'
        original = '#!/bin/sh\nexit 0\n'
        hook.write_text(original)
        hook.chmod(0o755)
        self.install()
        sync.uninstall(self.repo)
        self.assertEqual(hook.read_text(), original)
        self.assertFalse(self.config_path.exists())
        self.assertTrue((self.target / '.codex' / 'skills' / 'example' / 'SKILL.md').exists())

    def test_concurrent_runs_converge(self):
        self.install()
        script = Path(sync.__file__)
        processes = [subprocess.Popen([sync.sys.executable, str(script), 'run', '--config', str(self.config_path)], stdout=subprocess.PIPE, stderr=subprocess.PIPE) for _ in range(4)]
        for process in processes:
            _, error = process.communicate(timeout=20)
            self.assertEqual(process.returncode, 0, error)
        self.assertEqual(self.synchronize()['status'], 'synced')

    def test_detached_checkout_skips(self):
        self.install()
        self.git('checkout', '--detach', 'HEAD')
        self.assertEqual(self.synchronize()['status'], 'skipped')

    def test_interrupted_pointer_switch_is_retryable(self):
        self.install()
        (self.skill / 'SKILL.md').write_text('---\nname: renamed\n---\nUpdated\n')
        self.commit()
        replace = os.replace
        def fail_switch(source, destination):
            if Path(destination).name == 'current':
                raise OSError('simulated filesystem failure')
            return replace(source, destination)
        with mock.patch.object(sync.os, 'replace', side_effect=fail_switch):
            with self.assertRaisesRegex(OSError, 'simulated'):
                self.synchronize()
        self.synchronize()
        self.assertTrue((self.target / '.codex' / 'skills' / 'renamed' / 'SKILL.md').exists())
        self.assertFalse(os.path.lexists(self.target / '.codex' / 'skills' / 'example'))

    def test_skill_directory_move_keeps_name(self):
        self.install()
        self.git('mv', 'package/tools/example', 'package/example')
        self.commit()
        self.synchronize()
        self.assertIn('Version one', (self.target / '.codex' / 'skills' / 'example' / 'SKILL.md').read_text())


if __name__ == '__main__':
    unittest.main()
