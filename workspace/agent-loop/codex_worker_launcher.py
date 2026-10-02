#!/usr/bin/env python3
"""Qualify or resume one bound Codex CLI Worker. No App fallback or installation."""
from __future__ import annotations
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

from codex_rpc import RPC
from validate_context_package import ROOT, ValidationError, validate_file
from codex_writer_fence import check_writer_fence
from worker_preflight import check_profile, check_worktree, git, repository_name
from claude_worker_launcher import IDENTITY_ENV, binding, check_heartbeat_runtime
from codex_heartbeat import heartbeat, require_execution_fence
from delivery_readiness import selected_readiness, verify_admission
from human_authorization import authorization, prompt_context


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def save(path, value):
    """Atomic, crash-durable local receipt; never a second work ledger."""
    path = Path(path)
    temporary = path.with_name(path.name + '.tmp')
    with temporary.open('w') as out:
        os.chmod(temporary, 0o600)
        json.dump(value, out, sort_keys=True)
        out.write('\n')
        out.flush()
        os.fsync(out.fileno())
    os.replace(temporary, path)
    descriptor = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def child_environment(c):
    env = {k: v for k, v in os.environ.items() if k not in IDENTITY_ENV}
    env.update(SQUAD_AGENT=c['agent_id'], SQUAD_SESSION_ID='codex:' + c['native_session_id'],
               CODEX_THREAD_ID=c['native_session_id'], CODEX_SESSION_ID=c['native_session_id'],
               SQUAD_NO_AUTO_DAEMON='1', SQUAD_NO_BROWSER='1')
    return env


def config_file(path):
    c = validate_file(path, ROOT / 'schemas/codex-launch.schema.json')
    if str(uuid.UUID(c['native_session_id'])) != c['native_session_id']:
        raise ValidationError('native session must be a canonical UUID')
    if c['client'] != 'cli':
        raise ValidationError('Codex App delivery unavailable: CLI proof does not qualify App ownership or wake')
    for key in ('client_executable', 'coordination_executable'):
        p = Path(c[key])
        if not p.is_file() or not os.access(p, os.X_OK):
            raise ValidationError(key + ' is not executable')
    if not (Path(c['ledger_directory']) / '.squad').is_dir() or not Path(c['prompt_file']).is_file():
        raise ValidationError('initialized ledger and prompt file required')
    return c


def resume_worktree(assignment, c):
    path = c.get('checkpoint_file')
    if not path:
        check_worktree(assignment)
        return None
    checkpoint = validate_file(Path(path), ROOT / 'schemas/checkpoint.schema.json')
    code = checkpoint['code']
    worktree = Path(assignment['worktree']).resolve(strict=True)
    expected = {'repository': assignment['repository'], 'worktree': str(worktree),
                'branch': assignment['branch'], 'base_sha': assignment['base_sha'],
                'head_sha': git(worktree, 'rev-parse', 'HEAD'), 'dirty': bool(git(worktree, 'status', '--porcelain'))}
    if (checkpoint['assignment_id'] != assignment['assignment_id'] or code != expected
            or checkpoint['ownership']['item'] != assignment['item']
            or checkpoint['ownership']['holder'] != c['agent_id']
            or Path(git(worktree, 'rev-parse', '--show-toplevel')).resolve() != worktree
            or git(worktree, 'branch', '--show-current') != assignment['branch']
            or repository_name(git(worktree, 'remote', 'get-url', 'origin'), assignment.get('repository_host', 'github.com'), assignment.get('clone_layout', 'plain')) != assignment['repository']):
        raise ValidationError('resume checkpoint/worktree/ownership identity mismatch')
    result = subprocess.run(['git', '-C', str(worktree), 'merge-base', '--is-ancestor', assignment['base_sha'], expected['head_sha']],
                            capture_output=True, timeout=10)
    if result.returncode:
        raise ValidationError('resume head is not descended from the assigned base')
    return checkpoint


def executable(c):
    binary = c['client_executable']
    result = subprocess.run([binary, '--version'], capture_output=True, text=True, timeout=10)
    version = result.stdout.strip()
    if result.returncode or version != 'codex-cli 0.159.2':
        raise ValidationError('Codex version has no qualified adapter contract; requalify before adoption')
    for command, flags in [(('queue', '--help'), ('--thread', '--message', '--remote')),
                           (('resume', '--help'), ('--remote',)),
                           (('--help',), ('--sandbox', '--ask-for-approval'))]:
        result = subprocess.run([binary, *command], capture_output=True, text=True, timeout=10)
        if result.returncode or any(flag not in result.stdout for flag in flags):
            raise ValidationError('installed Codex CLI contract unavailable')
    return {'executable_sha256': digest(binary), 'version': version}


def selection(c):
    return {key: c[key] for key in ('client', 'model', 'effort', 'provider', 'sandbox',
                                   'approval_policy', 'approvals_reviewer')}


def live_target(rpc, c, worktree):
    # Never resume an unloaded target here: that could create a second executor.
    loaded = rpc.call('thread/loaded/list', {})
    if c['native_session_id'] not in loaded.get('data', []):
        raise ValidationError('owning native thread is not loaded; no receiver or replacement started')
    thread = rpc.call('thread/read', {'threadId': c['native_session_id']})['thread']
    if (thread.get('id') != c['native_session_id'] or thread.get('cwd') != str(Path(worktree).resolve())
            or thread.get('modelProvider') != c['provider'] or thread.get('model') != c['model']
            or thread.get('reasoningEffort') != c['effort'] or thread.get('canAcceptDirectInput') is not True):
        raise ValidationError('loaded native target identity/model/effort/capability mismatch')
    # Rejoin an already-loaded thread without any settings overrides. The reply
    # exposes effective approval/sandbox, unlike persisted metadata or model/list.
    effective = rpc.call('thread/resume', {'threadId': c['native_session_id'], 'excludeTurns': True})
    check_effective(effective, c)
    return effective


def check_effective(effective, c):
    sandbox = {'read-only': 'readOnly', 'workspace-write': 'workspaceWrite',
               'danger-full-access': 'dangerFullAccess'}[c['sandbox']]
    if (effective.get('model') != c['model'] or effective.get('reasoningEffort') != c['effort']
            or effective.get('modelProvider') != c['provider']
            or effective.get('approvalPolicy') != c['approval_policy']
            or effective.get('approvalsReviewer') != c['approvals_reviewer']
            or effective.get('sandbox', {}).get('type') != sandbox):
        raise ValidationError('effective runtime permissions/model differ from selected launch policy')


def server_identity(c):
    os.kill(c['server_pid'], 0)
    result = subprocess.run(['ps', '-p', str(c['server_pid']), '-o', 'command='], capture_output=True,
                            text=True, timeout=5)
    expected = c['client_executable'] + ' app-server --listen ' + c['endpoint']
    if result.returncode or not result.stdout.strip().startswith(expected):
        raise ValidationError('endpoint owner process does not match the selected native server')
    socket = Path(c['endpoint'].removeprefix('unix://'))
    info = socket.lstat()
    target = socket.resolve(strict=True).lstat()
    return [c['server_pid'], info.st_dev, info.st_ino, target.st_dev, target.st_ino]


def check_launch(assignment, config_path):
    check_profile(assignment)
    c = config_file(config_path)
    check_writer_fence(c)
    checkpoint = resume_worktree(assignment, c)
    delivery = selected_readiness(assignment, c)
    if delivery['selected_phase'] not in ('source', 'merge'):
        raise ValidationError('deployment/acceptance must use the project delivery adapter; this launcher admits source work')
    env = child_environment(c)
    if binding(assignment, c, env) != 'bound':
        raise ValidationError('exact native reservation binding required before Codex resume')
    verify_admission(assignment, c, env, delivery)
    check_heartbeat_runtime(c, env)
    capability = subprocess.run([c['coordination_executable'], 'heartbeat', '--help'],
                                cwd=c['ledger_directory'], env=env, capture_output=True, text=True, timeout=10)
    if capability.returncode or '--json' not in capability.stdout or '--require-primary' not in capability.stdout:
        raise ValidationError('Structured exact-primary heartbeat runtime unavailable; reviewed install required')
    check_delivery_runtime(c, env)
    result = subprocess.run([c['coordination_executable'], 'claim-inspect', assignment['item']],
                            cwd=c['ledger_directory'], env=env, capture_output=True, text=True, timeout=10)
    if result.returncode:
        raise ValidationError('primary ownership could not be checked')
    claim = json.loads(result.stdout)['env_claim']
    if claim and (claim.get('item') != assignment['item'] or claim.get('holder') != c['agent_id']
                  or claim.get('state') != 'held'):
        raise ValidationError('primary work is owned by another actor or recovery is required')
    if checkpoint and claim and claim.get('generation') != checkpoint['ownership']['generation']:
        raise ValidationError('resume primary ownership generation changed')
    if delivery['environment_required_now']:
        from claude_worker_launcher import check_resources
        check_resources(assignment, c, env)
        profile = check_profile(assignment)
        resource = profile.get('resources', {}).get(delivery['environment_phase'])
        if not resource:
            raise ValidationError('auto-deploy merge lacks a declared protected environment')
        result = subprocess.run([c['coordination_executable'], 'claim-inspect', resource], cwd=c['ledger_directory'],
                                env=env, capture_output=True, text=True, timeout=10)
        if result.returncode:
            raise ValidationError('auto-deploy environment claim could not be checked')
        protected = json.loads(result.stdout)['env_claim']
        if not protected or protected.get('holder') != c['agent_id'] or protected.get('state') != 'held':
            raise ValidationError('auto-deploy merge requires current protected environment ownership')
    require_execution_fence(c)
    binary = check_qualification(c)
    owner = server_identity(c)
    with RPC(c['endpoint']) as rpc:
        live_target(rpc, c, assignment['worktree'])
    if server_identity(c) != owner:
        raise ValidationError('native server incarnation changed during admission')
    return {'status': 'ready', 'binding': 'bound', 'coordination_access': 'verified',
            'primary_ownership': claim or 'unclaimed', 'claim_heartbeat': 'supervised',
            'runtime_approval': {'policy': c['approval_policy'], 'reviewer': c['approvals_reviewer'],
                                 'sandbox': c['sandbox'], 'verified': True},
            'environment': 'identity-sanitized', 'binary': binary, 'model_entitlement': 'isolated_completed',
            'delivery': {'client': 'cli', 'route': 'structured_queue', 'owner': owner, 'app': 'unavailable'},
            'authorization': authorization(c, assignment), 'delivery_readiness': delivery,
            'config_sha256': digest(config_path)}


def check_delivery_runtime(c, env):
    for command, flag in [(('terminal-events', 'listen', '--help'), '--defer-delivery'),
                          (('terminal-events', 'delivered', '--help'), '--delivery-session')]:
        result = subprocess.run([c['coordination_executable'], *command], cwd=c['ledger_directory'],
                                env=env, capture_output=True, text=True, timeout=10)
        if result.returncode or flag not in result.stdout:
            raise ValidationError('coordination runtime lacks deferred native acceptance; install reviewed matching runtime before adoption')


def check_qualification(c):
    binary = executable(c)
    proof = json.loads(Path(c['qualification_file']).read_text())
    if (proof.get('schema_version') != 'codex.control-plane-qualification.v1'
            or proof.get('selection') != selection(c) or proof.get('binary') != binary
            or proof.get('entitlement') != 'completed' or proof.get('idle_queue') != 'completed'
            or not 0 <= time.time() - proof.get('checked_at', 0) <= 86400):
        raise ValidationError('current isolated entitlement/idle queue qualification required')
    return binary


def wait_turn(rpc, thread, turn_id, timeout=90):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        pending, rpc.notifications = rpc.notifications, []
        for message in pending:
            params = message.get('params', {})
            if (message.get('method') == 'turn/completed' and params.get('threadId') == thread
                    and (turn_id is None or params.get('turn', {}).get('id') == turn_id)):
                if params['turn']['status'] != 'completed':
                    raise ValidationError('isolated model entitlement/queue turn failed')
                return
        message = rpc.receive(deadline)
        if 'id' in message and 'method' in message:
            rpc.send({'id': message['id'], 'error': {'code': -32601, 'message': 'probe tools disabled'}})
        else:
            rpc.notifications.append(message)
    raise ValidationError('isolated turn deadline expired')


def qualify(c, directory):
    """Explicit bounded probe of a NEW isolated thread, never an assigned one."""
    if c['client'] != 'cli':
        raise ValidationError('App probe/adoption unavailable')
    binary = executable(c)
    directory = Path(directory).resolve(strict=True)
    if any(directory.iterdir()):
        raise ValidationError('probe directory must be empty and isolated')
    with RPC(c['endpoint'], probe=True) as rpc:
        result = rpc.call('thread/start', {'model': c['model'], 'modelProvider': c['provider'],
                          'cwd': str(directory), 'approvalPolicy': c['approval_policy'],
                          'approvalsReviewer': c['approvals_reviewer'], 'sandbox': 'read-only',
                          'config': {'model_reasoning_effort': c['effort'], 'mcp_servers': {}},
                          'baseInstructions': 'Isolated transport probe. Reply with the requested literal only. Never use tools.',
                          'developerInstructions': 'No tools, no filesystem operations or other sessions.'})
        thread = result['thread']['id']
        if thread == c['native_session_id']:
            raise ValidationError('probe must not target the assigned native session')
        probe = dict(c, native_session_id=thread, sandbox='read-only')
        # A newly created thread has no persisted rollout until its first turn;
        # inspect thread/start's effective result instead of trying to resume it.
        check_effective(result, probe)
        turn = rpc.call('turn/start', {'threadId': thread, 'model': c['model'], 'effort': c['effort'],
                        'input': [{'type': 'text', 'text': 'Reply SQUAD_PROBE_ENTITLEMENT.', 'text_elements': []}]})
        wait_turn(rpc, thread, turn['turn']['id'])
        # Exercise the installed CLI queue command with its explicit remote target.
        result = subprocess.run([c['client_executable'], 'queue', '--remote', c['endpoint'],
                                 '--thread', thread, '--message', 'Reply SQUAD_PROBE_IDLE_QUEUE.'],
                                env=child_environment(probe), capture_output=True, text=True, timeout=15)
        if result.returncode:
            raise ValidationError('installed CLI native queue probe failed')
        wait_turn(rpc, thread, None)
        live_target(rpc, probe, directory)
        rpc.call('thread/unsubscribe', {'threadId': thread})
    proof = {'schema_version': 'codex.control-plane-qualification.v1', 'checked_at': time.time(),
             'binary': binary, 'selection': selection(c), 'entitlement': 'completed',
             'idle_queue': 'completed', 'probe_thread': thread, 'probe_sandbox': 'read-only',
             'app_delivery': 'unavailable', 'native_message_id_dedupe': 'not_assumed'}
    save(c['qualification_file'], proof)
    return proof


def argv(c, worktree, prompt):
    return [c['client_executable'], '--remote', c['endpoint'], '--model', c['model'],
            '-c', 'model_provider=' + json.dumps(c['provider']),
            '-c', 'model_reasoning_effort=' + json.dumps(c['effort']),
            '-c', 'approvals_reviewer=' + json.dumps(c['approvals_reviewer']),
            '--sandbox', c['sandbox'], '--ask-for-approval', c['approval_policy'],
            '--cd', worktree, 'resume', c['native_session_id'], prompt]


def main():
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('--assignment', type=Path, required=True)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--check', action='store_true')
    parser.add_argument('--qualify', type=Path, help='explicit empty isolated probe directory; performs two model turns')
    args = parser.parse_args()
    try:
        a = validate_file(args.assignment, ROOT / 'schemas/assignment-envelope.schema.json')
        c = config_file(args.config)
        authorization(c, a)
        if args.qualify:
            print(json.dumps(qualify(c, args.qualify), sort_keys=True))
            return 0
        receipt = check_launch(a, args.config)
        if args.check:
            print(json.dumps(receipt, sort_keys=True))
            return 0
        state = Path(c['state_directory'])
        state.mkdir(mode=0o700, parents=True, exist_ok=True)
        with (state / (c['native_session_id'] + '.lock')).open('a') as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as error:
                raise ValidationError('one launcher already owns this native session') from error
            env = child_environment(c)
            heartbeat(a, c, env, check=True)
            prompt = Path(c['prompt_file']).read_text()
            prompt += prompt_context(c, a)
            from codex_receiver import supervise
            return supervise(argv(c, a['worktree'], prompt), a, c, env, args.config, receipt['delivery']['owner'])
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        reason = str(error) if isinstance(error, ValidationError) else 'Codex input/runtime unavailable; values withheld'
        print(json.dumps({'status': 'blocked', 'reason': reason}), file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
