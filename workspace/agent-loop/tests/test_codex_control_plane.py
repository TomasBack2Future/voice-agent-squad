import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch, Mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
import codex_worker_launcher as launcher
import codex_receiver as receiver
from validate_context_package import ValidationError

NATIVE = '00000000-0000-0000-0000-000000000001'


class FakeRPC:
    def __init__(self, c):
        self.c = c
        self.calls = []
        self.loaded = True
        self.history = []
        self.queue = []
        self.fail = False
        self.override = {}

    def __enter__(self): return self
    def __exit__(self, *_): pass

    def call(self, method, params):
        self.calls.append((method, params))
        if method == 'thread/loaded/list': return {'data': [NATIVE] if self.loaded else []}
        if method == 'thread/read':
            return {'thread': dict(id=NATIVE, cwd=self.c['worktree'], model=self.c['model'],
                                   modelProvider='openai', reasoningEffort='medium', canAcceptDirectInput=True,
                                   turns=self.history, **self.override)}
        if method == 'thread/resume':
            return dict(model=self.c['model'], reasoningEffort='medium', modelProvider='openai',
                        approvalPolicy='on-request', approvalsReviewer=self.c['approvals_reviewer'],
                        sandbox={'type': 'workspaceWrite'})
        if method == 'thread/queue/list': return {'data': self.queue, 'nextCursor': None}
        if method == 'thread/queue/add':
            self.queue.append(params)
            if self.fail: raise TimeoutError('lost reply')
            return {'queuedSubmission': {'id': 'submission', 'clientUserMessageId': params['clientUserMessageId']}}
        raise AssertionError(method)


class CodexControlPlaneTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        (self.root / '.squad').mkdir()
        self.binary = self.root / 'codex'
        self.binary.write_text('#!/bin/sh\nexit 0\n')
        self.binary.chmod(0o700)
        self.prompt = self.root / 'prompt'
        self.prompt.write_text('Bound one-Issue prompt')
        self.c = dict(schema_version='agent-loop.codex-launch.v1', native_session_id=NATIVE,
                      agent_id='worker', dispatcher_agent_id='dispatcher', client='cli',
                      model='test-model', effort='medium', provider='openai', sandbox='workspace-write',
                      approval_policy='on-request', approvals_reviewer='auto_review',
                      endpoint='unix://' + str(self.root / 'server.sock'), server_pid=os.getpid(),
                      client_executable=str(self.binary), coordination_executable=str(self.binary),
                      ledger_directory=str(self.root), prompt_file=str(self.prompt),
                      qualification_file=str(self.root / 'qualification.json'), state_directory=str(self.root))
        self.path = self.root / 'launch.json'
        self.path.write_text(json.dumps(self.c))
        self.a = dict(assignment_id='repo/1/1', repository='owner/repo', worktree=str(self.root),
                      item='TASK-1', reservation={'key': 'DISPATCH-1', 'generation': 1},
                      authorization=dict(source_mutation=True, pull_request=True, merge=True, staging=False,
                                         production=False, issue_close=True))
        self.rc = dict(self.c, worktree=str(self.root), role='worker', incarnation='one',
                       owner_started_at='process-incarnation',
                       owner_pid=os.getpid(), max_seconds=60, reservation='DISPATCH-1', generation=1,
                       item='TASK-1', server_identity=[1,2,3,4,5])
        self.rpc = FakeRPC(self.rc)
        self.event = dict(event_id='worker-terminal-v1/DISPATCH-1/1/' + NATIVE + '/decision-resolved/1',
                          item_id='TASK-1', kind='decision-resolved', outcome_id=1,
                          source_message_id=1, delivered_at=0, processed_at=0)
        self.journal = self.root / 'journal.json'

    def proof(self, **changes):
        data = dict(schema_version='codex.control-plane-qualification.v1', checked_at=time.time(),
                    binary={'version':'codex-cli 0.159.2', 'executable_sha256':'binary'},
                    selection=launcher.selection(self.c), entitlement='completed', idle_queue='completed')
        data.update(changes)
        Path(self.c['qualification_file']).write_text(json.dumps(data))
        return data

    def test_entitlement_failure_and_stale_binary_do_not_claim_ready(self):
        binary = {'version':'codex-cli 0.159.2', 'executable_sha256':'binary'}
        with patch.object(launcher, 'executable', return_value=binary):
            for changes in ({'entitlement':'failed'}, {'idle_queue':'unverified'},
                            {'checked_at':time.time()-86401}, {'binary':{}}, {'selection':{}}):
                self.proof(**changes)
                with self.assertRaises(ValidationError): launcher.check_qualification(self.c)
            self.proof()
            self.assertEqual(launcher.check_qualification(self.c), binary)

    def test_preflight_joins_binding_ownership_and_live_runtime_before_ready(self):
        from types import SimpleNamespace
        with patch.object(launcher,'require_execution_fence'), patch.object(launcher,'check_profile'), patch.object(launcher,'resume_worktree'), \
             patch.object(launcher,'binding',return_value='bound'), patch.object(launcher,'check_heartbeat_runtime'), patch.object(launcher,'check_delivery_runtime'), \
             patch.object(launcher,'check_qualification',return_value={'version':'qualified'}), \
             patch.object(launcher,'server_identity',return_value=[1,2,3,4,5]), \
             patch.object(launcher,'RPC',return_value=self.rpc), \
             patch.object(launcher.subprocess,'run',side_effect=lambda argv,**kw: SimpleNamespace(returncode=0,stdout='--json --require-primary' if '--help' in argv else json.dumps({'env_claim':None}))):
            receipt=launcher.check_launch(self.a,self.path)
            self.assertEqual(receipt['binding'],'bound')
            self.assertEqual(receipt['delivery']['app'],'unavailable')
            self.assertTrue(receipt['runtime_approval']['verified'])
            self.rpc.loaded=False
            with self.assertRaises(ValidationError):launcher.check_launch(self.a,self.path)
        with patch.object(launcher,'check_profile'), patch.object(launcher,'resume_worktree'), \
             patch.object(launcher,'binding',return_value='pending'):
            with self.assertRaisesRegex(ValidationError,'binding required'):launcher.check_launch(self.a,self.path)

    def test_resume_checkpoint_retains_original_base_and_exact_current_head(self):
        work=self.root/'git';work.mkdir()
        def git(*args):
            return subprocess.check_output(['git','-C',str(work),*args],stderr=subprocess.DEVNULL,text=True).strip()
        git('init','-b','owned');git('config','user.name','Test');git('config','user.email','test@example.invalid')
        git('remote','add','origin','git@github.com:owner/repo.git')
        (work/'AGENTS.md').write_text('Isolated source fixture');git('add','AGENTS.md');git('commit','-m','base')
        base=git('rev-parse','HEAD')
        (work/'repair').write_text('owned source change');git('add','repair');git('commit','-m','fix: repair')
        head=git('rev-parse','HEAD')
        a=dict(self.a,worktree=str(work),branch='owned',base_sha=base)
        checkpoint=dict(schema_version='agent-loop.checkpoint.v1',assignment_id=a['assignment_id'],attempt_id='one',
                        phase='implementation',contract_revision='d1.1',
                        code=dict(repository=a['repository'],worktree=str(work),branch='owned',base_sha=base,head_sha=head,dirty=False),
                        ownership=dict(item='TASK-1',holder='worker',generation=1),completed=[],unresolved=[],external_operations=[],
                        next_action='continue same task',forbidden_actions=[])
        path=self.root/'checkpoint';path.write_text(json.dumps(checkpoint))
        c=dict(self.c,checkpoint_file=str(path))
        self.assertEqual(launcher.resume_worktree(a,c)['code']['head_sha'],head)
        checkpoint['code']['head_sha']=base;path.write_text(json.dumps(checkpoint))
        with self.assertRaises(ValidationError):launcher.resume_worktree(a,c)

    def test_app_target_is_unavailable_and_no_cli_fallback(self):
        for client in ('app', 'unknown'):
            self.path.write_text(json.dumps(dict(self.c, client=client)))
            with self.assertRaises(ValidationError): launcher.config_file(self.path)
        with self.assertRaises(ValidationError):
            receiver.run(self.write_receiver(dict(self.rc, client='app')))

    def write_receiver(self, c):
        path = self.root / 'receiver.json'; path.write_text(json.dumps(c)); return path

    def test_exact_loaded_target_and_effective_policy(self):
        result = launcher.live_target(self.rpc, self.c, self.root)
        self.assertEqual(result['approvalsReviewer'], 'auto_review')
        self.assertEqual(self.rpc.calls[-1][1], {'threadId':NATIVE, 'excludeTurns':True})
        self.rpc.loaded = False
        with self.assertRaises(ValidationError): launcher.live_target(self.rpc, self.c, self.root)
        self.assertEqual(self.rpc.calls[-1][0], 'thread/loaded/list')
        # An unloaded/stopped owner must never be instantiated by the receiver.
        self.assertEqual(sum(m == 'thread/resume' for m,_ in self.rpc.calls), 1)

    def test_resume_compares_against_selected_not_persisted_policy(self):
        # Reproduces the 2026-10-05 failure: resume ran under a stale
        # workspace-write/on-request readback while Full Access was selected.
        selected = dict(self.c, sandbox='danger-full-access', approval_policy='never',
                        approvals_reviewer='user')
        self.path.write_text(json.dumps(selected))
        config = launcher.config_file(self.path)
        stale = dict(model=config['model'], modelProvider='openai', reasoningEffort='medium',
                     approvalPolicy='on-request', approvalsReviewer='auto_review',
                     sandbox={'type': 'workspaceWrite'})
        original = self.rpc.call
        def call(method, params):
            if method == 'thread/resume':
                self.rpc.calls.append((method, params))
                return copy.deepcopy(stale)
            return original(method, params)
        with patch.object(self.rpc, 'call', side_effect=call):
            with self.assertRaisesRegex(ValidationError, 'selected'):
                launcher.live_target(self.rpc, config, self.root)

    def test_model_effort_permission_mismatch_is_fail_closed(self):
        for key, value in [('model','other'), ('modelProvider','other'), ('reasoningEffort','high'),
                           ('approvalPolicy','never'), ('approvalsReviewer','user'),
                           ('sandbox', {'type':'dangerFullAccess'})]:
            effective = dict(model='test-model', modelProvider='openai', reasoningEffort='medium',
                             approvalPolicy='on-request', approvalsReviewer='auto_review',
                             sandbox={'type':'workspaceWrite'})
            effective[key] = value
            with self.subTest(key=key), self.assertRaises(ValidationError):
                launcher.check_effective(effective, self.c)

    def test_worker_schema_accepts_selected_full_access_and_never(self):
        for sandbox, policy in [('danger-full-access', 'on-request'),
                                ('workspace-write', 'never'),
                                ('danger-full-access', 'never')]:
            with self.subTest(sandbox=sandbox, policy=policy):
                selected = dict(self.c, sandbox=sandbox, approval_policy=policy,
                                approvals_reviewer='user')
                self.path.write_text(json.dumps(selected))
                config = launcher.config_file(self.path)
                self.assertEqual(config, selected)
                argv = launcher.argv(config, str(self.root), 'prompt')
                self.assertEqual(argv[argv.index('--sandbox') + 1], sandbox)
                self.assertEqual(argv[argv.index('--ask-for-approval') + 1], policy)
                self.assertFalse(any('bypass' in value for value in argv))
        for key, value in [('sandbox', 'full-access'), ('approval_policy', 'always'),
                           ('approvals_reviewer', 'unverified')]:
            self.path.write_text(json.dumps(dict(self.c, **{key: value})))
            with self.subTest(key=key), self.assertRaises(ValidationError):
                launcher.config_file(self.path)

    def test_full_access_requires_exact_effective_resume_reply(self):
        selected = dict(self.c, sandbox='danger-full-access', approval_policy='never',
                        approvals_reviewer='user')
        self.path.write_text(json.dumps(selected))
        config = launcher.config_file(self.path)
        # Persisted thread metadata matches selection, but does not prove policy.
        effective = dict(model='test-model', modelProvider='openai', reasoningEffort='medium',
                         approvalPolicy='never', approvalsReviewer='user',
                         sandbox={'type': 'dangerFullAccess'})
        original = self.rpc.call
        def call(method, params):
            if method == 'thread/resume':
                self.rpc.calls.append((method, params))
                return copy.deepcopy(effective)
            return original(method, params)
        with patch.object(self.rpc, 'call', side_effect=call):
            self.assertEqual(launcher.live_target(self.rpc, config, self.root), effective)
            for key, value in [('approvalPolicy', 'on-request'),
                               ('sandbox', {'type': 'workspaceWrite'}),
                               ('approvalsReviewer', 'auto_review'), ('model', 'other'),
                               ('reasoningEffort', 'high'), ('modelProvider', 'other')]:
                before = copy.deepcopy(effective)
                effective[key] = value
                with self.subTest(key=key), self.assertRaisesRegex(ValidationError, 'effective runtime'):
                    launcher.live_target(self.rpc, config, self.root)
                effective.clear(); effective.update(before)
        resumes = [params for method, params in self.rpc.calls if method == 'thread/resume']
        self.assertEqual(len(resumes), 7)
        self.assertTrue(all(params == {'threadId': NATIVE, 'excludeTurns': True} for params in resumes))
        self.assertNotIn('thread/queue/add', [method for method, _ in self.rpc.calls])
        self.assertEqual(config, selected)

    def test_child_identity_and_argv_preserve_exact_native_and_permissions(self):
        with patch.dict(os.environ, {'SQUAD_AGENT':'parent','CLAUDE_SESSION_ID':'parent','CODEX_THREAD_ID':'parent'}):
            env = launcher.child_environment(self.c)
            self.assertEqual(env['SQUAD_SESSION_ID'], 'codex:' + NATIVE)
            self.assertEqual(env['CODEX_THREAD_ID'], NATIVE)
            self.assertNotIn('CLAUDE_SESSION_ID', env)
            self.assertEqual(os.environ['SQUAD_AGENT'], 'parent')
        argv = launcher.argv(self.c, str(self.root), 'prompt')
        self.assertEqual(argv[-3:], ['resume', NATIVE, 'prompt'])
        self.assertIn('approvals_reviewer="auto_review"', argv)
        self.assertIn('on-request', argv)
        self.assertFalse(any('bypass' in value for value in argv))

    def test_human_receipt_is_bounded_context_not_sensitive_permission(self):
        self.assertEqual(launcher.authorization(self.c,self.a)['status'], 'absent')
        auth = dict(assignment_id='repo/1/1', repository='owner/repo', reference='human:message-1',
                    operations=['source','test','managed_review'],
                    managed_review=dict(provider='grok',destination='existing-review-account',
                                        content='source_diff_and_issue_contract_only'))
        c = dict(self.c, human_authorization=auth)
        self.path.write_text(json.dumps(c))
        launcher.config_file(self.path)
        self.assertEqual(launcher.authorization(c,self.a)['receipt'],auth)
        self.assertEqual(launcher.authorization(c,self.a)['additional_sensitive_disclosure'], 'not_granted')
        c['human_authorization']['assignment_id'] = 'foreign'
        with self.assertRaises(ValidationError): launcher.authorization(c,self.a)
        auth['arbitrary_credentials'] = 'SECRET'
        self.path.write_text(json.dumps(c))
        with self.assertRaises(ValidationError): launcher.config_file(self.path)

    def test_accepted_unacknowledged_replays_keep_receiver_for_next_event(self):
        c = dict(self.rc, role='dispatcher', controller_epoch=2)
        event = dict(self.event, kind='handoff-complete',
                     event_id=self.event['event_id'].replace('decision-resolved', 'handoff-complete'))
        later = dict(event, outcome_id=2, source_message_id=2,
                     event_id=event['event_id'].rsplit('/', 1)[0] + '/2')
        def packet(item):
            return dict(type='worker-terminal-delivery-v1', recipient=c['agent_id'],
                        delivery_session=c['incarnation'], events=[item])
        path = self.write_receiver(c)
        with patch.object(receiver, 'check_qualification'), patch.object(receiver, 'controller_receiver'), \
             patch.object(receiver, 'alive', return_value=True), patch.object(receiver, 'server_identity', return_value=[1,2,3,4,5]), \
             patch.object(receiver, 'RPC', return_value=self.rpc), \
             patch.object(receiver, 'listen', side_effect=[packet(event)] * 4 + [packet(later), None]), \
             patch.object(receiver.subprocess, 'run', return_value=Mock(returncode=0)):
            self.assertEqual(receiver.run(path), 0)
        self.assertEqual(sum(method == 'thread/queue/add' for method, _ in self.rpc.calls), 2)
        journal = json.loads((self.root / (NATIVE + '.delivery.json')).read_text())
        self.assertEqual(journal[event['event_id']]['state'], 'accepted')
        self.assertEqual(journal[later['event_id']]['state'], 'accepted')

    def test_uncertain_replay_still_stops_without_another_native_submission(self):
        c = dict(self.rc, role='dispatcher', controller_epoch=2)
        event = dict(self.event, kind='handoff-complete',
                     event_id=self.event['event_id'].replace('decision-resolved', 'handoff-complete'))
        receipt = dict(type='worker-terminal-delivery-v1', recipient=c['agent_id'],
                       delivery_session=c['incarnation'], events=[event])
        path = self.write_receiver(c)
        self.rpc.fail = True
        with patch.object(receiver, 'check_qualification'), patch.object(receiver, 'controller_receiver'), \
             patch.object(receiver, 'alive', return_value=True), patch.object(receiver, 'server_identity', return_value=[1,2,3,4,5]), \
             patch.object(receiver, 'RPC', return_value=self.rpc), \
             patch.object(receiver, 'listen', return_value=receipt) as listen, \
             patch.object(receiver.subprocess, 'run') as delivered:
            with self.assertRaisesRegex(TimeoutError, 'lost reply'):
                receiver.run(path)
            listen.assert_called_once()
            delivered.assert_not_called()
        self.assertEqual(sum(method == 'thread/queue/add' for method, _ in self.rpc.calls), 1)
        journal = json.loads((self.root / (NATIVE + '.delivery.json')).read_text())
        self.assertEqual(journal[event['event_id']]['state'], 'intent')

    def test_duplicate_and_restart_do_not_resubmit_native_queue(self):
        receiver.deliver(self.rpc, self.event, self.rc, self.journal)
        receiver.deliver(self.rpc, self.event, dict(self.rc,incarnation='restart'), self.journal)
        receiver.deliver(self.rpc, dict(self.event, delivered_at=123), dict(self.rc,incarnation='restart'), self.journal)
        receiver.deliver(self.rpc, self.event, dict(self.rc, endpoint='unix:///qualified-replacement'), self.journal)
        self.assertEqual(sum(m == 'thread/queue/add' for m,_ in self.rpc.calls),1)
        data = json.loads(self.journal.read_text())
        self.assertEqual(data[self.event['event_id']]['state'],'accepted')
        self.assertNotIn('ack', [m for m,_ in self.rpc.calls])

    def test_lost_reply_recovers_from_native_queue_without_duplicate(self):
        self.rpc.fail = True
        with self.assertRaises(TimeoutError): receiver.deliver(self.rpc,self.event,self.rc,self.journal)
        receiver.deliver(self.rpc,self.event,dict(self.rc,incarnation='restart'),self.journal)
        self.assertEqual(sum(m == 'thread/queue/add' for m,_ in self.rpc.calls),1)
        self.assertEqual(json.loads(self.journal.read_text())[self.event['event_id']]['state'],'accepted')

    def test_uncertain_absent_native_input_stops_instead_of_retrying(self):
        self.rpc.fail = True
        with self.assertRaises(TimeoutError): receiver.deliver(self.rpc,self.event,self.rc,self.journal)
        self.rpc.queue = []
        with self.assertRaisesRegex(ValidationError,'uncertain'):
            receiver.deliver(self.rpc,self.event,self.rc,self.journal)
        self.assertEqual(sum(m == 'thread/queue/add' for m,_ in self.rpc.calls),1)

    def test_completed_native_user_input_can_recover_lost_acceptance_reply(self):
        self.rpc.fail=True
        with self.assertRaises(TimeoutError):receiver.deliver(self.rpc,self.event,self.rc,self.journal)
        queued=self.rpc.queue.pop()
        self.rpc.history=[{'items':[{'type':'userMessage','content':queued['input']}]}]
        receiver.deliver(self.rpc,self.event,self.rc,self.journal)
        self.assertEqual(sum(m=='thread/queue/add' for m,_ in self.rpc.calls),1)

    def test_wrong_recipient_stale_generation_and_incarnation(self):
        receipt = dict(type='worker-terminal-delivery-v1',recipient='worker',delivery_session='one',events=[self.event])
        self.assertEqual(receiver.validate_events(receipt,self.rc),[self.event])
        for changes in ({'recipient':'other'},{'delivery_session':'old'},{'events':[]},
                        {'events':[dict(self.event,event_id=self.event['event_id'].replace('/1/','/2/',1))]},
                        {'events':[dict(self.event,outcome_id=999)]}):
            with self.assertRaises(ValidationError): receiver.validate_events(dict(receipt,**changes),self.rc)

    def test_dispatcher_terminal_message_is_data_not_acceptance(self):
        c = dict(self.rc,role='dispatcher',agent_id='dispatcher')
        event = dict(self.event,kind='reconcile-needed',event_id=self.event['event_id'].replace('decision-resolved','reconcile-needed'))
        receipt = dict(type='worker-terminal-delivery-v1',recipient='dispatcher',delivery_session='one',events=[event])
        receiver.validate_events(receipt,c)
        text = receiver.message(event,c)
        self.assertIn('does not prove acceptance or termination',text)
        self.assertIn('After handling',text)

    def test_keyed_runtime_failure_event_matches_real_submit_id(self):
        # D84-7(b): the real Submit keyed event carries both the message id
        # and the request-key suffix; the receiver must accept that exact
        # form and still bind kind/outcome to the receipt row.
        event_id = 'worker-terminal-v1/DISPATCH-SQUAD-84/1/01a11bd3-7f39-7a13-a38b-4a82e4c9ffc3/runtime-failure/11102/ep-1'
        event = dict(item_id='BUG-018',kind='runtime-failure',outcome_id=11102,
                     source_message_id=11102,event_id=event_id)
        c = dict(self.rc,role='dispatcher',agent_id='dispatcher')
        receipt = dict(type='worker-terminal-delivery-v1',recipient='dispatcher',delivery_session='one',events=[event])
        self.assertEqual(receiver.validate_events(receipt,c),[event])
        tampered = dict(event,outcome_id=999)
        with self.assertRaises(ValidationError):
            receiver.validate_events(dict(receipt,events=[tampered]),c)

    def test_receipt_applies_handling_key_to_persisted_event(self):
        # 1b417f3 review finding 2: the handling budget key must apply to
        # the event the PR actually publishes — event_id plus the outcome
        # body — so distinct persisted outages key distinctly and auth
        # from the body token never continues once terminal.
        import muse_failure_handling as handling
        one = {'event_id': 'worker-terminal-v1/D/1/n/runtime-failure/3/ep-1',
               'item_id': 'BUG-018', 'kind': 'runtime-failure', 'outcome_id': 3,
               'source_message_id': 3,
               'body': 'runtime-failure ep-1 exhausted turn=t1 request=r1 attempt=1 provider=meta'}
        two = {'event_id': 'worker-terminal-v1/D/1/n/runtime-failure/4/ep-2',
               'item_id': 'BUG-018', 'kind': 'runtime-failure', 'outcome_id': 4,
               'source_message_id': 4,
               'body': 'runtime-failure ep-2 auth turn=t5 request=r5 attempt=1 provider=meta'}
        key_one = handling.episode_key(one)
        key_two = handling.episode_key(two)
        self.assertNotEqual(key_one, key_two)
        self.assertEqual(handling.decide(two, terminal=True, seen=set())[0], 'stop')
        self.assertEqual(handling.decide(two, terminal=True, seen={key_one})[0], 'stop')
        three = dict(two, event_id='worker-terminal-v1/D/1/n/runtime-failure/5/ep-3',
                     body='runtime-failure ep-3 exhausted turn=t6 request=r6 attempt=1 provider=meta')
        self.assertEqual(handling.decide(three, terminal=True, seen={key_one})[0], 'continue')

    def test_stopped_owner_and_replaced_incarnation(self):
        path = self.write_receiver(self.rc)
        with patch.object(receiver,'process_start',return_value='process-incarnation'):
            self.assertTrue(receiver.alive(self.rc,path))
        with patch.object(receiver,'process_start',return_value='reused-pid'):
            self.assertFalse(receiver.alive(self.rc,path))
        with patch.object(receiver.os,'kill',side_effect=ProcessLookupError):
            self.assertFalse(receiver.alive(self.rc,path))
        path.write_text(json.dumps(dict(self.rc,incarnation='new')))
        with patch.object(receiver,'process_start',return_value='process-incarnation'):
            self.assertFalse(receiver.alive(self.rc,path))

    def test_transient_heartbeat_nonzero_is_retried_without_stopping_client(self):
        self._transient_heartbeat(ValueError('SQLite temporarily unavailable'))

    def test_transient_heartbeat_timeout_is_retried_without_stopping_client(self):
        self._transient_heartbeat(subprocess.TimeoutExpired('heartbeat',10))

    def _transient_heartbeat(self, failure):
        child=Mock(pid=os.getpid())
        child.__enter__=Mock(return_value=child);child.__exit__=Mock(return_value=False)
        child.wait.side_effect=[subprocess.TimeoutExpired('client',30),subprocess.TimeoutExpired('client',30),0]
        helper=Mock(pid=os.getpid())
        helper.__enter__=Mock(return_value=helper);helper.__exit__=Mock(return_value=False)
        helper.poll.return_value=None
        helper.wait.return_value=0
        with patch.object(receiver,'require_execution_fence'), patch.object(receiver.subprocess,'Popen',side_effect=[child,helper]), \
             patch.object(receiver,'process_start',return_value='start'), \
             patch.object(receiver,'heartbeat',side_effect=[failure,None]) as renew:
            self.assertEqual(receiver.supervise(['client'],self.a,self.c,{},self.path,[1,2,3,4,5]),0)
            self.assertEqual(renew.call_count,2)
            child.terminate.assert_not_called();child.kill.assert_not_called()
            helper.terminate.assert_called_once()

    def test_unqualified_execution_fence_blocks_worker_before_native_or_client_calls(self):
        with patch.object(receiver.subprocess,'Popen') as popen, patch.object(receiver,'RPC') as rpc:
            with self.assertRaisesRegex(ValidationError,'execution fence unavailable'):
                receiver.supervise(['client'],self.a,self.c,{},self.path,[1,2,3,4,5])
            with self.assertRaisesRegex(ValidationError,'execution fence unavailable'):
                receiver.run(self.write_receiver(self.rc))
            popen.assert_not_called();rpc.assert_not_called()

    def test_unqualified_fence_preserves_full_access_policy_and_custody(self):
        # Qualification must not pretend that a permission downgrade, a new
        # client, or a process kill establishes a persistent native fence.
        c = dict(self.c, sandbox='danger-full-access', approval_policy='never', approvals_reviewer='user')
        rc = dict(self.rc, sandbox='danger-full-access', approval_policy='never', approvals_reviewer='user')
        before = copy.deepcopy((self.a, c, rc))
        path = self.write_receiver(rc)
        with patch.object(receiver.subprocess, 'Popen') as popen, patch.object(receiver, 'RPC') as rpc, \
             patch.object(receiver, 'controller_receiver') as controller, patch.object(receiver, 'writer_intent') as writer:
            with self.assertRaisesRegex(ValidationError, 'execution fence unavailable'):
                receiver.supervise(['client'], self.a, c, {}, self.path, [1,2,3,4,5])
            with self.assertRaisesRegex(ValidationError, 'execution fence unavailable'):
                receiver.run(path)
            popen.assert_not_called(); rpc.assert_not_called()
            controller.assert_not_called(); writer.assert_not_called()
        self.assertEqual((self.a, c, rc), before)

    def test_receiver_never_uses_paused_or_old_target_timer_fallback(self):
        c = dict(self.rc,endpoint='unix:///missing-old-target')
        receipt=dict(type='worker-terminal-delivery-v1',recipient='worker',delivery_session='one',events=[self.event])
        with patch.object(receiver,'require_execution_fence'), patch.object(receiver,'check_qualification'), patch.object(receiver,'listen',return_value=receipt), \
             patch.object(receiver,'alive',return_value=True), \
             patch.object(receiver,'server_identity',return_value=[9,9,9,9,9]), \
             patch.object(receiver,'RPC') as rpc, \
             patch.object(receiver.subprocess,'run',side_effect=AssertionError('no fallback control/message')):
            with self.assertRaisesRegex(ValidationError,'incarnation changed'):
                receiver.run(self.write_receiver(c))
            rpc.assert_not_called()


if __name__ == '__main__': unittest.main()

class ControllerLifetimeTests(unittest.TestCase):
    def test_supervisor_releases_only_after_client_and_helper_join(self):
        for helper_status in (None, 2):
            with self.subTest(helper_status=helper_status), tempfile.TemporaryDirectory() as state:
                c=dict(role='dispatcher', agent_id='dispatcher', controller_epoch=1, native_session_id=NATIVE,
                       state_directory=state, worktree=state)
                child=Mock(pid=os.getpid())
                child.__enter__=Mock(return_value=child);child.__exit__=Mock(return_value=False)
                helper=Mock(pid=os.getpid())
                helper.__enter__=Mock(return_value=helper);helper.__exit__=Mock(return_value=False)
                helper.poll.return_value=helper_status
                helper.wait.return_value=helper_status or 0
                joined=[]
                def wait(timeout):
                    self.assertFalse(any(call.kwargs.get('release') for call in custody.call_args_list))
                    if not joined:
                        joined.append('interval')
                        raise subprocess.TimeoutExpired('client',timeout)
                    joined.append('client')
                    return 0
                child.wait.side_effect=wait
                def lease(config, release=False):
                    if release:
                        self.assertEqual(joined,['interval','client'])
                        helper.wait.assert_called_once_with(timeout=10)
                        self.assertEqual(json.loads((Path(state)/(NATIVE+'.writer.json')).read_text())['state'],'joined')
                    else:
                        popen.assert_not_called()
                with patch.object(receiver,'controller_receiver',side_effect=lease) as custody, \
                     patch.object(receiver.subprocess,'Popen',side_effect=[child,helper]) as popen, \
                     patch.object(receiver,'process_start',return_value='start'):
                    self.assertEqual(receiver.supervise(['client'],None,c,{},Path(state)/'launch',[1,2,3,4,5]),0)
                self.assertEqual(custody.call_count,2)
                self.assertEqual(custody.call_args_list[0].args,custody.call_args_list[1].args)
                child.terminate.assert_not_called();child.kill.assert_not_called()
                if helper_status is None:helper.terminate.assert_called_once()
                else:helper.terminate.assert_not_called()

    def test_unbound_continuity_cannot_spawn_controller(self):
        c={'role':'dispatcher'}
        with patch.object(receiver.subprocess,'run') as run:
            with self.assertRaises(ValidationError):receiver.controller_receiver(c)
            run.assert_not_called()

    def test_dispatcher_ack_command_names_its_own_native(self):
        event=dict(event_id='worker-terminal-v1/D-1/1/worker-native/blocked/9',item_id='TASK-1',kind='blocked',outcome_id=9,source_message_id=9)
        text=receiver.message(event,{'role':'dispatcher','native_session_id':NATIVE})
        self.assertIn('--native-session '+NATIVE,text)
        self.assertNotIn('--native-session worker-native',text)

    def test_helper_failure_retains_lease_while_original_client_is_live(self):
        c=dict(role='dispatcher',client='cli',controller_epoch=2,server_identity=[1,2,3,4,5],max_seconds=60,
               native_session_id=NATIVE,state_directory='/fixture',endpoint='unix:///fixture',worktree='/fixture')
        rpc=Mock();rpc.__enter__=Mock(return_value=rpc);rpc.__exit__=Mock(return_value=False)
        with tempfile.TemporaryDirectory() as state:
            c['state_directory']=state
            for failure in (None,ValidationError('transport temporarily unavailable')):
                with self.subTest(failure=failure),patch.object(receiver,'validate_file',return_value=c),patch.object(receiver,'check_qualification'),patch.object(receiver,'RPC',return_value=rpc),patch.object(receiver,'live_target'),patch.object(receiver,'controller_receiver') as custody,patch.object(receiver,'alive',return_value=True),patch.object(receiver,'listen',side_effect=failure,return_value=None):
                    if failure:
                        with self.assertRaises(ValidationError):receiver.run(Path(state)/'config.json')
                    else:self.assertEqual(receiver.run(Path(state)/'config.json'),0)
                    self.assertEqual(custody.call_args_list,[unittest.mock.call(c)])
