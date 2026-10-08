"""Bounded transient-timeout continuation: dedupe, retry cap, external-op check.

A transient timeout resumes only unfinished steps of the same native/task
assignment. User pause, unhandled decisions, live external operations, old
timeouts and old reservations are never treated as transient. No permanent
monitor or timer is created; the caller drives one bounded attempt.
"""
from __future__ import annotations

MAX_ATTEMPTS = 3


def fingerprint(failure):
    """Stable dedupe key: operation + component revision + error class."""
    return '%s|%s|%s' % (failure.get('operation', '?'), failure.get('revision', '?'),
                         failure.get('error_class', '?'))


def continuation_attempt(failures, seen, external_terminal, paused=False,
                         pending_decision=False, live_operation=False, max_attempts=MAX_ATTEMPTS):
    """Decide one bounded continuation attempt for transient timeouts.

    failures: ordered failure records, newest last. seen: fingerprints
    already attempted. Returns (action, reason) where action is one of
    'resume-unfinished', 'wait', 'stop'. Only a fresh transient-timeout
    fingerprint under the retry cap with terminal external state resumes;
    everything else stops or waits without spawning monitors.
    """
    if paused:
        return 'stop', 'user pause is not a transient error; await explicit resume'
    if pending_decision:
        return 'stop', 'unhandled decision blocks automatic continuation'
    if live_operation or not external_terminal:
        return 'wait', 'external operation is live or unverified; reconcile before retry'
    if not failures:
        return 'stop', 'no failure recorded; nothing to resume'
    current = failures[-1]
    if current.get('error_class') != 'transient-timeout':
        return 'stop', 'non-transient failure requires a changed condition, not a retry'
    if current.get('stale') is True:
        return 'stop', 'old timeout log must not retrigger; reconcile original custody'
    key = fingerprint(current)
    prior = [f for f in failures[:-1] if fingerprint(f) == key]
    if key in seen or len(prior) + 1 >= max_attempts:
        return 'stop', 'retry budget exhausted for %s; escalate with evidence' % key
    unfinished = [s for s in current.get('steps', []) if s.get('state') not in ('completed', 'joined')]
    if not unfinished:
        return 'stop', 'all steps completed or joined; no blind rerun of finished deployment'
    return 'resume-unfinished', 'attempt %d resumes %d unfinished step(s) of %s' % (
        len(prior) + 2, len(unfinished), key)
