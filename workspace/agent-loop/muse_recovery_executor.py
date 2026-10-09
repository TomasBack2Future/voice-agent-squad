#!/usr/bin/env python3
"""Same-native recovery after a Muse runtime-failure event (#84, option B).

Admission fails closed before any return path: the delivered event must name
this Worker's reservation/generation/native, the native session log must show
the failed turn as its last run record, `muse_failure_handling.decide` must
return `continue`, and each failure episode gets at most one attempt,
recorded durably here before anything changes. Termination targets only the
client recorded in the session's own route facts and never escalates past
SIGTERM.

The continuation is the MSP stopped-session interface on the same native:
`muse serve` with the original model, max effort, allowAll and the Worker's
hooks, `session/resume`, then `goal/resume` (or one `turn/start` when the
session has no goal). Reservation, generation, claim and decision are
rechecked immediately before the turn starts. The Dispatcher runs this
visibly in the Worker's cmux workspace. There is no composer input, and this
module writes nothing to the ledger. Parity with the TUI --yolo posture is
deliberate: a claim lost mid-turn is not fenced (see README).
"""
from __future__ import annotations
import argparse
import fcntl
import json
import os
from pathlib import Path
import queue
import re
import shutil
import signal
import sqlite3
import subprocess
import sys
import tempfile
import time

import muse_failure_handling as handling
import muse_session_host as msp

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


def _session_directory(config):
    found = sorted(Path(config['muse_data_directory']).glob('sessions/*/*/*/%s' % config['native_session_id']))
    if len(found) != 1 or not (found[0] / 'session.jsonl').is_file():
        raise NotAdmitted('native session log not found exactly once')
    return found[0]


def _session_log(config):
    path = _session_directory(config) / 'session.jsonl'
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]


#: The native goals.db status of a finished goal (Muse 1.4.4).
FINISHED_GOAL = 'complete'


def goal_status(config):
    """Durable status of the native's goal, or None when it has none.

    The native schema keys goals.db by session_id, so a session has at most
    one goal row.
    """
    path = _session_directory(config) / 'goals.db'
    if not path.exists():
        return None
    with sqlite3.connect('file:%s?mode=ro' % path, uri=True) as db:
        row = db.execute('SELECT status FROM goals WHERE session_id=?',
                         (config['native_session_id'],)).fetchone()
    return row[0] if row else None


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


def spend_attempt(event, config):
    """Compare-and-set the episode's single attempt; a concurrent or repeat run loses."""
    key = handling.episode_key(event)
    won = []
    def update(attempts):
        if key not in attempts:
            attempts[key] = {'event_id': event['event_id'], 'stage': 'admitted', 'at': int(time.time())}
            won.append(key)
    _locked_attempts(config, update)
    if not won:
        raise NotAdmitted('continuation budget for %s already spent' % key)


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


def terminate_failed_client(config, client_marker, expected_pid=None):
    """SIGTERM the session's own recorded client; refuse any other process.

    The session log is re-read immediately before the signal: its last run
    record must still be the failed terminal, and its route-facts pid must
    still be the one admission saw, so a client that started a newer run or
    was replaced meanwhile is never signalled. The pid must also still run
    the configured Muse client in this workspace, so a reused pid or a
    bystander is never signalled. No SIGKILL: a client that ignores SIGTERM
    stays running and the recovery stops with that fact.
    """
    records = _session_log(config)
    if _last_run_terminal(records) != 'failed':
        raise NotAdmitted('session changed since admission: last run is no longer the failed terminal')
    pid = _client_route(records).get('pid')
    if expected_pid is not None and pid != expected_pid:
        raise NotAdmitted('session changed since admission: client pid %r is not the admitted %r' % (pid, expected_pid))
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


def _coordination(config, env, *args):
    try:
        result = subprocess.run([config['coordination_executable'], *args], cwd=config['ledger_directory'],
                                env=env, capture_output=True, text=True,
                                timeout=config.get('coordination_timeout_seconds', 30))
    except subprocess.TimeoutExpired:
        raise NotAdmitted('coordination read %s timed out' % args[0]) from None
    except (OSError, subprocess.SubprocessError):
        raise NotAdmitted('coordination read %s could not run' % args[0]) from None
    if result.returncode:
        raise NotAdmitted('coordination read %s failed' % args[0])
    return result.stdout


def check_custody(config, env):
    """The reservation, generation, exact claim and decision must still allow this Worker.

    Custody is the claim row itself (item, holder, `held`, claim generation)
    read through `claim-inspect`, never agent registration: a Muse Worker can
    hold its claim without being a registered agent.
    """
    rows = [r for r in json.loads(_coordination(config, env, 'dispatch', 'list', '--json', '--active'))
            if r.get('reservation_key') == config['reservation']]
    if [(r.get('generation'), r.get('state'), r.get('worker_thread_id'), r.get('reserved_by')) for r in rows] != [
            (config['generation'], 'dispatched', config['native_session_id'], config['controller_agent_id'])]:
        raise NotAdmitted('reservation, generation or native binding changed')
    claim = json.loads(_coordination(config, env, 'claim-inspect', config['item'])).get('env_claim') or {}
    if ((claim.get('item'), claim.get('holder'), claim.get('state'), claim.get('generation'))
            != (config['item'], config['agent_id'], 'held', config['claim_generation'])):
        raise NotAdmitted('Worker no longer holds its claim (exact item/holder/held/generation)')
    raw = _coordination(config, env, 'terminal-events', 'decision-get', '--reservation', config['reservation'],
                        '--generation', str(config['generation']),
                        '--worker-session', config['native_session_id'])
    decision = json.loads(raw) if raw.strip() else {}
    revision, action = decision.get('revision') or 0, decision.get('action') or ''
    # decision-get reports "no adopted decision" as revision 0 with an empty
    # action; any adopted revision must be exactly `proceed`.
    if (revision, action) == (0, ''):
        return 0
    if revision < 1 or action != 'proceed':
        raise NotAdmitted('current decision revision %s is %r, not proceed' % (revision, action))
    return revision


def _initialize(host, config):
    result = host.rpc('initialize', {'clientInfo': {'name': 'squad_muse_recovery', 'version': '1'}})
    if (result.get('serverInfo', {}).get('version') != config['expected_server_version']
            or result.get('schema') != {'version': 1, 'fingerprint': config['expected_schema_fingerprint']}):
        raise NotAdmitted('Muse MSP identity/schema is not the qualified one')
    host.rpc('initialized', {}, True)


def _resume(host, config):
    result = host.rpc('session/resume', {'commandId': msp.uuid7(), 'sessionId': config['native_session_id'],
                                         'excludeItems': True})
    session = result.get('session', {})
    if (session.get('sessionId') != config['native_session_id'] or session.get('modelId') != config['model']
            or session.get('providerId') != config['provider'] or session.get('activeTurnId') is not None
            or session.get('status') not in ('idle', 'notLoaded') or result.get('pendingRequests')
            or (result.get('lastTurn') or {}).get('terminal') != 'failed'):
        raise NotAdmitted('resumed session is not the idle failed native with its original model')


class Unsupported(NotAdmitted):
    """No admitted continuation exists for this goal state; recorded, nothing runs."""


def continuation_mode(config):
    """Map the native's durable goal state to its only admitted continuation.

    active or no goal -> one turn/start (no goal is created or touched);
    blocked by the failure -> goal/resume; user-paused or unknown -> stop;
    complete -> stop as a recorded unsupported gap.
    """
    status = goal_status(config)
    if status in (None, 'active'):
        return 'turn'
    if status == 'blocked':
        return 'goal'
    if status == FINISHED_GOAL:
        raise Unsupported('unsupported: the native goal is complete')
    raise NotAdmitted('goal is %s; it is never resumed by recovery' % status)


def _continue(host, config, mode):
    if mode == 'goal':
        started = host.rpc('goal/resume', {'commandId': msp.uuid7(), 'sessionId': config['native_session_id']})
    else:
        started = host.rpc('turn/start', {'commandId': msp.uuid7(), 'sessionId': config['native_session_id'],
                                          'reasoningEffort': config['reasoning_effort'],
                                          'input': [{'type': 'text', 'text': config['continuation_prompt']}]})
    if not started.get('turnId'):
        raise NotAdmitted('continuation was not admitted as a turn')
    return started['turnId']


def _await_quiet(host, first_turn, max_seconds, quiet_seconds):
    """Follow the continuation until no turn runs and none starts for quiet_seconds.

    A resumed goal may chain further turns. Closing the host while one runs
    would cut it off, so the wait ends only at a quiet idle point, or at
    max_seconds, which is reported rather than treated as success.
    """
    running, terminals = {first_turn}, []
    deadline = time.time() + max_seconds
    quiet_since = None
    while time.time() < deadline:
        if not running and quiet_since is not None and time.time() - quiet_since >= quiet_seconds:
            return terminals
        try:
            message = host.events.get(timeout=0.2)
        except queue.Empty:
            continue
        method, params = message.get('method'), message.get('params') or {}
        if method == 'host/exited':
            raise NotAdmitted('Muse host exited during the continuation')
        if method == 'turn/started':
            running.add(params.get('turnId'))
            quiet_since = None
        elif method == 'turn/completed':
            running.discard(params.get('turnId'))
            terminals.append(params.get('terminal'))
            if not running:
                quiet_since = time.time()
    raise NotAdmitted('continuation still running at max_seconds')


#: Top-level live Muse settings copied into the private layer. Anything else is dropped.
SETTINGS_ALLOWLIST = ('schema_version', 'provider', 'model', 'reasoning_effort', 'tui', 'model_catalog',
                      'permissions', 'endpoint_transport')
_CREDENTIAL_KEY = re.compile(r'api_?key|token|secret|passw|credential|authorization|cookie|private_key', re.I)


def _credential_keys(value, path=''):
    if isinstance(value, dict):
        for key, item in value.items():
            here = '%s.%s' % (path, key) if path else str(key)
            if _CREDENTIAL_KEY.search(str(key)):
                yield here
            yield from _credential_keys(item, here)
    elif isinstance(value, list):
        for index, item in enumerate(value):
            yield from _credential_keys(item, '%s[%d]' % (path, index))


def serve_config_home(config, tag):
    """Private XDG config home for one recovery host only.

    `muse serve` admits command hooks from the user settings layer, not from
    `-c hooks=` (1.4.4 qualification: handlers=0 vs 1). The private home mirrors
    every entry of the real config home by symlink, so other tools keep their
    configuration and Muse credentials are never copied. Only muse/settings.json
    is a private copy with the Worker's hooks added, published atomically
    (fsync then rename). Each call gets its own directory and never removes one,
    so overlapping recoveries cannot delete each other's live config. Live
    settings are not edited and nothing is written into the Worker's workspace.
    """
    real = Path(config.get('config_root') or os.environ.get('XDG_CONFIG_HOME') or Path.home() / '.config')
    home = Path(config['state_directory']) / 'serve-config-home' / ('%s-%d-%d' % (tag, os.getpid(), time.time_ns()))
    root = real.resolve() if real.is_dir() else None
    muse = real / 'muse'
    live = json.loads((muse / 'settings.json').read_text()) if (muse / 'settings.json').is_file() else {}
    leaked = sorted(_credential_keys(live))
    if leaked:
        raise NotAdmitted('live Muse settings carry credential-like keys %s; private layer refused' % leaked)
    settings = {key: live[key] for key in SETTINGS_ALLOWLIST if key in live}
    settings['hooks'] = config['hooks']
    (home / 'muse').mkdir(parents=True, exist_ok=False)

    def mirror(entry, link):
        # Only entries that really live inside the user config root are mirrored.
        if root is not None and entry.resolve().is_relative_to(root):
            link.symlink_to(entry)

    for entry in (real.iterdir() if root is not None else ()):
        if entry.name != 'muse':
            mirror(entry, home / entry.name)
    for entry in (muse.iterdir() if muse.is_dir() else ()):
        if entry.name != 'settings.json' and not entry.name.endswith('.lock'):
            mirror(entry, home / 'muse' / entry.name)
    descriptor, temp = tempfile.mkstemp(dir=home / 'muse', prefix='.settings.', suffix='.tmp')
    try:
        with os.fdopen(descriptor, 'w') as output:
            output.write(json.dumps(settings))
            output.flush()
            os.fsync(output.fileno())
        os.chmod(temp, 0o600)
        os.replace(temp, home / 'muse' / 'settings.json')
    except BaseException:
        Path(temp).unlink(missing_ok=True)
        raise
    return home


def recover(event, config, **flags):
    """Admit, spend the episode budget, stop the failed client, continue the same native."""
    env = msp.child_environment(config)
    admitted = admit(event, config, **flags)
    try:
        mode = continuation_mode(config)
    except Unsupported as gap:
        evidence = {'event_id': event['event_id'], 'episode': admitted['episode'], 'stopped': str(gap)}
        msp.atomic(Path(config['state_directory']) / ('recovery-%s.json' % admitted['episode']), evidence)
        return evidence
    check_custody(config, env)
    # Built and validated before anything is spent or signalled: credential-like
    # live settings refuse here with nothing changed.
    home = serve_config_home(config, admitted['episode'])
    try:
        spend_attempt(event, config)
    except NotAdmitted:
        shutil.rmtree(home, ignore_errors=True)
        raise
    evidence = {'event_id': event['event_id'], 'episode': admitted['episode'], 'config_home': str(home)}
    try:
        evidence['client'] = terminate_failed_client(config, config['client_marker'],
                                                     admitted['client'].get('pid'))
        record_attempt(event, config, 'client-stopped')
        state = Path(config['state_directory'])
        host = msp.Host(config, state, dict(env, XDG_CONFIG_HOME=str(home)))
        try:
            _initialize(host, config)
            _resume(host, config)
            host.rpc('session/setReasoningEffort', {'commandId': msp.uuid7(), 'sessionId': config['native_session_id'],
                                                    'reasoningEffort': config['reasoning_effort']})
            host.rpc('session/setApprovalMode', {'commandId': msp.uuid7(), 'sessionId': config['native_session_id'],
                                                 'mode': 'allowAll'})
            evidence['decision_revision'] = check_custody(config, env)
            evidence['mode'] = mode
            evidence['turn_id'] = _continue(host, config, mode)
            record_attempt(event, config, 'turn-started')
            evidence['turn_terminals'] = _await_quiet(host, evidence['turn_id'], config.get('max_seconds', 3600),
                                                      config.get('quiet_seconds', 15))
            evidence['terminal'] = evidence['turn_terminals'][-1]
            record_attempt(event, config, 'turn-' + str(evidence['terminal']))
        finally:
            host.close()
    except (NotAdmitted, ValueError, OSError, queue.Empty, subprocess.SubprocessError) as error:
        record_attempt(event, config, 'stopped')
        evidence['stopped'] = str(error) or type(error).__name__
    finally:
        # The host has exited (or never started); this config home is ours alone.
        shutil.rmtree(home, ignore_errors=True)
        evidence['config_home_removed'] = not home.exists()
        msp.atomic(Path(config['state_directory']) / ('recovery-%s.json' % admitted['episode']), evidence)
    return evidence


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--event-json', type=Path, required=True)
    parser.add_argument('--paused', action='store_true')
    parser.add_argument('--completed', action='store_true')
    parser.add_argument('--live-operation', action='store_true')
    parser.add_argument('--pending-decision', action='store_true')
    parser.add_argument('--check', action='store_true', help='admission only; change nothing')
    args = parser.parse_args(argv)
    flags = dict(paused=args.paused, completed=args.completed,
                 live_operation=args.live_operation, pending_decision=args.pending_decision)
    try:
        event, config = json.loads(args.event_json.read_text()), json.loads(args.config.read_text())
        if args.check:
            admitted = admit(event, config, **flags)
            mode = continuation_mode(config)
            revision = check_custody(config, msp.child_environment(config))
            print(json.dumps(dict(admitted, admitted=True, mode=mode, decision_revision=revision)))
            return 0
        evidence = recover(event, config, **flags)
    except (NotAdmitted, OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        print(json.dumps({'admitted': False, 'reason': str(error)}))
        return 3
    print(json.dumps(evidence))
    return 0 if evidence.get('terminal') == 'completed' else 4


if __name__ == '__main__':
    sys.exit(main())
