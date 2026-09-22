# Opt-in batch dispatch

Apply only after explicit user batch authorization and a verified
`minimum-baseline-and-batch-contract READY` checkpoint. Read the canonical batch
contract at `../../studio-issue-worker/references/batch-delivery.md`. The
Dispatcher owns design admission and scheduling: it does not implement, merge, accept releases,
claim primary/ENV, or close product Issues.

Freeze the manifest, integration branch, exact primary release item, CI/policy
evidence revision, at most two development roles plus one integration role,
allowed paths, dependencies, and final acceptance owner. Pass `delivery-mode:
batch-v1` plus that exact manifest and role to each new Worker. Do not convert
active nonbatch Workers. Missing READY/policy/CI proof leaves the hold intact.
A00 READY is distinct from A00 Issue closure; A14 waits for applicable
implementations/evidence, not child closure. Preserve actual functional and
shared-path dependency gates. Use the three Epic793 boundaries in the contract.

Reserve the exact primary canonical source before release-task creation just as
for an Issue, attach the one primary item, and bind the returned generation.
Never duplicate a reservation/task while creation/binding/transfer is uncertain.
The release item is primary ownership, not another ENV lock. Global WIP <= 5
includes every nonterminal development/integration task and unbound reservation;
per batch at most two development owners and one integration owner. Paused tasks
count. Count bound tasks once even when they accept multiple child contributions.

Reconcile handoff events in child and primary threads. An acknowledged and
committed transfer plus verified terminal developer/no external operation allows
its bound task to leave WIP; retain its active reservation and batch-owned child
hold as duplicate prevention until exact acceptance and closure. Do not close
that reservation `completed`, create a replacement or remove a hold at
implementation-ready/integrated. Missing receipt means incomplete transfer.
A newly assigned sequential child requires explicit manifest/path verification;
workers never self-discover the next Issue.
Prefer reusing a verified available chain owner for that explicit next assignment
when doing so avoids re-bootstrap and context transfer. Issue granularity need
not force a new task per child. Keep one active child claim and the current
per-batch/global WIP limits. Distinguish implementation, path-release, acceptance
and external-access edges; record which phase the evidence actually blocks.

Only the release owner supplies per-child exact-SHA acceptance and closes the
Issues. Reconcile completed reservations only against that evidence-backed
closure. A failed acceptance or stopped owner leaves holds/reservations intact
until explicit handoff/recovery. Final child bookkeeping is serial after ENV
release so no owner collects more than primary plus one child claim.

Notify on real readiness/ownership transitions, not periodic observations. A00
reports READY to the Dispatcher; this does not itself release any child hold.

When reporting a donor's terminal handoff, name the recipient task/primary,
actual PR target/merge status, outstanding acceptance and next gate; say
“开发已交接，Issue 未完成”. Keep donor task status separate from child delivery
state. Follow the recipient for remaining work; do not wake/recreate the donor
or require per-child staging deployments. Verify the recipient's continued
ownership; a stopped recipient with pending children is an unresolved handoff/
blocker to reconcile, never evidence that the batch finished. Existing foreground
waits and non-interrupting intervention rules still apply.
