"""Dispatcher-side handling contract for Muse runtime-failure events.

Decisions are pure functions over (event, ledger-observed state) so the
owning Dispatcher cycle and the regression tests share one contract:

- D84-2 ordering: an event that arrives before the target reaches a terminal
  state is `awaiting-terminal`: never acked, never consumed as handled, never
  dropped, and never spends the continuation budget. The existing receiver
  redelivers it; the Dispatcher re-evaluates on a later delivery.
- Recovery is decided only after the terminal state (goal blocked / run
  terminal) is confirmed. At most one continuation per continuous outage
  within the existing budget. Auth/quota/config failures, pauses and
  completed tasks never continue.
- No hook -> reply -> failure loop: a continued outage key is terminal for
  this episode; no duplicate Worker, relay restart or replay of external
  operations is admitted here.
"""
from __future__ import annotations

import re

NON_RETRYABLE = ('auth', 'quota', 'config')

_ERROR_CLASSES = ('exhausted', 'connection', 'auth', 'quota', 'config', 'unknown')

_BODY_CLASS = re.compile(r'\Aruntime-failure ep-[1-9][0-9]* (\S+)')
_EPISODE_SUFFIX = re.compile(r'\Aep-[1-9][0-9]*\Z')


def error_class(event) -> str:
    """Resolve the closed-enum error class for a receipt.

    Prefer an explicit field; otherwise parse the sanitized message-body
    token the hook persists. Anything unrecognized stays 'unknown',
    which is retryable only through the normal budget path.
    """
    direct = event.get('error_class')
    if direct in _ERROR_CLASSES:
        return direct
    match = _BODY_CLASS.match(str(event.get('body') or ''))
    if match and match.group(1) in _ERROR_CLASSES:
        return match.group(1)
    return 'unknown'


def episode_key(event) -> str:
    """Stable key per continuous outage: reservation/gen/native/class/episode.

    Later turns/requests of the unchanged outage share the key, so one
    continuation per outage is enforceable without a new ledger table.
    The episode identity (opened at the first failure after healthy
    progress) gives each independent outage its own budget; without it,
    one consumed budget would suppress every later outage (D84-4).
    Both components come from durable persisted state: the /ep-N
    event-id suffix and the body class token (1b417f3 review), so
    receipts carrying only event_id still key distinctly.
    """
    parts = (event.get('event_id') or '').split('/')
    if len(parts) >= 6:
        suffix = parts[6] if len(parts) >= 7 and _EPISODE_SUFFIX.match(parts[6]) else ''
        episode = suffix or event.get('episode_id', 'ep-?')
        return '%s|%s|%s|%s|%s' % (parts[1], parts[2], parts[3],
                                    error_class(event), episode)
    return 'unknown-outage'


def decide(event, terminal=False, seen=frozenset(), paused=False,
           completed=False, live_operation=False, pending_decision=False):
    """Return (action, reason).

    Actions: 'continue' (admit one bounded continuation), 'awaiting-terminal'
    (keep pending, must not ack), 'stop' (no continuation; ack only after the
    owning cycle records its reconciliation). `seen` holds episode keys that
    already consumed the continuation budget.
    """
    if event.get('kind') != 'runtime-failure':
        return 'stop', 'not a runtime-failure observation'
    # D84-2 ordering: NOTHING pre-terminal is consumable. Auth/quota/config,
    # spent budgets, pauses and completions all resolve to stop only AFTER
    # the terminal state is confirmed; before that the event stays
    # awaiting-terminal (never acked, re-evaluated on later delivery).
    # Only a live external operation keeps its own awaiting-terminal reason.
    if live_operation:
        return 'awaiting-terminal', 'external operation live or unverified; reconcile before any decision, not acked'
    if not terminal:
        return 'awaiting-terminal', 'target turn still settling; keep pending and re-evaluate on later delivery, not acked'
    if paused:
        return 'stop', 'paused task never auto-continues; await explicit resume'
    if completed:
        return 'stop', 'completed task never continues; failure is observation only'
    if pending_decision:
        return 'stop', 'unhandled decision blocks automatic continuation'
    resolved = error_class(event)
    if resolved in NON_RETRYABLE:
        return 'stop', '%s failure never gets a blind continue' % resolved
    key = episode_key(event)
    if key in seen:
        return 'stop', 'continuation budget for %s already spent; no duplicate continuation' % key
    return 'continue', 'terminal failure confirmed; admit one bounded continuation for %s' % key
