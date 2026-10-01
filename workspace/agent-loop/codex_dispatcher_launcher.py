#!/usr/bin/env python3
"""Controlled same-native Dispatcher continuity; never stop or migrate an owner."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import subprocess
import sys
import uuid

from codex_rpc import RPC
from codex_worker_launcher import (check_delivery_runtime, check_effective, check_qualification, child_environment,
                                   digest, live_target, qualify, selection, server_identity)
from codex_receiver import process_start, supervise
from validate_context_package import ROOT, ValidationError, validate_file


def config_file(path):
    c = validate_file(path, ROOT / 'schemas/codex-dispatcher-launch.schema.json')
    if str(uuid.UUID(c['native_session_id'])) != c['native_session_id']:
        raise ValidationError('native session must be a canonical UUID')
    if c['client'] != 'cli' or c['agent_id'] != c['dispatcher_agent_id']:
        raise ValidationError('selected callback actor must be the existing Dispatcher')
    for key in ('client_executable', 'coordination_executable'):
        if not Path(c[key]).is_file() or not os.access(c[key], os.X_OK):
            raise ValidationError('selected executable unavailable')
    if not (Path(c['ledger_directory']) / '.squad').is_dir() or not Path(c['prompt_file']).is_file():
        raise ValidationError('existing ledger and Dispatcher prompt required')
    return c


def fence(c):
    receipt = validate_file(Path(c['transition_file']), ROOT / 'schemas/codex-dispatcher-transition.schema.json')
    if (receipt['native_session_id'] != c['native_session_id'] or receipt['agent_id'] != c['agent_id']
            or receipt['worktree'] != str(Path(c['worktree']).resolve(strict=True))
            or receipt['prior_selection'] != dict(selection(c), client='app')
            or digest(receipt['checkpoint_file']) != receipt['checkpoint_sha256']):
        raise ValidationError('transition native/actor/policy/checkpoint mismatch')
    if receipt['old_writer_scope'] != 'dedicated-process':
        raise ValidationError('shared App backend has no qualified per-thread ownership fence; transition blocked without stopping backend')
    # A quiet or unloaded thread is not proof that the old writer was joined.
    # Operator receipt plus independently checked PID/start fence are both needed.
    current_start = process_start(receipt['old_writer_pid'])
    if current_start is None:
        try:
            os.kill(receipt['old_writer_pid'], 0)
        except ProcessLookupError:
            pass
        else:
            raise ValidationError('old writer process identity unavailable; cannot prove stopped')
    if current_start == receipt['old_writer_started_at']:
        raise ValidationError('old App writer remains active; no resume or receiver started')
    if receipt['old_writer_pid'] == c['server_pid']:
        raise ValidationError('old writer and selected server must have separate custody')
    result = subprocess.run([c['coordination_executable'], 'dispatch', 'list', '--json'],
                            cwd=c['ledger_directory'], env=child_environment(c),
                            capture_output=True, text=True, timeout=10)
    if result.returncode:
        raise ValidationError('Dispatcher reservation custody unavailable')
    rows = json.loads(result.stdout)
    for expected in receipt['reservation_fences']:
        matches = [r for r in rows if r.get('reservation_key') == expected['key']]
        if (len(matches) != 1 or matches[0].get('reserved_by') != c['agent_id']
                or matches[0].get('generation') != expected['generation']
                or matches[0].get('worker_thread_id') != expected['worker_native_session_id']
                or matches[0].get('state') not in ('dispatched', 'completed')):
            raise ValidationError('Dispatcher callback reservation custody changed')
    return receipt


def check_launch(c):
    receipt = fence(c)
    check_delivery_runtime(c, child_environment(c))
    binary = check_qualification(c)
    owner = server_identity(c)
    with RPC(c['endpoint']) as rpc:
        # Only this explicit transition may load a dormant exact UUID. No settings
        # overrides, new thread, fork, claim, scheduler or timer is created.
        resumed = rpc.call('thread/resume', {'threadId': c['native_session_id'], 'excludeTurns': True})
        if resumed['thread']['id'] != c['native_session_id']:
            raise ValidationError('native continuity failed')
        check_effective(resumed, c)
        live_target(rpc, c, c['worktree'])
    fence(c)
    if server_identity(c) != owner:
        raise ValidationError('selected server incarnation changed')
    return {'status': 'ready', 'role': 'dispatcher', 'native_session_id': c['native_session_id'],
            'callback_agent': c['agent_id'], 'old_writer': 'joined-and-process-fenced',
            'checkpoint_sha256': receipt['checkpoint_sha256'], 'policy': selection(c),
            'effective_policy': 'verified-without-overrides', 'server_identity': owner,
            'binary': binary, 'app_delivery': 'unavailable', 'route': 'same-native-cli',
            'authorization_reference': receipt['authorization_reference'],
            'handling': 'explicit-terminal-events-ack', 'timers': 'untouched',
            'return': 'join-cli-and-receiver-before-same-native-app-resume'}


def main():
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--check', action='store_true')
    parser.add_argument('--qualify', type=Path)
    args = parser.parse_args()
    try:
        c = config_file(args.config)
        if args.qualify:
            print(json.dumps(qualify(c, args.qualify), sort_keys=True))
            return 0
        state = Path(c['state_directory'])
        state.mkdir(mode=0o700, parents=True, exist_ok=True)
        with (state / (c['native_session_id'] + '.lock')).open('a') as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as error:
                raise ValidationError('one launcher already owns this native session') from error
            receipt = check_launch(c)
            if args.check:
                print(json.dumps(receipt, sort_keys=True))
                return 0
            prompt = Path(c['prompt_file']).read_text()
            prompt += ('\nContinue existing Dispatcher policy/checkpoint and one bounded reconciliation. '
                       'No new assignment, controller, timer or installation authority. '
                       'Queue acceptance is not handling; acknowledge only after reconciliation.')
            receiver = {k: v for k, v in c.items() if k != 'transition_file'}
            receiver['schema_version'] = 'agent-loop.codex-launch.v1'
            # Preserve saved effective settings rather than setting new permissions.
            command = [c['client_executable'], '--remote', c['endpoint'], '--cd', c['worktree'],
                       'resume', c['native_session_id'], prompt]
            return supervise(command, None, receiver, child_environment(c), args.config,
                             receipt['server_identity'])
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        reason = str(error) if isinstance(error, ValidationError) else 'Dispatcher continuity unavailable; values withheld'
        print(json.dumps({'status': 'blocked', 'reason': reason}), file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
