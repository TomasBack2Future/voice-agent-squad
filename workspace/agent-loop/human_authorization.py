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


ROUTINE_OPERATIONS = frozenset({'source', 'test', 'pull_request', 'managed_review'})


def effective_grant(config, assignment, current_revision, requested=None, holds=None, superseded_holds=None):
    """Compute the currently effective grant for routine operations.

    Reuses the receipt's operations without new approval when they stay
    within the assignment. A revoked receipt, an active hold, or any
    requested operation outside the receipt fails closed: new scope is
    never auto-expanded, and a hold superseded by the current revision is
    never revived on resume.
    """
    receipt = authorization(config, assignment).get('receipt')
    if receipt is None:
        raise ValidationError('no human authorization receipt to reuse')
    if receipt.get('revoked') is True:
        raise ValidationError('human authorization receipt was revoked; new approval required')
    requested = list(requested or [])
    active = set(holds or [])
    active -= set(superseded_holds or [])
    blocked = sorted(set(receipt['operations']) & active)
    if blocked:
        raise ValidationError('operations under active hold: ' + ','.join(blocked))
    operations = list(receipt['operations'])
    new_scope = [op for op in requested if op not in receipt['operations']]
    if new_scope:
        raise ValidationError('new authorization scope requires explicit approval: ' + ','.join(sorted(new_scope)))
    held = [op for op in requested if op in active]
    if held:
        raise ValidationError('operation is under active hold: ' + ','.join(sorted(held)))
    return {'status': 'effective', 'revision': current_revision, 'operations': operations,
            'reference': receipt['reference'], 'routine': sorted(set(operations) & ROUTINE_OPERATIONS)}
