import json
from pathlib import Path
import unittest

from test_worker_preflight import WorkerPreflightTests
from test_claude_worker_launcher import LauncherTests
import worker_preflight as preflight


class InterceptorPreflightTests(unittest.TestCase):
    setUp = WorkerPreflightTests.setUp
    git = WorkerPreflightTests.git
    check = WorkerPreflightTests.check
    def select_interceptor(self):
        self.profile = Path(preflight.ROOT) / 'projects/interceptor/profile.json'
        self.assignment.update(repository='ipt/interceptor', repository_host='git.agoralab.co', clone_layout='bitbucket-server',
                               project_profile={'id':'interceptor','version':1,'path':str(self.profile)})
        self.assignment["authorization"] = {key: key == "source_mutation" for key in self.assignment["authorization"]}
        # Issue intentionally remains in Studio/GitHub, independently of code origin.
        self.git('remote','set-url','origin','ssh://git@git.agoralab.co/ipt/interceptor.git')

    def test_interceptor_with_github_tracking_issue(self):
        self.select_interceptor()
        self.assertEqual('context-checked', self.check()['status'])

    def test_interceptor_rejects_foreign_host_same_path(self):
        self.select_interceptor()
        for remote in ('ssh://git@evil.example/ipt/interceptor.git', 'git@github.com:ipt/interceptor.git',
                       'ssh://git@git.agoralab.co/ipt/other.git'):
            self.git('remote','set-url','origin',remote)
            with self.assertRaises(preflight.ValidationError): self.check()

    def test_profile_host_cannot_be_omitted_or_substituted(self):
        self.select_interceptor()
        for host in ('github.com','evil.example'):
            self.assignment['repository_host']=host
            with self.assertRaisesRegex(preflight.ValidationError,'profile identity'): self.check()

    def test_human_pr_lane_rejects_automatic_delivery_authority(self):
        self.select_interceptor()
        for key in ('pull_request','merge','staging','production','issue_close'):
            self.assignment['authorization'][key]=True
            with self.assertRaisesRegex(preflight.ValidationError,'human-PR'):self.check()
            self.assignment['authorization'][key]=False

    def test_remote_parser_accepts_only_explicit_host_and_safe_paths(self):
        for remote in ('ssh://git@git.agoralab.co/ipt/interceptor.git',
                       'git@git.agoralab.co:ipt/interceptor.git',
                       'https://git.agoralab.co/scm/ipt/interceptor.git'):
            self.assertEqual('ipt/interceptor',preflight.repository_name(remote,'git.agoralab.co','bitbucket-server'))
        for remote in ('/tmp/ipt/interceptor', 'https://git.agoralab.co.evil/scm/ipt/interceptor.git',
                       'https://token:secret@git.agoralab.co/scm/ipt/interceptor.git',
                       'https://git.agoralab.co/scm/../interceptor',
                       'https://git.agoralab.co/scm/ipt/interceptor.git?x=1'):
            with self.assertRaises(preflight.ValidationError):preflight.repository_name(remote,'git.agoralab.co','bitbucket-server')


class InterceptorLauncherTests(unittest.TestCase):
    setUp = LauncherTests.setUp
    git = LauncherTests.git
    prepare = LauncherTests.prepare
    save_rows = LauncherTests.save_rows
    run_launcher = LauncherTests.run_launcher
    def test_interceptor_binds_github_tracking_reservation(self):
        InterceptorPreflightTests.select_interceptor(self)
        self.prepare()
        self.row.update(state='dispatched',worker_thread_id=self.session);self.save_rows()
        result=self.run_launcher()
        self.assertEqual(0,result.returncode,result.stderr)
        self.assertTrue(self.started.exists())

    def test_direct_launcher_cannot_borrow_profile(self):
        InterceptorPreflightTests.select_interceptor(self)
        self.assignment['project_profile']['path']=str(preflight.ROOT/'projects/studio/profile.json')
        self.prepare()
        result=self.run_launcher(check=True)
        self.assertNotEqual(0,result.returncode)
        self.assertFalse(self.started.exists())

class HTTPSLayoutTests(unittest.TestCase):
    def test_github_rejects_scm_alias_but_accepts_real_scm_owner(self):
        self.assertEqual('scm/widget',preflight.repository_name('https://github.com/scm/widget.git'))
        for remote in ('https://github.com/scm/acme/widget.git','https://git@github.com/scm/acme/widget.git','https://github.com//acme/widget.git'):
            with self.assertRaises(preflight.ValidationError):preflight.repository_name(remote)
        with self.assertRaises(preflight.ValidationError):
            preflight.repository_name('https://github.com/scm/acme/widget.git','github.com','bitbucket-server')

    def test_bitbucket_layout_is_explicit_and_profile_bound(self):
        remote='https://git.example.test/scm/acme/widget.git'
        with self.assertRaises(preflight.ValidationError):preflight.repository_name(remote,'git.example.test')
        self.assertEqual('acme/widget',preflight.repository_name(remote,'git.example.test','bitbucket-server'))
        with self.assertRaises(preflight.ValidationError):
            preflight.repository_name('https://git.example.test/acme/widget.git','git.example.test','bitbucket-server')
