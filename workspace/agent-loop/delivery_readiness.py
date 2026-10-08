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


def workflow_facts(raw):
    """Parse committed YAML; unsupported/ambiguous execution never proves CI-only."""
    try:
        import yaml
    except ImportError:
        raise ValidationError('PyYAML workflow parser unavailable; install pinned workflow requirements') from None
    class Loader(yaml.BaseLoader):
        def construct_mapping(self, node, deep=False):
            result = {}
            for key, value in node.value:
                name = self.construct_object(key, deep=deep)
                if not isinstance(name, str) or name in result:
                    raise ValidationError('duplicate or unsupported workflow key')
                result[name] = self.construct_object(value, deep=deep)
            return result
    try:
        if len(raw) > 1 << 20:
            raise ValidationError('workflow exceeds inspection bound')
        if any(isinstance(token, (yaml.tokens.AliasToken, yaml.tokens.AnchorToken, yaml.tokens.TagToken)) for token in yaml.scan(raw)):
            raise ValidationError('workflow aliases/anchors/tags require supported independent qualification')
        workflow = yaml.load(raw, Loader=Loader)
    except yaml.YAMLError:
        raise ValidationError('workflow YAML unavailable or malformed') from None
    if not isinstance(workflow, dict):
        raise ValidationError('workflow must be a mapping')
    triggers = workflow.get('on')
    if isinstance(triggers, str): triggers = {triggers: None}
    if isinstance(triggers, list): triggers = {key: None for key in triggers}
    if not isinstance(triggers, dict) or not triggers:
        raise ValidationError('actual workflow triggers unavailable')
    events = set()
    for event, config in triggers.items():
        if event == 'push':
            if isinstance(config, dict) and 'tags' in config:
                events.add('tag_push')
            # Unless positively tag-only, a push can include main. Branch/path
            # filters do not weaken the gate through hand-written labels.
            if not isinstance(config, dict) or 'tags' not in config or 'branches' in config:
                events.add('main_push')
        elif event in ('pull_request', 'workflow_dispatch', 'workflow_run', 'repository_dispatch', 'release'):
            events.add(event)
        else:
            raise ValidationError('unsupported actual workflow trigger: ' + event)
    safe_actions = ('actions/checkout@', 'actions/setup-go@', 'actions/setup-python@', 'actions/setup-node@',
                    'actions/cache@', 'actions/upload-artifact@', 'actions/download-artifact@', 'golangci/golangci-lint-action@')
    effect = 'ci'
    jobs = workflow.get('jobs', {})
    if not isinstance(jobs, dict):
        raise ValidationError('unsupported workflow jobs')
    for job in jobs.values():
        if not isinstance(job, dict) or 'uses' in job:
            raise ValidationError('reusable workflow effect requires independent qualification')
        for step in job.get('steps', []):
            if not isinstance(step, dict):
                raise ValidationError('unsupported workflow step')
            action = step.get('uses', '')
            run = step.get('run', '')
            if action and not action.startswith(safe_actions):
                # Unknown actions can deploy; never accept a self-labeled CI effect.
                raise ValidationError('workflow action effect is unqualified; cannot trust self-labeled CI/deploy evidence')
            if run:
                for line in run.splitlines():
                    line = line.strip()
                    if not line or line.startswith('#'): continue
                    if re.match(r'(?:helm (?:upgrade|install)|kubectl (?:apply|set|rollout)|gh workflow run)(?: |$)', line):
                        effect = 'deploy'
                    elif not re.fullmatch(r'(?:go (?:test|vet|build)|golangci-lint run|python3 -m unittest(?: discover)?|node --test)(?: [A-Za-z0-9_./*:=, -]+)?', line):
                        raise ValidationError('workflow command effect is unqualified; cannot derive a weaker environment lane')
    return workflow, events, effect


def verify_workflow_inventory(snapshot, assignment):
    """Read the complete commit tree and blobs, never index/worktree provenance."""
    worktree = Path(assignment['worktree']).resolve(strict=True)
    def git(*args):
        result = subprocess.run(['git', '-C', str(worktree), *args], capture_output=True, timeout=10)
        if result.returncode: raise ValidationError('committed workflow evidence unavailable')
        return result.stdout
    head = git('rev-parse', 'HEAD').decode().strip()
    if head != snapshot.get('head_sha'):
        raise ValidationError('workflow inventory revision is not the current source head')
    paths = {p for p in git('ls-tree', '-r', '--name-only', '-z', head, '--', '.github/workflows').decode().split('\0') if p.endswith(('.yml', '.yaml'))}
    inventory = snapshot.get('workflow_inventory')
    if not paths or not isinstance(inventory, list) or len(inventory) != len(paths):
        raise ValidationError('complete committed workflow inventory required')
    declared = {entry.get('path'): entry for entry in inventory if isinstance(entry, dict)}
    if set(declared) != paths:
        raise ValidationError('workflow inventory omits or adds a committed workflow')
    facts = {}
    for name, entry in declared.items():
        raw = git('show', head + ':' + name)
        path = worktree / name
        if (hashlib.sha256(raw).hexdigest() != entry.get('sha256') or path.is_symlink()
                or not path.is_file() or path.read_bytes() != raw):
            raise ValidationError('workflow evidence or active file differs from committed blob')
        facts[name] = workflow_facts(raw)
    covered = {name: set() for name in paths}
    for node in snapshot['trigger_chain']:
        name = node.get('workflow_path')
        if name not in paths or node['revision'] != head:
            raise ValidationError('trigger node lacks committed workflow evidence')
        workflow, events, effect = facts[name]
        if node['event'] not in events or (effect == 'deploy' and node['effect'] != 'deploy'):
            raise ValidationError('declared trigger/effect weakens actual committed workflow')
        if node['event'] == 'workflow_run':
            config = workflow['on']['workflow_run']
            upstream = config.get('workflows', []) if isinstance(config, dict) else []
            parents = [n for n in snapshot['trigger_chain'] if n['id'] in node.get('triggered_by', [])]
            if not parents or any(facts[parent['workflow_path']][0].get('name') not in upstream for parent in parents):
                raise ValidationError('workflow_run upstream does not match committed workflows')
        covered[name].add(node['event'])
    if any(covered[name] != facts[name][1] for name in paths):
        raise ValidationError('trigger chain omits an actual committed event')


def closure_plan(snapshot, assignment, c, business_trigger=None, goal_complete=False, config_changed=False):
    """Express Issue closure separately from user-goal completion.

    Evaluates the readiness snapshot, then reports whether the Issue is
    complete, whether the user goal is complete, and the exact next step
    with its owner. A missing business trigger (e.g. the canary batch that
    exercises the real recovery path) or a changed effective config (e.g.
    a stale 1/1 reference now actually 32/64) keeps the goal open with a
    concrete follow-up instead of a blanket re-approval or redeploy.
    """
    result = evaluate(snapshot, assignment, c)
    issue_complete = result['phase_gates']['closure']['ready']
    if goal_complete and business_trigger and not config_changed:
        return {'issue_complete': issue_complete, 'goal_complete': True,
                'next_step': 'none', 'owner': 'none', 'stale_config': ''}
    if not business_trigger:
        return {'issue_complete': issue_complete, 'goal_complete': False,
                'next_step': 'prepare and authorize one canary batch that triggers the actual post-release business path',
                'owner': c.get('agent_id', 'worker'),
                'stale_config': 'effective config changed since release instruction (e.g. 1/1 now 32/64); re-verify before follow-up' if config_changed else ''}
    if config_changed:
        return {'issue_complete': issue_complete, 'goal_complete': False,
                'next_step': 're-verify plan against current effective config, then rerun affected checks only',
                'owner': c.get('agent_id', 'worker'),
                'stale_config': 'release instruction references stale 1/1; actual is 32/64'}
    return {'issue_complete': issue_complete, 'goal_complete': False,
            'next_step': 'complete remaining goal assertions; Issue closure alone does not prove them',
            'owner': c.get('agent_id', 'worker'), 'stale_config': ''}


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
