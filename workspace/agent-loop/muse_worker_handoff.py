"""Validate a fresh native's adoption of an already joined source assignment."""
import hashlib
import json
from pathlib import Path
import subprocess

from muse_worker_tools import child_environment
from validate_context_package import ValidationError


def check_handoff(assignment, c):
    selected = c['handoff']
    pin_fields = {'request_id', 'prior_writer', 'prior_writer_sha256'}
    legacy_fields = {'request_id', 'legacy_stop_id'}
    if set(selected) not in (pin_fields, legacy_fields):
        raise ValidationError('source pin and legacy stopped-custody configurations are separate')
    result = subprocess.run([c['coordination_executable'], 'dispatch', 'worker-handoff-get', selected['request_id']],
                            cwd=c['ledger_directory'], env=child_environment(c), text=True,
                            capture_output=True, timeout=10, check=True)
    if len(result.stdout) > 262144:
        raise ValidationError('source handoff receipt exceeds its bound')
    receipt = json.loads(result.stdout)
    if 'legacy_stop_id' in selected:
        return check_legacy_handoff(assignment, c, receipt)
    if receipt.get('legacy_stop'):
        raise ValidationError('legacy stopped custody cannot impersonate a source writer journal')
    path = Path(selected['prior_writer']).resolve(strict=True)
    if path.is_relative_to(Path(c['workspace']).resolve()):
        raise ValidationError('original writer evidence must remain outside the mutable worktree')
    with path.open('rb') as source:
        raw = source.read(32769)
    if len(raw) > 32768 or hashlib.sha256(raw).hexdigest() != selected['prior_writer_sha256']:
        raise ValidationError('original writer evidence changed')
    writer = json.loads(raw)
    if writer.get('state') != 'joined':
        raise ValidationError('original source writer is unjoined')
    previous, current = receipt['previous'], receipt['reservation']
    custody = writer['custody']
    expected = {'native_session_id': previous['worker_thread_id'], 'agent_id': receipt['previous_actor'],
                'claim_generation': receipt['claim_generation'] - 1,
                'dispatcher_agent_id': c['dispatcher_agent_id'], 'controller_native': c['controller_native'],
                'controller_epoch': c['controller_epoch']}
    if (custody != expected or current['worker_thread_id'] != c['native_session_id']
            or current['generation'] != assignment['reservation']['generation']
            or current['reservation_key'] != assignment['reservation']['key']
            or current['canonical_item_id'] != assignment['item']
            or current['source_ref'] != 'github:' + assignment['issue']
            or current['reserved_by'] != c['dispatcher_agent_id']
            or receipt['claim_generation'] != c['claim_generation']
            or receipt['request_id'] != selected['request_id']):
        raise ValidationError('source handoff receipt differs from this exact assignment')
    original = dict(writer['assignment'])
    original['reservation'] = dict(original['reservation'], generation=current['generation'])
    if original != assignment:
        raise ValidationError('source handoff changed the original assignment or authorization')
    join_path = (Path(writer['evidence']) / 'join.json').resolve(strict=True)
    if join_path.is_relative_to(Path(c['workspace']).resolve()):
        raise ValidationError('original native/tool join must remain outside the mutable worktree')
    with join_path.open('rb') as source:
        join_raw = source.read(32769)
    if len(join_raw) > 32768:
        raise ValidationError('original native/tool join exceeds its bound')
    join = json.loads(join_raw)
    if (join.get('joined') is not True or join.get('binding', {}).get('id') != receipt['execution_id']
            or join['binding'].get('native') != custody['native_session_id']):
        raise ValidationError('source handoff lacks its exact original native/tool join')
    return receipt


def check_legacy_handoff(assignment, c, receipt):
    selected, stop = c['handoff'], receipt.get('legacy_stop')
    if (not isinstance(stop, dict) or stop.get('id') != selected['legacy_stop_id']
            or stop.get('state') != 'transferred' or not stop.get('observation_sha256')
            or not stop.get('processes') or receipt.get('execution_id')):
        raise ValidationError('legacy handoff lacks its actual native/host stopped-custody receipt')
    original = stop['input']['assignment']
    prior = stop['input']['handoff']
    current = receipt['reservation']
    controller = {'actor': c['dispatcher_agent_id'], 'native_session': c['controller_native'], 'epoch': c['controller_epoch']}
    if (prior['controller'] != controller or prior['expected'] != receipt['previous']
            or prior['claim']['actor'] != receipt['previous_actor']
            or prior['claim']['generation'] + 1 != receipt['claim_generation']
            or receipt['claim_generation'] != c['claim_generation']
            or current['worker_thread_id'] != c['native_session_id']
            or current['generation'] != assignment['reservation']['generation']
            or current['reservation_key'] != assignment['reservation']['key']
            or current['canonical_item_id'] != assignment['item']
            or current['source_ref'] != 'github:' + assignment['issue']
            or current['reserved_by'] != c['dispatcher_agent_id']
            or receipt['request_id'] != selected['request_id']
            or stop['input']['workspace'] != c['workspace']):
        raise ValidationError('legacy stop and replacement differ from this exact assignment')
    retained = dict(original, reservation=dict(original['reservation'], generation=current['generation']))
    if retained != assignment:
        raise ValidationError('legacy handoff changed the original assignment or authorization')
    result = subprocess.run([c['coordination_executable'], 'dispatch', 'worker-stop-source-check', stop['id'],
                             '--workspace', c['workspace']], cwd=c['ledger_directory'], env=child_environment(c),
                            text=True, capture_output=True, timeout=15, check=True)
    verified = json.loads(result.stdout)
    if verified != {'state': 'verified', 'workspace_sha256': stop['workspace_sha256']}:
        raise ValidationError('legacy retained source snapshot changed')
    return receipt
