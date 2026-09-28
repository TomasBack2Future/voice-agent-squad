import copy
from datetime import datetime, timedelta, timezone
import importlib.util
from pathlib import Path
import unittest

ROOT=Path(__file__).resolve().parents[1]
def module(name):
    spec=importlib.util.spec_from_file_location(name, ROOT/(name+'.py'))
    result=importlib.util.module_from_spec(spec);spec.loader.exec_module(result);return result
planner=module('rolling_delivery')
validator=module('validate_context_package')
NOW=datetime(2026,9,28,4,0,tzinfo=timezone.utc)

def snapshot():
    return {'schema_version':'agent-loop.rolling_delivery.v1','observed_at':NOW.isoformat(),'batch_id':'batch-1',
            'acceptance_owner':'owner-1','cutoff_at':(NOW+timedelta(minutes=30)).isoformat(),
            'policy':{'wip_limit':10,'user_decision_ref':'decision:10'},
            'decision':{'revision':2,'action':'proceed','evidence_ref':'decision:2'},
            'members':[{'id':'a','state':'review-ready','head_sha':'a'*40,'ci_head':'a'*40,'reviewed_head':'a'*40,
                        'ci_green':True,'adopted_decision_revision':2},
                       {'id':'b','state':'developing','dependencies':['a'],'adopted_decision_revision':2}]}

class RollingTests(unittest.TestCase):
    def test_ready_member_does_not_wait_for_whole_batch(self):
        result=planner.evaluate(snapshot(),now=NOW)
        self.assertEqual(['a'],result['merge_next'])
        self.assertEqual([],result['freeze_members'])

    def test_cutoff_freezes_only_integrated_dependency_closed_set(self):
        value=snapshot();value['members'][0]['state']='integrated';value['cutoff_at']=NOW.isoformat()
        result=planner.evaluate(value,now=NOW)
        self.assertEqual(['a'],result['freeze_members']);self.assertEqual(['b'],result['deferred'])
        value['members'][0]['state']='developing';value['members'][1]['state']='integrated'
        self.assertEqual([],planner.evaluate(value,now=NOW)['freeze_members'])

    def test_user_wip_override_counts_paused_and_unbound(self):
        value=snapshot();value['tasks']=[{'id':str(i),'terminal':False,'paused':True} for i in range(9)]
        value['reservations']=[{'active':True}]
        self.assertEqual({'count':10,'limit':10,'new_dispatch_capacity':0},planner.evaluate(value,now=NOW)['wip'])
        del value['policy']['user_decision_ref']
        with self.assertRaises(ValueError):planner.evaluate(value,now=NOW)

    def test_stale_hold_unadopted_decision_and_changed_head_do_not_merge(self):
        for mutate in (lambda v:v['decision'].update(action='hold'),lambda v:v['members'][0].update(adopted_decision_revision=1),
                       lambda v:v['members'][0].update(head_sha='b'*40)):
            value=snapshot();mutate(value)
            self.assertEqual([],planner.evaluate(value,now=NOW)['merge_next'])
        with self.assertRaises(ValueError):planner.evaluate(snapshot(),now=NOW+timedelta(minutes=6))

    def test_cycles_and_unknown_dependencies_refused(self):
        for dependencies in (['b'],['missing']):
            value=snapshot();value['members'][0]['dependencies']=dependencies
            with self.assertRaises(ValueError):planner.evaluate(value,now=NOW)

    def test_done_reservation_is_reconciliation_not_dispatch_capacity(self):
        value=snapshot();value['members'][0].update(task_done=True,reservation_active=True)
        self.assertEqual('a',planner.evaluate(value,now=NOW)['reconcile'][0]['id'])

    def test_shared_paths_serialize_merges_only(self):
        value=snapshot();value['members'][0]['exclusive_paths']=['shared.go']
        other=copy.deepcopy(value['members'][0]);other['id']='c';value['members'].append(other)
        result=planner.evaluate(value,now=NOW)
        self.assertEqual(['a'],result['merge_next']);self.assertIn('shared-path-predecessor',result['waiting']['c'])

    def ready_review(self):
        value=snapshot(); value['decision']['action']='hold'
        m=value['members'][1];m.update(base_sha='b'*40,head_sha='c'*40,ci_green=False)
        m['review']={'ready_at':(NOW-timedelta(seconds=40)).isoformat(), 'in_flight':False,
                     'admission':{'base_sha':'b'*40,'head_sha':'c'*40,'receipt':'admission.json',
                                  'verified':True,'fast_gates_passed':True,'companion_audit_complete':True,'self_review_passed':True}}
        return value

    def test_review_starts_during_merge_hold_ci_and_predecessor_wait(self):
        result=planner.evaluate(self.ready_review(),now=NOW)
        self.assertEqual(['b'],result['review_next'])
        self.assertEqual([],result['merge_next'])
        self.assertEqual(40,result['reviews']['b']['ready_wait_seconds'])

    def test_running_or_previous_attempt_is_reconciled_not_resampled(self):
        for change in ({'in_flight':True}, {'attempt':{'base_sha':'b'*40,'head_sha':'c'*40,'status':'timeout'}}):
            value=self.ready_review();value['members'][1]['review'].update(change)
            result=planner.evaluate(value,now=NOW)
            self.assertEqual([],result['review_next']);self.assertEqual('reconcile',result['reviews']['b']['action'])

    def test_review_requires_complete_current_admission_and_review_dependencies(self):
        for mutate in (lambda r:r['admission'].update(head_sha='d'*40),
                       lambda r:r['admission'].update(fast_gates_passed=False),
                       lambda r:r.update(review_blockers=['interface-not-defined'])):
            value=self.ready_review();mutate(value['members'][1]['review'])
            self.assertEqual([],planner.evaluate(value,now=NOW)['review_next'])

    def test_checkpoint_cannot_repeat_dispatched_intent(self):
        for state in ('submitted','uncertain','running','completed','reconciled'):
            value={'operation_receipts':[{'intent':'release-1','state':state}], 'next_operation':{'action':'dispatch','intent':'release-1'}}
            with self.assertRaises(validator.ValidationError):validator.validate_checkpoint_operations(value)
            value['next_operation']['action']='reconcile';validator.validate_checkpoint_operations(value)
