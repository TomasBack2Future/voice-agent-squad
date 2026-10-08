#!/usr/bin/env python3
"""Muse PostLLMCall(status=failed) hook adapter. Observation only.

Validates the exact ledger-bound Worker native, reservation generation and
controller recipient; rejects unrelated, reminder or subagent sessions.
Publishes a sanitized runtime-failure observation through the existing
`stuck` + `terminal-events publish --kind runtime-failure` path (pointers
and closed enums only: no prompt, body or credentials; switches to #88's
atomic submission API when it merges). Dedupes by
reservation/generation/native plus continuous episode. Retries publication
a bounded number of times; a failed publication stays pending on disk,
never falsely delivered.

The hook never waits for its own turn to end (D84-2 ordering): it publishes
the observation and returns quickly. StopFailure stays supplemental until
exact-version proof exists, so it is never admitted here.

Episode lifecycle (D84-4): a failure episode closes on verified healthy
progress (a successful PostLLMCall or model/tool progress on the same
native). A later independent failure opens a NEW episode with its own
bounded budget. Within one continuous outage, repeats still dedupe. The
open marker is set only after publication is durably confirmed; a failed
publication stays pending and is replayed on the next hook invocation.
"""
from __future__ import annotations
import argparse
import fcntl
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

ERROR_CLASSES = ('exhausted', 'connection', 'auth', 'quota', 'config', 'unknown')
MAX_ATTEMPTS = 3
CALL_TIMEOUT = 10

_PATTERNS = [
    ('auth', re.compile(r'api[ _-]?key|unauthori|rejected|invalid.?token|forbidden', re.I)),
    ('quota', re.compile(r'quota|rate.?limit|too many requests|429', re.I)),
    ('exhausted', re.compile(r'after \d+ provider attempts|exhaust', re.I)),
    ('config', re.compile(r'unknown model|catalog|not found|invalid.?model|misconfig', re.I)),
    ('connection', re.compile(r'connection|refused|reset|timeout|timed out|network|unreachable|5\d\d|econn|socket', re.I)),
]


def classify_error(error: str) -> str:
    text = error or ''
    for name, pattern in _PATTERNS:
        if pattern.search(text):
            return name
    return 'unknown'


def admits(event: dict, config: dict) -> bool:
    """True only for the bound native's failed PostLLMCall turn.

    Rejects unrelated/reminder sessions (wrong session id), child agent
    sessions (agent_id/agent_type present), non-failed turns, and every
    other hook event including StopFailure (unqualified: never observed).
    """
    if event.get('hook_event_name') != 'PostLLMCall':
        return False
    if event.get('status') != 'failed':
        return False
    if event.get('session_id') != config.get('native_session_id'):
        return False
    if event.get('agent_id') or event.get('agent_type'):
        return False
    return True


def observe(event: dict, config: dict) -> dict:
    """Build the sanitized observation. Raw error text never leaves."""
    return {
        'reservation': config['reservation'],
        'generation': config['generation'],
        'native_session_id': config['native_session_id'],
        'controller_agent_id': config['controller_agent_id'],
        'turn_id': str(event.get('turn_id', ''))[:128],
        'request_id': str(event.get('request_id', ''))[:128],
        'attempt': int(event.get('attempt', 0) or 0),
        'provider': str(event.get('provider', ''))[:64],
        'error_class': classify_error(str(event.get('error', ''))),
        'observed_at': int(time.time()),
    }


def _snapshot_path(config):
    state = Path(config['state_directory'])
    state.mkdir(parents=True, exist_ok=True)
    return state / 'failure-episodes.json'


def _snapshots(config):
    path = _snapshot_path(config)
    if not path.exists():
        return {}
    try:
        data = json.loads(path.read_text())
    except (OSError, ValueError):
        return {}
    return data if isinstance(data, dict) else {}


def _save(config, data):
    path = _snapshot_path(config)
    temp = path.with_suffix('.tmp')
    temp.write_text(json.dumps(data, sort_keys=True))
    temp.replace(path)


def episode_key(observation, config):
    return '%s|%d|%s' % (config['reservation'], config['generation'],
                         config['native_session_id'])


def _locked(config, fn):
    path = _snapshot_path(config)
    with path.with_suffix('.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        try:
            return fn()
        finally:
            fcntl.flock(lock, fcntl.LOCK_UN)


def _current_episode(data, key):
    seen = data.get(key, {})
    if not seen.get('episode_open') and not seen.get('pending'):
        return None
    return seen


def duplicate(observation, config) -> bool:
    """Dedupe within one continuous outage; new episodes re-admit.

    The first observation of a continuous episode is admitted; repeats
    (same or later turns/requests of the unchanged open episode) are
    duplicates. A closed episode (healthy progress observed) admits its
    next failure as a new episode. A pending episode (publication never
    confirmed) is NOT a duplicate: the caller must replay it.
    One lock-guarded read keeps concurrent hook firings consistent; the
    open marker itself is written only after confirmed publication.
    """
    def check():
        data = _snapshots(config)
        seen = _current_episode(data, episode_key(observation, config))
        if seen is None:
            return False
        if seen.get('pending'):
            return False
        return True
    return _locked(config, check)


def note_progress(event, config) -> bool:
    """Close the episode on verified healthy progress of the same native.

    Returns True when an open episode was closed. Progress on another
    native, a subagent session, or a non-PostLLMCall event never closes.
    """
    if event.get('hook_event_name') != 'PostLLMCall':
        return False
    if event.get('status') == 'failed':
        return False
    if event.get('session_id') != config.get('native_session_id'):
        return False
    if event.get('agent_id') or event.get('agent_type'):
        return False
    def close():
        data = _snapshots(config)
        key = episode_key(None, config)
        seen = data.get(key)
        if not seen or not seen.get('episode_open'):
            return False
        seen['episode_open'] = False
        seen['closed_at'] = int(time.time())
        seen['closed_by_turn'] = str(event.get('turn_id', ''))[:128]
        data[key] = seen
        _save(config, data)
        return True
    return _locked(config, close)


def _mark_open(config, observation, episode_id):
    def mark():
        data = _snapshots(config)
        key = episode_key(observation, config)
        seen = data.get(key, {})
        data[key] = {'episode_open': True, 'pending': False,
                     'episode_id': episode_id,
                     'turns': seen.get('turns', []) + [observation['turn_id'] + '|' + observation['request_id']],
                     'first_observed_at': seen.get('first_observed_at', observation['observed_at']),
                     'sequence': seen.get('sequence', 0) + (0 if seen.get('episode_open') else 1)}
        _save(config, data)
        return data[key]
    return _locked(config, mark)


def _mark_pending(config, observation):
    def mark():
        data = _snapshots(config)
        key = episode_key(observation, config)
        seen = data.get(key, {})
        data[key] = {'episode_open': False, 'pending': True,
                     'turns': seen.get('turns', []),
                     'first_observed_at': seen.get('first_observed_at', observation['observed_at']),
                     'sequence': seen.get('sequence', 0)}
        _save(config, data)
    _locked(config, mark)


def _episode_id(config, observation):
    data = _snapshots(config)
    seen = data.get(episode_key(observation, config), {})
    return 'ep-%d' % (seen.get('sequence', 0) + 1)


def _call(config, env, argv):
    result = subprocess.run([config['squad_executable'], *argv],
                            cwd=config['ledger_directory'], env=env,
                            capture_output=True, text=True, timeout=CALL_TIMEOUT)
    if result.returncode:
        raise subprocess.CalledProcessError(result.returncode, argv, result.stdout, result.stderr)
    return result.stdout


def _submit(body, config, env, observation):
    """Existing-path submission: stuck, resolve id via history, publish.

    `stuck` prints no message id, so the adapter re-reads the canonical
    item history and matches its own exact body. The body embeds the
    episode id plus closed-enum fields, which makes the match exact and
    lets retries find the already-posted row instead of duplicating it.
    Replaced by #88's atomic submission API when it merges.
    """
    item = config['item']
    try:
        _call(config, env, ['stuck', '--to', item, body])
    except subprocess.CalledProcessError as error:
        raise error
    out = _call(config, env, ['history', item])
    outcome = None
    for line in out.splitlines():
        match = re.match(r'\s*#(\d+)\s+\[', line)
        if match and line.rstrip().endswith(body):
            outcome = int(match.group(1))
    if outcome is None:
        raise ValueError('posted observation not found in item history')
    argv = ['terminal-events', 'publish', '--reservation', config['reservation'],
           '--generation', str(config['generation']),
           '--worker-session', config['native_session_id'],
           '--kind', 'runtime-failure', '--outcome', str(outcome)]
    if config.get('expected_decision'):
        argv += ['--expected-decision', str(config['expected_decision'])]
    return json.loads(_call(config, env, argv))


def publish(event, config, attempts=MAX_ATTEMPTS):
    observation = observe(event, config)
    if duplicate(observation, config):
        return {'state': 'duplicate'}
    episode_id = _episode_id(config, observation)
    observation['episode_id'] = episode_id
    env = {k: v for k, v in os.environ.items()
           if k not in ('CODEX_THREAD_ID', 'CODEX_SESSION_ID', 'CLAUDE_SESSION_ID',
                        'MUSE_SESSION_ID', 'SQUAD_NATIVE_SESSION_ID', 'SQUAD_SESSION_ID', 'SQUAD_AGENT')}
    env.update(SQUAD_AGENT=config['agent_id'],
               SQUAD_NATIVE_SESSION_ID=config['native_session_id'],
               SQUAD_SESSION_ID='muse:' + config['native_session_id'],
               SQUAD_NO_AUTO_DAEMON='1', SQUAD_NO_BROWSER='1', SQUAD_NO_HYGIENE='1')
    body = 'runtime-failure %s %s turn=%s request=%s attempt=%d provider=%s' % (
        episode_id, observation['error_class'], observation['turn_id'],
        observation['request_id'], observation['attempt'], observation['provider'])
    last = None
    for _ in range(max(1, attempts)):
        try:
            receipt = _submit(body, config, env, observation)
        except (OSError, ValueError, subprocess.SubprocessError) as error:
            last = error
            continue
        _mark_open(config, observation, episode_id)
        receipt.setdefault('episode_id', episode_id)
        return receipt
    _mark_pending(config, observation)
    state = Path(config['state_directory'])
    state.mkdir(parents=True, exist_ok=True)
    pending_path = state / 'pending.json'
    try:
        pending = json.loads(pending_path.read_text())
    except (OSError, ValueError):
        pending = []
    pending.append(observation)
    temp = pending_path.with_suffix('.tmp')
    temp.write_text(json.dumps(pending))
    temp.replace(pending_path)
    raise last


def run(config_path: Path, event: dict) -> int:
    config = json.loads(config_path.read_text())
    if not admits(event, config):
        # Non-failed PostLLMCall of the bound native is verified healthy
        # progress: close the episode so the next independent failure
        # re-publishes. Unrelated sessions never close.
        note_progress(event, config)
        return 0
    try:
        publish(event, config)
    except (OSError, ValueError, subprocess.SubprocessError):
        # Publication stays pending on disk; the hook itself must not fail
        # the turn or wait. Exit 0 keeps observation-only semantics.
        return 0
    return 0


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--event')
    args = parser.parse_args(argv)
    try:
        if args.event is not None:
            event = json.loads(args.event)
        else:
            event = json.load(sys.stdin)
        return run(args.config, event)
    except (OSError, ValueError, KeyError):
        return 0


if __name__ == '__main__':
    sys.exit(main())
