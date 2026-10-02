#!/usr/bin/env python3
"""Adopt a fresh legitimate Dispatcher only after installed ledger CAS handoff."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import subprocess
import sys
import uuid

from codex_rpc import RPC
from codex_worker_launcher import (check_delivery_runtime, check_effective, check_qualification,
                                   child_environment, live_target, server_identity)
from codex_receiver import supervise
from validate_context_package import ROOT, ValidationError, validate_file


def controller_fence(c):
    def read(*args):
        result = subprocess.run([c['coordination_executable'], 'dispatch', *args],
                                cwd=c['ledger_directory'], env=child_environment(c),
                                capture_output=True, text=True, timeout=10)
        if result.returncode:
            raise ValidationError('installed controller handoff binding unavailable')
        return json.loads(result.stdout)
    current = read('controller-status')
    handoff = read('handoff-get', '--request-id', c['handoff_request_id'])
    if (current.get('actor') != c['agent_id'] or current.get('native_session') != c['native_session_id']
            or current.get('epoch') != c['controller_epoch']
            or handoff.get('new_actor') != c['agent_id'] or handoff.get('new_native') != c['native_session_id']
            or handoff.get('epoch') != c['controller_epoch'] or handoff.get('request_id') != c['handoff_request_id']
            or handoff.get('old_actor') == c['agent_id'] or handoff.get('old_native') == c['native_session_id']):
        raise ValidationError('fresh Dispatcher actor/native/epoch handoff mismatch')
    return handoff


def check_launch(c):
    if (str(uuid.UUID(c['native_session_id'])) != c['native_session_id']
            or c['agent_id'] != c['dispatcher_agent_id'] or c['client'] != 'cli'):
        raise ValidationError('exact fresh native and legitimate new Dispatcher actor required')
    handoff = controller_fence(c)
    # This PID belongs only to the NEW inactive dedicated CLI. It is never the
    # old shared App backend; ledger CAS excludes old owner protocol operations.
    try:
        os.kill(c['inactive_writer_pid'], 0)
    except ProcessLookupError:
        pass
    else:
        raise ValidationError('new inactive CLI writer has not exited; no second native executor')
    if not c['inactive_writer_joined'] or c['inactive_writer_pid'] == c['server_pid']:
        raise ValidationError('new dedicated writer join required; native server must remain separate')
    check_delivery_runtime(c, child_environment(c))
    binary = check_qualification(c)
    owner = server_identity(c)
    with RPC(c['endpoint']) as rpc:
        resumed = rpc.call('thread/resume', {'threadId': c['native_session_id'], 'excludeTurns': True})
        if resumed['thread']['id'] != c['native_session_id']:
            raise ValidationError('fresh Dispatcher native continuity failed')
        check_effective(resumed, c)
        live_target(rpc, c, c['worktree'])
    controller_fence(c)
    if server_identity(c) != owner:
        raise ValidationError('new selected server incarnation changed')
    return {'status': 'ready', 'native_session_id': c['native_session_id'], 'actor': c['agent_id'],
            'controller_epoch': c['controller_epoch'], 'handoff': handoff,
            'server_identity': owner, 'binary': binary, 'write_exclusion': 'installed-ledger-owner-and-recipient-fence',
            'handling': 'explicit-native-bound-terminal-events-ack', 'timers': 'untouched'}


def main():
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    try:
        c = validate_file(args.config, ROOT / 'schemas/codex-dispatcher-adoption.schema.json')
        for key in ('client_executable', 'coordination_executable'):
            if not Path(c[key]).is_file() or not os.access(c[key], os.X_OK):
                raise ValidationError('reviewed installed executable unavailable')
        if not (Path(c['ledger_directory']) / '.squad').is_dir() or not Path(c['prompt_file']).is_file():
            raise ValidationError('existing ledger and new Dispatcher prompt required')
        state = Path(c['state_directory'])
        state.mkdir(mode=0o700, parents=True, exist_ok=True)
        with (state / (c['native_session_id'] + '.lock')).open('a') as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as error:
                raise ValidationError('one launcher already owns this new native') from error
            receipt = check_launch(c)
            if args.check:
                print(json.dumps(receipt, sort_keys=True))
                return 0
            receiver = {k: v for k, v in c.items() if k not in ('handoff_request_id', 'inactive_writer_pid', 'inactive_writer_joined')}
            receiver['schema_version'] = 'agent-loop.codex-launch.v1'
            prompt = Path(c['prompt_file']).read_text()
            command = [c['client_executable'], '--remote', c['endpoint'], '--cd', c['worktree'],
                       'resume', c['native_session_id'], prompt]
            return supervise(command, None, receiver, child_environment(c), args.config, receipt['server_identity'])
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        reason = str(error) if isinstance(error, ValidationError) else 'fresh Dispatcher adoption unavailable; values withheld'
        print(json.dumps({'status': 'blocked', 'reason': reason}), file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
