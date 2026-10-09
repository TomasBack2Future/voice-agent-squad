#!/usr/bin/env python3
"""Admission for same-native recovery after a Muse runtime-failure event (#84).

Fails closed before any return path: the delivered event must name this
Worker's reservation/generation/native, the native session log must show the
failed turn as its last run record, `muse_failure_handling.decide` must return
`continue`, and each failure episode gets at most one attempt, recorded
durably here. Termination targets only the client recorded in the session's
own route facts and never escalates past SIGTERM. No composer input, model
call or ledger write happens in this module.
"""
from __future__ import annotations
import argparse
import fcntl
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time

import muse_failure_handling as handling

_EVENT = re.compile(r'\Aworker-terminal-v1/([A-Za-z0-9_.:-]+)/([1-9][0-9]*)/([A-Za-z0-9_.-]+)'
                    r'/runtime-failure/[1-9][0-9]*/(ep-[1-9][0-9]*)\Z')


class NotAdmitted(ValueError):
    """The recovery precondition failed; nothing was changed."""


def _event_identity(event, config):
    match = _EVENT.fullmatch(str(event.get('event_id') or ''))
    if match is None or event.get('kind') != 'runtime-failure':
        raise NotAdmitted('not a keyed runtime-failure event')
    reservation, generation, native, episode = match.groups()
    if (reservation, int(generation), native) != (config['reservation'], config['generation'],
                                                    config['native_session_id']):
        raise NotAdmitted('event belongs to another reservation, generation or native')
    return episode


def _session_log(config):
    found = sorted(Path(config['muse_data_directory']).glob(
        'sessions/*/*/*/%s/session.jsonl' % config['native_session_id']))
    if len(found) != 1:
        raise NotAdmitted('native session log not found exactly once')
    return [json.loads(line) for line in found[0].read_text().splitlines() if line.strip()]


def _last_run_terminal(records):
    runs = [r['payload'].get('event', {}) for r in records
            if r.get('payload_type') == 'runtime.session' and r.get('payload', {}).get('kind') == 'run']
    if not runs or runs[-1].get('kind') != 'terminal':
        return None
    return runs[-1].get('terminal')


def _client_route(records):
    routes = [r['payload'].get('record', {}) for r in records
              if r.get('payload_type') == 'runtime.session.route_facts']
    return routes[-1] if routes else {}


def _state_path(config):
    state = Path(config['state_directory'])
    state.mkdir(parents=True, exist_ok=True)
    return state / 'recovery-attempts.json'


def _locked_attempts(config, update=None):
    path = _state_path(config)
    with path.with_suffix('.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        try:
            try:
                attempts = json.loads(path.read_text())
            except (OSError, ValueError):
                attempts = {}
            if update is not None:
                update(attempts)
                temp = path.with_suffix('.tmp')
                temp.write_text(json.dumps(attempts, sort_keys=True))
                temp.replace(path)
            return attempts
        finally:
            fcntl.flock(lock, fcntl.LOCK_UN)


def record_attempt(event, config, stage):
    """Durably spend this episode's single attempt (or advance its stage)."""
    key = handling.episode_key(event)
    def update(attempts):
        attempts[key] = {'event_id': event['event_id'], 'stage': stage, 'at': int(time.time())}
    _locked_attempts(config, update)
    return key


def admit(event, config, paused=False, completed=False, live_operation=False, pending_decision=False):
    """Return the admitted continuation or raise NotAdmitted with the reason."""
    episode = _event_identity(event, config)
    records = _session_log(config)
    terminal = _last_run_terminal(records) == 'failed'
    spent = set(_locked_attempts(config))
    action, reason = handling.decide(event, terminal=terminal, seen=spent, paused=paused,
                                     completed=completed, live_operation=live_operation,
                                     pending_decision=pending_decision)
    if action != 'continue':
        raise NotAdmitted('%s: %s' % (action, reason))
    return {'action': action, 'episode': episode, 'key': handling.episode_key(event),
            'client': _client_route(records)}


def _process_args(pid):
    """Command line of a live process; None once it exited (zombies included)."""
    result = subprocess.run(['ps', '-o', 'stat=,args=', '-p', str(pid)], capture_output=True, text=True)
    if result.returncode != 0:
        return None
    stat, _, args = result.stdout.strip().partition(' ')
    return None if stat.startswith('Z') else args.strip()


def terminate_failed_client(config, client_marker):
    """SIGTERM the session's own recorded client; refuse any other process.

    The pid comes from the native log's route facts. It must still run the
    configured Muse client in this workspace, so a reused pid or a bystander
    is never signalled. No SIGKILL: a client that ignores SIGTERM stays running
    and the recovery stops with that fact.
    """
    route = _client_route(_session_log(config))
    pid = route.get('pid')
    if not isinstance(pid, int) or pid <= 1:
        raise NotAdmitted('session route facts carry no client pid')
    args = _process_args(pid)
    if args is None:
        return {'pid': pid, 'state': 'already-exited'}
    if client_marker not in args or config['workspace'] not in args:
        raise NotAdmitted('pid %d is not the failed client of this session' % pid)
    os.kill(pid, signal.SIGTERM)
    deadline = time.time() + config.get('terminate_grace_seconds', 30)
    while time.time() < deadline:
        if _process_args(pid) is None:
            return {'pid': pid, 'state': 'terminated'}
        time.sleep(0.2)
    raise NotAdmitted('failed client %d did not exit after SIGTERM' % pid)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--event-json', type=Path, required=True)
    parser.add_argument('--paused', action='store_true')
    parser.add_argument('--completed', action='store_true')
    parser.add_argument('--live-operation', action='store_true')
    parser.add_argument('--pending-decision', action='store_true')
    args = parser.parse_args(argv)
    try:
        result = admit(json.loads(args.event_json.read_text()), json.loads(args.config.read_text()),
                       paused=args.paused, completed=args.completed,
                       live_operation=args.live_operation, pending_decision=args.pending_decision)
    except (NotAdmitted, OSError, ValueError, KeyError) as error:
        print(json.dumps({'admitted': False, 'reason': str(error)}))
        return 3
    print(json.dumps(dict(result, admitted=True)))
    return 0


if __name__ == '__main__':
    sys.exit(main())
