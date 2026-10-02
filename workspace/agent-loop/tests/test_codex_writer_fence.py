import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest

ROOT=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT))
from codex_writer_fence import check_writer_fence
from validate_context_package import ValidationError

class WriterFenceTests(unittest.TestCase):
    def test_killed_launcher_does_not_admit_second_native_writer(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary); c=dict(native_session_id='00000000-0000-0000-0000-000000000001',agent_id='dispatcher',state_directory=str(root),worktree=str(root))
            script='''import json,sys,subprocess,time
from pathlib import Path
sys.path.insert(0,sys.argv[1])
import codex_receiver as receiver
c=json.loads(sys.argv[2]);pids=[];original=subprocess.Popen
def fixture_popen(argv,**kwargs):
 if 'codex_receiver.py' in str(argv):argv=[sys.executable,'-c','import time;time.sleep(60)']
 child=original(argv,**kwargs)
 if argv[0]=='ps':return child
 pids.append(child.pid);temporary=Path(c['state_directory'],'pids.tmp');temporary.write_text(json.dumps(pids));temporary.replace(Path(c['state_directory'],'pids.json'));return child
receiver.subprocess.Popen=fixture_popen
receiver.controller_receiver=lambda *args,**kwargs:None # Isolated non-ledger writer crash fixture.
receiver.supervise([sys.executable,'-c','import time;time.sleep(60)'],None,c,{},Path(c['state_directory'],'config.json'),[1,2,3,4,5])
'''
            launcher=subprocess.Popen([sys.executable,'-c',script,str(ROOT),json.dumps(c)])
            pids=[]
            try:
                deadline=time.monotonic()+10
                while time.monotonic()<deadline:
                    path=root/'pids.json'
                    if path.exists():
                        pids=json.loads(path.read_text())
                        if len(pids)==2:break
                    if launcher.poll() is not None:self.fail('fixture launcher exited before child spawn')
                    time.sleep(.02)
                self.assertEqual(len(pids),2)
                launcher.kill();launcher.wait(timeout=5) # Only this isolated fixture launcher.
                os.kill(pids[0],0) # Original client survived; flock alone is insufficient.
                with self.assertRaises(ValidationError):check_writer_fence(c)
            finally:
                if launcher.poll() is None:launcher.kill();launcher.wait(timeout=5)
                for pid in pids:
                    try:os.kill(pid,signal.SIGKILL) # Only recorded fixture children, never business sessions.
                    except ProcessLookupError:pass


class WriterJournalTests(unittest.TestCase):
    def test_unknown_intent_is_not_expired_or_replaced(self):
        from codex_writer_fence import writer_intent
        with tempfile.TemporaryDirectory() as state:
            c=dict(state_directory=state,native_session_id='native',agent_id='actor')
            writer_intent(c)
            with self.assertRaises(ValidationError):writer_intent(c)
            c['agent_id']='other'
            with self.assertRaises(ValidationError):check_writer_fence(c)

    def test_joined_receipt_rejects_live_original_child(self):
        from codex_writer_fence import writer_intent,writer_record
        with tempfile.TemporaryDirectory() as state:
            c=dict(state_directory=state,native_session_id='native',agent_id='actor')
            r=writer_intent(c);writer_record(c,r,state='joined',writer_pid=os.getpid(),helper_pid=os.getpid())
            with self.assertRaises(ValidationError):check_writer_fence(c)

    def test_actual_joined_fixture_children_allow_next_intent(self):
        from codex_writer_fence import writer_intent,writer_record
        with tempfile.TemporaryDirectory() as state:
            c=dict(state_directory=state,native_session_id='native',agent_id='actor')
            r=writer_intent(c)
            children=[subprocess.Popen([sys.executable,'-c','pass']) for _ in range(2)]
            exits=[child.wait(timeout=5) for child in children]
            writer_record(c,r,state='joined',writer_pid=children[0].pid,helper_pid=children[1].pid,writer_exit=exits[0],helper_exit=exits[1])
            check_writer_fence(c)
            self.assertEqual(writer_intent(c)['state'],'launch-intent')

    def test_controller_receiver_custody_precedes_native_spawn(self):
        from unittest.mock import patch,Mock
        import codex_receiver as receiver
        with tempfile.TemporaryDirectory() as state:
            c=dict(state_directory=state,native_session_id='native',agent_id='actor',worktree=state,controller_epoch=2,
                   coordination_executable='/fixture/squad',ledger_directory=state)
            with patch.object(receiver,'child_environment',return_value={}),patch.object(receiver.subprocess,'run',return_value=Mock(returncode=1)),patch.object(receiver.subprocess,'Popen') as popen:
                with self.assertRaises(ValidationError):receiver.supervise(['client'],None,c,{},Path(state)/'config.json',[1,2,3,4,5])
                popen.assert_not_called()

if __name__=='__main__':unittest.main()
