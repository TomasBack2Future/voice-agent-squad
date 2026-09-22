# Dispatch reliability and batch monitoring

Read for batch dispatch, asynchronous task creation, and efficiency reconciliation.

## One central monitor
The Dispatcher owns batch progress observation, dependency transitions, WIP accounting,
and deduplicated wake-ups. Integration Workers own handoff validation, integration,
review/CI, merge, staging acceptance and cleanup; they do not poll other task progress.
When no actionable release work exists, the integration Worker records a durable
waiting checkpoint and yields its turn, retaining ownership. Waiting is not completion.
A developer records a frozen handoff offer on both items and sends one offer-ready
event to the originating Dispatcher. The Dispatcher verifies the offer and wakes an
idle integration owner once per reservation generation/head/event. Active owners
receive no routine reminders. Direct ACK/commit/accepted transfer messages remain valid.
Observe with compact snapshots and saved cursors on the existing heartbeat; no new timer.

## Asynchronous creation
Before creation, persist reservation key/generation and a creation-attempt identifier.
Persist the returned clientThreadId immediately when setup is queued. A queued or
unknown attempt is live work and prevents another create call. Absence from list_threads
is not proof of failure. Resolve using task callbacks and verified task metadata.
Bind the real thread ID to the same generation and read back the binding.
Never pass a clientThreadId as a real thread ID. Expiration does not authorize replacing
an unresolved creation: reconcile it first. If the API lacks idempotency or creation-status
lookup, preserve uncertainty and report it; do not emulate a retry by creating a duplicate.
Worker startup must verify its actual thread identity; another bound owner means stop
without force-registering or claiming that owner's identity.

## Complete assignments and efficient execution
Check code, mirrored index/schema files, exact supporting tests and deployment docs
together when assigning paths. Include necessary companion paths after checking live
ownership; keep changes limited to the named Issue. Bundle discovered amendments.
Pass an existing verified test-environment descriptor when available: engine/context,
image digest and edition/version, bootstrap entrypoint, network strategy, resource owner,
and cleanup boundaries. Missing details are preparation work, not a claim of readiness.
Keep the user's Sol/medium default and existing concurrency bounds. The requested 1.5x
speed refers to the app's Fast-mode lightning button, not a workflow KPI.
The local service_tier preference controls this independently of model/effort;
fast maps to priority requests. Preserve the user's enabled priority preference.
The create_thread tool has no explicit tier field: verify effective configuration
or UI state rather than claiming a per-task override was sent.
Record preparation, implementation, review/CI, dependency wait and acceptance durations
to evaluate improvement. Do not increase concurrency, weaken checks or repeat deployment
to claim speed. Use a speed setting only when the tool exposes and confirms it.

## Lessons from Epic793 on 2026-09-13
An unlisted queued task was created again while the original already held STUDIO-062.
The duplicate eventually exited without product changes. Binding must be reconciled,
not retried through task creation. Index and deployment-doc paths were admitted through
two amendments; companion paths belong in one scoped assignment. Repeated environment
bootstrap exposed indexer storage-mode and SDK advertised-address failures. Share engine
and image cache, isolate task data, and validate the actual SDK route before scale tests.
These instructions reduce recurrence but do not implement API-level idempotency.

## Convergence supervision

Track meaningful state changes and their cause, not presence messages. A confirmed
user pause or host-offline interval is not an execution fault; an unclassified
quiet interval stays unknown. When an existing heartbeat observes a stalled
required preparation stage, repeated companion amendments, or two verified
findings in one behavior family, reconcile the owner checkpoint once. Request a
bounded recovery/design sweep only when evidence shows a correctness or scope
risk. Do not routinely interrupt active Workers or invent another monitor.

Before a cross-provider/model takeover, verify the destination can represent the
same canonical task/reservation, route callbacks and retain external-operation
ownership. Persist both provider and actual task ID. A foreign UUID, textual
handoff or successful model call is not proof of binding. Failed rebind or callback
routing requires control-plane repair before any new writer; never borrow another
agent identity, force-register it, or treat a note as replacing the reservation.
Record handoff interruption separately from model performance; do not infer token
savings or comparative model quality from an uncontrolled switch.
