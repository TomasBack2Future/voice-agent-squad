"""Propagate an existing operator-supplied receipt without expanding authority."""
from validate_context_package import ValidationError


def authorization(config, assignment):
    receipt = config.get('human_authorization')
    if receipt is None:
        return {'status': 'absent', 'additional_sensitive_disclosure': 'not_granted'}
    if receipt['assignment_id'] != assignment['assignment_id'] or receipt['repository'] != assignment['repository']:
        raise ValidationError('human authorization receipt belongs to another assignment')
    permissions = {'source': 'source_mutation', 'pull_request': 'pull_request',
                   'gated_merge': 'merge', 'issue_close': 'issue_close'}
    if any(not assignment['authorization'][permissions[op]] for op in receipt['operations'] if op in permissions):
        raise ValidationError('human receipt cannot expand assignment operations')
    return {'status': 'present', 'receipt': receipt, 'additional_sensitive_disclosure': 'not_granted'}


def prompt_context(config, assignment):
    import json
    return ('\nExisting human authorization receipt (context only; runtime permission unchanged):\n'
            + json.dumps(authorization(config, assignment), sort_keys=True))
