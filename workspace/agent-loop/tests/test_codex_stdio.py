"""Owned-stdio contract tests; synthetic host/proof, never native/provider calls."""
import copy
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import codex_worker_launcher as launcher
import codex_stdio_contract as contract
from validate_context_package import ValidationError

VERSION = 'codex-cli 0.159.0-alpha.12.1'
BINARY = '1180e2d56ea06ec583092acd933345685da3441cb1769a436d76dbf320613e75'
ALL = '7243ba241962af92ca60581f1a81808ebda4212a800f8b205f54703bcfd508c5'
V2 = 'e77b7d1436a78f431a74b2cb263a862e92ae40d70411bc63835b47ab2168827c'

class QualifiedStdioTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.schemas=self.root/'schemas';self.schemas.mkdir();(self.schemas/'shape.json').write_text('{}')
        self.refs={}
        for n in range(8):
            f=self.root/('receipt-'+str(n));f.write_text('synthetic exact record'+str(n));self.refs[str(f)]=hashlib.sha256(f.read_bytes()).hexdigest()
        self.c = dict(transport='owned-stdio', client='cli', client_executable='/synthetic/codex',
                      model='gpt-6.1-sol', provider='openai', effort='medium', service_tier='priority',
                      sandbox='read-only', approval_policy='never', approvals_reviewer='user',
                      qualification_file=str(self.root/'proof.json'),protocol_directory=str(self.schemas),native_session_id='00000000-0000-0000-0000-000000000001')
        self.proof = dict(schema_version='squad.current-cli-transport-qualification.v1',
            binary=dict(version=VERSION, sha256=BINARY),
            protocol=dict(generated_schema_files=440, aggregate_schema_sha256=ALL, v2_schema_sha256=V2),
            actual_selection=dict(model='gpt-6.1-sol', modelProvider='openai', reasoningEffort='medium',
                serviceTier='priority', approvalPolicy='never', approvalsReviewer='user',
                sandbox=dict(type='readOnly', networkAccess=False)),
            probe_thread='isolated-thread', loaded_owner=dict(data=['isolated-thread'], nextCursor=None),
            before_queue_idle=dict(type='idle'), after_queue_idle=dict(type='idle'),
            queue_intent=dict(threadId='isolated-thread',clientUserMessageId='client-one',literal='exact synthetic input'),
            queue_acceptance=dict(queuedSubmission=dict(id='accepted-one',clientUserMessageId='client-one',
                input=[dict(type='text',text='exact synthetic input',text_elements=[])])),
            source_turn=dict(turn_id='first',user_item=dict(type='userMessage',content=[dict(type='text',text='initial')]),assistant_items=[]),
            queued_turn=dict(turn_id='second',user_item=dict(type='userMessage',clientId='client-one',
                content=[dict(type='text',text='exact synthetic input',text_elements=[])]),assistant_items=[]),
            actual_inference_turns=2, actual_queue_submissions=1, actual_turn_start_requests=1,
            cleanup=dict(all_hosts_waited_exit0=True, all_readers_joined=True,both_native_turns_completed=True,
                own_thread_archived=True,owned_process_groups_remaining=[],total_from_original_start_seconds=148),
            server_incarnations=[dict(pid=1,start='synthetic',incarnation='owned',cleanup=dict(server_waited=True,server_exit=0,
                stdout_reader_joined=True,stderr_reader_joined=True,terminal_turns_joined=[dict(id='first',status='completed'),dict(id='second',status='completed')]))],
            immutable_associations=self.refs)
        # Installer package pins the complete immutable proof; no unpinned booleans.
        self.write_proof()
    def write_proof(self):
        raw=json.dumps(self.proof).encode(); Path(self.c['qualification_file']).write_bytes(raw)
        self.c['qualification_sha256']=hashlib.sha256(raw).hexdigest()
    def checked(self):
        with patch.object(launcher.subprocess,'run',return_value=SimpleNamespace(returncode=0,stdout=VERSION)), \
             patch.object(launcher,'digest',return_value=BINARY), \
             patch.object(contract,'executable_identity',return_value={'version':VERSION,'executable_sha256':BINARY}), \
             patch.object(contract,'PROOF_SHA',self.c['qualification_sha256']), \
             patch.object(contract,'SCHEMA_COUNT',1), \
             patch.object(contract,'SCHEMA_SHA',hashlib.sha256(json.dumps([{'path':'shape.json','sha256':hashlib.sha256(b'{}').hexdigest()}],sort_keys=True,separators=(',',':')).encode()).hexdigest()):
            return launcher.check_qualification(self.c)
    def test_actual_qualified_stdio_identity_traverses_transport_gate(self):
        self.assertEqual(self.checked()['version'],VERSION)
    def test_stdio_proof_cannot_qualify_unix_or_other_selection(self):
        for key,value in [('transport','unix'),('client','app'),('sandbox','danger-full-access'),
                          ('effort','high'),('service_tier','default'),('approval_policy','on-request')]:
            with self.subTest(key=key):
                old=self.c[key];self.c[key]=value
                with self.assertRaises(ValidationError):self.checked()
                self.c[key]=old

    def test_concrete_evidence_tampering_rejects_even_repinned_fixture(self):
        original=copy.deepcopy(self.proof)
        for key,path,value in [('wrongSchema',['protocol','v2_schema_sha256'],'other'),
            ('wrongSelection',['actual_selection','serviceTier'],'default'),
            ('wrongClient',['queued_turn','user_item','clientId'],'other'),
            ('wrongInput',['queue_acceptance','queuedSubmission','input'],[]),
            ('missingJoin',['cleanup','all_readers_joined'],False),
            ('wrongThread',['queue_intent','threadId'],'other'),
            ('partialProof',['loaded_owner','data'],[]),
            ('sameTurn',['queued_turn','turn_id'],'first')]:
            with self.subTest(key=key):
                self.proof=copy.deepcopy(original);obj=self.proof
                for p in path[:-1]:obj=obj[p]
                obj[path[-1]]=value;self.write_proof()
                with self.assertRaises(ValidationError):self.checked()
        self.proof=original;self.write_proof()
        (self.root/'receipt-0').write_text('rewritten')
        with self.assertRaises(ValidationError):self.checked()
    def test_portable_evidence_relocation_keeps_original_proof_bytes(self):
        moved=self.root/'moved';moved.mkdir()
        original=Path(self.c['qualification_file']).read_bytes()
        for path in self.refs:
            f=Path(path);(moved/f.name).write_bytes(f.read_bytes());f.unlink()
        self.c['evidence_directory']=str(moved)
        self.assertEqual(self.checked()['version'],VERSION)
        self.assertEqual(Path(self.c['qualification_file']).read_bytes(),original)
        (moved/'receipt-0').write_text('rewritten')
        with self.assertRaises(ValidationError):self.checked()
    def test_actual_protocol_mutation_rejects(self):
        (self.schemas/'shape.json').write_text('{"changed":true}')
        with self.assertRaises(ValidationError):self.checked()
    def test_unpinned_binary_is_never_executed_even_for_version(self):
        marker=self.root/'executed';binary=self.root/'unregistered'
        binary.write_text('#!'+sys.executable+'\nfrom pathlib import Path\nPath('+repr(str(marker))+').write_text("executed")\nprint('+repr(VERSION)+')\n');binary.chmod(0o700)
        self.c['client_executable']=str(binary)
        with self.assertRaises(ValidationError):launcher.check_qualification(self.c)
        self.assertFalse(marker.exists())
    def test_proof_hash_is_not_boolean_qualification(self):
        Path(self.c['qualification_file']).write_text('{"qualified":true}')
        with self.assertRaises(ValidationError):self.checked()


# Actual local synthetic JSONL subprocess; no Codex/model/account/ledger involved.
import subprocess
import threading
from codex_rpc import OwnedStdioRPC, LIMIT
import codex_receiver as receiver

HOST = r'''
import sys,json
native='00000000-0000-0000-0000-000000000001'
for line in sys.stdin:
 q=json.loads(line)
 if 'id' not in q:continue
 m=q['method'];p=q['params']
 if m=='initialize':r={}
 elif m=='thread/loaded/list':r={'data':[native]}
 elif m=='thread/read':r={'thread':{'id':native,'cwd':sys.argv[1],'model':'gpt-6.1-sol','modelProvider':'openai','reasoningEffort':'medium','canAcceptDirectInput':True,'turns':[]}}
 elif m=='thread/resume':r={'model':'gpt-6.1-sol','modelProvider':'openai','reasoningEffort':'medium','serviceTier':'priority','approvalPolicy':'never','approvalsReviewer':'user','sandbox':{'type':'readOnly','networkAccess':False}}
 elif m=='thread/queue/add':r={'queuedSubmission':{'id':'synthetic-accepted','clientUserMessageId':p['clientUserMessageId'],'input':p['input']}}
 elif m=='thread/queue/list':r={'data':[],'nextCursor':None}
 else:r={}
 print(json.dumps({'id':q['id'],'result':r}),flush=True)
'''

class OwnedPipeTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name).resolve()
        self.c=dict(client_executable=sys.executable,transport='owned-stdio',client='cli',native_session_id='00000000-0000-0000-0000-000000000001',
            model='gpt-6.1-sol',provider='openai',effort='medium',service_tier='priority',sandbox='read-only',approval_policy='never',approvals_reviewer='user')
        self.qualification=patch.object(launcher,'check_qualification',return_value={'transport':'owned-stdio'});self.qualification.start();self.addCleanup(self.qualification.stop)
    def child(self, script=HOST):
        p=subprocess.Popen([sys.executable,'-u','-c',script,str(self.root),'app-server','--listen','stdio://'],
            stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
        def cleanup():
            if p.poll() is None:
                p.stdin.close()
                try:p.wait(timeout=2)
                except subprocess.TimeoutExpired:p.terminate();p.wait(timeout=2)
            for pipe in (p.stdin,p.stdout,p.stderr):
                if not pipe.closed:pipe.close()
            OwnedStdioRPC._owners.pop(p,None)
        self.addCleanup(cleanup);return p
    def test_loaded_selection_acceptance_idempotent_journal_and_actual_child_join(self):
        p=self.child();rpc=OwnedStdioRPC(p,self.c);owner=rpc.identity()
        with self.assertRaisesRegex(ValidationError,'fresh'):rpc.call('thread/queue/add',{'threadId':self.c['native_session_id']})
        ready=launcher.check_owned_stdio(self.c,rpc,owner,self.root);self.assertTrue(ready['live_owner_verified'])
        event=dict(event_id='synthetic-event',item_id='TASK',kind='handoff-complete',outcome_id=1,source_message_id=1)
        cfg=dict(self.c,agent_id='dispatcher',role='dispatcher')
        path=self.root/'journal'
        receiver.deliver_owned_stdio(rpc,event,cfg,owner,self.root,path)
        stored=json.loads(path.read_text());self.assertEqual(stored['synthetic-event']['native_acceptance']['id'],'synthetic-accepted')
        sequence=rpc.sequence
        receiver.deliver(rpc,event,cfg,path);self.assertEqual(rpc.sequence,sequence)
        self.assertEqual(rpc.close()['joined_exit'],0)
        self.assertIsNotNone(p.poll())
        with self.assertRaises(ValidationError):rpc.call('thread/read',{})
    def test_no_shared_reader_wrong_owner_native_or_policy_override(self):
        p=self.child();rpc=OwnedStdioRPC(p,self.c)
        with self.assertRaises(ValidationError):OwnedStdioRPC(p,self.c)
        bad=dict(rpc.identity(),incarnation='different')
        with self.assertRaises(ValidationError):launcher.check_owned_stdio(self.c,rpc,bad,self.root)
        for method,params in [('thread/read',{'threadId':'other'}),('thread/resume',{'threadId':self.c['native_session_id'],'sandbox':'danger-full-access'}),('turn/start',{})]:
            with self.assertRaises(ValidationError):rpc.call(method,params)
        errors=[]
        def other():
            try:rpc.call('thread/loaded/list',{})
            except ValidationError:errors.append(True)
        t=threading.Thread(target=other);t.start();t.join();self.assertEqual(errors,[True]);rpc.close()
    def test_stdio_fullaccess_app_tier_mismatch_never_live_ready(self):
        for key,value in [('approvalPolicy','on-request'),('serviceTier','default'),('sandbox',{'type':'dangerFullAccess'})]:
            effective=copy.deepcopy(contract.SELECTION);effective[key]=value
            with self.assertRaises(ValidationError):launcher.check_effective(effective,self.c)
    def test_malformed_partial_overlimit_and_lost_reply_are_uncertain(self):
        for code in ["sys.stdout.write('{bad\\n');sys.stdout.flush()", "sys.stdout.write('{');sys.stdout.flush()", "sys.stdout.write('x'*(2*1024*1024+1));sys.stdout.flush()"]:
            script="import sys,json,time\nfor line in sys.stdin:\n q=json.loads(line)\n if 'id' not in q:continue\n if q['method']=='initialize':print(json.dumps({'id':q['id'],'result':{}}),flush=True)\n else:\n  "+code+"\n  time.sleep(.2)\n"
            p=self.child(script);rpc=OwnedStdioRPC(p,self.c)
            with self.assertRaises(ValidationError):rpc.call('thread/read',{'threadId':self.c['native_session_id']},timeout=.05)
            self.assertTrue(rpc.uncertain)
            with self.assertRaises(ValidationError):rpc.call('thread/queue/add',{'threadId':self.c['native_session_id']})
    def test_uncertain_readback_requires_exact_client_input_not_prose(self):
        from unittest.mock import Mock
        rpc=Mock();thread=self.c['native_session_id'];text='exact payload'
        rpc.call.side_effect=[{'data':[],'nextCursor':None},{'thread':{'turns':[{'items':[{'type':'userMessage','clientId':'other','content':[{'type':'text','text':text}]}]}]}}]
        self.assertFalse(receiver.native_contains(rpc,thread,text,'original-client'))
    def test_readiness_drift_before_queue_bytes_is_retryable_prepared_intent(self):
        p=self.child();rpc=OwnedStdioRPC(p,self.c);owner=rpc.identity()
        event=dict(event_id='prewrite',item_id='TASK',kind='handoff-complete',outcome_id=1,source_message_id=1)
        c=dict(self.c,agent_id='dispatcher',role='dispatcher');path=self.root/'journal';calls=[]
        def readiness(*args):
            calls.append(True)
            if len(calls)==2:raise ValidationError('effective policy drift')
            return {}
        with patch.object(launcher,'live_target',side_effect=readiness):
            with self.assertRaises(ValidationError):receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
        self.assertEqual(json.loads(path.read_text())['prewrite']['state'],'prepared')
        receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
        self.assertEqual(json.loads(path.read_text())['prewrite']['state'],'accepted');rpc.close()
    def test_transient_prewrite_rpc_error_recovers_after_fresh_readiness(self):
        script=HOST.replace("native='00000000-0000-0000-0000-000000000001'", "native='00000000-0000-0000-0000-000000000001'\nreads=0")
        script=script.replace("m=q['method'];p=q['params']", "m=q['method'];p=q['params']\n if m=='thread/read':\n  reads+=1\n  if reads==2:\n   print(json.dumps({'id':q['id'],'error':{'message':'transient'}}),flush=True);continue")
        p=self.child(script);rpc=OwnedStdioRPC(p,self.c);owner=rpc.identity()
        event=dict(event_id='transient',item_id='TASK',kind='handoff-complete',outcome_id=1,source_message_id=1)
        c=dict(self.c,agent_id='dispatcher',role='dispatcher');path=self.root/'journal'
        with self.assertRaises(ValidationError):receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
        self.assertTrue(rpc.uncertain);self.assertFalse(rpc.queue_uncertain)
        self.assertEqual(json.loads(path.read_text())['transient']['state'],'prepared')
        receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
        self.assertFalse(rpc.queue_uncertain);self.assertEqual(json.loads(path.read_text())['transient']['state'],'accepted');rpc.close()
    def test_owner_registration_is_atomic_before_any_pipe_io(self):
        gate=threading.Event();lock=threading.Lock()
        class PausingSet(dict):
            checks=0
            def __contains__(self,value):
                present=super().__contains__(value)
                with lock:self.checks+=1;number=self.checks
                if not present:
                    if number==1:gate.wait(.08)
                    else:gate.set()
                return present
        registry=PausingSet();p=self.child();objects=[];errors=[]
        def construct():
            try:objects.append(OwnedStdioRPC(p,self.c))
            except ValidationError:errors.append(True)
        with patch.object(OwnedStdioRPC,'_owners',registry), \
             patch.object(OwnedStdioRPC,'call',return_value={}), patch.object(OwnedStdioRPC,'send'):
            threads=[threading.Thread(target=construct) for _ in range(2)]
            for t in threads:t.start()
            for t in threads:t.join(2)
            self.assertFalse(any(t.is_alive() for t in threads))
            self.assertEqual(len(objects),1);self.assertEqual(errors,[True])
    def test_initialize_failure_retains_public_original_owner_for_bounded_join(self):
        script=HOST.replace("if m=='initialize':r={}", "if m=='initialize':\n  print(json.dumps({'id':q['id'],'error':{'message':'synthetic initialize failure'}}),flush=True);continue")
        child=self.child(script)
        with self.assertRaises(ValidationError) as failed:OwnedStdioRPC(child,self.c)
        owner=getattr(failed.exception,'owner',None)
        self.assertIsNotNone(owner,'failed acquisition must retain public original join custody')
        self.assertIs(owner.child,child)
        self.assertIn(child,OwnedStdioRPC._owners)
        with self.assertRaises(ValidationError):OwnedStdioRPC(child,self.c)
        with self.assertRaises(ValidationError):owner.call('thread/loaded/list',{})
        self.assertEqual(owner.close()['joined_exit'],0)
        self.assertNotIn(child,OwnedStdioRPC._owners)

    def test_rejection_before_acquisition_exposes_no_foreign_cleanup_authority(self):
        for invalid in (None,[],{},object()):
            with self.subTest(child_type=type(invalid).__name__):
                with self.assertRaises(ValidationError) as failed:OwnedStdioRPC(invalid,self.c)
                self.assertFalse(hasattr(failed.exception,'owner'))
        child=self.child();owner=OwnedStdioRPC(child,self.c)
        with self.assertRaises(ValidationError) as duplicate:OwnedStdioRPC(child,self.c)
        self.assertFalse(hasattr(duplicate.exception,'owner'))
        self.assertIs(OwnedStdioRPC._owners.get(child),owner)
        self.assertEqual(owner.close()['joined_exit'],0)

    def test_all_post_acquisition_startup_failures_retain_join_only_owner(self):
        original_call=OwnedStdioRPC.call;original_send=OwnedStdioRPC.send
        def short_call(rpc,method,params,**kwargs):
            return original_call(rpc,method,params,timeout=.03,**kwargs)
        def fail_initialized(rpc,value):
            if value.get('method')=='initialized':raise OSError('private startup detail')
            return original_send(rpc,value)
        scripts={
            'malformed':HOST.replace("if m=='initialize':r={}", "if m=='initialize':\n  sys.stdout.write('{bad\\n');sys.stdout.flush();continue"),
            'truncated':HOST.replace("if m=='initialize':r={}", "if m=='initialize':\n  sys.stdout.write('{');sys.stdout.flush();continue"),
            'timeout':HOST.replace("if m=='initialize':r={}", "if m=='initialize':continue")}
        for failure in ('malformed','truncated','timeout','set-blocking','initialized'):
            with self.subTest(failure=failure):
                child=self.child(scripts.get(failure,HOST))
                from contextlib import ExitStack
                with ExitStack() as stack:
                    stack.enter_context(patch.object(OwnedStdioRPC,'call',short_call))
                    if failure=='set-blocking':stack.enter_context(patch('codex_rpc.os.set_blocking',side_effect=OSError('private startup detail')))
                    if failure=='initialized':stack.enter_context(patch.object(OwnedStdioRPC,'send',fail_initialized))
                    with self.assertRaises(ValidationError) as failed:OwnedStdioRPC(child,self.c)
                owner=failed.exception.owner
                self.assertIs(OwnedStdioRPC._owners.get(child),owner)
                self.assertTrue(owner.startup_failed);self.assertNotIn('private',str(failed.exception))
                for method,params in [('initialize',{}),('thread/loaded/list',{}),
                        ('thread/resume',{'threadId':self.c['native_session_id'],'excludeTurns':True}),
                        ('thread/queue/add',{'threadId':self.c['native_session_id']})]:
                    with self.assertRaises(ValidationError):owner.call(method,params)
                with self.assertRaises(ValidationError):owner.send({'method':'initialized'})
                with self.assertRaises(ValidationError):launcher.check_owned_stdio(self.c,owner,owner.identity(),self.root)
                self.assertEqual(owner.close()['joined_exit'],0)
                self.assertNotIn(child,OwnedStdioRPC._owners)

    def test_interrupt_at_atomic_lease_publication_preserves_original_exception_and_owner(self):
        for interruption in (KeyboardInterrupt(),SystemExit(5)):
            with self.subTest(interruption=type(interruption).__name__):
                class InterruptedRegistry(dict):
                    def __setitem__(self,key,value):
                        super().__setitem__(key,value)
                        raise interruption
                registry=InterruptedRegistry();child=self.child()
                with patch.object(OwnedStdioRPC,'_owners',registry):
                    with self.assertRaises(type(interruption)) as failed:OwnedStdioRPC(child,self.c)
                    self.assertIs(failed.exception,interruption)
                    owner=failed.exception.owner;self.assertIs(registry.get(child),owner)
                    self.assertTrue(owner.startup_failed)
                    self.assertEqual(owner.close()['joined_exit'],0);self.assertNotIn(child,registry)

    def test_failed_startup_join_timeout_interruption_wrong_owner_and_pipes_retain_custody(self):
        child=self.child()
        with patch('codex_rpc.os.set_blocking',side_effect=OSError('synthetic setup failure')):
            with self.assertRaises(ValidationError) as failed:OwnedStdioRPC(child,self.c)
        owner=failed.exception.owner
        with patch('codex_rpc.os.getpid',return_value=owner.owner_pid+1):
            with self.assertRaises(ValidationError):owner.close()
        failures=[]
        def wrong_thread():
            try:owner.close()
            except ValidationError:failures.append(True)
        thread=threading.Thread(target=wrong_thread);thread.start();thread.join();self.assertEqual(failures,[True])
        with self.assertRaises(ValidationError) as rejected:OwnedStdioRPC(child,self.c)
        self.assertFalse(hasattr(rejected.exception,'owner'))
        original=child.stdin;foreign=self.root/'foreign-pipe'
        with foreign.open('wb') as unrelated:
            child.stdin=unrelated
            with self.assertRaisesRegex(ValidationError,'pipes changed'):owner.close()
            self.assertFalse(unrelated.closed);child.stdin=original
        for outcome in (subprocess.TimeoutExpired(child.args,.01),KeyboardInterrupt()):
            with patch.object(child,'wait',side_effect=outcome),patch.object(child,'terminate') as terminate,patch.object(child,'kill') as kill:
                with self.assertRaises(type(outcome) if isinstance(outcome,KeyboardInterrupt) else ValidationError):owner.close(timeout=.01)
                terminate.assert_not_called();kill.assert_not_called()
                self.assertIs(OwnedStdioRPC._owners.get(child),owner);self.assertFalse(owner.closed)
        self.assertEqual(owner.close()['joined_exit'],0)
        self.assertTrue(all(pipe.closed for pipe in owner._pipes));self.assertNotIn(child,OwnedStdioRPC._owners)

    def test_failed_startup_cleanup_rejects_reused_descriptor_before_closing_pipes(self):
        import os
        child=self.child()
        with patch('codex_rpc.os.set_blocking',side_effect=OSError('synthetic setup failure')):
            with self.assertRaises(ValidationError) as failed:OwnedStdioRPC(child,self.c)
        owner=failed.exception.owner;descriptor=child.stderr.fileno();original=os.dup(descriptor)
        try:
            with (self.root/'unrelated').open('wb') as unrelated:
                os.dup2(unrelated.fileno(),descriptor)
                with self.assertRaisesRegex(ValidationError,'pipes changed'):owner.close()
                self.assertFalse(child.stdin.closed)
                self.assertIs(OwnedStdioRPC._owners.get(child),owner)
        finally:
            os.dup2(original,descriptor);os.close(original)
        self.assertEqual(owner.close()['joined_exit'],0)

    def test_join_timeout_retains_original_handle_and_owner_without_kill(self):
        p=self.child();rpc=OwnedStdioRPC(p,self.c)
        with patch.object(p,'wait',side_effect=subprocess.TimeoutExpired(p.args,.01)), \
             patch.object(p,'terminate') as terminate, patch.object(p,'kill') as kill:
            with self.assertRaisesRegex(ValidationError,'retain original custody'):rpc.close(timeout=.01)
            terminate.assert_not_called();kill.assert_not_called()
            self.assertFalse(rpc.closed);self.assertIn(p,OwnedStdioRPC._owners)
    def test_uncertain_intent_is_not_sent_again_without_exact_history(self):
        from unittest.mock import Mock
        rpc=Mock()
        def lost(method,params,**kwargs):
            kwargs['before_send']()
            raise TimeoutError('lost reply')
        rpc.call.side_effect=lost
        c=dict(self.c,agent_id='dispatcher',role='dispatcher')
        e=dict(event_id='one',item_id='TASK',kind='handoff-complete',outcome_id=1,source_message_id=1)
        path=self.root/'journal'
        with self.assertRaises(TimeoutError):receiver.deliver(rpc,e,c,path)
        self.assertEqual(json.loads(path.read_text())['one']['state'],'intent')
        rpc.call.reset_mock();rpc.call.side_effect=[{'data':[],'nextCursor':None},{'thread':{'turns':[]}}]
        with self.assertRaises(ValidationError):receiver.deliver(rpc,e,c,path)
        self.assertEqual([v.args[0] for v in rpc.call.call_args_list],['thread/queue/list','thread/read'])
    def test_malformed_queue_acceptance_retains_wire_intent_and_blocks_new_write(self):
        script=HOST.replace("'id':'synthetic-accepted'", "'id':None")
        p=self.child(script);rpc=OwnedStdioRPC(p,self.c);owner=rpc.identity()
        c=dict(self.c,agent_id='dispatcher',role='dispatcher');path=self.root/'journal'
        event=dict(event_id='malformed',item_id='TASK',kind='handoff-complete',outcome_id=1,source_message_id=1)
        with self.assertRaisesRegex(ValidationError,'acceptance malformed'):
            receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
        self.assertEqual(json.loads(path.read_text())['malformed']['state'],'intent')
        self.assertTrue(rpc.queue_uncertain)
        launcher.check_owned_stdio(self.c,rpc,owner,self.root)
        with self.assertRaisesRegex(ValidationError,'original queue outcome uncertain'):
            rpc.call('thread/queue/add',rpc.pending_queue)
        rpc.close()

    def test_malformed_acceptance_exact_readback_persists_acceptance_without_resubmission(self):
        # The synthetic host retains the input although the reply is malformed.
        # This is source recovery coverage, not native deduplication qualification.
        script=HOST.replace("native='00000000-0000-0000-0000-000000000001'", "native='00000000-0000-0000-0000-000000000001'\nqueued=[]\nadds=0")
        script=script.replace("elif m=='thread/queue/add':r=", "elif m=='thread/queue/add':\n  adds+=1\n  from pathlib import Path\n  Path(sys.argv[1]+'/wire-count').write_text(str(adds))\n  queued.append({'id':'stored','clientUserMessageId':p['clientUserMessageId'],'input':p['input']})\n  r=")
        script=script.replace("'id':'synthetic-accepted'", "'id':None").replace("r={'data':[],'nextCursor':None}", "r={'data':queued,'nextCursor':None}")
        child=self.child(script);rpc=OwnedStdioRPC(child,self.c);owner=rpc.identity()
        c=dict(self.c,agent_id='dispatcher',role='dispatcher');path=self.root/'journal'
        event=dict(event_id='exact-recovery',item_id='TASK',kind='handoff-complete',outcome_id=1,source_message_id=1)
        with self.assertRaisesRegex(ValidationError,'acceptance malformed'):
            receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
        self.assertTrue(rpc.uncertain);self.assertTrue(rpc.queue_uncertain)
        self.assertEqual(json.loads(path.read_text())['exact-recovery']['state'],'intent')
        original=copy.deepcopy(rpc.pending_queue)
        accepted=dict(id='stored',clientUserMessageId=original['clientUserMessageId'],input=original['input'])
        # Ambiguous, mismatched and incomplete readback cannot join this intent.
        for label,pending in [
                ('ambiguous',{'data':[accepted,accepted],'nextCursor':None}),
                ('wrong-client',{'data':[dict(accepted,clientUserMessageId='other')],'nextCursor':None}),
                ('wrong-input',{'data':[dict(accepted,input=[])],'nextCursor':None}),
                ('incomplete',{'data':[accepted],'nextCursor':'more'})]:
            with self.subTest(readback=label), patch.object(rpc,'call',side_effect=[pending,{'thread':{'turns':[]}}]) as calls:
                with self.assertRaises(ValidationError):receiver.deliver(rpc,event,c,path)
                self.assertTrue(rpc.queue_uncertain);self.assertEqual(rpc.pending_queue,original)
                self.assertEqual(json.loads(path.read_text())['exact-recovery']['state'],'intent')
                self.assertTrue(all(call.args[0] in ('thread/queue/list','thread/read') for call in calls.call_args_list))
        # Real synthetic pipe readback works while both uncertainty flags are set.
        pending=rpc.call('thread/queue/list',{'threadId':self.c['native_session_id'],'limit':100})
        history=rpc.call('thread/read',{'threadId':self.c['native_session_id'],'includeTurns':True})
        self.assertEqual(len(pending['data']),1);self.assertIn('thread',history)
        receiver.deliver(rpc,event,c,path)
        self.assertEqual(json.loads(path.read_text())['exact-recovery']['state'],'accepted')
        self.assertFalse(rpc.queue_uncertain);self.assertFalse(rpc.uncertain)
        self.assertIsNone(rpc.pending_queue)
        sequence=rpc.sequence;receiver.deliver(rpc,event,c,path)
        self.assertEqual(rpc.sequence,sequence)
        self.assertEqual((self.root/'wire-count').read_text(),'1')
        rpc.close()

    def test_zero_wire_size_rejection_keeps_prepared_and_other_event_can_progress(self):
        import os
        child=self.child();rpc=OwnedStdioRPC(child,self.c);owner=rpc.identity()
        c=dict(self.c,agent_id='dispatcher',role='dispatcher');path=self.root/'journal'
        event=dict(event_id='oversize',item_id='x'*LIMIT,kind='handoff-complete',outcome_id=1,source_message_id=1)
        original_send=rpc.send;queue_writes=[]
        def observe(value, **kwargs):
            if value.get('method')!='thread/queue/add':return original_send(value, **kwargs)
            with patch('codex_rpc.os.write',wraps=os.write) as writes:
                try:return original_send(value, **kwargs)
                finally:queue_writes.append(writes.call_count)
        with patch.object(rpc,'send',side_effect=observe):
            with self.assertRaises(ValidationError):receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
        self.assertEqual(queue_writes,[0])
        self.assertEqual(json.loads(path.read_text())['oversize']['state'],'prepared')
        self.assertFalse(rpc.queue_uncertain);self.assertIsNone(rpc.pending_queue)
        healthy=dict(event,event_id='healthy',item_id='TASK')
        receiver.deliver_owned_stdio(rpc,healthy,c,owner,self.root,path)
        self.assertEqual(json.loads(path.read_text())['healthy']['state'],'accepted');rpc.close()

    def test_zero_wire_write_readiness_failure_keeps_prepared_until_fresh_qualification(self):
        import os,select
        child=self.child();rpc=OwnedStdioRPC(child,self.c);owner=rpc.identity()
        c=dict(self.c,agent_id='dispatcher',role='dispatcher');path=self.root/'journal'
        event=dict(event_id='no-wire',item_id='TASK',kind='handoff-complete',outcome_id=1,source_message_id=1)
        original_send=rpc.send;queue_writes=[];real_select=select.select
        def observe(value, **kwargs):
            if value.get('method')!='thread/queue/add':return original_send(value, **kwargs)
            def unwritable(read,write,error,timeout):
                return ([],[],[]) if write else real_select(read,write,error,timeout)
            with patch('select.select',side_effect=unwritable),patch('codex_rpc.os.write',wraps=os.write) as writes:
                try:return original_send(value, **kwargs)
                finally:queue_writes.append(writes.call_count)
        with patch.object(rpc,'send',side_effect=observe):
            with self.assertRaises(ValidationError):receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
        self.assertEqual(queue_writes,[0])
        self.assertEqual(json.loads(path.read_text())['no-wire']['state'],'prepared')
        self.assertFalse(rpc.queue_uncertain);self.assertIsNone(rpc.pending_queue)
        receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
        self.assertEqual(json.loads(path.read_text())['no-wire']['state'],'accepted');rpc.close()

    def test_not_sent_witness_is_exact_local_single_use_and_not_caller_forgeable(self):
        from codex_rpc import OwnedStdioNotSent
        child=self.child();rpc=OwnedStdioRPC(child,self.c);owner=rpc.identity()
        launcher.check_owned_stdio(self.c,rpc,owner,self.root)
        params={'threadId':self.c['native_session_id'],'clientUserMessageId':'large',
                'input':[{'type':'text','text':'x'*LIMIT,'text_elements':[]}]}
        with self.assertRaises(OwnedStdioNotSent) as caught:rpc.call('thread/queue/add',params)
        witness=caught.exception
        with self.assertRaises(ValidationError):rpc.consume_not_sent(witness,dict(params,clientUserMessageId='wrong'))
        with self.assertRaises(ValidationError):rpc.consume_not_sent(OwnedStdioNotSent(rpc,params),params)
        rpc.consume_not_sent(witness,params)
        with self.assertRaises(ValidationError):rpc.consume_not_sent(witness,params)
        self.assertFalse(rpc.queue_uncertain);rpc.close()

    def test_serialization_and_first_select_failures_produce_exact_prepared_witness(self):
        import os,select
        for failure in ('serialization','select-error'):
            with self.subTest(failure=failure):
                child=self.child();rpc=OwnedStdioRPC(child,self.c);owner=rpc.identity()
                c=dict(self.c,agent_id='dispatcher',role='dispatcher');path=self.root/('journal-'+failure)
                event=dict(event_id=failure,item_id='TASK',kind='handoff-complete',outcome_id=1,source_message_id=1)
                send=rpc.send
                def rejected(value,**kwargs):
                    if value.get('method')!='thread/queue/add':return send(value,**kwargs)
                    with patch('codex_rpc.os.write',wraps=os.write) as writes:
                        try:
                            target='codex_rpc.json.dumps' if failure=='serialization' else 'select.select'
                            with patch(target,side_effect=ValueError('bounded encoding') if failure=='serialization' else OSError('first select')):
                                return send(value,**kwargs)
                        finally:self.assertEqual(writes.call_count,0)
                with patch.object(rpc,'send',side_effect=rejected):
                    with self.assertRaises(ValidationError):receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
                self.assertEqual(json.loads(path.read_text())[failure]['state'],'prepared')
                self.assertFalse(rpc.queue_uncertain)
                receiver.deliver_owned_stdio(rpc,dict(event,event_id=failure+'-healthy'),c,owner,self.root,path)
                rpc.close()

    def test_marker_exceptions_never_mean_absent_commit_or_not_sent_replay(self):
        for committed in (False,True):
            with self.subTest(committed=committed):
                child=self.child();rpc=OwnedStdioRPC(child,self.c);owner=rpc.identity()
                c=dict(self.c,agent_id='dispatcher',role='dispatcher');path=self.root/('marker-'+str(committed))
                event=dict(event_id='marker',item_id='TASK',kind='handoff-complete',outcome_id=1,source_message_id=1)
                real_save=receiver.save
                def marker(path,value):
                    if value['marker']['state']=='intent':
                        if committed:real_save(path,value)
                        raise ValueError('unknown marker outcome')
                    return real_save(path,value)
                with patch.object(receiver,'save',side_effect=marker):
                    with self.assertRaises(ValueError):receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
                self.assertEqual(json.loads(path.read_text())['marker']['state'],'intent' if committed else 'prepared')
                self.assertTrue(rpc.queue_uncertain);self.assertIsNone(rpc._not_sent)
                with self.assertRaises(ValidationError):receiver.deliver_owned_stdio(rpc,dict(event,event_id='other'),c,owner,self.root,path)
                rpc.close()

    def test_partial_write_and_interrupted_possible_write_retain_intent(self):
        import os
        for partial in (False,True):
            with self.subTest(partial=partial):
                child=self.child();rpc=OwnedStdioRPC(child,self.c);owner=rpc.identity()
                c=dict(self.c,agent_id='dispatcher',role='dispatcher');path=self.root/('partial-'+str(partial))
                event=dict(event_id='partial',item_id='TASK',kind='handoff-complete',outcome_id=1,source_message_id=1)
                send=rpc.send;calls=[];real_write=os.write
                def interrupted(fd,data):
                    calls.append(len(data))
                    if partial and len(calls)==1:return real_write(fd,data[:3])
                    raise KeyboardInterrupt('possible write boundary')
                def queue_only(value,**kwargs):
                    if value.get('method')!='thread/queue/add':return send(value,**kwargs)
                    with patch('codex_rpc.os.write',side_effect=interrupted):return send(value,**kwargs)
                with patch.object(rpc,'send',side_effect=queue_only):
                    with self.assertRaises(KeyboardInterrupt):receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
                self.assertEqual(len(calls),2 if partial else 1)
                self.assertEqual(json.loads(path.read_text())['partial']['state'],'intent')
                self.assertTrue(rpc.queue_uncertain);self.assertIsNone(rpc._not_sent)
                rpc.close()

    def test_not_sent_cannot_keep_prepared_after_journal_or_incarnation_change(self):
        import select
        child=self.child();rpc=OwnedStdioRPC(child,self.c);owner=rpc.identity()
        c=dict(self.c,agent_id='dispatcher',role='dispatcher');path=self.root/'changed-journal'
        event=dict(event_id='changed',item_id='TASK',kind='handoff-complete',outcome_id=1,source_message_id=1)
        send=rpc.send;real_select=select.select
        def changed_select(read,write,error,timeout):
            if write:
                journal=json.loads(path.read_text());journal['changed']['state']='intent';receiver.save(path,journal)
                return [],[],[]
            return real_select(read,write,error,timeout)
        def queue_only(value,**kwargs):
            if value.get('method')!='thread/queue/add':return send(value,**kwargs)
            with patch('select.select',side_effect=changed_select):return send(value,**kwargs)
        with patch.object(rpc,'send',side_effect=queue_only):
            with self.assertRaisesRegex(ValidationError,'journal custody changed'):receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
        self.assertEqual(json.loads(path.read_text())['changed']['state'],'intent')
        rpc.close()
        other=self.child();new=OwnedStdioRPC(other,self.c);new_owner=new.identity()
        journal=json.loads(path.read_text());journal['changed']['state']='prepared';receiver.save(path,journal)
        with self.assertRaisesRegex(ValidationError,'owner/incarnation changed'):receiver.deliver_owned_stdio(new,event,c,new_owner,self.root,path)
        new.close()

    def test_prewrite_owner_loss_has_no_not_sent_witness_and_blocks_protected_write(self):
        import os,select
        child=self.child();rpc=OwnedStdioRPC(child,self.c);owner=rpc.identity();thread=rpc.owner_thread
        c=dict(self.c,agent_id='dispatcher',role='dispatcher');path=self.root/'lost-owner'
        event=dict(event_id='lost',item_id='TASK',kind='handoff-complete',outcome_id=1,source_message_id=1)
        send=rpc.send;real_select=select.select
        def lose(read,write,error,timeout):
            result=real_select(read,write,error,timeout)
            if write:rpc.owner_thread=-1
            return result
        def queue_only(value,**kwargs):
            if value.get('method')!='thread/queue/add':return send(value,**kwargs)
            with patch('select.select',side_effect=lose),patch('codex_rpc.os.write',wraps=os.write) as writes:
                try:return send(value,**kwargs)
                finally:self.assertEqual(writes.call_count,0)
        try:
            with patch.object(rpc,'send',side_effect=queue_only):
                with self.assertRaises(ValidationError):receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
            self.assertIsNone(rpc._not_sent)
            with self.assertRaises(ValidationError):receiver.deliver_owned_stdio(rpc,dict(event,event_id='other'),c,owner,self.root,path)
            self.assertIsNone(child.poll())  # No kill on transport/custody failure.
        finally:
            rpc.owner_thread=thread  # Undo the isolated mock only for actual EOF+join.
            rpc.close()

    def test_journal_lock_and_marker_compare_reject_competing_writer(self):
        import fcntl
        child=self.child();rpc=OwnedStdioRPC(child,self.c);owner=rpc.identity()
        c=dict(self.c,agent_id='dispatcher',role='dispatcher');path=self.root/'competing'
        event=dict(event_id='competing',item_id='TASK',kind='handoff-complete',outcome_id=1,source_message_id=1)
        with path.with_name(path.name+'.lock').open('a') as lock:
            fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
            with self.assertRaisesRegex(ValidationError,'writer already active'):receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
        self.assertFalse(path.exists());self.assertFalse(rpc.queue_uncertain)
        send=rpc.send
        def changed(value,**kwargs):
            if value.get('method')=='thread/queue/add':
                journal=json.loads(path.read_text());journal['competing']['identity']['recipient']='other';receiver.save(path,journal)
            return send(value,**kwargs)
        with patch.object(rpc,'send',side_effect=changed):
            with self.assertRaisesRegex(ValidationError,'CAS rejected'):receiver.deliver_owned_stdio(rpc,event,c,owner,self.root,path)
        self.assertTrue(rpc.queue_uncertain);self.assertIsNone(rpc._not_sent)
        self.assertEqual(json.loads(path.read_text())['competing']['state'],'prepared')
        rpc.close()

    def test_replaced_pipe_object_never_borrows_original_send_owner(self):
        import os
        child=self.child();rpc=OwnedStdioRPC(child,self.c);original=child.stdin
        duplicate=os.fdopen(os.dup(original.fileno()),'wb',buffering=0)
        child.stdin=duplicate
        try:
            with patch('codex_rpc.os.write',wraps=os.write) as writes:
                with self.assertRaises(ValidationError):rpc.send({'method':'initialized'})
                self.assertEqual(writes.call_count,0)
            self.assertIsNone(rpc._not_sent)
        finally:
            child.stdin=original;duplicate.close();rpc.close()

    def test_notification_and_private_error_output_remain_bounded(self):
        script="import sys,json\nfor line in sys.stdin:\n q=json.loads(line)\n if 'id' not in q:continue\n if q['method']=='initialize':print(json.dumps({'id':q['id'],'result':{}}),flush=True)\n else:print(json.dumps({'id':q['id'],'error':{'message':'PRIVATE-SECRET'}}),flush=True)\n"
        p=self.child(script);rpc=OwnedStdioRPC(p,self.c)
        with self.assertRaises(ValidationError) as e:rpc.call('thread/read',{'threadId':self.c['native_session_id']})
        self.assertNotIn('SECRET',str(e.exception));self.assertTrue(rpc.uncertain)
    def test_standalone_stdio_receiver_rejects_before_any_spawn(self):
        path=self.root/'config';path.write_text(json.dumps({'transport':'owned-stdio'}))
        with self.assertRaises(ValidationError):receiver.run(path)

if __name__=='__main__':unittest.main()
