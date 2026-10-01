"""Structured exact-custody renewal; no stderr-derived ownership decisions."""
import json
import subprocess
from validate_context_package import ValidationError


class CustodyRejected(ValidationError):
    """A verified negative atomic custody receipt, not a CLI failure."""


def heartbeat(assignment, c, env, check=False):
    argv = [c['coordination_executable'], 'heartbeat', '--json', '--require-primary',
            '--reservation', assignment['reservation']['key'], '--generation',
            str(assignment['reservation']['generation']), '--worker-session', c['native_session_id']]
    if check:
        argv.append('--check')
    result = subprocess.run(argv, cwd=c['ledger_directory'], env=env,
                            capture_output=True, text=True, timeout=10)
    try:
        receipt = json.loads(result.stdout)
    except (ValueError, TypeError):
        raise ValidationError('Heartbeat outcome unavailable; no custody conclusion from CLI stderr') from None
    if (not isinstance(receipt, dict) or receipt.get('schema_version') != 'squad.worker-heartbeat.v1'
            or receipt.get('agent') != c['agent_id']
            or receipt.get('reservation') != assignment['reservation']['key']
            or type(receipt.get('generation')) is not int
            or receipt.get('generation') != assignment['reservation']['generation']
            or receipt.get('worker_session') != c['native_session_id']
            or receipt.get('require_primary') is not True):
        raise ValidationError('Heartbeat receipt unavailable or belongs to another custody fence')
    if receipt.get('outcome') == 'custody-rejected' and result.returncode != 0:
        raise CustodyRejected('Verified atomic Worker custody rejection; no claim reacquired')
    if receipt.get('outcome') != ('verified' if check else 'renewed') or result.returncode:
        raise ValidationError('Heartbeat renewal unavailable; bounded retry required, custody not verified')


def require_execution_fence(c):
    # 0.159.2 has no qualified persistent claim-loss execution fence. Interrupt
    # applies to one turn, unsubscribe to one subscriber, and elicitation pauses
    # timeout accounting. None fences future tool writes from the native owner.
    raise ValidationError('Codex claim-loss execution fence unavailable; Worker adoption blocked before client/receiver start. Runtime policy unchanged.')
