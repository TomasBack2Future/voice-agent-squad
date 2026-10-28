#!/usr/bin/env python3
"""Original native source execution using the existing Squad custody and tool runtime."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import uuid

from claude_worker_launcher import authorization, binding, check_heartbeat_runtime, heartbeat
from codex_native_host import NativeCLI  # retained import for existing Codex callers
from native_runtime import native_host, native_identity, validate_selection
from native_source_failure import publish_failure
from muse_session_host import atomic
from muse_worker_container import Runtime
from muse_worker_launcher import publish_report
from muse_worker_handoff import check_handoff
from muse_worker_tools import child_environment, coordination
from validate_context_package import ROOT, ValidationError, validate_file
from worker_preflight import check_profile, check_worktree, git, repository_name


def source_config(path):
    config = validate_file(path, ROOT / 'schemas/native-source.schema.json')
    workspace = Path(config['workspace']).resolve(strict=True)
    protected = [ROOT, *(Path(config[key]) for key in ('native_state', 'state_directory',
                 'coordination_home', 'ledger_directory', 'client_executable',
                 'coordination_executable', 'prompt_file')), Path(config['tool_runtime']['executable'])]
    if config.get('auth_file'):
        protected.append(Path(config['auth_file']))
    if any(path.resolve().is_relative_to(workspace) for path in protected):
        raise ValidationError('native runtime, credentials and custody must remain outside mutable source')
    return config


def prepare(config_path):
    config = source_config(config_path)
    if config.get('native_session_id'):
        raise ValidationError('native already allocated; preserve the original task identity')
    root = Path(config['native_state'])
    root.mkdir(parents=True, mode=0o700, exist_ok=True)
    host = native_host(root, dict(config, role='worker'))
    try:
        turn = host.turn('Reply exactly SQUAD_SOURCE_BOOTSTRAP. No tools or file operations.', 'bootstrap-' + str(uuid.uuid4()), resume=False)
        terminal = host.wait(turn, seconds=90)
        if terminal['status'] != 'completed':
            raise ValidationError('native bootstrap failed; source task not started')
        config.update(native_session_id=host.config['native_session_id'], role='worker')
        atomic(config_path, config)
        atomic(root / 'bootstrap.json', {'native': config['native_session_id'], 'binary': host.binary,
                                       'effective': host.effective, 'terminal': terminal})
        return {'native': config['native_session_id'], 'status': 'prepared', 'binary': host.binary}
    finally:
        host.close()


def check_launch(assignment, config, resume=False):
    if config.get('role') != 'worker':
        raise ValidationError('explicit native source Worker selection required')
    validate_selection(config)
    if Path(config['workspace']).resolve() != Path(assignment['worktree']).resolve():
        raise ValidationError('source workspace differs from assignment')
    check_profile(assignment)
    if resume or config.get('handoff'):
        if config.get('handoff'):
            if resume:
                raise ValidationError('fresh native handoff and same-native resume are separate')
            check_handoff(assignment, config)
        work = Path(config['workspace']).resolve()
        if (Path(git(work, 'rev-parse', '--show-toplevel')).resolve() != work
                or git(work, 'branch', '--show-current') != assignment['branch']
                or repository_name(git(work, 'remote', 'get-url', 'origin'), assignment.get('repository_host', 'github.com'),
                                   assignment.get('clone_layout', 'plain')) != assignment['repository']):
            raise ValidationError('retained source identity changed')
        git(work, 'merge-base', '--is-ancestor', assignment['base_sha'], 'HEAD')
    else:
        check_worktree(assignment)
    if (not assignment['authorization']['source_mutation'] or assignment['authorization']['staging']
            or assignment['authorization']['production']):
        raise ValidationError('native adapter is source-only')
    authorization(config, assignment)
    env = child_environment(config)
    if binding(assignment, dict(config, coordination_mode='native'), env) != 'bound':
        raise ValidationError('exact Worker reservation is not bound')
    heartbeat(assignment, config, env, check=True)
    inspected = subprocess.run([config['coordination_executable'], 'claim-inspect', assignment['item']],
                    cwd=config['ledger_directory'], env=env, capture_output=True, text=True, timeout=10, check=True)
    claim = json.loads(inspected.stdout)['env_claim']
    if (not claim or claim.get('holder') != config['agent_id'] or claim.get('state') != 'held'
            or claim.get('generation') != config['claim_generation']):
        raise ValidationError('original primary claim generation changed; no client admitted')
    check_heartbeat_runtime(config, env)
    current = subprocess.run([config['coordination_executable'], 'dispatch', 'controller-status',
                             '--actor', config['dispatcher_agent_id']], cwd=config['ledger_directory'],
                            env=env, capture_output=True, text=True, timeout=10, check=True)
    if json.loads(current.stdout) != {'actor': config['dispatcher_agent_id'], 'native_session': config['controller_native'],
                                     'epoch': config['controller_epoch']}:
        raise ValidationError('original controller identity changed')
    Runtime(config)


def references(assignment):
    profile = Path(assignment['project_profile']['path'])
    if not profile.is_absolute():
        profile = Path(assignment['worktree']) / profile
    paths = {'role': ROOT / 'roles/worker/SKILL.md', 'profile': profile, 'lifecycle': ROOT / 'lifecycle-continuity.md'}
    return {key: {'path': str(path.resolve(strict=True)), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest()}
            for key, path in paths.items()}


def run(assignment_path, config_path, resume=False):
    assignment = validate_file(assignment_path, ROOT / 'schemas/assignment-envelope.schema.json')
    config = source_config(config_path)
    check_launch(assignment, config, resume)
    root = Path(config['state_directory']); root.mkdir(parents=True, mode=0o700, exist_ok=True)
    with (root / (config['native_session_id'] + '.lock')).open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        writer = root / (config['native_session_id'] + '.writer.json')
        custody = {key: config[key] for key in ('native_session_id', 'agent_id', 'claim_generation',
                   'dispatcher_agent_id', 'controller_native', 'controller_epoch')}
        if writer.exists():
            old = json.loads(writer.read_text())
            if old.get('state') != 'joined' or old.get('assignment') != assignment or old.get('custody') != custody:
                raise ValidationError('original source writer is unresolved; no replacement admitted')
            if old.get('pid'):
                try:
                    os.kill(old['pid'], 0)
                except ProcessLookupError:
                    pass
                else:
                    raise ValidationError('original source writer still exists')
        state = root / str(uuid.uuid4()); state.mkdir(mode=0o700)
        config.update(execution_binding=str(state / 'binding.json'), execution_id=config['runtime'] + '-' + state.name,
                      assignment=assignment, startup_references=references(assignment), delivery_incarnation=state.name)
        bridge = state / 'bridge-config.json'; atomic(bridge, config)
        mcp = {'squad_worker': {'command': sys.executable,
               'args': [str(ROOT / 'codex_worker_tools.py'), '--config', str(bridge)],
               'env': {native_identity(config): config['native_session_id'], 'PYTHONDONTWRITEBYTECODE': '1'},
               'startup_timeout_sec': 30}}
        if config['runtime'] == 'muse':
            del mcp['squad_worker']['startup_timeout_sec']
        prompt = ('Exact assignment:\n' + json.dumps(assignment)
                  + '\nRead all three startup references with squad_worker.read_file before mutations:\n'
                  + json.dumps(config['startup_references'])
                  + '\nUse the assigned Squad source tools. Use events({}) and decision_get({}) to read current decisions, '
                    'then acknowledge each event after handling it. A hold prohibits source writes. '
                    'Record actual completion or blocker with report, then reply.\n'
                  + Path(config['prompt_file']).read_text())
        host = native_host(Path(config['native_state']), config, mcp)
        native_pid = None; pin = None; pinned = False; attempted = False
        terminal = None; output = None
        atomic(writer, {'state': 'intent', 'assignment': assignment, 'custody': custody, 'evidence': str(state)})
        try:
            turn = host.turn(prompt, 'source-' + state.name)
            native_pid = host.child.pid
            atomic(writer, {'state': 'running', 'pid': native_pid, 'assignment': assignment, 'custody': custody, 'evidence': str(state)})
            pin = dict(kind='worker', id=config['execution_id'], item=assignment['item'], actor=config['agent_id'],
                       native=config['native_session_id'], claim_generation=config['claim_generation'],
                       reservation=assignment['reservation']['key'], source_ref='github:' + assignment['issue'],
                       generation=assignment['reservation']['generation'], controller=config['dispatcher_agent_id'],
                       controller_native=config['controller_native'], epoch=config['controller_epoch'],
                       expires_at=int(time.time()) + config.get('max_seconds', 600), client_pid=native_pid,
                       tool_runtime=config['tool_runtime'])
            atomic(state / 'binding.json', pin)
            env = child_environment(config)
            attempted = True
            coordination(config, 'acquire'); pinned = True
            terminal = host.wait(turn, seconds=config.get('max_seconds', 600))
            atomic(state / 'startup.json', {'native': config['native_session_id'], 'binary': host.binary,
                   'effective': host.effective, 'references': config['startup_references']})
            if terminal['status'] != 'completed' or not (state / 'report.json').exists():
                raise ValidationError('native source turn ended without a completed durable report')
            output = {'native': config['native_session_id'], 'status': json.loads((state / 'report.json').read_text())['status'],
                      'evidence': str(state), 'turn': terminal['id']}
        finally:
            (state / 'closed').touch()
            cleanup_error = None
            if host.child is not None:
                native_pid = host.child.pid
            if attempted and not pinned:
                try:
                    status = coordination(config, 'inspect')
                    if status.get('state') == 'active':
                        pinned = True
                    elif status.get('state') != 'absent':
                        raise ValidationError('unresolved source acquisition state')
                except (OSError, ValueError, subprocess.SubprocessError) as error:
                    cleanup_error = error
            if pinned:
                try: coordination(config, 'suspend')
                except (OSError, ValueError, subprocess.SubprocessError) as error: cleanup_error = error
            try: host.close()
            except (OSError, ValueError, subprocess.SubprocessError) as error: cleanup_error = error
            bridge_info = json.loads((state / 'bridge.json').read_text()) if (state / 'bridge.json').exists() else None
            bridge_live = False
            if bridge_info:
                deadline = time.monotonic() + 5
                while True:
                    try: os.kill(bridge_info['pid'], 0); bridge_live = True
                    except ProcessLookupError: bridge_live = False; break
                    if time.monotonic() >= deadline: break
                    time.sleep(.05)
            operations = json.loads((state / 'operations.json').read_text()) if (state / 'operations.json').exists() else []
            for op in operations:
                if op.get('kind') == 'container' and 'container' in op:
                    runtime = Runtime(config)
                    if not runtime.joined(op): runtime.stop(op)
                    if not bridge_live and runtime.joined(op): op['state'] = 'joined'
            native_join_path = Path(config['native_state']) / 'native-join.json'
            native_join = json.loads(native_join_path.read_text()) if native_join_path.exists() else {}
            native_joined = native_pid is None or (native_join.get('pid') == native_pid and native_join.get('joined') is True)
            joined = native_joined and not bridge_live and all(op['state'] in ('completed', 'joined') for op in operations)
            atomic(state / 'operations.json', operations)
            atomic(state / 'join.json', {'native_pid': native_pid, 'native_exit': native_join.get('exit_code') if native_pid else None,
                   'bridge_live': bridge_live, 'bridge_pid': bridge_info['pid'] if bridge_info else None,
                   'binding': pin, 'operations': operations, 'joined': joined, 'terminal': terminal})
            if joined and cleanup_error is None:
                if pinned and terminal and terminal['status'] == 'completed' and (state / 'report.json').exists():
                    atomic(state / 'outcome.json', publish_report(config, env, pin, state))
                elif pinned:
                    publish_failure(config, env, pin, state)
                if pinned:
                    coordination(config, 'close', str(state / 'join.json'))
                for op in operations:
                    if op.get('kind') == 'container' and 'container' in op:
                        Runtime(config).call(['container', 'rm', op['container']])
                atomic(writer, {'state': 'joined', 'pid': native_pid, 'assignment': assignment, 'custody': custody, 'evidence': str(state)})
            else:
                raise ValidationError('source cleanup is unresolved; original execution custody retained')
        return output


def reconcile(assignment_path, config_path, state):
    state = state.resolve(strict=True)
    assignment = validate_file(assignment_path, ROOT / 'schemas/assignment-envelope.schema.json')
    selected = json.loads(config_path.read_text())
    config = json.loads((state / 'bridge-config.json').read_text())
    if (config['assignment'] != assignment or any(config[key] != selected[key] for key in
            ('agent_id', 'native_session_id', 'ledger_directory', 'coordination_home', 'dispatcher_agent_id',
             'controller_native', 'controller_epoch', 'claim_generation', 'state_directory'))):
        raise ValidationError('original source reconciliation identity changed')
    writer = Path(config['state_directory']) / (config['native_session_id'] + '.writer.json')
    current = json.loads(writer.read_text())
    if (current.get('assignment') != assignment or not current.get('evidence')
            or Path(current['evidence']).resolve(strict=True) != state):
        raise ValidationError('source reconciliation does not own the retained writer')
    if not (state / 'binding.json').exists():
        if current.get('state') != 'intent' or current.get('pid'):
            raise ValidationError('unbound source writer lacks an unstarted intent')
        if any((state / name).exists() for name in ('bridge.json', 'operations.json', 'report.json')):
            raise ValidationError('source tool custody exists; no unstarted reconciliation admitted')
        native = Path(config['native_state'])
        owner = json.loads((native / 'native-owner.json').read_text())
        input_path = native / 'input-intent.json'
        if not input_path.exists() and config['runtime'] == 'muse':
            inputs = list(native.glob('input-*.json'))
            if len(inputs) != 1:
                raise ValidationError('source input history is ambiguous; preserve its custody')
            input_path = inputs[0]
        intent = json.loads(input_path.read_text())
        proof = json.loads((native / 'native-join.json').read_text())
        if (owner != {'role': 'worker', 'workspace': config['workspace'], 'native': config['native_session_id']}
                or intent.get('state') != 'terminal' or not intent.get('client_id', '').startswith('bootstrap-')
                or intent.get('pid') != proof.get('pid') or proof.get('joined') is not True):
            raise ValidationError('original native input may have started; preserve its custody')
        try: os.kill(proof['pid'], 0)
        except ProcessLookupError: pass
        else: raise ValidationError('bootstrap process remains present')
        atomic(state / 'join.json', {'joined': True, 'binding': None, 'source_not_started': True,
                                    'bootstrap_join': str(native / 'native-join.json')})
        atomic(writer, dict(current, state='joined', pid=None))
        return {'status': 'reconciled-unstarted', 'native': config['native_session_id'], 'evidence': str(state)}
    joined = json.loads((state / 'join.json').read_text())
    pin = json.loads((state / 'binding.json').read_text())
    if joined.get('joined') is not True or joined.get('binding') != pin:
        raise ValidationError('original native/tool joins remain unresolved')
    for pid in (joined['native_pid'], joined.get('bridge_pid')):
        if not pid: continue
        try: os.kill(pid, 0)
        except ProcessLookupError: continue
        raise ValidationError('original native/tool process remains present')
    for op in joined['operations']:
        if op.get('kind') == 'container' and 'container' in op and not Runtime(config).joined(op):
            raise ValidationError('original container custody remains unresolved')
    env = child_environment(config)
    coordination(config, 'check-read')
    if (state / 'report.json').exists() and (joined.get('terminal') or {}).get('status') == 'completed':
        atomic(state / 'outcome.json', publish_report(config, env, pin, state))
    else:
        publish_failure(config, env, pin, state)
    coordination(config, 'close', str(state / 'join.json'))
    for op in joined['operations']:
        if op.get('kind') == 'container' and 'container' in op:
            Runtime(config).call(['container', 'rm', op['container']])
    atomic(writer, dict(current, state='joined'))
    return {'status': 'reconciled', 'native': config['native_session_id'], 'evidence': str(state)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--assignment', type=Path)
    parser.add_argument('--prepare', action='store_true')
    parser.add_argument('--resume', action='store_true')
    parser.add_argument('--check', action='store_true')
    parser.add_argument('--reconcile', type=Path)
    args = parser.parse_args()
    if args.reconcile:
        result = reconcile(args.assignment, args.config, args.reconcile)
    elif args.check:
        assignment = validate_file(args.assignment, ROOT / 'schemas/assignment-envelope.schema.json')
        check_launch(assignment, source_config(args.config), args.resume)
        result = {'status': 'launch-checked', 'native_qualified': False, 'surface': 'original-native-cli', 'execution': 'custody-pin-and-contained-tools'}
    else:
        result = prepare(args.config) if args.prepare else run(args.assignment, args.config, args.resume)
    print(json.dumps(result))


if __name__ == '__main__':
    main()
