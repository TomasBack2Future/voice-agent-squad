import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from delivery_readiness import evaluate, selected_readiness, trigger_lane, verify_admission
from validate_context_package import ValidationError
from claude_worker_launcher import check_resources


class DeliveryReadinessTests(unittest.TestCase):
    def setUp(self):
        self.a = dict(assignment_id='repo/1/1', item='TASK-1', reservation={'key':'DISPATCH-1','generation':1},
                      authorization=dict(source_mutation=True,pull_request=True,merge=True,staging=True,production=False,issue_close=True))
        self.c = dict(native_session_id='same-native',agent_id='worker',dispatcher_agent_id='dispatcher')
        self.s = dict(schema_version='agent-loop.delivery-readiness.v1',
                      binding=dict(assignment_id='repo/1/1',reservation='DISPATCH-1',generation=1,native_session_id='same-native',worker_agent_id='worker'),
                      selected_phase='merge',outcome='pr-created',head_sha='a'*40,
                      admission=dict(owner='dispatcher',revision=2,action='proceed',evidence_ref='decision:2'),
                      gates=dict(ci=True,review=True,head_sha='a'*40),
                      environment=dict(phase='staging',holder='other',generation=1,verified=True,evidence_ref='claim:1'),
                      acceptance=dict(access='blocked',owner='worker',evidence_ref='fixture-auth:unavailable'),
                      candidate=dict(verified=False),trigger_chain=self.manual())
        self.s['outcome_receipts']={outcome:dict(verified=True,evidence_ref=outcome+':fixture',head_sha='a'*40)
                                   for outcome in ('pr-created','source-merged','staging-accepted','issue-closed')}

    def node(self,key,event,effect,parents=()):
        return dict(id=key,event=event,effect=effect,triggered_by=list(parents),verified=True,evidence_ref='workflow:'+key,revision='a'*40)

    def manual(self):
        return [self.node('pr','pull_request','ci'),self.node('ci','main_push','ci'),
                self.node('release','tag_push','publish'),self.node('deploy','workflow_dispatch','deploy')]

    def test_manual_source_merge_is_independent_of_fixture_access(self):
        result = evaluate(self.s,self.a,self.c)
        self.assertEqual(result['lane'],'manual-deploy')
        self.assertTrue(result['phase_gates']['merge']['ready'])
        self.assertFalse(result['environment_required_now'])
        self.assertIn('fixture-access',result['phase_gates']['acceptance']['blockers'])
        self.assertFalse(result['phase_gates']['closure']['ready'])
        self.assertEqual(result['same_native_resume'],'same-native')

    def test_source_admission_skips_manual_deploy_resource_prerequisites(self):
        with tempfile.TemporaryDirectory() as tmp:
            p=Path(tmp)/'readiness.json';p.write_text(json.dumps(self.s))
            c=dict(self.c,delivery_readiness_file=str(p))
            with patch('delivery_readiness.verify_admission'), patch('claude_worker_launcher.subprocess.run',side_effect=AssertionError('source merge must not check staging resource/fixture')):
                self.assertEqual(check_resources(self.a,c,{}),[])

    def test_stale_current_admission_does_not_resume_a_new_native_worker(self):
        from types import SimpleNamespace
        c=dict(self.c,delivery_readiness_file='selected',coordination_executable='squad',ledger_directory='/isolated')
        readiness=evaluate(self.s,self.a,c)
        for value in (dict(revision=1,action='proceed',worker_agent='worker'),
                      dict(revision=2,action='hold',worker_agent='worker'),
                      dict(revision=2,action='proceed',worker_agent='foreign')):
            with patch('delivery_readiness.subprocess.run',return_value=SimpleNamespace(returncode=0,stdout=json.dumps(value))):
                with self.assertRaises(ValidationError):verify_admission(self.a,c,{},readiness)

    def test_auto_deploy_trigger_chain_retains_environment_merge_gate(self):
        self.s['trigger_chain'] = [self.node('ci','main_push','ci'),self.node('deploy','workflow_run','deploy',['ci'])]
        result = evaluate(self.s,self.a,self.c)
        self.assertEqual(result['lane'],'auto-deploy')
        self.assertIn('auto-deploy-ENV-ownership',result['phase_gates']['merge']['blockers'])
        self.assertTrue(result['environment_required_now'])
        self.s['environment']['holder']='worker'
        self.assertTrue(evaluate(self.s,self.a,self.c)['phase_gates']['merge']['ready'])

    def test_main_push_that_dispatches_deploy_is_still_an_auto_deploy_lane(self):
        self.s['trigger_chain']=[self.node('ci','main_push','ci'),
                                 self.node('deploy','workflow_dispatch','deploy',['ci'])]
        result=evaluate(self.s,self.a,self.c)
        self.assertEqual(result['lane'],'auto-deploy')
        self.assertIn('auto-deploy-ENV-ownership',result['phase_gates']['merge']['blockers'])

    def test_auto_deploy_merge_requires_authorization_for_actual_target(self):
        self.s['trigger_chain']=[self.node('deploy','main_push','deploy')]
        self.s['environment']['holder']='worker'
        self.s['environment']['phase']='production'
        result=evaluate(self.s,self.a,self.c)
        self.assertIn('auto-deploy-environment-not-authorized',result['phase_gates']['merge']['blockers'])
        self.a['authorization']['production']=True
        self.assertTrue(evaluate(self.s,self.a,self.c)['phase_gates']['merge']['ready'])

    def test_generic_squad_source_only_requires_no_staging(self):
        self.a['authorization']['staging']=False
        self.s['trigger_chain']=[self.node('ci','main_push','ci'),self.node('release','tag_push','publish')]
        self.s['outcome']='source-merged'
        result=evaluate(self.s,self.a,self.c)
        self.assertEqual(result['lane'],'source-only')
        self.assertEqual(result['phase_gates']['acceptance']['blockers'],['not-applicable'])
        self.assertTrue(result['phase_gates']['closure']['ready'])
        self.assertEqual(selected_readiness(self.a,self.c)['staging'],'not-applicable')

    def test_distinct_outcomes_never_treat_pr_or_merge_as_staging_acceptance(self):
        for outcome in ('working','pr-created','source-merged'):
            self.s['outcome']=outcome
            result=evaluate(self.s,self.a,self.c)
            self.assertFalse(result['phase_gates']['closure']['ready'])
            self.assertEqual(result['outcome'],outcome)
        self.s['outcome']='staging-accepted'
        self.assertTrue(evaluate(self.s,self.a,self.c)['phase_gates']['closure']['ready'])
        self.s['outcome']='issue-closed'
        self.assertEqual(evaluate(self.s,self.a,self.c)['outcome'],'issue-closed')

    def test_merge_admission_never_bypasses_review_ci_or_human_hold(self):
        for change in ('ci','review'):
            s=copy.deepcopy(self.s);s['gates'][change]=False
            self.assertIn('exact-review-and-CI',evaluate(s,self.a,self.c)['phase_gates']['merge']['blockers'])
        s=copy.deepcopy(self.s);s['admission']['action']='hold'
        self.assertEqual(evaluate(s,self.a,self.c)['next_owner'],'dispatcher')
        self.assertIn('dispatcher-admission',evaluate(s,self.a,self.c)['phase_gates']['merge']['blockers'])

    def test_wrong_native_owner_revision_and_unverified_chain_fail_closed(self):
        for key,value in [('native_session_id','new-native'),('generation',2),('worker_agent_id','other')]:
            s=copy.deepcopy(self.s);s['binding'][key]=value
            with self.assertRaises(ValidationError):evaluate(s,self.a,self.c)
        s=copy.deepcopy(self.s);s['admission']['owner']='foreign'
        with self.assertRaises(ValidationError):evaluate(s,self.a,self.c)
        s=copy.deepcopy(self.s);s['trigger_chain'][0]['verified']=False
        with self.assertRaises(ValidationError):evaluate(s,self.a,self.c)
        with self.assertRaises(ValidationError):selected_readiness(self.a,self.c)

    def test_missing_manual_candidate_is_not_fabricated_from_source_merge(self):
        self.s['outcome']='source-merged';self.s['selected_phase']='deploy'
        result=evaluate(self.s,self.a,self.c)
        self.assertIn('verified-deployment-candidate',result['phase_gates']['deploy']['blockers'])
        self.assertTrue(result['phase_gates']['merge']['ready'])

    def test_trigger_cycle_and_missing_downstream_evidence_are_rejected(self):
        nodes=[self.node('a','workflow_run','ci',['b']),self.node('b','workflow_run','deploy',['a'])]
        with self.assertRaises(ValidationError):trigger_lane(nodes)
        with self.assertRaises(ValidationError):trigger_lane([self.node('a','workflow_run','deploy',['missing'])])

    def test_source_merge_does_not_fabricate_an_acceptance_or_closure_receipt(self):
        self.s['outcome']='staging-accepted'
        del self.s['outcome_receipts']['staging-accepted']
        with self.assertRaises(ValidationError):evaluate(self.s,self.a,self.c)
        self.s['outcome']='issue-closed'
        with self.assertRaises(ValidationError):evaluate(self.s,self.a,self.c)


if __name__=='__main__':unittest.main()
