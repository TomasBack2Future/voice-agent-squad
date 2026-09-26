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
reading. Publish a new blocker or terminal outcome with `--expected-decision REV`.
If rejected as stale, reload and reconcile before ending the turn. Released
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
