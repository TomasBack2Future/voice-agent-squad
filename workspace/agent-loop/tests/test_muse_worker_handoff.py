import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parents[1]))
from muse_worker_handoff import check_handoff


class HandoffStartupTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / 'work').mkdir()
        custody = dict(native_session_id='old-native', agent_id='old', claim_generation=1,
                       dispatcher_agent_id='controller', controller_native='controller-native', controller_epoch=2)
        self.a = dict(reservation=dict(key='D', generation=2), item='T', issue='owner/repo#1',
                      authorization=dict(source_mutation=True), worktree=str(self.root / 'work'))
        prior = dict(self.a, reservation=dict(key='D', generation=1))
        self.writer = dict(state='joined', custody=custody, assignment=prior, evidence=str(self.root))
        self.path = self.root / 'writer.json'
        self.path.write_text(json.dumps(self.writer))
        (self.root / 'join.json').write_text(json.dumps(dict(joined=True, binding=dict(id='pin', native='old-native'))))
        self.c = dict(custody, native_session_id='new-native', agent_id='new', claim_generation=2,
                      coordination_executable='/fixture/squad', ledger_directory=str(self.root), workspace=self.a['worktree'],
                      handoff=dict(request_id='handoff', prior_writer=str(self.path),
                                   prior_writer_sha256=hashlib.sha256(self.path.read_bytes()).hexdigest()))
        self.receipt = dict(request_id='handoff', previous=dict(worker_thread_id='old-native'),
                            previous_actor='old', claim_generation=2, execution_id='pin',
                            reservation=dict(worker_thread_id='new-native', generation=2, reservation_key='D',
                                             canonical_item_id='T', source_ref='github:owner/repo#1', reserved_by='controller'))

    def check(self):
        with patch('muse_worker_handoff.child_environment', return_value={}), patch('muse_worker_handoff.subprocess.run',
                   return_value=subprocess.CompletedProcess([], 0, json.dumps(self.receipt), '')):
            return check_handoff(self.a, self.c)

    def test_exact_joined_assignment_is_retained(self):
        self.assertEqual(self.check(), self.receipt)

    def test_changed_receipt_cannot_adopt(self):
        self.receipt['reservation']['generation'] = 3
        with self.assertRaisesRegex(ValueError, 'exact assignment'):
            self.check()

    def test_changed_writer_cannot_adopt(self):
        self.path.write_text('{}')
        with self.assertRaisesRegex(ValueError, 'evidence changed'):
            self.check()

    def test_missing_tool_join_cannot_adopt(self):
        (self.root / 'join.json').write_text(json.dumps(dict(joined=False, binding=dict(id='pin', native='old-native'))))
        with self.assertRaisesRegex(ValueError, 'native/tool join'):
            self.check()

    def test_authorization_cannot_expand(self):
        self.a['authorization'] = dict(source_mutation=True, production=True)
        with self.assertRaisesRegex(ValueError, 'authorization'):
            self.check()


class LegacyHandoffStartupTests(HandoffStartupTests):
    def setUp(self):
        super().setUp()
        self.c['handoff'] = dict(request_id='handoff', legacy_stop_id='legacy-stop')
        self.stop = dict(id='legacy-stop', state='transferred', observation_sha256='b' * 64,
                         processes=[dict(pid=100, start='verified', executable='fixture')], workspace_sha256='c' * 64,
                         input=dict(workspace=self.c['workspace'], assignment=self.writer['assignment'],
                                    handoff=dict(controller=dict(actor='controller', native_session='controller-native', epoch=2),
                                                 expected=self.receipt['previous'], claim=dict(actor='old', generation=1))))
        self.receipt.update(execution_id='', legacy_stop=self.stop)

    def check(self):
        results = [subprocess.CompletedProcess([], 0, json.dumps(self.receipt), ''),
                   subprocess.CompletedProcess([], 0, json.dumps({'state': 'verified', 'workspace_sha256': self.stop['workspace_sha256']}), '')]
        with patch('muse_worker_handoff.child_environment', return_value={}), patch('muse_worker_handoff.subprocess.run', side_effect=results):
            return check_handoff(self.a, self.c)

    def test_changed_writer_cannot_adopt(self):
        self.c['handoff']['prior_writer'] = str(self.path)
        with self.assertRaisesRegex(ValueError, 'separate'):
            self.check()

    def test_missing_tool_join_cannot_adopt(self):
        self.stop['state'] = 'prepared'
        with self.assertRaisesRegex(ValueError, 'stopped-custody'):
            self.check()

    def test_legacy_cannot_manufacture_source_pin(self):
        self.receipt['execution_id'] = 'manufactured-pin'
        with self.assertRaisesRegex(ValueError, 'stopped-custody'):
            self.check()
