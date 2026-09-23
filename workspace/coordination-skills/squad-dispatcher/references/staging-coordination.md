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

Select the release path explicitly using the Worker
[candidate contract](../../studio-issue-worker/references/staging-candidates.md).
Record verified repository capability and assign `staging-candidate-v1` only after
CI/prefetch no longer auto-rolls out and the explicit immutable, deduplicated
deployment entry is implemented. Then prepare merge/CI/images/prefetch outside
ENV; one owner claims only at deployment. Until then retain guarded-merge locking.
Missing capability is concrete implementation work: within standing authority,
admit/dispatch that bounded repair with its owner and gate instead of silently
keeping every task serialized or asking the user to repeat the same decision.
Config-only image reuse and selective checks likewise require implemented
classification; never bypass existing gates by prose instruction.

## Make the next batch concrete

For multiple ready compatible tasks, record the actual member set, owner and
next action during this cycle. Freeze one integration candidate and a final main
PR using the existing batch transfer contract; no new release role/session is
required when a current owner can accept that responsibility. Reserve independent
read-only acceptance for peers so all Issues do not repeat the common deployment.
Keep each requirement and failure disposition visible. If no batch can be formed,
record the concrete incompatibility or readiness gap and advance one ready task.

Choose the next admitted release by recorded priority and dependent work, then
waiting age. Do not claim the current atomic `claim --wait` is FIFO or priority
aware. Apply scheduling before new waits are started; coordinate existing waits
only through an explicit authorized migration, without stealing claims. Routine
ordering within existing authority is Dispatcher work, not a new user decision.
Never indefinitely delay a ready dependency-unblocking task to fill a batch.

A skill reload is not a migration receipt. For current Workers persist the exact
new mode, capability, batch/owner and next action, obtain acknowledgment and keep
identity/custody intact. Leave in-flight rollouts under their existing contract.
Honor a dispatch pause; a narrowly authorized optimization does not reopen the
ordinary queue. Use the existing event cycle and bounded interventions, not a
new daemon or routine progress pings.

Measure before claiming improvement: candidate readiness, lock queue time, lock
occupation, actual rollout, common checks, per-Issue checks, unique deployment
count and retry reason. Use wall time (not a sum of concurrent jobs) for latency.
Unknown or estimated values are not observed savings. Release ENV once required
version-sensitive checks and safe cleanup finish, before Issue bookkeeping.

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

### Maintained holder inspection

Before candidate dispatch, run the installed `squad claim-inspect ENV-001` in the
selected coordination ledger repository. It emits `env_claim` with `item`,
`holder`, `generation`, `claimed_at` (UTC RFC3339), and `state`. Require `held`
and exact agreement with the admitted claim. A null claim or command error
cannot authorize dispatch. The command is read-only and repository-scoped;
Worker-written database readers and `status.claimed_by` are not substitutes for
this exact contract. Reading the authoritative ledger does not itself grant
ownership. Verify the installed command against the deployment consumer before
activating the candidate path; publishing a skill alone does not install it.
