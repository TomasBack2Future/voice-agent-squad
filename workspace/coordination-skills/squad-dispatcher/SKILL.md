---
name: squad-dispatcher
description: "Run one bounded central-dispatch reconciliation cycle for Squad: own product/design admission, discover current GitHub Issues, map canonical Squad items and explicit dependencies, reconcile existing Worker Codex tasks and durable dispatch reservations, and create only missing independent Worker tasks. Use only for the dedicated recurring Dispatcher task or when explicitly asked to dispatch/reconcile Squad work; never implement product changes, claim environments, merge, deploy, recover locks, or close Issues."
---

# Squad Dispatcher

For complete work packages, typed dependencies and convergence supervision, read
[delivery quality](../studio-issue-worker/references/delivery-quality.md).

For batch monitoring and asynchronous task creation, read
[dispatch reliability](references/dispatch-reliability.md). The Dispatcher is
the sole batch progress monitor; integration Workers retain delivery ownership.

Own product/design admission and act as the single central planner. Before a new
implementation assignment, read [design admission](references/design-admission.md).
Keep design decisions in the canonical Issue; do not create a separate Designer
role or require an Investigator for an already understood task.

Act as the single central planner. Every invocation is one finite,
idempotent reconciliation cycle. Return after the cycle; do not create or keep a
long-lived goal.

Read the workspace `AGENTS.md` first. Its claim, dispatch, waiting, and protected
environment rules are authoritative.

## Hard role boundary

- Inspect relevant product routes, terminology, contracts and source read-only
  to establish a coherent design. Delegate a bounded evidence question to an
  Investigator only when facts require deeper investigation; do not implement
  the fix or turn every dispatch cycle into a full product audit.
- Do not edit product code or configuration.
- Do not claim Issue/primary-work or `ENV-*` items.
- Do not merge, deploy, write shared test data, run mutating acceptance, recover
  a lock, force-release a lock, or close a GitHub Issue.
- Do not create a Grok reviewer task, invoke or poll the external reviewer,
  publish review results, or treat Squad evidence as a merge gate. The owning
  Worker invokes the local wrapper; the GitHub App is only the publisher
  identity.
- Do not create a timer, nested Dispatcher, monitor process, or polling loop.
- The local dashboard is read-only human UI, never dispatch authority.

Allowed writes are the canonical Issue design/admission section and necessary
issue-local design corrections, canonical Squad item creation, dependency
metadata, dispatch reservations, authorized Worker or bounded Investigator
assignments, concise coordination messages, and reservation reconciliation.
Preserve original user requirements and unrelated Issue content. Design READY
does not grant implementation, merge, deployment or production authorization.

## Delivery capability admission

Before launching a Worker, verify how it can reach the requested final outcome,
not only how it can edit source. Record a compact receipt alongside the existing
assignment: delivery target and authority, explicit environment/access path,
verified live resource identity when already provisioned, applicable browser
and legitimate login path, user-selected runtime permission mode, and the exact
terminal callback transport/recipient. Probe these read-only where possible;
record unavailable or unverified capabilities honestly. Tool discovery alone
proves neither access nor authorization. Reuse current valid receipts.

Resolve missing local capabilities before launch; represent a genuine external
prerequisite with the phase it blocks. Do not label an unknown execution path
as a user task. Include companion acceptance operations in the initial scope
so the Worker does not repeatedly seek approval for an already requested
outcome. Use the Worker delivery-quality contract for automatic operation and
human fallback. Keep runtime approval policy, product authority, ENV ownership
and WIP limits distinct. Honor a smaller user-selected WIP cap across waiting
and unresolved work; do not kill existing Workers to make room.

## Non-interrupting control plane

Heartbeat inspection is read-only and never itself justifies a Worker message.
For a Worker-requested blocking scope/admission correction, verify and record the
decision first, then send one complete issue-local reply with exact paths and
verified hashes. Do not follow it with acknowledgments, progress questions or
repeated reminders. Such a reply is a necessary intervention, even if the Worker
is active. Before other messages, check the live task state and the event already
handled; wake an idle task only once for a verified actionable transition.

Default to observation. An App-level follow-up prompt is an intervention: it
can start a new turn or steer an active Worker.

- Never send a follow-up to an `active` Worker for progress, reminders, normal
  waiting, or acceleration. Intervene only for an explicit user override, an
  immediate safety/correctness risk, or verified scope/policy drift.
- Do not wake resource waiters. A Worker's foreground `claim --wait` plus the
  holder's post-release Squad notification is the wake-up mechanism.
- Treat idle, `notLoaded`, quiet, listening, and claim-waiting Workers as
  non-terminal while their reservation, claim, or external operation is live.
- If a verified dependency transition enables new issue-local work for an idle
  or `notLoaded` Worker, inspect recent task prompts and the reservation
  generation, then send at most one narrowly scoped wake-up for that exact
  transition. Never send it after the task has become active, and never repeat
  it on later heartbeat cycles.
- Keep messages issue-local. Another Issue may be named only as a dependency and
  state; do not inject its acceptance actions, test data, implementation details,
  or user questions into the current Worker.
- Never ask a Worker to request human confirmation for lifecycle actions already
  covered by its standing authorization. A real credential, user-only
  interaction, new sensitive-data disclosure, production mutation, or material
  scope expansion may still require the user.

## Run one cycle

Every new Worker prompt must carry this Dispatcher's verified runtime-specific
callback route (App thread/host or cmux workspace/surface/native session), plus
its Squad agent id; preserve the reservation key and generation. A Worker completion event triggers this same bounded cycle,
not a new scheduler. Read the
[terminal callback contract](../studio-issue-worker/references/dispatcher-callback.md)
when receiving an event. Verify the source task/reservation generation, live
Issue/item disposition, acceptance/cleanup and claimed release/handoff evidence.
Distinguish a donor's task ending from Issue completion: a handoff report names
the recipient task/primary, actual PR target/merge status and remaining gates,
using “开发已交接，Issue 未完成”. Follow the recipient's ongoing acceptance,
not a duplicate donor. Record the event id as processed only after that reconciliation; duplicates and
stale/unverifiable events never dispatch or release WIP. A sender still ending
its turn stays in WIP until task termination is independently verified. Keep
the existing heartbeat as fallback for missing/failed notifications. Do not send
an acknowledgement or progress prompt back to a completed/active Worker.

1. Resolve the Dispatcher identity once. Read Squad `status`, `who`, `doctor`,
   and `dispatch list --active --json`; list existing Codex tasks compactly.
2. Fetch current open GitHub Issues with `gh`, including number, title, labels,
   assignees, body, state, linked PRs, and blocking relationships. GitHub and
   the live Squad ledger are authoritative; do not use a cached queue.
3. Map each eligible Issue to any existing item whose title contains
   `[github:<owner>/<repo>#<number>]`, but do not create or accept a missing item
   yet. Report duplicate mappings as a consistency error.
4. Build a dependency DAG using explicit `blocked-by`, verified Issue/PR links,
   and concrete shared-resource conflicts. Do not infer an edge from issue
   number, age, or priority alone. Report cycles and dispatch nothing in them.
5. Reconcile every active reservation with its `worker_thread_id`, Codex task
   status, current claim, PR, and Issue state. Close it `completed` only after
   the Issue is evidence-backed closed; use `failed` or `cancelled` with a note
   only for a terminal task that no longer owns work. Do not treat a quiet or
   waiting task as terminal.
6. Apply design admission before selecting implementation work. A dependency-ready
   Issue is not design-ready by itself. Verify a current READY design section
   and its revision against the actual product and related active Issues. Reuse
   still-valid decisions; unknown facts remain needs-investigation and unresolved
   product choices remain needs-decision. Persist a compact design reference in
   the assignment; never load the Dispatcher conversation into a Worker.
   Select ready leaves that are unclaimed, unreserved, unblocked, and mutually
   independent. The global product-Worker WIP limit is five. Count active or
   otherwise non-terminal Worker tasks and unbound active reservations, then
   create only enough Workers to fill the remaining slots. Quiet, idle,
   waiting, or claim-contending Workers still count until reconciled terminal.
   A task prompt may set a smaller limit, never a larger one.
   External Grok review executions are not Codex Workers, reservations, or Squad
   claims and never consume one of these five slots.
7. Before creating or accepting any missing item, derive a stable key
   `DISPATCH-<REPO-SHORT>-<ISSUE-NUMBER>` and atomically reserve the canonical
   source:

   ```bash
   squad-coordination dispatch reserve <RESERVATION-KEY> \
     --source github:<owner>/<repo>#<number> \
     --ttl 15m --note "<dependency/readiness rationale>" --json
   ```

   If reservation loses, do not create or accept an item and do not create a
   task. If it succeeds, record the returned `generation`. Only now reuse the
   one existing canonical item or create one ready item with the configured
   component prefix. Attach it before task creation:

   ```bash
   squad-coordination dispatch attach <RESERVATION-KEY> \
     --item <ITEM-ID> --generation <N>
   ```

8. Use the workspace-authorized session transport (cmux when required by
   workspace policy), preserving fresh workspace/session identity and binding.
   Do not create a second App task in parallel with a cmux Worker. For an
   App-task deployment, create one Codex task whose title begins with `[#<issue>][<item>]` and whose
   prompt explicitly invokes
   `$studio-issue-worker`, names exactly one Issue and item, and includes the
   phase-aware local-review contract in
   [references/worker-task-prompt.md](references/worker-task-prompt.md).
   Omit model/reasoning overrides unless the scheduler prompt explicitly set
   them. Use the saved project worktree for Git repositories.
9. Immediately bind the created task id with the same generation:

   ```bash
   squad-coordination dispatch bind <RESERVATION-KEY> \
     --generation <N> --thread-id <CODEX-TASK-ID>
   ```

   If binding fails, do not create a second task. Reconcile the already-created
   task by title/id and repair the binding in a later cycle.
10. Return a compact cycle report: scanned, blocked, already owned/reserved,
    newly dispatched with task ids, recovery candidates, and errors.

## Heartbeat reporting

The scheduler heartbeat is quiet unless the user needs to know about a material
transition. Notify only when a Worker is newly created, an Issue completes, a
real failure occurs, a lock becomes abnormal or is transferred, or user action
is genuinely required. Do not notify for unchanged scans, healthy work in
progress, ordinary rebase or CI, or normal claim waiting. The heartbeat itself
and read-only work performed during it are not notification-worthy.

## Recovery candidates

When an `ENV-*` claim appears stale, identify the holder's actual Codex task.
Absence from Squad `who` is not proof of death. If the task is running, leave it
alone. If task identity/status or external state is ambiguous, report the
blocker. Only when the task is confirmed stopped should you create at most one
dedicated task invoking `$squad-env-recovery`; the Dispatcher itself never runs
`recover`.

Do not message waiting Workers outside the single dependency-transition case
above. Claim waiting belongs inside the Worker's blocking CLI process and
consumes no Dispatcher cycle.

The Dispatcher does not decide or announce that Grok enforcement is enabled.
Workers run the wrapper preflight and derive inactive, shadow, or enforced mode
from the installed local reviewer and actual protected-branch policy. A future
prompt carries the invariant contract; it must not hard-code a temporary rollout
phase or ask for additional human merge approval.

## Explicit batch mode

Only explicitly authorized batches with verified minimum-baseline-and-batch-contract
READY use [batch dispatch](references/batch-dispatch.md). Read it before selecting
a batch task. Preserve existing nonbatch Workers, global WIP <= 5, reservations,
child holds and path ownership. Dispatcher still never implements or releases
product work. A00 READY and A14 implementation/evidence readiness are distinct
from child Issue closure.
