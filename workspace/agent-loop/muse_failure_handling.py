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

NON_RETRYABLE = ('auth', 'quota', 'config')


def episode_key(event) -> str:
    """Stable key per continuous outage: reservation/gen/native/class/episode.

    Later turns/requests of the unchanged outage share the key, so one
    continuation per outage is enforceable without a new ledger table.
    The episode identity (opened at the first failure after healthy
    progress) gives each independent outage its own budget; without it,
    one consumed budget would suppress every later outage (D84-4).
    """
    parts = (event.get('event_id') or '').split('/')
    if len(parts) >= 6:
        return '%s|%s|%s|%s|%s' % (parts[1], parts[2], parts[3],
                                    event.get('error_class', 'unknown'),
                                    event.get('episode_id', 'ep-?'))
    return 'unknown-outage'


def decide(event, terminal=False, seen=frozenset(), paused=False,
           completed=False, live_operation=False, pending_decision=False):
    """Return (action, reason).

    Actions: 'continue' (admit one bounded continuation), 'awaiting-terminal'
    (keep pending, must not ack), 'stop' (no continuation; ack only after the
    owning cycle records its reconciliation). `seen` holds episode keys that
    already consumed the continuation budget.
    """
    if paused:
        return 'stop', 'paused task never auto-continues; await explicit resume'
    if completed:
        return 'stop', 'completed task never continues; failure is observation only'
    if pending_decision:
        return 'stop', 'unhandled decision blocks automatic continuation'
    if live_operation:
        return 'awaiting-terminal', 'external operation live or unverified; reconcile before any decision, not acked'
    if event.get('kind') != 'runtime-failure':
        return 'stop', 'not a runtime-failure observation'
    if event.get('error_class') in NON_RETRYABLE:
        return 'stop', '%s failure never gets a blind continue' % event.get('error_class')
    key = episode_key(event)
    if key in seen:
        return 'stop', 'continuation budget for %s already spent; no duplicate continuation' % key
    if not terminal:
        return 'awaiting-terminal', 'target turn still settling; keep pending and re-evaluate on later delivery, not acked'
    return 'continue', 'terminal failure confirmed; admit one bounded continuation for %s' % key
