# Shared staging coordination

Use one existing deployment owner and the existing ENV claim; do not add a
Designer, release session or fine-grained Project/Suite/Session locks merely to
coordinate staging. The Dispatcher owns design and compatibility decisions,
never the deployment or environment claim.

Before dispatch, group already-ready compatible changes by actual release
component. Do not wait indefinitely for unfinished work to fill a batch. Record
one durable coordination note in the owning Squad thread, referenced by member
threads: member Issues/PRs, exact candidate component SHA/digest tuple, deployment
owner, required combined checks, per-member acceptance owner and result, fixture
isolation, deadline and recovery policy. Use existing messages/checkpoints;
this is not a claim that a new batch CLI/schema has been implemented. Keep these
fields out of strict assignment-envelope schemas; use referenced admission or
coordination metadata.

Verify the combined candidate, including route/configuration/schema conflicts;
independent green PRs do not prove integration. Reuse exact candidate CI and
one deployment receipt across covered Issues. Separate release trains may have
different SHAs. Evidence must map each requirement to its component and actual
acceptance window; an older or merely healthy deployment is insufficient.

One assigned existing Worker owns the ENV mutation and recovery. Other Workers
can concurrently perform read-only acceptance against the protected candidate,
without acquiring that same ENV claim. Use isolated fixture identifiers. Shared
writes still obey installed ownership rules: if delegated writes are unsupported,
the ENV owner performs them; do not invent claim sharing. Serialize concrete
conflicts. Keep the version protected only for required version-sensitive checks,
then release ENV; final Issue bookkeeping does not require the lock.

Prepare review, artifacts, parameters, access and acceptance scripts before ENV
where supported. The current guarded-merge path still holds ENV across merge and
post-merge CI; do not claim that a skill update removes that wait. Moving acquisition
to deployment time requires the repository's merge/deploy admission to support it.
Likewise config-only image reuse and selective CI/acceptance require implemented
workflow classification; never bypass existing gates by prose instruction.

Ordinary staging delivery performs no rollback rehearsal. Apply the Worker staging
recovery policy: brief unavailability is acceptable within a recorded repair window,
not a reason to exercise a successful rollback path on every release. Automate
normal deployment, queries and recovery under existing authority. Use bounded
scripted data assertions for metrics; reserve browser checks for presentation or
legitimate session-based access. User fallback is for genuinely unavailable access,
failed recovery or a material product decision, not routine commands or restarts.

On skill refresh, read the new installed files rather than relying on conversation
memory; record resolved source commit and the changed rules. New assignments pin
the new package. Existing Workers retain identity, claims and scope; notify them
only for a necessary authorized correction, never restart them just to refresh.

## Trigger chain and interrupted holders

Before promising one deployment for a batch, inspect the complete trigger chain:
merge/push, CI completion, prefetch/downstream workflows, and actual dispatch.
A deploy workflow declaring only workflow_dispatch can still be invoked
automatically by another workflow. Record how intermediate candidates are
suppressed or coalesced through an existing supported mechanism. If none exists,
state the required workflow change; a skill edit cannot implement batching.
Do not disable gates or rely on timing races to suppress intermediate releases.

During the existing reconciliation cycle, distinguish ordinary active waiters
from a holder paused by a denied/cancelled tool call. Apply the shared
[interrupted ownership contract](../../../agent-loop/roles/worker/references/acceptance-readiness.md).
Read-only verification and one actionable escalation/handoff are appropriate;
force-release, unsolicited resume and routine waiter pings are not. Expose the
held resource, verified external state and next owner action rather than leaving
all other Workers silently waiting. No additional polling process is needed.
