import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import codex_dispatcher_launcher as launcher
from codex_worker_launcher import digest, selection
from validate_context_package import ValidationError
import test_codex_control_plane as helpers


class DispatcherContinuityTests(unittest.TestCase):
    def setUp(self):
        helpers.CodexControlPlaneTests.setUp(self)
        self.c.update(schema_version='agent-loop.codex-dispatcher-launch.v1',
                      agent_id='dispatcher', worktree=str(self.root), controller_epoch=1,
                      transition_file=str(self.root / 'transition.json'))
        self.transition = dict(schema_version='agent-loop.codex-dispatcher-transition.v1',
            native_session_id=self.c['native_session_id'], agent_id='dispatcher',worktree=str(self.root),
            prior_selection=dict(selection(self.c),client='app'), checkpoint_file=str(self.prompt),
            checkpoint_sha256=digest(self.prompt), old_writer_pid=999999,old_writer_started_at='old-start',
            old_writer_scope='dedicated-process',old_writer_joined=True,old_receiver_joined=True,external_operations_joined=True,
            authorization_reference='issue:50/d1.2',
            reservation_fences=[dict(key='DISPATCH-1',generation=1,worker_native_session_id=self.c['native_session_id'])])
        self.rows=[dict(reservation_key='DISPATCH-1',generation=1,worker_thread_id=self.c['native_session_id'],
                        reserved_by='dispatcher',state='dispatched')]
        self.persist()

    def persist(self):
        self.path.write_text(json.dumps(self.c))
        Path(self.c['transition_file']).write_text(json.dumps(self.transition))

    def test_continuity_requires_epoch_and_exact_installed_controller(self):
        self.c.pop('controller_epoch');self.persist()
        with self.assertRaises(ValidationError):launcher.config_file(self.path)
        self.c['controller_epoch']=1;self.persist()
        binding=dict(actor='dispatcher',native_session=self.c['native_session_id'],epoch=1)
        with patch.object(launcher.subprocess,'run',return_value=Mock(returncode=0,stdout=json.dumps(binding))):
            self.assertEqual(launcher.controller_binding(self.c),binding)
        for key,value in (('actor','foreign'),('native_session','foreign'),('epoch',2)):
            with self.subTest(key=key), patch.object(launcher,'fence',return_value=self.transition), \
                 patch.object(launcher.subprocess,'run',return_value=Mock(returncode=0,stdout=json.dumps(dict(binding,**{key:value})))), \
                 patch.object(launcher,'RPC') as rpc:
                with self.assertRaisesRegex(ValidationError,'actor/native/epoch mismatch'):launcher.check_launch(self.c)
                rpc.assert_not_called()

    def fence(self, start=None):
        with patch.object(launcher.os,'kill',side_effect=ProcessLookupError if start is None else None), \
             patch.object(launcher.subprocess,'run',return_value=Mock(returncode=0,stdout=json.dumps(self.rows))):
            return launcher.fence(self.c)

    def test_dispatcher_resumes_exact_native_without_setting_overrides(self):
        rpc=helpers.FakeRPC(self.c)
        rpc.loaded=False
        original=rpc.call
        def call(method,params):
            if method=='thread/resume':
                rpc.loaded=True
                return dict(original(method,params),thread={'id':self.c['native_session_id']})
            return original(method,params)
        rpc.call=call
        with patch.object(launcher,'controller_binding',return_value={}), patch.object(launcher,'fence',return_value=self.transition), patch.object(launcher,'check_delivery_runtime'), \
             patch.object(launcher,'check_qualification',return_value={}), \
             patch.object(launcher,'server_identity',return_value=[1,2,3,4,5]), \
             patch.object(launcher,'RPC',return_value=rpc):
            result=launcher.check_launch(launcher.config_file(self.path))
        self.assertEqual(result['callback_agent'],'dispatcher')
        for method,params in rpc.calls:
            if method=='thread/resume':
                self.assertEqual(params,{'threadId':self.c['native_session_id'],'excludeTurns':True})
        self.assertNotIn('thread/start',[m for m,_ in rpc.calls])

    def test_live_pid_with_mismatched_recorded_start_cannot_pass_fence(self):
        with patch.object(launcher.os,'kill',return_value=None), \
             patch.object(launcher.subprocess,'run',return_value=Mock(returncode=0,stdout=json.dumps(self.rows))):
            with self.assertRaisesRegex(ValidationError,'remains active'):launcher.fence(self.c)

    def test_active_old_writer_and_unjoined_operation_reject_before_resume(self):
        with self.assertRaisesRegex(ValidationError,'remains active'):self.fence('old-start')
        for key in ('old_writer_joined','old_receiver_joined','external_operations_joined'):
            old=self.transition[key];self.transition[key]=False;self.persist()
            with self.assertRaises(ValidationError):self.fence()
            self.transition[key]=old

    def test_shared_backend_rejected_without_process_stop_or_native_calls(self):
        self.transition['old_writer_scope']='shared-backend';self.persist()
        with patch.object(launcher.os,'kill',side_effect=AssertionError('must not inspect process as cutover fence')), patch.object(launcher,'RPC') as rpc:
            with self.assertRaisesRegex(ValidationError,'per-thread ownership fence'):launcher.fence(self.c)
            rpc.assert_not_called()

    def test_checkpoint_callback_and_policy_changes_reject(self):
        self.fence()
        self.prompt.write_text('Changed checkpoint')
        with self.assertRaisesRegex(ValidationError,'checkpoint mismatch'):self.fence()
        self.transition['checkpoint_sha256']=digest(self.prompt);self.persist()
        self.rows[0]['reserved_by']='foreign'
        with self.assertRaisesRegex(ValidationError,'custody changed'):self.fence()
        self.rows[0]['reserved_by']='dispatcher'
        self.c['sandbox']='read-only'
        with self.assertRaisesRegex(ValidationError,'policy/checkpoint'):self.fence()

    def test_existing_policy_preserved_for_dispatcher_and_worker_schema(self):
        self.c.update(sandbox='danger-full-access',approval_policy='never')
        self.transition['prior_selection']=dict(selection(self.c),client='app');self.persist()
        launcher.config_file(self.path);self.fence()
        from codex_worker_launcher import check_effective,config_file
        check_effective(dict(model='test-model',reasoningEffort='medium',modelProvider='openai',
                            approvalPolicy='never',approvalsReviewer='auto_review',sandbox={'type':'dangerFullAccess'}),self.c)
        self.c['schema_version']='agent-loop.codex-launch.v1'
        self.c.pop('worktree');self.c.pop('transition_file');self.c.pop('controller_epoch');self.path.write_text(json.dumps(self.c))
        self.assertEqual(config_file(self.path), self.c)

if __name__=='__main__':unittest.main()
