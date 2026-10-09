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
        'error_class': classify_error(str(event.get('error', ''))),
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


def _adopt_orphan_pending(config, data, key):
    """Adopt a frozen payload whose snapshot row was never written.

    D84-11 finding 2: the hook may die after _store_pending but before
    any snapshot write. The orphan in pending.json belongs to this
    assignment (reservation/generation/native must match) and its key
    was never advanced: synthesize the pending row from it so the next
    publish or healthy flush replays the EXACT frozen body instead of
    allocating a fresh episode over a possibly committed request key.
    Returns the adopted row, or None when there is nothing to adopt.
    Only closed or missing rows are adopted; an open or pending row
    always wins. Caller must hold the episode lock.
    """
    seen = data.get(key, {})
    if seen.get('episode_open') or seen.get('pending'):
        return None
    try:
        pending = json.loads((Path(config['state_directory']) / 'pending.json').read_text())
    except (OSError, ValueError):
        return None
    if isinstance(pending, list):
        entries = [e for e in pending if isinstance(e, dict) and e.get('episode_id')]
    elif isinstance(pending, dict):
        entries = [e for e in pending.values() if isinstance(e, dict) and e.get('episode_id')]
    else:
        return None
    queued = set(_queued_outage_ids(config, key))
    for entry in entries:
        if (entry.get('reservation') != config['reservation']
                or entry.get('generation') != config['generation']
                or entry.get('native_session_id') != config['native_session_id']):
            continue
        if seen.get('advanced_episode') == entry['episode_id']:
            continue
        if entry['episode_id'] in queued:
            # Queued outages are owned by the queue drain, not by orphan
            # adoption: adopting one would erase the queue, the boundary
            # and the advanced identity and swallow the live failure
            # (4c756a4 review finding 2).
            continue
        adopted = {'episode_open': False, 'pending': True,
                   'episode_id': entry['episode_id'],
                   'turns': seen.get('turns', []),
                   'first_observed_at': seen.get('first_observed_at', entry.get('observed_at', int(time.time()))),
                   'sequence': seen.get('sequence', 0)}
        data[key] = adopted
        _save(config, data)
        return adopted
    return None


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


def _compose_body(episode_id, observation):
    return 'runtime-failure %s %s turn=%s request=%s attempt=%d provider=%s' % (
        episode_id, observation['error_class'], observation['turn_id'],
        observation['request_id'], observation['attempt'], observation['provider'])


def _pending_observation(config, episode_id=None):
    """Return the stored pending observation for an episode, or None.

    The pending store is keyed by episode: at most one immutable payload
    per episode. Later failures in the same outage never overwrite it.
    """
    path = Path(config['state_directory']) / 'pending.json'
    try:
        pending = json.loads(path.read_text())
    except (OSError, ValueError):
        return None
    if isinstance(pending, list):
        if not pending:
            return None
        if episode_id is None:
            last = pending[-1]
            return last if isinstance(last, dict) else None
        for entry in pending:
            if isinstance(entry, dict) and entry.get('episode_id') == episode_id:
                return entry
        return None
    if isinstance(pending, dict):
        if episode_id is None:
            return None
        entry = pending.get(episode_id)
        return entry if isinstance(entry, dict) else None
    return None


def _store_pending(config, observation):
    """Freeze the episode payload immutably; never overwrite an existing one."""
    state = Path(config['state_directory'])
    state.mkdir(parents=True, exist_ok=True)
    path = state / 'pending.json'
    try:
        pending = json.loads(path.read_text())
    except (OSError, ValueError):
        pending = {}
    if isinstance(pending, list):
        merged = {}
        for entry in pending:
            if isinstance(entry, dict) and entry.get('episode_id') and entry['episode_id'] not in merged:
                merged[entry['episode_id']] = entry
        pending = merged
    if not isinstance(pending, dict):
        pending = {}
    if observation.get('episode_id') and observation['episode_id'] not in pending:
        pending[observation['episode_id']] = observation
    temp = path.with_suffix('.tmp')
    temp.write_text(json.dumps(pending))
    temp.replace(path)


def _drop_pending(config, observation):
    path = Path(config['state_directory']) / 'pending.json'
    try:
        pending = json.loads(path.read_text())
    except (OSError, ValueError):
        return
    if isinstance(pending, list):
        pending = [p for p in pending
                   if not (isinstance(p, dict) and p.get('episode_id') == observation.get('episode_id'))]
    elif isinstance(pending, dict):
        pending.pop(observation.get('episode_id', ''), None)
    else:
        return
    temp = path.with_suffix('.tmp')
    temp.write_text(json.dumps(pending))
    temp.replace(path)


def note_progress(event, config, env=None, attempts=1) -> bool:
    """Close the episode on verified healthy progress of the same native.

    Returns True when an episode was closed. Progress on another
    native, a subagent session, or a non-PostLLMCall event never closes.

    A pending (never confirmed) episode is replayed first: if the flush
    succeeds the episode is marked open-then-closed, so the delivered
    observation keeps its identity and the next outage opens a NEW
    episode with a fresh budget. If the flush fails the pending state
    is kept and nothing closes.
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
        if not seen:
            # D84-11 finding 2: an orphan frozen payload with no snapshot
            # row still flushes on a healthy turn.
            seen = _adopt_orphan_pending(config, data, key)
            if seen is None:
                return False
        if seen.get('pending'):
            if env is None:
                return False
            stored = _pending_observation(config, seen.get('episode_id'))
            if stored is None:
                return False
            stored = dict(stored, episode_id=seen['episode_id'])
            body = _compose_body(seen['episode_id'], stored)
            last = None
            for _ in range(max(1, attempts)):
                try:
                    _submit(body, config, env, stored)
                except (OSError, ValueError, subprocess.SubprocessError) as error:
                    last = error
                    continue
                # Snapshot BEFORE dropping the payload (4f43571 review
                # finding 2): a crash between the two must leave the frozen
                # body recoverable, never a committed server row with no
                # local body and a still-pending snapshot row.
                seen['episode_open'] = False
                seen['pending'] = False
                seen['closed_at'] = int(time.time())
                seen['closed_by_turn'] = str(event.get('turn_id', ''))[:128]
                # D84-8: a still-pending flush confirms delivery here, so the
                # advance happens exactly once at this point, tracked by
                # the advancing episode's identity — a new episode always
                # advances again, repeats of the same one never do.
                if seen.get('advanced_episode') != seen.get('episode_id'):
                    seen['sequence'] = seen.get('sequence', 0) + 1
                    seen['advanced_episode'] = seen.get('episode_id')
                # The flush consumed the episode: its boundary is cleared
                # so a later episode never inherits it (4f43571 finding 1).
                seen.pop('healthy_boundary', None)
                data[key] = seen
                _save(config, data)
                _drop_pending(config, stored)
                # D84-13: a post-boundary outage preserved while this
                # episode was pending is delivered now, under its own key.
                # The old episode stays closed either way; a failed queued
                # delivery keeps its own pending payload for the next turn.
                _flush_queued_outage(config, env, key, attempts,
                                     str(event.get('turn_id', '')))
                return True
            # D84-11 finding 1: the healthy turn is verified progress even
            # when its flush attempt fails. Record the boundary durably so
            # a later confirmed replay closes this episode instead of
            # leaving it open and collapsing the next outage into it.
            seen['healthy_boundary'] = str(event.get('turn_id', ''))[:128]
            data[key] = seen
            _save(config, data)
            return False
        if not seen.get('episode_open'):
            # D84-13: a closed row may still carry a queued post-boundary
            # outage whose delivery failed on an earlier healthy turn.
            # Retry its bounded delivery; the row stays closed either way.
            if _queued_outage_ids(config, key) and env is not None:
                return _flush_queued_outage(config, env, key, attempts,
                                            str(event.get('turn_id', '')))
            return False
        seen['episode_open'] = False
        seen['closed_at'] = int(time.time())
        seen['closed_by_turn'] = str(event.get('turn_id', ''))[:128]
        data[key] = seen
        _save(config, data)
        # Closing an open episode may reveal queued outages preserved
        # while an earlier episode was pending (4c756a4 finding 1):
        # drain them now under their own keys instead of leaving the
        # queue stranded behind an open row.
        if _queued_outage_ids(config, key) and env is not None:
            _flush_queued_outage(config, env, key, attempts,
                                 str(event.get('turn_id', '')))
        return True
    return _locked(config, close)


def _queued_outage_ids(config, key):
    """Return the preserved post-boundary outage episode ids, in order.

    D84-15: one entry per independent outage, recorded on the snapshot
    row that preserved it. Each payload lives in the existing immutable
    pending store under its own episode key. A legacy single-string
    slot from an older head reads as a one-entry queue.
    """
    data = _snapshots(config)
    queued = data.get(key, {}).get('queued_outage')
    if isinstance(queued, str):
        return [queued] if queued else []
    if isinstance(queued, list):
        return [entry for entry in queued if isinstance(entry, str) and entry]
    return []


def _queued_outage_id(config, key):
    """Return the head preserved post-boundary outage id, or None."""
    ids = _queued_outage_ids(config, key)
    return ids[0] if ids else None


def _close_delivered_queued(config, key, queued, closing_turn):
    """Record a queued outage as delivered-and-closed, never reopened.

    D84-15: flushing a queued outage on a healthy turn commits its own
    event and advances the sequence exactly once for it, but the row
    stays closed: the healthy turn that delivered it already separates
    it from any later failure, so the next failure is always a NEW
    outage instead of a duplicate of the reopened one.
    """
    data = _snapshots(config)
    seen = data.get(key, {})
    rest = [entry for entry in _queued_outage_ids(config, key) if entry != queued]
    if rest:
        seen['queued_outage'] = rest
    else:
        seen.pop('queued_outage', None)
    if seen.get('advanced_episode') != queued:
        seen['sequence'] = seen.get('sequence', 0) + 1
        seen['advanced_episode'] = queued
    seen['episode_open'] = False
    seen['pending'] = False
    seen['episode_id'] = queued
    seen['closed_at'] = int(time.time())
    seen['closed_by_turn'] = closing_turn[:128]
    data[key] = seen
    _save(config, data)


def _flush_queued_outage(config, env, key, attempts, closing_turn=''):
    """Deliver preserved post-boundary outages, each under its own key.

    Called after the old episode closed on a healthy turn. Bounded:
    each queued payload gets at most `attempts` submits per call, in
    queue order; the first undeliverable payload stops the drain so a
    later turn retries it first. Each success commits its own event and
    closes it (never reopened). Failure leaves the remaining queue and
    payloads untouched for the next turn. Returns True when at least
    one queued outage was delivered.
    """
    delivered = False
    for queued in list(_queued_outage_ids(config, key)):
        stored = _pending_observation(config, queued)
        if stored is None:
            return delivered
        stored = dict(stored, episode_id=queued)
        body = _compose_body(queued, stored)
        confirmed = False
        for _ in range(max(1, attempts)):
            try:
                _submit(body, config, env, stored)
            except (OSError, ValueError, subprocess.SubprocessError):
                continue
            confirmed = True
            break
        if not confirmed:
            return delivered
        _close_delivered_queued(config, key, queued, closing_turn)
        _drop_pending(config, stored)
        delivered = True
    return delivered


def _mark_open_unlocked(config, observation, episode_id):
    data = _snapshots(config)
    key = episode_key(observation, config)
    seen = data.get(key, {})
    # D84-8: sequence advances exactly once per episode, at the moment its
    # delivery is first confirmed (first success or confirmed replay).
    # The guard is the advancing episode's identity: repeats of the same
    # episode never re-advance, a new episode advances again, and close
    # paths never advance. A stale 'advanced' boolean from an older head
    # is dropped, not carried. A recorded healthy boundary (D84-11) is
    # carried, not dropped: the replay-confirmation path consumes it.
    bump = 0 if seen.get('advanced_episode') == episode_id else 1
    boundary = seen.get('healthy_boundary')
    # A confirmed replay must not drop preserved queued outages: the
    # boundary branch below delivers the head and keeps the tail
    # (4c756a4 review finding 1).
    carried = {name: seen[name] for name in ('queued_outage', 'queued_boundary') if seen.get(name)}
    data[key] = {'episode_open': True, 'pending': False,
                 'episode_id': episode_id,
                 'advanced_episode': episode_id,
                 **({'healthy_boundary': boundary} if boundary else {}),
                 **carried,
                 'turns': seen.get('turns', []) + [observation['turn_id'] + '|' + observation['request_id']],
                 'first_observed_at': seen.get('first_observed_at', observation['observed_at']),
                 'sequence': seen.get('sequence', 0) + bump}
    _save(config, data)
    return data[key]


def _mark_pending_unlocked(config, observation):
    data = _snapshots(config)
    key = episode_key(observation, config)
    seen = data.get(key, {})
    # A failed replay must not erase a recorded healthy boundary, the
    # advanced-episode identity, or preserved queued outages: the
    # boundary still belongs to this episode until a confirmed replay
    # consumes it (7a01892 review), and the queue rides along until the
    # healthy-turn flush delivers it (D84-13/D84-15).
    carried = {name: seen[name] for name in ('healthy_boundary', 'advanced_episode', 'queued_outage',
                                             'queued_boundary')
               if seen.get(name)}
    data[key] = {'episode_open': False, 'pending': True,
                 'episode_id': observation.get('episode_id', seen.get('episode_id', '')),
                 'turns': seen.get('turns', []),
                 'first_observed_at': seen.get('first_observed_at', observation['observed_at']),
                 'sequence': seen.get('sequence', 0),
                 **carried}
    _save(config, data)


def _mark_open(config, observation, episode_id):
    return _locked(config, lambda: _mark_open_unlocked(config, observation, episode_id))


def _mark_pending(config, observation):
    _locked(config, lambda: _mark_pending_unlocked(config, observation))


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
    """Publish under one lock from episode check through open marker.

    Holding the episode lock across the bounded squad calls closes the
    check-then-publish window: overlapping hook runs serialize, and the
    second sees the first's open episode instead of double-publishing.
    Subprocess calls stay bounded (CALL_TIMEOUT each, attempts small) so
    the lock never blocks a hook longer than its own publish budget.
    """
    observation = observe(event, config)
    path = _snapshot_path(config)
    with path.with_suffix('.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        try:
            return _publish_locked(observation, config, attempts)
        finally:
            fcntl.flock(lock, fcntl.LOCK_UN)


def _drain_queued_before_live(config, key, attempts):
    """Deliver queued outages ahead of a live failure on a closed row.

    Each queued payload is submitted under its own key and closed on
    success, exactly like the healthy-turn drain. Entries that fail
    stay queued for a later turn; no live failure is ever merged into
    a queued payload.
    """
    env = _hook_env(config)
    _flush_queued_outage(config, env, key, attempts, closing_turn='failure-turn-drain')


def _publish_after_queue_drain(observation, config, attempts):
    """Publish the live failure after a queue drain on a closed row.

    The row is closed (or still closed with a remaining queue): the
    live failure is a NEW outage under the next key. A failure to
    submit it leaves it pending under its own key; the remaining queue
    is untouched.
    """
    data = _snapshots(config)
    key = episode_key(observation, config)
    seen = data.get(key, {})
    episode_id = 'ep-%d' % (seen.get('sequence', 0) + 1 + len(_queued_outage_ids(config, key)))
    live = dict(observation, episode_id=episode_id)
    _store_pending(config, live)
    body = _compose_body(episode_id, live)
    env = _hook_env(config)
    last = None
    for _ in range(max(1, attempts)):
        try:
            receipt = _submit(body, config, env, live)
        except (OSError, ValueError, subprocess.SubprocessError) as error:
            last = error
            continue
        _mark_open_unlocked(config, live, episode_id)
        _drop_pending(config, live)
        receipt.setdefault('episode_id', episode_id)
        # c3b0d81 review: the pre-drain may have been partial, so a
        # remainder can still sit on this now-open row; drain it on
        # this same failure turn instead of stranding it.
        if _queued_outage_ids(config, key):
            _flush_queued_outage(config, env, key, attempts,
                                 closing_turn=observation.get('turn_id', ''))
        return receipt
    row = _snapshots(config).get(key, {})
    row.update({'episode_open': False, 'pending': True, 'episode_id': episode_id,
                'first_observed_at': row.get('first_observed_at', live['observed_at'])})
    _save(config, dict(_snapshots(config), **{key: row}))
    raise last


def _publish_locked(observation, config, attempts):
    data = _snapshots(config)
    key = episode_key(observation, config)
    seen = _current_episode(data, key)
    if seen is None:
        # D84-11 finding 2: adopt an orphan frozen payload before
        # allocating anything, so a possibly committed request key is
        # replayed identically instead of overwritten by a fresh body.
        # Queued outages are never orphans: a closed row with an
        # undelivered queue drains the queue first and preserves the
        # live failure as a new outage (4c756a4 review finding 2).
        if _queued_outage_ids(config, key):
            _drain_queued_before_live(config, key, attempts)
            return _publish_after_queue_drain(observation, config, attempts)
        adopted = _adopt_orphan_pending(config, data, key)
        if adopted is not None:
            seen = adopted
    if seen is not None and not seen.get('pending'):
        return {'state': 'duplicate'}
    live = dict(observation)
    if seen is not None and seen.get('pending') and seen.get('episode_id'):
        # Immutable pending replay (D84-6): resend the EXACT frozen payload
        # of the first attempt. Later failures in the same outage only add
        # local dedupe diagnostics; they never rewrite the submitted body.
        episode_id = seen['episode_id']
        frozen = _pending_observation(config, episode_id)
        if frozen is not None:
            observation = dict(frozen, episode_id=episode_id)
        observation['episode_id'] = episode_id
        replay = True
    else:
        episode_id = 'ep-%d' % (data.get(episode_key(observation, config), {}).get('sequence', 0) + 1)
        observation['episode_id'] = episode_id
        replay = False
    env = _hook_env(config)
    body = _compose_body(episode_id, observation)
    if not replay:
        # Freeze before the first attempt so a lost reply can replay the
        # identical payload even if this process dies mid-submit.
        _store_pending(config, observation)
    last = None
    for _ in range(max(1, attempts)):
        try:
            receipt = _submit(body, config, env, observation)
        except (OSError, ValueError, subprocess.SubprocessError) as error:
            last = error
            continue
        _mark_open_unlocked(config, observation, episode_id)
        _drop_pending(config, observation)
        receipt.setdefault('episode_id', episode_id)
        boundary = _snapshots(config).get(key, {}).get('healthy_boundary')
        if replay and not boundary and _queued_outage_ids(config, key):
            # c3b0d81 review: a replay success without a boundary leaves
            # the row open with the queue still attached; the tail must
            # drain on this same failure turn instead of stranding until
            # a healthy turn that may never come during the outage.
            _flush_queued_outage(config, env, key, attempts, closing_turn=observation.get('turn_id', ''))
            tail = _queued_outage_ids(config, key)
            if not tail:
                final = _snapshots(config)
                receipt['episode_id'] = final.get(key, {}).get('episode_id', episode_id)
            return receipt
        if replay and boundary:
            # D84-11 finding 1: a healthy turn landed while this episode
            # was pending. The replay confirmation CLOSES the old episode
            # at the recorded boundary, then the post-boundary outage is a
            # NEW episode with a fresh key. A preserved queued outage
            # (D84-13) wins over the live failure: both are the same
            # continuous post-boundary outage, and the queued observation
            # is its first failure; the live one is same-outage dedupe.
            queued = _queued_outage_id(config, key)
            new_outage = live
            if queued is not None:
                stored = _pending_observation(config, queued)
                if stored is not None:
                    new_outage = dict(stored, episode_id=queued)
            return _publish_new_outage_after_boundary(new_outage, config, env, boundary, attempts, key)
        return receipt
    if replay and _snapshots(config).get(key, {}).get('healthy_boundary'):
        # D84-13: the replay failed but a healthy boundary is recorded,
        # so the live failure is a post-boundary NEW outage, not a
        # same-outage repeat. Preserve its real observation under its own
        # episode key in the immutable pending store; the healthy-turn
        # flush delivers it after the old episode closes. The queued
        # payload is frozen once and never rewritten by later failures.
        _preserve_queued_outage(config, key, live)
    _mark_pending_unlocked(config, observation)
    raise last


def _preserve_queued_outage(config, key, live):
    """Freeze a post-boundary live failure as a queued new outage.

    D84-15: each independent outage gets its own queue entry and
    immutable payload. A failure in the SAME continuous outage as the
    queue tail (no new healthy boundary since it was preserved) is
    same-outage dedupe and preserves nothing new. A failure AFTER a
    newer healthy boundary appends a new entry under the next key. Key
    allocation counts the old pending episode plus the queued entries:
    sequence + 1 + len(queue) + 1.
    """
    data = _snapshots(config)
    seen = data.get(key, {})
    queue = _queued_outage_ids(config, key)
    boundary = seen.get('healthy_boundary', '')
    if queue and seen.get('queued_boundary') == boundary:
        return queue[-1]
    episode_id = 'ep-%d' % (seen.get('sequence', 0) + 1 + len(queue) + 1)
    queued = dict(live, episode_id=episode_id)
    _store_pending(config, queued)
    seen['queued_outage'] = queue + [episode_id]
    seen['queued_boundary'] = boundary
    data[key] = seen
    _save(config, data)
    return episode_id


def _publish_new_outage_after_boundary(live, config, env, boundary, attempts, key):
    """Close a replay-confirmed episode at its healthy boundary and publish.

    The old episode is marked closed with its recorded boundary turn; the
    boundary is CONSUMED exactly once here and then cleared. The live
    failure becomes a NEW episode under the next key with its own
    immutable payload — unless a queued outage already froze a different
    body under that key, in which case the frozen body wins so one key
    never carries two bodies (11a67a2 review). Delivered queued entries
    are closed, never reopened, and the remaining tail keeps draining
    on this same failure turn (9c583c4 review): no open row may strand
    queued outages while the outage continues. On submit failure the
    new episode stays pending WITHOUT the boundary (D84-12): the
    boundary belongs only to the episode it closed, so a later replay
    of the new episode confirms it open normally instead of spawning a
    spurious further episode.
    """
    data = _snapshots(config)
    seen = data.get(key, {})
    seen['episode_open'] = False
    seen['pending'] = False
    seen['closed_at'] = int(time.time())
    seen['closed_by_turn'] = boundary
    seen.pop('healthy_boundary', None)
    data[key] = seen
    _save(config, data)
    episode_id = 'ep-%d' % (seen.get('sequence', 0) + 1)
    live['episode_id'] = episode_id
    frozen = _pending_observation(config, episode_id)
    if frozen is not None and _compose_body(episode_id, dict(frozen, episode_id=episode_id)) != _compose_body(episode_id, live):
        # A queued outage (D84-13) already froze a DIFFERENT body under
        # this key. One key carries exactly one body: submit the frozen
        # one, never the live one — otherwise a later lost-reply
        # recovery replays the frozen body and wedges on
        # payload-conflict (11a67a2 review). The live failure is the
        # same continuous post-boundary outage, kept as diagnostics.
        live = dict(frozen, episode_id=episode_id)
    # The head of the queue is being delivered on this call whenever
    # the allocated key is a queued entry: a preserved queued outage
    # always wins over any live failure, whether the caller passed the
    # live observation or the queued one (9c583c4 review).
    submit_queued = episode_id in _queued_outage_ids(config, key)
    _store_pending(config, live)
    body = _compose_body(episode_id, live)
    last = None
    for _ in range(max(1, attempts)):
        try:
            receipt = _submit(body, config, env, live)
        except (OSError, ValueError, subprocess.SubprocessError) as error:
            last = error
            continue
        if submit_queued:
            # The delivered entry was a preserved queued outage: close
            # it like the healthy-turn drain does, then keep draining
            # the tail on this same failure turn so no later failure
            # short-circuits to duplicate with outages still queued.
            _close_delivered_queued(config, key, episode_id, boundary)
            _drop_pending(config, live)
            receipt.setdefault('episode_id', episode_id)
            _flush_queued_outage(config, env, key, attempts, closing_turn=boundary)
            tail = _queued_outage_ids(config, key)
            if not tail:
                final = _snapshots(config)
                delivered = final.get(key, {}).get('episode_id', episode_id)
                receipt['episode_id'] = delivered
            return receipt
        _mark_open_unlocked(config, live, episode_id)
        _drop_pending(config, live)
        receipt.setdefault('episode_id', episode_id)
        return receipt
    failed = _snapshots(config)
    row = failed.get(key, {})
    row.update({'episode_open': False, 'pending': True, 'episode_id': episode_id,
                'first_observed_at': row.get('first_observed_at', live['observed_at'])})
    row.pop('healthy_boundary', None)
    # The new pending row owns its payload directly through episode_id.
    # The delivered head of the queue is consumed; any entries BEHIND it
    # are later independent outages and stay queued (D84-15).
    rest = [entry for entry in _queued_outage_ids(config, key) if entry != episode_id]
    if rest:
        row['queued_outage'] = rest
    else:
        row.pop('queued_outage', None)
    failed[key] = row
    _save(config, failed)
    raise last


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
        # Non-failed PostLLMCall of the bound native is verified healthy
        # progress: flush a pending episode first, then close, so the next
        # independent failure re-publishes with a fresh budget. Unrelated
        # sessions never close. Bounded to one flush attempt.
        try:
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
