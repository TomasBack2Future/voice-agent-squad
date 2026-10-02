import copy
import json
from pathlib import Path
import sys
import unittest
import tempfile
from unittest.mock import Mock, patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import codex_dispatcher_adoption as adoption
from validate_context_package import ValidationError

NATIVE='00000000-0000-0000-0000-000000000002'
class DispatcherAdoptionTests(unittest.TestCase):
    def setUp(self):
        self.state=tempfile.TemporaryDirectory();self.addCleanup(self.state.cleanup)
        self.c=dict(state_directory=self.state.name,native_session_id=NATIVE,agent_id='new',dispatcher_agent_id='new',client='cli',
                    coordination_executable='/fixture/squad',ledger_directory='/fixture/ledger',
                    controller_epoch=2,handoff_request_id='handoff-1',inactive_writer_pid=999999,
                    inactive_writer_joined=True,server_pid=123,endpoint='unix:///fixture/server',worktree='/fixture/worktree')
        self.current=dict(actor='new',native_session=NATIVE,epoch=2)
        self.handoff=dict(new_actor='new',new_native=NATIVE,epoch=2,request_id='handoff-1',old_actor='old',old_native='old-native')
    def read(self,current=None,handoff=None):
        return [Mock(returncode=0,stdout=json.dumps(current or self.current)),Mock(returncode=0,stdout=json.dumps(handoff or self.handoff))]
    def test_legitimate_distinct_owner_binding_and_stale_wrong_actor_native_epoch(self):
        for changed in [dict(self.current,actor='old'),dict(self.current,native_session='other'),dict(self.current,epoch=1)]:
            with patch.object(adoption,'child_environment',return_value={}),patch.object(adoption.subprocess,'run',side_effect=self.read(changed)):
                with self.assertRaises(ValidationError):adoption.controller_fence(self.c)
        for changed in [dict(self.handoff,old_actor='new'),dict(self.handoff,old_native=NATIVE),dict(self.handoff,new_actor='foreign')]:
            with patch.object(adoption,'child_environment',return_value={}),patch.object(adoption.subprocess,'run',side_effect=self.read(handoff=changed)):
                with self.assertRaises(ValidationError):adoption.controller_fence(self.c)
    def test_live_new_cli_rejects_before_rpc_without_killing_shared_backend(self):
        with patch.object(adoption,'controller_fence',return_value=self.handoff),patch.object(adoption.os,'kill',return_value=None) as kill,patch.object(adoption,'RPC') as rpc:
            with self.assertRaises(ValidationError):adoption.check_launch(self.c)
            kill.assert_called_once_with(999999,0);rpc.assert_not_called()
    def test_supported_new_native_uses_no_policy_overrides_or_old_app_rpc(self):
        rpc=Mock();rpc.__enter__=Mock(return_value=rpc);rpc.__exit__=Mock(return_value=False)
        rpc.call.return_value={'thread':{'id':NATIVE}}
        with patch.object(adoption,'controller_fence',return_value=self.handoff) as fence,patch.object(adoption.os,'kill',side_effect=ProcessLookupError),patch.object(adoption,'child_environment',return_value={}),patch.object(adoption,'check_delivery_runtime'),patch.object(adoption,'check_qualification',return_value={'version':'fixture'}),patch.object(adoption,'server_identity',return_value=[1,2,3,4,5]),patch.object(adoption,'RPC',return_value=rpc),patch.object(adoption,'check_effective'),patch.object(adoption,'live_target'):
            result=adoption.check_launch(self.c)
            self.assertEqual(result['actor'],'new');self.assertEqual(fence.call_count,2)
            rpc.call.assert_called_once_with('thread/resume',{'threadId':NATIVE,'excludeTurns':True})

if __name__=='__main__':unittest.main()
