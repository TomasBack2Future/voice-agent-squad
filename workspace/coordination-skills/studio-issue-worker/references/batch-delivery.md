# Explicit batch delivery v1

Read only for a prompt explicitly assigning `delivery-mode: batch-v1`. Ordinary
single-Issue Workers keep their existing lifecycle. An Epic link, shared branch,
label, or this file alone never opts a running Worker into batch mode.

## Admission and roles

Batch progress monitoring and dependency wake-ups belong exclusively to the
originating Dispatcher. Do not poll another Worker's progress. When only waiting
for a child offer, persist the waiting checkpoint and yield while retaining primary
ownership. Developers send one offer-ready event (generation/head/offer pointer)
to the Dispatcher after recording the frozen offer; it verifies and wakes the idle
recipient once. Direct handoff ACK/commit/accepted messages remain part of delivery.

The authorized Dispatcher records a frozen batch manifest and includes its exact
revision in each prompt: batch ID, child Issues/items and reservation generations,
role (`developer` or `integration-release`), exact primary release item, integration
branch, target main branch, allowed paths, dependencies, acceptance owner, and CI
matrix/policy evidence. Missing identity, policy audit or ownership blocks batch
mutation. Preparation may build local tools while admission remains blocked.

At most two development owners and one integration/release owner run per batch;
all count within global product WIP <= 5, including paused/nonterminal tasks and
unbound active reservations. Count a task once, not once per linked child. A
handoff does not remove a still-active task from WIP. A release owner has one
canonical primary release item, and ENV only when ready. No third exclusive
release/environment lock and no simultaneous collection of child claims.

Developers claim their one assigned canonical child, edit only its declared
paths, test/review a stable child PR targeting the integration branch, then stop
writing at implementation-ready. They never push the integration branch, merge
main, publish release images, prefetch, deploy, or write shared acceptance data.
A serial developer may take the next child only by an explicit Dispatcher
assignment after the previous responsibility transfer is acknowledged; it must
not scan/start the next Issue itself. Shared storage/schema/handler/test paths
remain mutually exclusive across development lanes.

The integration owner alone writes the integration branch under the exact primary
release claim. It accepts frozen child contributions and owns their integrated
code, release, acceptance and final Issue disposition. That explicitly scoped
primary claim covers accepted child contributions: it is not permission to edit
an unhanded child still owned by another developer. Before acceptance, any fix
goes back to the child's sole writer. After acceptance, the integration owner
fixes it, or performs the reverse explicit handoff before a developer resumes.

## Audited transfer without claiming every child

The installed CLI has atomic `claim`; `reassign` releases and mentions and is
not an atomic ownership transfer. Never describe it as compare-and-swap. Use
this ordered protocol; any partial step stays held/reserved and fails closed:

1. Child holder posts an implementation-ready offer on child and primary item:
   batch/manifest revision, child reservation generation, branch, exact base/head,
   PR, paths, tests/review evidence, acceptance matrix, cleanup, remaining gates,
   and target owner identity. Freeze its branch and PR input. Keep the child claim
   and active reservation while awaiting acknowledgment. No ENV is needed.
2. Integration owner must already hold the exact primary claim. It validates the
   offered tuple, ownership, dependencies, no overlapping writer, and required
   checks, then acknowledges that exact offer in both item threads. This promises
   responsibility; it does not authorize code edits while the child is claimed.
3. Child holder posts `handoff-committed` referencing the acknowledgment, marks
   the canonical child blocked with reason `batch-owned <batch>/<primary>/<owner>
   at <head>` (releasing its claim), and notifies the owner. Do not mark done or
   close its Issue. Retain the active child reservation until final acceptance.
   Dispatcher must preserve the batch-owned hold even though no child claim is
   now present. A free-looking item is not admission.
4. Integration owner verifies the release event and unchanged exact tuple, posts
   `handoff-accepted` in both threads, and only then edits/merges the contribution
   under its primary claim. The child remains blocked against duplicate dispatch.
   Record offered and integrated commit identities; rebases require revalidation.
5. The developer may terminate only after accepted transfer, all its claims are
   released, and it has no external operation. Dispatcher excludes this verified
   terminal bound task from WIP while retaining the reservation as a duplicate
   tombstone. An unbound active reservation always counts. A paused developer
   still counts. Do not close it `completed` merely because implementation landed.

If acknowledgment, release, or receipt is missing/ambiguous, do not merge, create
a replacement, or resume a writer. Reconcile the exact item history/task once.
A stopped integration owner requires an explicit primary-work successor handoff;
protected ENV additionally follows the existing recovery protocol. Never infer
transfer from silence, task disappearance or a PR merge alone.

## States and acceptance

Task-turn completion and Issue delivery state are separate. At startup announce
the assigned role and exact final acceptance owner. At transfer the developer
records a user-visible handoff summary on the child Issue and final task response:
`开发已交接，Issue 未完成`, PR link and actual target branch/merge status,
recipient task id/link, primary item, pending functional/performance/SLS/cleanup
criteria and next concrete gate. Retain the title's Issue/item/PR prefix and add
a short `已交接·待批次验收` status; never label it simply completed or accepted.
Do not edit a frozen PR review input just to publish handoff status.

The recipient maintains the per-child acceptance matrix under its primary claim.
Receipt transfers continued ownership, not just an offer to help: integrate/fix
accepted contributions, wait for release readiness and ENV, perform full-batch
review/CI/main merge/deployment, each child's functional and SLS evidence/cleanup,
and final disposition under the authorized manifest. Do not end at a preparation
commit, integration PR or healthy rollout while useful authorized work remains.
If blocked by admission/dependency/access, identify the exact gate, retained
ownership and next action; never self-expand the manifest or return children to
unassigned. Dispatcher reports that blocker under the recipient, not the donor.

- `implementation-ready`: frozen authored contribution and local/child CI/review;
  no runtime acceptance claim and no Issue closure.
- `integrated`: contribution present in exact integration SHA; children remain
  open with batch-owned holds and reservations.
- `exact-SHA-accepted`: release owner has exact SHA/digests/provenance, unmixed
  healthy environment, each Issue's actual acceptance, the log disposition in
  [staging.md](staging.md), and cleanup. Shared smoke
  alone cannot accept a child. Record not-applicable criteria with reasons.
- `closed`: release owner closes the accepted Issue with its evidence. After
  safely releasing ENV, it may briefly claim each released/blocked child one at a
  time alongside primary, record done and release before the next. No third claim.
  Dispatcher then closes the matching reservation generation as completed.

A00 publishes `minimum-baseline-and-batch-contract READY` independently of Issue
closure. It must contain exact inventory/rules revisions, installed copy hashes,
per-operation baseline/budget freeze, actual policy and demonstrated CI matrix,
known missing measurements and runtime acceptance owner. Unknown DB p95/p99 is
missing evidence, never zero or mock performance. Runtime instrumentation travels
with batch one; preparation does not independently deploy or seed shared staging.

A14 is admitted when all applicable implementations and prior batch evidence are
ready, not when all children already closed. It uses the final batch's exact SHA;
there is no empty extra deployment. A failure of applicable acceptance keeps
that Issue open and its hold/reservation intact. Do not remove functional
prerequisites or authorize final-main merge based on planning readiness.

## CI and release

Audit live rulesets and branch protection for both exact integration branch and
main via `gh`, including App-pinned required checks and strict/base policy. Record
exact SHA and observed checks; names in a document cannot create or waive policy.
Child PRs keep all actual required checks, type/targeted regression and applicable
contract tests. Stable integration runs the full relevant deterministic suite.
Only image publication/deployment work that is not required by real policy may
be deferred to the release candidate. Define/test the classification matrix and
prove it with a genuine minimal CI run before admitting developers. No skip
marker, forged Check, dummy green job or admin bypass. When current policy needs
an image check, satisfy it until policy is explicitly changed by its owner.

Final main PR reviews the complete batch diff and exact current base/head; child
reviews do not substitute for it. After quick gate/self-review, run doctor with
explicit risk-appropriate effort and one wrapper review alongside full CI, with
the user-selected 20-minute cap. Keep inputs frozen, retain process/exit evidence,
and follow actual inactive/shadow/enforced policy. Use host execution according
to current permissions; never pass unsupported escalation fields. No same-tuple
approval shopping, raw Grok, manual Check, reviewer task, or hosted review waiter.

Only the integration owner can acquire ENV after all current-tuple gates pass;
select the acquisition phase using [candidate delivery](staging-candidates.md).
An activated candidate path prepares main CI/images/prefetch outside ENV; the
legacy path still claims before final main merge. Revalidate afterward. Named
read-only acceptance peers can verify their child against the owner's protected
tuple concurrently without acquiring ENV or taking over release ownership.
The ordinary immutable-SHA/digest, rollout, health,
acceptance-repair (one repair/60 minutes), rollback and release rules all apply.
A prefetch consumer must accept the actual grouped workflow artifacts bound to
source revision, component and digest; never regenerate an old per-component
chain for compatibility. Admission requires source CI terminal success before
deploy/prefetch launch, and no waiter occupying the sole runner needed by its
prerequisite. Record precise owner/path/gate when repair overlaps another task.

Freeze separate hosted job minutes, workflow/job counts, image build counts,
deployment count and retry reasons. Three planned batches do not imply a claimed
percentage saving. Heavy fixed-seed scale fixtures stay in an isolated synthetic
environment, never shared million-row staging or bulk real LLM calls.

Epic793 boundaries: batch one #795→#796→#797 and #801→#802; batch two
#798→#799→#800; batch three #803, #804→#805→#806, #807 and final #808.
Preserve #783/#788/#761 dependencies, active path ownership, #674 pause and #648
retirement gate. Only Dispatcher releases eligible child holds/assigns Workers;
A00 preparation owner reports evidence and never performs those transitions.

## Behavioral contract audit

Run `node --test .agents/skills/studio-issue-worker/scripts/*.test.mjs`
from the tracked workspace to exercise partial/stale handoffs, duplicate writers,
WIP including paused tasks, child holds, final acceptance, A14 readiness and
release admission. `check-batch-state.mjs` audits an independently verified
snapshot read-only; it never replaces atomic Squad claim or live GitHub policy.
For accepted/closed children, the audit snapshot's `acceptance.logs` contains
`status: verified`, the accepted release `sha` and sanitized `evidence` pointer;
or `status: not-applicable`, `affectedStructuredLogPath: false` and a concrete
`reason`. Pending/missing/stale evidence fails that acceptance check. These
fields summarize independently checked evidence; filling them never proves it.
