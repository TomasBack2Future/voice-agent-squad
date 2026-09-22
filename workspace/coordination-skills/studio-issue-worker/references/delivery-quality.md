# Delivery quality and convergence

Read when defining an Issue work package, changing paginated/partial/streamed
reads, receiving repeated review findings, or planning reusable test resources.
These rules apply to the named work only; existing ownership, ENV, CI and review
policy remain authoritative.

## Complete work packages

Keep Issue acceptance granular, but prefer a stable owner for a sequential
technical chain. A later Issue still requires explicit Dispatcher assignment,
its canonical claim and an acknowledged transfer of the preceding work. Never
claim multiple children or infer permission to scan the queue.

Before implementation, map each requirement to an observable assertion and the
earliest useful test. Inspect callers and companion paths together: source,
production/staging index parity, schema assertions, generated manifests, request
inventories, fixtures, route/browser tests and documentation. Include conditional
companions in the initial assignment after checking live ownership. Shared test
registries need a named writer and an update route before child CI starts.
Missing directly related companions go in one evidenced amendment to Dispatcher;
continue independent work, but do not silently override another owner's paths.

Represent dependency edges as `implementation`, `path-release`, `acceptance` or
`external-access`, with evidence and the phase each blocks. An OPEN Issue may
already satisfy an implementation edge; verify its integrated revision and
remaining acceptance owner. Never waive a true acceptance or access prerequisite.

Before first product changes, use a small successful vertical slice on the
actual target database edition/version and the real caller/SDK path. Prioritize
new SQL syntax, index plan behavior and changed response consumers. Scale tests
follow readiness, not the reverse. For external services, resolve an existing
authorized target, source/image identity and permitted test operation early;
missing configuration is a bounded discovery task, not automatic missing authority.

## Acceptance and performance

Record required correctness/security/resource limits separately from latency
objectives and diagnostic measurements. Preserve existing approved hard budgets;
only an explicit scope change can change their disposition. For newly planned
work, avoid inventing p95/p99 hard gates before a credible baseline exists.
Successful-operation samples require expected status, correct semantic result,
correct fixture type and sample count. Report intentional 4xx/error-path latency
separately. Never average failed requests into successful-operation performance.

For read changes, jointly test the relevant invariants:
- pagination: deep pages remain reachable, no missing/duplicate rows, stable
  timestamp ties, bounded cache, separate aggregates and independent cursors;
- partial views: every consumer uses its requested shape, mutation receipts
  reconcile the correct entity including index zero, revisions fence stale data;
- streams: distinguish stale idle state from the current committed turn,
  preserve irreversible commits across truncation/cancellation, and reset locks
  on route/session changes. A transport optimization must preserve completion.

After two successive verified findings in the same behavior family, pause new
review sampling for one owner-led design/consumer sweep. Record the invariant,
concrete reproduction and related callers; fix and test the complete chain before
one new substantive head. This is a convergence checkpoint, not a reviewer quota
or permission to stop useful work. Disprove findings against the Issue and code;
do not blindly implement a reviewer suggestion or use tests that only mirror it.

## Reusable resources and evidence

Use a credential-free descriptor: resource IDs, current custodian, immutable
image, database version/edition, schema and fixture version, namespace, ports,
readiness result, and `retain` or `ephemeral` disposition. Reuse compatible
resources with one mutator per namespace; record custody separately from Issue
closure. Clean ephemeral test rows/processes without deleting retained databases.
General cleanup instructions never override explicit retention. Immediately
before deletion, verify live exact IDs/custodian/disposition; missing or stale
evidence blocks deletion. The read-only `scripts/delivery-check.mjs` cleanup
check rejects retained/unknown/other-owned targets; it does not wrap Docker or
make a declaration proof of live state. Do not invoke raw deletion to bypass it.

An accepted contribution may reuse unchanged evidence only with explicit
product/schema/fixture/component provenance. Changed behavior gets targeted new
evidence plus all required final gates. Do not reset a fixture just for handoff.
After two no-progress observations, or ten minutes of preparation without a
credible completion estimate, diagnose the current stage once before retrying.
Preserve useful work and do not relax required criteria to manufacture progress.

## Accounting and completion

At meaningful transitions record start/end, task/Issue/PR, revision, phase,
operation/run/attempt ID and waiting reason. Separate implementation, review,
CI, environment preparation, dependency wait, acceptance, user pause, host
offline and unknown. Exclude confirmed travel/user pauses from controllable
cycle time; silence alone is not host failure or Dispatcher delay.

Record token usage from observed per-request counters or deduplicated cumulative
deltas. Cached input is part of input; reasoning is part of output. Missing
usage is unknown. Account quota snapshots are shared, keyed by window/reset and
are not Epic-specific cost. Count Actions by run+attempt+job, separate hosted
from self-hosted duration, and never add parallel job time to elapsed lead time.
Keep prompts, reasoning, raw tool output and secrets out of reports.

At accepted closure reconcile child status, checklist, title and reservation;
retain incidents and explicitly changed acceptance scope. Both Git reverts and
environment-only rollbacks count as rollback events.

Run the read-only preflight with an independently checked snapshot:
`node <skill>/scripts/delivery-check.mjs <snapshot.json>`.
Use `schema: squad.delivery-check.v1` and the applicable action: `work-package`
before product edits, `dependency` when resolving an edge, `successful-samples`
before publishing successful-operation timings, `review` before sampling, and
`cleanup` immediately before deleting test resources. The executable fixtures in
`scripts/delivery-check.test.mjs` define the small action-specific input shapes;
reuse or extend the existing manifest/acceptance/review evidence instead of
creating a second ledger, long prose checklist, new polling loop or extra hosted
job. Keep only the fields the applicable check consumes; repeated unchanged
checks add no value, except the required fresh live pre-delete verification.
Retain the sanitized snapshot and result with the Issue evidence. Not-applicable
actions require no invented snapshot. A failed preflight requires correcting the
evidence or plan, not overriding the result. Run all skill contract tests with
`node --test <skill>/scripts/*.test.mjs` when these contracts change; record actual
CI coverage separately when no repository workflow owns these local helpers.
Its checks guide the owner; atomic Squad claims and real branch/ENV gates still
provide authority. Never fill a snapshot from assumptions just to obtain a pass.
