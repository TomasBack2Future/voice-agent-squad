"""Opt-in isolated real Muse source Worker/new/resume/decision/outcome proof."""
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
import unittest

ROOT=Path(__file__).parents[1]
sys.path.insert(0,str(ROOT))
import muse_session_host as native
import muse_worker_launcher as launcher
from muse_worker_tools import child_environment


@unittest.skipUnless(os.environ.get('MUSE_WORKER_QUALIFICATION'), 'explicit isolated native Worker qualification config required')
class MuseWorkerNativeTests(unittest.TestCase):
    def test_new_resume_decision_and_terminal_report(self):
        selected=json.loads(Path(os.environ['MUSE_WORKER_QUALIFICATION']).read_text())
        # Retain proof instead of deleting a potentially unresolved native/cgroup.
        root=Path(tempfile.mkdtemp(dir=selected['state_directory'],prefix='native-worker-qualification-'))
        work=root/'workspace';ledger=root/'ledger';home=root/'coordination-home'
        for p in (work,ledger,home):p.mkdir(mode=0o700)
        def git(*args):return subprocess.check_output(['git',*args],cwd=work,text=True,stderr=subprocess.DEVNULL).strip()
        git('init','-b','qualification');git('config','user.name','Qualification');git('config','user.email','qualification@example.invalid')
        (work/'AGENTS.md').write_text('Isolated Muse qualification; no business or external operations.\n')
        git('add','AGENTS.md');git('commit','-m','fixture');git('remote','add','origin','git@github.com:TomasBack2Future/voice-agent-squad.git')
        subprocess.run(['git','init'],cwd=ledger,capture_output=True,check=True)
        c=dict(selected,workspace=str(work),state_directory=str(root/'execution'),coordination_home=str(home),ledger_directory=str(ledger),
               agent_id='qualification-worker',dispatcher_agent_id='qualification-controller',controller_native='qualification-controller-native',
               controller_epoch=1,claim_generation=1,native_session_id=native.uuid7(),prompt_file=str(root/'prompt.txt'),max_seconds=360)
        def squad(actor,*args):
            e=child_environment(dict(c,agent_id=actor))
            return subprocess.check_output([c['coordination_executable'],*args],cwd=ledger,env=e,text=True,stderr=subprocess.PIPE).strip()
        squad(c['dispatcher_agent_id'],'init','--yes')
        settings=ledger/'.squad/config.yaml';settings.write_text(settings.read_text().replace('default_worktree_per_claim: true','default_worktree_per_claim: false'))
        squad(c['dispatcher_agent_id'],'register','--as',c['dispatcher_agent_id'])
        squad(c['dispatcher_agent_id'],'dispatch','reserve','QUALIFY-MUSE','--source','github:TomasBack2Future/voice-agent-squad#1')
        squad(c['dispatcher_agent_id'],'dispatch','controller-bind','--native-session',c['controller_native'])
        squad(c['dispatcher_agent_id'],'new','BUG','Native Muse isolated proof','--ready')
        squad(c['dispatcher_agent_id'],'dispatch','attach','QUALIFY-MUSE','--item','BUG-001','--generation','1')
        squad(c['dispatcher_agent_id'],'dispatch','bind','QUALIFY-MUSE','--thread-id',c['native_session_id'],'--generation','1')
        squad(c['agent_id'],'register','--as',c['agent_id']);squad(c['agent_id'],'claim','BUG-001','--long')
        profile={'schema_version':'agent-loop.project-profile.v1','id':'muse-qualification','version':1,'repository':'TomasBack2Future/voice-agent-squad','context_by_phase':{},'gates':{},'resources':{},'risk_probes':[]}
        native.atomic(root/'profile.json',profile)
        a={'schema_version':'agent-loop.assignment.v1','assignment_id':'muse/qualification/1','issue':'TomasBack2Future/voice-agent-squad#1','item':'BUG-001',
           'reservation':{'key':'QUALIFY-MUSE','generation':1},'repository':profile['repository'],'worktree':str(work),'branch':'qualification','base_sha':git('rev-parse','HEAD'),
           'role':{'id':'worker','skill':'agent-loop-worker','version':1},'project_profile':{'id':profile['id'],'version':1,'path':str(root/'profile.json')},
           'authorization':{'source_mutation':True,'pull_request':False,'merge':False,'staging':False,'production':False,'issue_close':False},'evidence_required':['test','manual']}
        native.atomic(root/'assignment.json',a);native.atomic(root/'config.json',c)
        squad(c['dispatcher_agent_id'],'milestone','--to','BUG-001','Qualification proceed; bounded fixture only, no external operations.')
        with sqlite3.connect(home/'global.db') as db:
            outcome=db.execute("SELECT max(id) FROM messages WHERE agent_id=? AND thread='BUG-001'",(c['dispatcher_agent_id'],)).fetchone()[0]
        squad(c['dispatcher_agent_id'],'terminal-events','decision-set','--reservation','QUALIFY-MUSE','--generation','1','--worker-session',c['native_session_id'],
              '--expected-revision','0','--outcome',str(outcome),'--action','proceed')
        Path(c['prompt_file']).write_text('Isolated qualification only. Registration, primary claim and reservation are already held. Read the canonical role/profile. '
            'Use write_file to create proof.py containing def add(a,b): return a+b. Use run_command to execute python3 -c \'from proof import add; assert add(2,3)==5; print("NATIVE_WORKER_OK")\'. '
            'Record actual results with the report tool and stop. Handle/ack any queued proceed decision upon delivery. Do not run business, review, GitHub or environment operations.')
        first=launcher.run(root/'assignment.json',root/'config.json')
        self.assertEqual(first['status'],'completed');self.assertIn('return a+b',(work/'proof.py').read_text().replace(' ','').replace('returna+b','return a+b'))
        Path(c['prompt_file']).write_text('Resume the same isolated task. Preserve proof.py; rerun python3 -c \'from proof import add; assert add(2,3)==5; print("RESUME_WORKER_OK")\' with run_command. '
           'Use decision_get with no arguments to read the current scoped decision and report actual completed verification with the report tool. No external operations, rewrites, dispatch or claim release.')
        second=launcher.run(root/'assignment.json',root/'config.json',resume=True)
        self.assertEqual(second['native'],first['native']);self.assertEqual(second['status'],'completed')
        with sqlite3.connect(home/'global.db') as db:
            self.assertEqual(db.execute("SELECT count(*) FROM execution_authorizations WHERE state='active'").fetchone()[0],0)
            self.assertGreater(db.execute("SELECT processed_at FROM terminal_event_receipts WHERE recipient=? AND kind='decision-resolved'",(c['agent_id'],)).fetchone()[0],0)
            self.assertEqual(db.execute("SELECT count(*) FROM terminal_event_receipts WHERE recipient=? AND kind='handoff-complete'",(c['dispatcher_agent_id'],)).fetchone()[0],2)
        for run in (first,second):
            state=Path(run['evidence']);session=json.loads((state/'native.json').read_text())
            self.assertEqual(session['session']['modelId'],native.MODEL);self.assertEqual(session['session']['approvalMode']['mode'],'allowAll')
            self.assertIn('--disable-shell',session['server_arguments']);self.assertIn('--disable-write',session['server_arguments'])
            self.assertTrue(json.loads((state/'join.json').read_text())['joined'])
            self.assertEqual(set(json.loads((state/'startup-loaded.json').read_text())['loaded']), {'role','profile'})
        previous={p.parent for p in Path(c['state_directory']).glob('*/operations.json')}
        stopped=threading.Event();hold_errors=[]
        def send_hold():
            try:
                deadline=time.monotonic()+180
                while not stopped.is_set() and time.monotonic()<deadline:
                    for journal in Path(c['state_directory']).glob('*/operations.json'):
                        if journal.parent in previous:
                            continue
                        if any(op.get('state')=='running' for op in json.loads(journal.read_text())):
                            squad(c['dispatcher_agent_id'],'milestone','--to','BUG-001','Qualification hold: stop the owned running command and report blocked.')
                            with sqlite3.connect(home/'global.db') as db:
                                message=db.execute("SELECT max(id) FROM messages WHERE agent_id=? AND thread='BUG-001'",(c['dispatcher_agent_id'],)).fetchone()[0]
                            squad(c['dispatcher_agent_id'],'terminal-events','decision-set','--reservation','QUALIFY-MUSE','--generation','1','--worker-session',c['native_session_id'],
                                  '--expected-revision','1','--outcome',str(message),'--action','hold','--condition','Isolated qualification: stop and join the running command')
                            return
                    time.sleep(0.1)
                raise AssertionError('native running command was not observed before hold')
            except BaseException as error:
                hold_errors.append(error)
        Path(c['prompt_file']).write_text('Isolated in-flight hold qualification. Preserve proof.py. Run exactly sleep 60 with run_command and timeout_seconds 120. '
            'The controller will issue hold while it runs. On rejection, read decision_get, acknowledge the delivered hold event after handling, '
            'and use report with status blocked and the actual stopped-container evidence. Do not run further mutations, claim release or external operations. Then reply.')
        monitor=threading.Thread(target=send_hold)
        monitor.start()
        try:
            held=launcher.run(root/'assignment.json',root/'config.json',resume=True)
        finally:
            stopped.set();monitor.join(timeout=15)
        self.assertFalse(monitor.is_alive());self.assertFalse(hold_errors,hold_errors)
        self.assertEqual(held['status'],'blocked');self.assertEqual(held['native'],first['native'])
        held_state=Path(held['evidence'])
        self.assertTrue(json.loads((held_state/'join.json').read_text())['joined'])
        self.assertEqual(json.loads((held_state/'report.json').read_text())['status'],'blocked')
        self.assertTrue(all(op['state']=='joined' for op in json.loads((held_state/'operations.json').read_text())))
        with sqlite3.connect(home/'global.db') as db:
            self.assertEqual(db.execute("SELECT count(*) FROM execution_authorizations WHERE state='active'").fetchone()[0],0)
            self.assertGreater(db.execute("SELECT processed_at FROM terminal_event_receipts WHERE recipient=? AND kind='decision-resolved' ORDER BY outcome_id DESC LIMIT 1",(c['agent_id'],)).fetchone()[0],0)
            self.assertEqual(db.execute("SELECT count(*) FROM terminal_event_receipts WHERE recipient=? AND kind='blocked'",(c['dispatcher_agent_id'],)).fetchone()[0],1)
        squad(c['agent_id'],'release','BUG-001')
        with self.assertRaisesRegex(ValueError,'primary claim changed'):
            launcher.check_launch(a,root/'config.json',resume=True)
        native.atomic(root/'qualification.json',{'new':first,'resume':second,'inflight_hold':held,'source_mutation':True,'command_verified':True,
                      'decision_handled':True,'outcome_published':True,'joined':True,'released_claim_rejected':True})
        print('Native Worker proof retained at '+str(root),flush=True)


if __name__=='__main__':unittest.main()
