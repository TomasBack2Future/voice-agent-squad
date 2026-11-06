# Current decisions and recovery

Use this contract only after the installed Squad exposes `terminal-events
 decision-get` and `decision-set`, and the Dispatcher explicitly adopts it for
this reservation. A skill reload alone does not migrate a running assignment.

Dispatcher: keep the decision detail in the canonical item message. Read
`terminal-events decision-get --reservation KEY --generation N --worker-session ID`,
then set its replacement with `decision-set` and the same identity,
`--expected-revision REV --outcome MESSAGE --action proceed|hold --condition TEXT`.
The CAS and wake are atomic. A hold names the actual unresolved condition and
its owner; recovery cites verified evidence. Reuse recorded user authorization;
do not ask again because a recap or older prompt omitted it. Never infer that an
explicit user stop has been lifted from elapsed time or a dependency transition.

Worker: upon wake and before pausing or reporting completion, read the effective
decision again. A received older event is not authority to overwrite it. Record
its revision in the existing checkpoint and acknowledge the current event after
reading. Submit a new blocker or terminal outcome with
`terminal-events submit --reservation KEY --generation N --worker-session ID --kind KIND --body-file OUTCOME --expected-decision REV`
(pass the observed revision unchanged; never auto-refresh). If rejected as
stale, reload and reconcile before ending the turn. Released
claims can receive a wake through proven custody history; reclaim normally
before any protected write. Do not restart or create another Worker for recovery.

Dispatcher's existing reconciliation cycle checks unresolved requests and
unacknowledged recoveries. Verify authentication/dependency readiness through the
existing authorized path, set the corresponding decision, and inspect the
receipt; publication is not acknowledgment. Resolve an unhealthy receiver using
session continuity, not keystroke callback injection. Group repeated failure
fingerprints (operation, component revision, error class) under one root-cause
owner; repair or change the failed condition before another attempt. Do not add a
monitor daemon, a new approval form, or another source of decision truth.

For rolling shared delivery, the decision identifies the common candidate owner,
allowed merge phase, per-Issue acceptance and handoff boundary. Record adoption
before changing delivery modes. Add `decision_revision`, `operation_receipts`
and `next_operation` to the existing checkpoint. Run validate_context_package.py
before resuming a typed checkpoint; if an intent was already submitted, reconcile
it instead of dispatching again. Preserve ambiguous operations and their locks.

## Structured waits and phase results

When the current decision is a hold with `waits_for`, `decision-get` shows each
wait's `satisfied` fact, `waits_satisfied` and `wait_overdue`. A hold whose
waits are all met, or whose time box ended, is not permission to proceed: submit
one `decision-request` citing that readback and keep working on what the hold
does not block (`blocks_phase`). A hold that only blocks one phase (for example
`merge`) never stops implementation, tests, review or acceptance preparation.

When a phase finishes, record its result before or with the report:
`terminal-events phase-result --reservation KEY --generation N --worker-session
ID --phase NAME --checks all|partial --verdict passed|failed|partial
--deferred TEXT --outcome MESSAGE`. `--checks all` means every planned check of
that phase ran, whatever the verdict; name what moved to a later candidate in
`--deferred` (for example a failed replay re-verified on the next candidate).
The record wakes the Dispatcher and is what dependents wait on; a prose report
or a receipt note alone does not release them. It is not acceptance or closure:
keep the Issue OPEN, ENV custody and human holds unchanged. A result that
cannot be recorded (older tool, rejected identity) is a capability gap to state
in the report, not a reason to repeat the phase.
