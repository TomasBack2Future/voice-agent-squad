#!/usr/bin/env python3
"""Read-only phase routing from independently verified project trigger evidence.

This module grants no merge/deploy/closure authority and never acquires ENV.
"""
from __future__ import annotations
import json
import hashlib
import re
import subprocess
from pathlib import Path
from validate_context_package import ValidationError

PHASES = ('source', 'merge', 'deploy', 'acceptance', 'closure')
OUTCOMES = ('working', 'pr-created', 'source-merged', 'staging-accepted', 'issue-closed')


def trigger_lane(chain):
    if not isinstance(chain, list) or not chain:
        raise ValidationError('explicit verified trigger chain required')
    nodes = {node['id']: node for node in chain}
    if len(nodes) != len(chain):
        raise ValidationError('duplicate trigger node')
    roots = set()
    visiting, visited = set(), set()
    def visit(key):
        if key in visiting:
            raise ValidationError('trigger chain cycle')
        if key in visited:
            return
        visiting.add(key)
        node = nodes[key]
        if (node.get('effect') not in ('ci', 'publish', 'deploy')
                or node.get('event') not in ('pull_request', 'main_push', 'tag_push', 'workflow_dispatch', 'workflow_run', 'repository_dispatch', 'release')
                or node.get('verified') is not True or not node.get('evidence_ref')
                or not re.fullmatch(r'[0-9a-f]{40}', node.get('revision', ''))):
            raise ValidationError('unverified actual workflow trigger/effect evidence')
        for parent in node.get('triggered_by', []):
            if parent not in nodes:
                raise ValidationError('missing downstream trigger evidence')
            visit(parent)
        if node['event'] == 'workflow_run' and not node.get('triggered_by'):
            raise ValidationError('workflow_run requires its actual upstream trigger evidence')
        if node['event'] == 'main_push':
            roots.add(key)
        visiting.remove(key)
        visited.add(key)
    for key in nodes:
        visit(key)
    reachable = set(roots)
    while True:
        old = set(reachable)
        for key, node in nodes.items():
            if set(node.get('triggered_by', [])) & reachable:
                reachable.add(key)
        if old == reachable:
            break
    if any(nodes[key]['effect'] == 'deploy' for key in reachable):
        return 'auto-deploy'
    return 'manual-deploy' if any(n['effect'] == 'deploy' for n in nodes.values()) else 'source-only'


def evaluate(snapshot, assignment, c):
    if snapshot.get('schema_version') != 'agent-loop.delivery-readiness.v1':
        raise ValidationError('unsupported phase readiness contract')
    identity = snapshot.get('binding', {})
    if (identity.get('assignment_id'), identity.get('reservation'), identity.get('generation'),
            identity.get('native_session_id'), identity.get('worker_agent_id')) != (
            assignment['assignment_id'], assignment['reservation']['key'], assignment['reservation']['generation'],
            c['native_session_id'], c['agent_id']):
        raise ValidationError('phase readiness belongs to another native assignment')
    phase, outcome = snapshot.get('selected_phase'), snapshot.get('outcome')
    if phase not in PHASES or outcome not in OUTCOMES:
        raise ValidationError('explicit selected phase and distinct outcome required')
    lane = trigger_lane(snapshot.get('trigger_chain'))
    if lane == 'source-only' and any(assignment['authorization'][p] for p in ('staging', 'production')):
        raise ValidationError('environment assignment lacks deployment evidence; cannot infer source-only delivery')
    receipts = snapshot.get('outcome_receipts', {})
    required = list(OUTCOMES[1:OUTCOMES.index(outcome) + 1])
    if lane == 'source-only':
        if outcome == 'staging-accepted':
            raise ValidationError('source-only lane cannot claim staging acceptance')
        required = [name for name in required if name != 'staging-accepted']
    for name in required:
        receipt = receipts.get(name, {})
        if receipt.get('verified') is not True or not receipt.get('evidence_ref') or receipt.get('head_sha') != snapshot.get('head_sha'):
            raise ValidationError('distinct delivery outcome lacks exact-revision evidence: ' + name)
    admission = snapshot.get('admission', {})
    admitted = (admission.get('owner') == c['dispatcher_agent_id'] and admission.get('action') == 'proceed'
                and type(admission.get('revision')) is int and admission['revision'] >= 1
                and bool(admission.get('evidence_ref')))
    if admission.get('owner') != c['dispatcher_agent_id']:
        raise ValidationError('phase admission must name the current Dispatcher owner')
    gates = snapshot.get('gates', {})
    exact = (gates.get('ci') is True and gates.get('review') is True
             and gates.get('head_sha') == snapshot.get('head_sha')
             and isinstance(snapshot.get('head_sha'), str) and bool(re.fullmatch(r'[0-9a-f]{40}', snapshot['head_sha'])))
    environment = snapshot.get('environment', {})
    environment_phase = environment.get('phase')
    environment_authorized = (environment_phase in ('staging', 'production')
                              and assignment['authorization'][environment_phase])
    owned = (environment.get('holder') == c['agent_id'] and environment.get('verified') is True
             and type(environment.get('generation')) is int and environment['generation'] >= 1
             and bool(environment.get('evidence_ref')))
    acceptance = snapshot.get('acceptance', {})
    access = (acceptance.get('access') == 'ready' and acceptance.get('owner') == c['agent_id']
              and bool(acceptance.get('evidence_ref')))
    deployment = snapshot.get('candidate', {})
    candidate = (deployment.get('verified') is True and bool(deployment.get('evidence_ref'))
                 and deployment.get('head_sha') == snapshot.get('head_sha'))
    reasons = {p: [] for p in PHASES}
    for p in PHASES:
        if not admitted:
            reasons[p].append('dispatcher-admission')
    if not assignment['authorization']['source_mutation']:
        reasons['source'].append('source-not-authorized')
    if not assignment['authorization']['merge']:
        reasons['merge'].append('merge-not-authorized')
    if not exact:
        reasons['merge'].append('exact-review-and-CI')
    if lane == 'auto-deploy' and not owned:
        reasons['merge'].append('auto-deploy-ENV-ownership')
    if lane == 'auto-deploy' and not environment_authorized:
        reasons['merge'].append('auto-deploy-environment-not-authorized')
    if lane != 'source-only':
        for p in ('deploy', 'acceptance'):
            if not environment_authorized:
                reasons[p].append('selected-environment-not-authorized')
            if not owned:
                reasons[p].append('ENV-ownership')
            if not candidate:
                reasons[p].append('verified-deployment-candidate')
        if not access:
            reasons['acceptance'].append('fixture-access')
        if outcome not in ('staging-accepted', 'issue-closed'):
            reasons['closure'].append('staging-acceptance-incomplete')
    else:
        reasons['deploy'].append('not-applicable')
        reasons['acceptance'].append('not-applicable')
        if outcome not in ('source-merged', 'issue-closed'):
            reasons['closure'].append('source-merge-incomplete')
    if not assignment['authorization']['issue_close']:
        reasons['closure'].append('issue-close-not-authorized')
    return {'schema_version': 'agent-loop.delivery-readiness-result.v1', 'advisory': True,
            'lane': lane, 'selected_phase': phase, 'outcome': outcome, 'binding': identity,
            'admission': admission, 'phase_gates': {p: {'ready': not why, 'blockers': why} for p, why in reasons.items()},
            'environment_required_now': phase in ('deploy', 'acceptance') and lane != 'source-only'
                                        or phase == 'merge' and lane == 'auto-deploy',
            'acceptance_owner': snapshot.get('acceptance', {}).get('owner'),
            'environment_phase': environment_phase,
            'next_owner': c['dispatcher_agent_id'] if not admitted else c['agent_id'],
            'same_native_resume': c['native_session_id']}


def selected_readiness(assignment, c):
    path = c.get('delivery_readiness_file')
    if path:
        snapshot = json.loads(Path(path).read_text())
        result = evaluate(snapshot, assignment, c)
        selected = assignment.get('project_profile')
        resources = {}
        if selected:
            profile = Path(selected['path'])
            if not profile.is_absolute():
                profile = Path(assignment['worktree']) / profile
            resources = json.loads(profile.read_text()).get('resources', {})
        project_environment = any(resources.get(p) for p in ('staging', 'production'))
        if project_environment and result['lane'] == 'source-only':
            raise ValidationError('environment project lacks deployment evidence; cannot infer source-only delivery')
        if project_environment or any(assignment['authorization'][p] for p in ('staging', 'production')):
            verify_workflow_inventory(snapshot, assignment)
        if not result['phase_gates'][result['selected_phase']]['ready']:
            raise ValidationError('selected phase blocked: ' + ','.join(result['phase_gates'][result['selected_phase']]['blockers']))
        return result
    if any(assignment['authorization'][p] for p in ('staging', 'production')):
        raise ValidationError('phase-specific actual trigger chain and current admission required')
    if not assignment['authorization']['source_mutation']:
        raise ValidationError('source work is not authorized')
    return {'lane': 'source-only', 'selected_phase': 'source', 'outcome': 'working',
            'environment_required_now': False, 'same_native_resume': c['native_session_id'],
            'staging': 'not-applicable', 'authority': 'assignment-source-only'}


def verify_workflow_inventory(snapshot, assignment):
    """Omission is not evidence: cover every tracked workflow at the exact head."""
    worktree = Path(assignment['worktree']).resolve(strict=True)
    result = subprocess.run(['git', '-C', str(worktree), 'ls-files', '-z', '--', '.github/workflows'],
                            capture_output=True, text=True, timeout=10)
    if result.returncode:
        raise ValidationError('current workflow inventory unavailable')
    paths = {p for p in result.stdout.split('\0') if p.endswith(('.yml', '.yaml'))}
    inventory = snapshot.get('workflow_inventory')
    if not paths or not isinstance(inventory, list) or len(inventory) != len(paths):
        raise ValidationError('complete current workflow inventory required')
    declared = {entry.get('path'): entry for entry in inventory if isinstance(entry, dict)}
    if set(declared) != paths:
        raise ValidationError('workflow inventory omits or adds a workflow')
    head = subprocess.run(['git', '-C', str(worktree), 'rev-parse', 'HEAD'],
                          capture_output=True, text=True, timeout=10)
    if head.returncode or head.stdout.strip() != snapshot.get('head_sha'):
        raise ValidationError('workflow inventory revision is not the current source head')
    covered = set()
    for node in snapshot['trigger_chain']:
        if node.get('workflow_path') not in paths or node['revision'] != snapshot['head_sha']:
            raise ValidationError('trigger node lacks current workflow inventory evidence')
        covered.add(node['workflow_path'])
    if covered != paths:
        raise ValidationError('trigger chain omits a current workflow')
    for name, entry in declared.items():
        path = worktree / name
        if path.is_symlink() or hashlib.sha256(path.read_bytes()).hexdigest() != entry.get('sha256'):
            raise ValidationError('workflow content differs from verified trigger evidence')


def verify_admission(assignment, c, env, readiness):
    if not c.get('delivery_readiness_file'):
        return
    result = subprocess.run([c['coordination_executable'], 'terminal-events', 'decision-get',
                             '--reservation', assignment['reservation']['key'],
                             '--generation', str(assignment['reservation']['generation']),
                             '--worker-session', c['native_session_id'],
                             '--expected-revision', str(readiness['admission']['revision'])],
                            cwd=c['ledger_directory'], env=env, capture_output=True, text=True, timeout=10)
    if result.returncode:
        raise ValidationError('phase admission is stale or unavailable; reread current bound decision')
    decision = json.loads(result.stdout)
    if (decision.get('revision') != readiness['admission']['revision'] or decision.get('action') != 'proceed'
            or decision.get('worker_agent') != c['agent_id']):
        raise ValidationError('phase admission is not the current same-Worker proceed decision')
