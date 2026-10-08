"""No network, credentials, systemd, or live state in updater regression tests."""
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('updater', Path(__file__).with_name('update.py'))
updater = importlib.util.module_from_spec(spec)
spec.loader.exec_module(updater)


class UpdateTests(unittest.TestCase):
    def test_exact_archive_ignores_dirty_and_untracked_build_input(self):
        with tempfile.TemporaryDirectory() as tmp:
            source, output = Path(tmp)/'source', Path(tmp)/'output'
            source.mkdir()
            output.mkdir()
            def git(*args):
                return subprocess.check_output(['git', '-C', str(source), *args], text=True).strip()
            git('init', '-q')
            (source/'main.go').write_text('package main // qualified\n')
            git('add', '.')
            git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid',
                'commit', '-qm', 'fixture')
            sha = git('rev-parse', 'HEAD')
            (source/'main.go').write_text('package main // unreviewed modification\n')
            (source/'extra.go').write_text('package main // untracked init\n')
            updater.export_commit(source, sha, output, prefix=())
            self.assertEqual((output/'main.go').read_text(), 'package main // qualified\n')
            self.assertFalse((output/'extra.go').exists())

    def test_store_change_stops_before_build_or_service_mutation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            previous = root/'releases'/'old'
            previous.mkdir(parents=True)
            (previous/'manifest.json').write_text(json.dumps({'sha': 'old'}))
            (root/'current').symlink_to(previous)
            calls = []
            def build(*args):
                calls.append(args)
                if args[:2] == ('git', 'rev-parse'):
                    return 'new'
                if args[:2] == ('git', 'diff'):
                    return 'internal/store/migrations/024.sql'
                return ''
            runs = json.dumps([{'headSha': 'new', 'status': 'completed',
                                'conclusion': 'success', 'databaseId': 1}])
            with patch.object(updater, 'ROOT', root), patch.object(updater, 'build_run', build), \
                    patch.object(updater, 'run', return_value=runs) as run:
                with self.assertRaisesRegex(RuntimeError, 'compatibility admission'):
                    updater.main()
                self.assertEqual(run.call_count, 1)  # only gh metadata, no systemctl
            self.assertFalse(any(args[0] == 'go' for args in calls))
            self.assertEqual((root/'current').resolve(), previous.resolve())


if __name__ == '__main__':
    unittest.main()
