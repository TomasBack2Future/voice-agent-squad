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

Before relying on unattended progress, verify the actual next-invocation route:
the selected ledger, current Dispatcher/native session, callback binding and
receiver owner/incarnation/liveness; for an existing heartbeat, verify its enabled
state and exact target. Record event-driven, periodic, both, or unavailable in the
existing checkpoint. A running receiver proves a transport is present, not that
every delivered event was handled or its dependency advanced. Reconcile durable
delivery and handled cursors separately from Worker outcome and reservation state.
An absent/paused heartbeat or one targeting an old session is not periodic fallback.
Report that limit; preserve explicit pauses and do not silently enable, retarget,
duplicate or replace a controller. Repair an evidenced route fault within existing
authority, using the session-continuity contract for any actual migration.

Read [assignment continuity](../../../agent-loop/lifecycle-continuity.md) before
creating a session from another chat, recovering a failed model connection or
rolling out changed instructions. An outside observer must distinguish transport
health from Goal continuation and verify actual resumed execution.

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

The supervision set is the unfinished work in the authorized batch, not the set
of live sessions. Keep partially retired and unassigned Issues in that set until
acceptance and cleanup are complete, or the user explicitly removes them from
scope. Build the set from the existing batch/checkpoint and current Issue state;
`dispatch list` without an active-state filter and `dispatch delivery KEY` can
reconcile known reservations, including `partial-retired` and `awaiting-owner`.
Use a fresh matching Issue observation when available; unknown is not closed.
An Issue without a reservation is still preparation/admission work. These reads
do not implement scheduling, grant authority or reopen a retired assignment.

In the existing checkpoint, retain each unfinished Issue's remaining assertion,
relevant component revision, next owner/action, last meaningful progress and
specific prerequisite with its next check time or event. Preserve these across
session retirement. A newly read timestamp, message, ACK or process exit is not
acceptance progress. Query changes within this retained set; an empty message
cursor is not proof that no work is ready.

Assess waiting per Issue and phase. A verified in-flight operation within its
bound, a user pause or a human-only prerequisite explains only that phase's wait.
It cannot exempt other Issues or independent preparation. After two existing
cycles without new acceptance, resolved blockers, delivery or effective handoff,
and without such a verified wait, investigate once and record a concrete action
through the responsible Dispatcher. A `blocked` label or a proposed future owner
alone must not reset the progress clock. An evidenced missing handoff or satisfied
prerequisite needs reconciliation in the current cycle, without waiting for two.

After deployment, compare each waiting assertion with the actual component
revision it needs; an API rollout does not update an external Worker. Release
only satisfied dependencies through the current decision/readback contract.
Before ending a cycle, reconcile ready continuation and preparation against the
actual WIP limit. Unused capacity is valid when its reasons are evidenced, not
because only existing sessions were considered. Do not invent work to fill it.

Keep a recurring prompt focused on these stable duties and checkpoint locations;
update current facts in the checkpoint instead of appending historical waits and
version snapshots to the prompt. Report what advanced, what remains and who owns
the next action. An outside supervisor checks this chain through the existing
Dispatcher; it does not become a second scheduler or repeat the acceptance.

Use [session continuity](session-continuity.md) for behavioral health checks and
cross-provider/model takeover. Quiet time alone never triggers replacement.
Preserve custody, human pauses and bounded observation windows; use the existing
event/heartbeat cycle, never a new poller or routine Worker reminders.

## Shared failure reconciliation

Apply the Worker's [shared-blocker contract](../../../agent-loop/roles/worker/references/operational-readiness.md#one-shared-blocker-one-repair-owner).
Group only failures with matching phase, environment, component identity/revision,
stable error code and normalized failed condition, supported by the actual run/trace.
For different component revisions, require evidence of the common cause and record
each affected revision explicitly; an HTTP status or the word "TokenUsage" alone
is not a fingerprint. `no_changes`, `timed_out`, storage failure and upstream model
failure must not be treated as one defect without causal evidence.

Reuse one canonical incident/item, live repair claim and affected-phase dependency.
Give each waiting item a concrete next action and resume condition. Deterministic
failures stop unchanged reruns; transient failures use the existing runbook's bounded
safe retry, or at most one safe retry if it sets no budget. Unknown attribution needs
bounded diagnosis. Preserve unaffected acceptance assertions and frozen provenance;
do not restart complete CI/deployment or stop independent implementation just because
one shared acceptance assertion failed. This never waives a required final gate.

Use `shared-failure` in the existing read-only delivery preflight before choosing
retry or repair wait, and `blocker` before assigning a human prerequisite. Reuse the
existing incident/checkpoint as its evidence source, not a parallel failure ledger.
