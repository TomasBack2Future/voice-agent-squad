#!/usr/bin/env python3
"""Muse PostLLMCall(status=failed) hook adapter. Observation only.

Validates the exact ledger-bound Worker native, reservation generation and
controller recipient; rejects unrelated, reminder or subagent sessions.
Publishes a sanitized runtime-failure observation through #88's atomic
`terminal-events submit` (pointers and closed enums only: no prompt, body
or credentials). Each failure episode maps to Submit's stable request key.
Retries publication a bounded number of times; a failed publication stays
pending on disk, never falsely delivered.

The hook never waits for its own turn to end (D84-2 ordering): it publishes
the observation and returns quickly. StopFailure stays supplemental until
exact-version proof exists, so it is never admitted here.

Episode lifecycle (D84-4): only native health signals allocate episodes.
The first failure with no open outage allocates the next ep-N and freezes
that first observation; repeats during the outage are deduped; verified
healthy progress (a successful PostLLMCall on the same native) ends it.
Delivery is a separate in-order outbox of frozen episodes, each replayed
with its own request key and immutable body until Submit confirms it.
Transport recovery or replay success never opens or closes an episode.
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
#: PostLLMCall statuses that are a failed model call. Muse ends a call whose
#: stream never produced a first event as `timed_out`, not `failed` (#84,
#: live 1372 capture), so both open or continue an outage.
FAILURE_STATUSES = ('failed', 'timed_out')
#: The only status that is verified healthy progress. `cancelled` and any
#: other value are neither failure nor health: they are audited and change
#: no episode.
HEALTHY_STATUS = 'success'
MAX_ATTEMPTS = 3
CALL_TIMEOUT = 10

_PATTERNS = [
    ('auth', re.compile(r'api[ _-]?key|unauthori|rejected|invalid.?token|forbidden', re.I)),
    ('quota', re.compile(r'quota|rate.?limit|too many requests|429', re.I)),
    ('exhausted', re.compile(r'after \d+ provider attempts|exhaust', re.I)),
    ('config', re.compile(r'unknown model|catalog|not found|invalid.?model|misconfig', re.I)),
    ('connection', re.compile(r'connection|refused|reset|timeout|timed out|no first model event|network|unreachable|5\d\d|econn|socket', re.I)),
]


def classify_error(error: str) -> str:
    text = error or ''
    for name, pattern in _PATTERNS:
        if pattern.search(text):
            return name
    return 'unknown'


def _bound_call(event: dict, config: dict) -> bool:
    """The bound native's own PostLLMCall: no unrelated, reminder or subagent session."""
    return (event.get('hook_event_name') == 'PostLLMCall'
            and event.get('session_id') == config.get('native_session_id')
            and not event.get('agent_id') and not event.get('agent_type'))


def admits(event: dict, config: dict) -> bool:
    """True only for the bound native's failed or timed-out PostLLMCall.

    Rejects unrelated/reminder sessions (wrong session id), child agent
    sessions (agent_id/agent_type present), every non-failure status, and
    every other hook event including StopFailure (unqualified: never observed).
    """
    return _bound_call(event, config) and event.get('status') in FAILURE_STATUSES


_SAFE_ID = re.compile(r'[A-Za-z0-9_.-]{1,128}\Z')
_SAFE_PROVIDER = re.compile(r'[A-Za-z0-9_.-]{1,64}\Z')


class UnsafeIdentifier(ValueError):
    """An admitted failure whose identifiers cannot form a valid body."""


#: Sentinel for legitimately absent fields (D84-9). Every captured real
#: Muse 1.4.3 failed PostLLMCall has no request_id and no provider; that
#: absence is normal payload shape, not a rejection. The token is fixed,
#: documented, and inside the closed Go body alphabet.
UNKNOWN_SENTINEL = 'unknown'


def _safe(value, pattern, name):
    text = str(value or '')
    if not pattern.fullmatch(text):
        raise UnsafeIdentifier('%s %r is missing or outside the closed body alphabet' % (name, text))
    return text


def _safe_or_unknown(value, pattern, name):
    if value is None or str(value) == '':
        return UNKNOWN_SENTINEL
    text = str(value)
    if not pattern.fullmatch(text):
        raise UnsafeIdentifier('%s %r is present but outside the closed body alphabet' % (name, text))
    return text


#: Compound request identity (D84-10). Real Muse 1.4.4 emits request_id
#: as <uuid>:<n>:<m>; colons are outside the closed Go body alphabet.
_COMPOUND_ID = re.compile(r'\A([A-Za-z0-9_.-]{1,64}):([0-9]{1,10}):([0-9]{1,10})\Z')


def _normalize_compound(value, pattern, name):
    """Normalize a legitimate compound ID into the closed alphabet.

    Mapping (documented, deterministic, bounded): <a>:<n>:<m> becomes
    <a>.<n>.<m>, then the result must satisfy the same closed pattern
    and length bound as a plain value. Anything that is not exactly the
    documented compound shape raises UnsafeIdentifier: no silent rewrite
    of arbitrary unsafe text.
    """
    match = _COMPOUND_ID.fullmatch(str(value))
    if match is None:
        raise UnsafeIdentifier('%s %r is present but outside the closed body alphabet' % (name, value))
    normalized = '%s.%s.%s' % match.groups()
    if not pattern.fullmatch(normalized):
        raise UnsafeIdentifier('%s %r normalizes outside the closed body alphabet' % (name, value))
    return normalized


def _error_class(event):
    found = classify_error(str(event.get('error', '')))
    if found == 'unknown' and event.get('status') == 'timed_out':
        return 'connection'
    return found


def observe(event: dict, config: dict) -> dict:
    """Build the sanitized observation. Raw error text never leaves.

    D84-9 (correcting D84-7(c)): turn_id is always present in real
    captures, so it stays strict. request_id/provider are legitimately
    absent from every real 1.4.3 failure capture: absent maps to the
    fixed UNKNOWN_SENTINEL. D84-10: real 1.4.4 emits compound
    <uuid>:<n>:<m> request IDs, deterministically normalized into the
    closed alphabet. A value that is present but neither closed nor a
    documented compound still raises UnsafeIdentifier: the caller
    records the precise rejection and never silently drops it.
    """
    request_id = event.get('request_id')
    if request_id is not None and str(request_id) != '' and ':' in str(request_id):
        request_id = _normalize_compound(request_id, _SAFE_ID, 'request_id')
    else:
        request_id = _safe_or_unknown(request_id, _SAFE_ID, 'request_id')
    return {
        'reservation': config['reservation'],
        'generation': config['generation'],
        'native_session_id': config['native_session_id'],
        'controller_agent_id': config['controller_agent_id'],
        'turn_id': _safe(event.get('turn_id'), _SAFE_ID, 'turn_id'),
        'request_id': request_id,
        'attempt': int(event.get('attempt', 0) or 0),
        'provider': _safe_or_unknown(event.get('provider'), _SAFE_PROVIDER, 'provider'),
        'error_class': _error_class(event),
        'observed_at': int(time.time()),
    }


def _record_rejection(config, reason):
    """Persist a precise local rejection; the failure is never silent."""
    state = Path(config['state_directory'])
    state.mkdir(parents=True, exist_ok=True)
    path = state / 'rejected.json'
    try:
        rejected = json.loads(path.read_text())
    except (OSError, ValueError):
        rejected = []
    if not isinstance(rejected, list):
        rejected = []
    rejected.append({'at': int(time.time()), 'reason': reason})
    temp = path.with_suffix('.tmp')
    temp.write_text(json.dumps(rejected))
    temp.replace(path)


_STATUS_TOKEN = re.compile(r'[a-z_-]{1,32}\Z')


def _record_ignored(config, event):
    """Audit a bound PostLLMCall that is neither failure nor health.

    Only the closed status token (or `invalid`) and turn id are kept,
    bounded to the most recent entries; no payload field leaves.
    """
    state = Path(config['state_directory'])
    state.mkdir(parents=True, exist_ok=True)
    path = state / 'ignored.json'
    try:
        ignored = json.loads(path.read_text())
    except (OSError, ValueError):
        ignored = []
    if not isinstance(ignored, list):
        ignored = []
    status = str(event.get('status'))
    turn = str(event.get('turn_id') or '')
    ignored.append({'at': int(time.time()),
                    'status': status if _STATUS_TOKEN.fullmatch(status) else 'invalid',
                    'turn_id': turn if _SAFE_ID.fullmatch(turn) else 'invalid'})
    temp = path.with_suffix('.tmp')
    temp.write_text(json.dumps(ignored[-50:]))
    temp.replace(path)


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


def _row(data, key):
    row = data.get(key)
    if not isinstance(row, dict):
        row = {}
    outbox = row.get('outbox')
    row['outbox'] = [entry for entry in outbox if isinstance(entry, dict)] if isinstance(outbox, list) else []
    row.setdefault('sequence', 0)
    row.setdefault('episode_open', False)
    return row


def duplicate(observation, config) -> bool:
    """True when the observation belongs to the current continuous outage.

    Only verified healthy progress ends an outage, so any failure while
    one is open is a repeat; delivery state never affects the answer.
    """
    def check():
        return _row(_snapshots(config), episode_key(observation, config))['episode_open']
    return _locked(config, check)


def _compose_body(episode_id, observation):
    return 'runtime-failure %s %s turn=%s request=%s attempt=%d provider=%s' % (
        episode_id, observation['error_class'], observation['turn_id'],
        observation['request_id'], observation['attempt'], observation['provider'])


def _drain_outbox(config, env, key, attempts):
    """Deliver frozen episodes in allocation order, each under its own key.

    Each entry is resubmitted with its exact frozen body and request
    key, so a lost reply or a crash after commit replays idempotently.
    An entry leaves the outbox only after Submit confirms it. The first
    undeliverable entry stops the drain and keeps its successors, so a
    later hook retries in order. Delivery never touches the episode
    allocation state. Returns (last receipt or None, last error or None).
    """
    receipt, last = None, None
    while True:
        data = _snapshots(config)
        row = _row(data, key)
        if not row['outbox']:
            return receipt, None
        entry = row['outbox'][0]
        body = _compose_body(entry['episode_id'], entry)
        delivered = None
        for _ in range(max(1, attempts)):
            try:
                delivered = _submit(body, config, env, entry)
            except (OSError, ValueError, subprocess.SubprocessError) as error:
                last = error
                continue
            break
        if delivered is None:
            return receipt, last
        row['outbox'] = [other for other in row['outbox']
                         if other.get('episode_id') != entry['episode_id']]
        data[key] = row
        _save(config, data)
        receipt = dict(delivered)
        receipt.setdefault('episode_id', entry['episode_id'])


def note_progress(event, config, env=None, attempts=1) -> bool:
    """End the current outage on verified healthy progress of the same native.

    Returns True when an open outage was ended. Progress on another
    native, a subagent session, a failed call or a non-PostLLMCall event
    never ends one. The boundary is recorded before any delivery, so a
    failed flush cannot merge the next outage into this one. With an
    env, undelivered episodes then get a bounded flush.
    """
    if not _bound_call(event, config) or event.get('status') != HEALTHY_STATUS:
        return False
    def close():
        data = _snapshots(config)
        key = episode_key(None, config)
        row = _row(data, key)
        closed = row['episode_open']
        if closed:
            row['episode_open'] = False
            row['closed_by_turn'] = str(event.get('turn_id', ''))[:128]
            data[key] = row
            _save(config, data)
        if env is not None:
            _drain_outbox(config, env, key, attempts)
        return closed
    return _locked(config, close)


def _call(config, env, argv):
    result = subprocess.run([config['squad_executable'], *argv],
                            cwd=config['ledger_directory'], env=env,
                            capture_output=True, text=True, timeout=CALL_TIMEOUT)
    if result.returncode:
        raise subprocess.CalledProcessError(result.returncode, argv, result.stdout, result.stderr)
    return result.stdout


def _live_revision(config, env):
    """Read the current decision revision; None when no decision exists."""
    out = _call(config, env, ['terminal-events', 'decision-get',
                              '--reservation', config['reservation'],
                              '--generation', str(config['generation']),
                              '--worker-session', config['native_session_id']])
    try:
        data = json.loads(out)
    except ValueError:
        return None
    revision = data.get('revision')
    return revision if isinstance(revision, int) else None


def _submit(body, config, env, observation):
    """Atomic submission through #88's common Submit (D84-5).

    One `terminal-events submit` call stores the sanitized message and
    the durable event in a single transaction and returns both IDs.
    The failure episode maps to Submit's stable request key: retries
    within one outage reuse the key and deduplicate to the same IDs,
    while a new independent outage uses a new key and records again.
    The live decision revision is read fresh for every submit so the
    fence never fails on a stale static value.
    """
    argv = ['terminal-events', 'submit', '--reservation', config['reservation'],
           '--generation', str(config['generation']),
           '--worker-session', config['native_session_id'],
           '--kind', 'runtime-failure', '--body', body,
           '--request-key', observation['episode_id']]
    revision = _live_revision(config, env)
    if revision is not None:
        argv += ['--expected-decision', str(revision)]
    return json.loads(_call(config, env, argv))


def publish(event, config, attempts=MAX_ATTEMPTS):
    """Record the failure and deliver undelivered episodes under one lock.

    Episode allocation depends only on native health: a failure with no
    open outage allocates the next ep-N and freezes this first
    observation in the outbox, atomically with marking the outage open;
    a failure during an open outage is a repeat and freezes nothing.
    Delivery then drains the outbox in order. Holding the lock across
    the bounded squad calls serializes overlapping hook runs.

    Returns the last confirmed Submit receipt, or {'state': 'duplicate'}
    when nothing new was allocated or delivered. Raises the last
    transport error when an entry stays undelivered; it remains in the
    outbox for the next hook.
    """
    observation = observe(event, config)
    def record():
        data = _snapshots(config)
        key = episode_key(observation, config)
        row = _row(data, key)
        if not row['episode_open']:
            row['sequence'] += 1
            episode_id = 'ep-%d' % row['sequence']
            row['episode_id'] = episode_id
            row['episode_open'] = True
            row['outbox'].append(dict(observation, episode_id=episode_id))
            data[key] = row
            _save(config, data)
        receipt, last = _drain_outbox(config, _hook_env(config), key, attempts)
        if last is not None:
            raise last
        return receipt if receipt is not None else {'state': 'duplicate'}
    return _locked(config, record)


def _hook_env(config):
    env = {k: v for k, v in os.environ.items()
           if k not in ('CODEX_THREAD_ID', 'CODEX_SESSION_ID', 'CLAUDE_SESSION_ID',
                        'MUSE_SESSION_ID', 'SQUAD_NATIVE_SESSION_ID', 'SQUAD_SESSION_ID', 'SQUAD_AGENT')}
    env.update(SQUAD_AGENT=config['agent_id'],
               SQUAD_NATIVE_SESSION_ID=config['native_session_id'],
               SQUAD_SESSION_ID='muse:' + config['native_session_id'],
               SQUAD_NO_AUTO_DAEMON='1', SQUAD_NO_BROWSER='1', SQUAD_NO_HYGIENE='1')
    return env


def run(config_path: Path, event: dict) -> int:
    config = json.loads(config_path.read_text())
    if not admits(event, config):
        # Only an explicit success of the bound native is verified healthy
        # progress: end the open outage, then flush undelivered episodes,
        # so the next independent failure publishes with a fresh budget.
        # Unrelated sessions never close. Bounded to one flush attempt.
        # Any other status of the bound native is audited, never health.
        try:
            if _bound_call(event, config) and event.get('status') != HEALTHY_STATUS:
                _record_ignored(config, event)
            else:
                note_progress(event, config, _hook_env(config))
        except (OSError, ValueError, subprocess.SubprocessError):
            pass
        return 0
    try:
        publish(event, config)
    except UnsafeIdentifier as error:
        # D84-7(c)/D84-9: precise local rejection, durable and inspectable.
        # Only present-but-unsafe identifiers reach here; legitimately
        # absent request_id/provider map to the sentinel and publish.
        # Nothing is submitted, nothing is pending, and the hook reports
        # the rejection on stderr instead of silently swallowing it.
        _record_rejection(config, str(error))
        print('muse-failure-hook rejected: %s' % error, file=sys.stderr)
        return 2
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
