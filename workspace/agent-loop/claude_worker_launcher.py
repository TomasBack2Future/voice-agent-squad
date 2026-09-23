#!/usr/bin/env python3
"""Check or launch one assigned Claude Worker; never claim, bind or install."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import uuid
import terminal_receiver

from validate_context_package import ROOT, ValidationError, validate_file
from worker_preflight import check_worktree

IDENTITY_ENV = ('CODEX_THREAD_ID', 'CODEX_SESSION_ID', 'SQUAD_SESSION_ID',
                'SQUAD_AGENT', 'CLAUDE_SESSION_ID')


def diagnostic(value: str) -> str:
    # Preserve actionable wrapper errors without echoing common credential forms.
    value = re.sub(r'(?i)(?:[\w-]*(?:token|password|secret|cookie|authorization|api[_-]?key)[\w-]*)\s*[:=].*',
                   '[credential field redacted]', value)
    value = re.sub(r'(?i)bearer\s+\S+', 'Bearer [redacted]', value)
    value = re.sub(r'(https?://)[^\s/@]+:[^\s/@]+@', r'\1[redacted]@', value)
    return ''.join(c for c in value if c in '\n\t' or c.isprintable())[:512].strip()


def config_file(path: Path) -> dict:
    c = validate_file(path, ROOT / 'schemas/claude-launch.schema.json')
    try:
        if str(uuid.UUID(c['native_session_id'])) != c['native_session_id']:
            raise ValueError()
    except ValueError as error:
        raise ValidationError('native_session_id must be a canonical UUID') from error
    for key in ('coordination_executable', 'client_executable'):
        p = Path(c[key])
        if not p.is_file() or not os.access(p, os.X_OK):
            raise ValidationError(f'{key} is not executable')
    if not (Path(c['ledger_directory']) / '.squad').is_dir():
        raise ValidationError('ledger_directory is not initialized')
    if not Path(c['prompt_file']).is_file():
        raise ValidationError('prompt_file is unavailable')
    if c.get('event_executable'):
        check = subprocess.run([c['event_executable'], 'terminal-events', 'publish', '--help'],
                               capture_output=True, text=True, timeout=10, check=False)
        if check.returncode or '--outcome' not in check.stdout:
            raise ValidationError('event_executable lacks structured terminal-events publish')
    return c


def child_environment(c: dict) -> dict:
    env = {k: v for k, v in os.environ.items() if k not in IDENTITY_ENV}
    suffix = hashlib.sha256(str(Path(c['ledger_directory']).resolve()).encode()).hexdigest()[:12]
    env.update(SQUAD_SESSION_ID=f"claude:{c['native_session_id']}:{suffix}",
               SQUAD_AGENT=c['agent_id'], SQUAD_NO_AUTO_DAEMON='1', SQUAD_NO_BROWSER='1')
    if c['coordination_mode'] == 'codex-wrapper-compat':
        # Local squad-coordination -> squad-codex may require these even for Claude.
        # Derive both from the assigned child; never inherit the Dispatcher id.
        env.update(CODEX_THREAD_ID=c['native_session_id'], CODEX_SESSION_ID=c['native_session_id'])
    return env


def binding(assignment: dict, c: dict, env: dict) -> str:
    try:
        p = subprocess.run([c['coordination_executable'], 'dispatch', 'list', '--json'],
                           cwd=c['ledger_directory'], env=env, capture_output=True,
                           text=True, timeout=10, check=False)
    except subprocess.TimeoutExpired as error:
        raise ValidationError('coordination read timed out; diagnose before retrying') from error
    if p.returncode:
        detail = diagnostic(p.stderr) or 'no usable stderr'
        raise ValidationError(f'coordination read failed (exit {p.returncode}): {detail}')
    try:
        rows = json.loads(p.stdout)
    except json.JSONDecodeError as error:
        raise ValidationError('coordination returned invalid JSON; not a binding wait') from error
    if not isinstance(rows, list) or any(not isinstance(r, dict) for r in rows):
        raise ValidationError('coordination returned an invalid reservation list')
    found = [r for r in rows if r.get('reservation_key') == assignment['reservation']['key']]
    if len(found) != 1:
        raise ValidationError('expected exactly one existing reservation')
    r = found[0]
    if (r.get('generation') != assignment['reservation']['generation']
            or r.get('source_ref') != 'github:' + assignment['issue']
            or r.get('canonical_item_id') != assignment['item']
            or r.get('reserved_by') != c['dispatcher_agent_id']
            or r.get('state') not in ('reserved', 'dispatched')):
        raise ValidationError('reservation identity, generation, owner or state changed')
    worker = r.get('worker_thread_id')
    if r['state'] == 'reserved' and not worker:
        return 'pending'
    if r['state'] == 'dispatched' and worker == c['native_session_id']:
        return 'bound'
    raise ValidationError('reservation is inconsistent or bound to another session')


def check_launch(assignment: dict, config_path: Path) -> dict:
    check_worktree(assignment)
    c = config_file(config_path)
    state = binding(assignment, c, child_environment(c))
    return {'status': 'ready', 'binding': state, 'coordination_access': 'verified',
            'environment': 'identity-sanitized', 'coordination_mode': c['coordination_mode'],
            'config_sha256': hashlib.sha256(config_path.read_bytes()).hexdigest(),
            'runtime_approval': 'not_checked', 'model_launch': 'not_performed'}


def wait_for_binding(assignment: dict, c: dict, env: dict, budget: float) -> None:
    deadline = time.monotonic() + budget
    while True:
        if binding(assignment, c, env) == 'bound':
            return
        if time.monotonic() >= deadline:
            raise ValidationError('binding deadline expired; no client started')
        time.sleep(min(1, max(0, deadline - time.monotonic())))


def receiver_arguments(c: dict, config_path: Path) -> list[str]:
    if not c.get('event_executable'):
        return []
    state = config_path.resolve().parent / ('receiver-' + c['native_session_id'])
    state.mkdir(mode=0o700, exist_ok=True)
    config = state / 'config.json'
    config.write_text(json.dumps(dict(native_session_id=c['native_session_id'], agent_id=c['agent_id'],
                     role='worker', state_directory=str(state), ledger_directory=c['ledger_directory'],
                     squad_executable=c['event_executable'], incarnation=str(uuid.uuid4()),
                     owner_pid=os.getpid()), indent=2) + '\n')
    setting = state / 'settings.json'
    setting.write_text(json.dumps(terminal_receiver.settings(config), indent=2) + '\n')
    return ['--settings', str(setting)]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('--assignment', type=Path, required=True)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--check', action='store_true', help='read-only; permits a valid unbound reservation')
    parser.add_argument('--wait-seconds', type=int, default=60)
    args = parser.parse_args()
    try:
        if not 0 <= args.wait_seconds <= 60:
            raise ValidationError('wait budget must be between 0 and 60 seconds')
        a = validate_file(args.assignment, ROOT / 'schemas/assignment-envelope.schema.json')
        if args.check:
            print(json.dumps(check_launch(a, args.config), sort_keys=True))
            return 0
        check_worktree(a)
        c = config_file(args.config)
        env = child_environment(c)
        wait_for_binding(a, c, env, args.wait_seconds)
        check_worktree(a)
        # Read the prompt only after binding. Never echo it or the child environment.
        prompt = Path(c['prompt_file']).read_text()
        receiver_args = receiver_arguments(c, args.config)
        os.chdir(a['worktree'])
        os.execve(c['client_executable'], [c['client_executable'], '--session-id',
                  c['native_session_id'], '--permission-mode', c['permission_mode'],
                  *receiver_args, prompt], env)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        reason = str(error) if isinstance(error, ValidationError) else 'launcher input or executable unavailable'
        print(json.dumps({'status': 'blocked', 'reason': reason}), file=sys.stderr)
        return 2
    return 0


if __name__ == '__main__':
    sys.exit(main())
