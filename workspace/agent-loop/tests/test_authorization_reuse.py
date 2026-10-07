import copy
import sys
from pathlib import Path
import unittest

sys.path.insert(0, str(Path(__file__).parents[1]))
from human_authorization import authorization, effective_grant
from validate_context_package import ValidationError

ASSIGNMENT = {
    'assignment_id': 'repo/1/1', 'repository': 'owner/repo',
    'authorization': {'source_mutation': True, 'pull_request': True, 'merge': False,
                      'staging': False, 'production': False, 'issue_close': False},
}
RECEIPT = {
    'assignment_id': 'repo/1/1', 'repository': 'owner/repo',
    'reference': 'human:message-1', 'operations': ['source', 'test', 'pull_request', 'managed_review'],
    'managed_review': {'provider': 'grok', 'destination': 'review:repo#1',
                       'content': 'source_diff_and_issue_contract_only'},
}


class GrantReuseTests(unittest.TestCase):
    def test_routine_ops_reuse_effective_grant_without_new_approval(self):
        grant = effective_grant({'human_authorization': copy.deepcopy(RECEIPT)}, ASSIGNMENT, current_revision=3)
        for op in ('source', 'test', 'pull_request', 'managed_review'):
            self.assertIn(op, grant['operations'])
        self.assertEqual(grant['revision'], 3)
        self.assertEqual(authorization({'human_authorization': copy.deepcopy(RECEIPT)}, ASSIGNMENT)['status'], 'present')

    def test_new_scope_hold_or_revocation_still_rejects(self):
        config = {'human_authorization': copy.deepcopy(RECEIPT)}
        with self.assertRaises(ValidationError):
            effective_grant(config, ASSIGNMENT, current_revision=3, requested=['production'])
        with self.assertRaises(ValidationError):
            effective_grant(config, ASSIGNMENT, current_revision=3, holds=['pull_request'])
        revoked = {'human_authorization': dict(copy.deepcopy(RECEIPT), revoked=True)}
        with self.assertRaises(ValidationError):
            effective_grant(revoked, ASSIGNMENT, current_revision=3)

    def test_resume_does_not_revive_superseded_hold(self):
        config = {'human_authorization': copy.deepcopy(RECEIPT)}
        grant = effective_grant(config, ASSIGNMENT, current_revision=5,
                                holds=[], superseded_holds=['pull_request'])
        self.assertIn('pull_request', grant['operations'])
        grant = effective_grant(config, ASSIGNMENT, current_revision=5,
                                holds=['staging'], superseded_holds=['pull_request'])
        self.assertIn('pull_request', grant['operations'])
        self.assertNotIn('staging', grant['operations'])


if __name__ == '__main__':
    unittest.main()
