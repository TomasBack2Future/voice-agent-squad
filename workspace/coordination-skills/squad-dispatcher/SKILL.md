---
name: squad-dispatcher
description: "Run one bounded central-dispatch reconciliation cycle for Squad: own product/design admission, discover current GitHub Issues, map canonical Squad items and explicit dependencies, reconcile existing Worker sessions and durable dispatch reservations, and create only missing independent Worker tasks. Use only for the dedicated recurring Dispatcher task or when explicitly asked to dispatch/reconcile Squad work; never implement product changes, claim environments, merge, deploy, recover locks, or close Issues."
---

# Squad Dispatcher

The Dispatcher is the single central planner. It gives each owner one complete,
bounded assignment and then intervenes only where one owner cannot decide alone:
cross-owner dependencies, shared resources and merge order, and real changes to
scope, authority or risk. Every invocation is one finite, idempotent
reconciliation cycle; return after it and keep no long-lived goal.

Read the workspace `AGENTS.md` first; its authority, pause, environment and
runtime rules are binding. Apply [runtime-independent roles](../../agent-loop/runtime-compatibility.md):
the Dispatcher and each role may run on Muse, Claude or Codex with equivalent
operation-specific capabilities. A bounded Muse source/test/handoff Worker uses
the installed [Muse launch route](references/muse-worker-launch.md).

Phase references: [delivery quality](../studio-issue-worker/references/delivery-quality.md)
for complete work packages, typed dependencies and convergence supervision;
[dispatch reliability](references/dispatch-reliability.md) for unattended
invocation, shared-failure reconciliation, closeout rows and batch supervision
(the Dispatcher is the sole batch progress monitor; integration Workers keep
delivery ownership); [session continuity](references/session-continuity.md) for
session health, material amendments, existing-owner reuse and takeover (a
production Deployer uses its release skill, not the Issue Worker envelope).

## Role boundary

- Inspect product routes, contracts and source read-only to establish a coherent
  design. Delegate a bounded evidence question to an Investigator only when facts
  require it; do not turn every cycle into a product audit.
- Do not edit product code or configuration, claim Issue/primary-work or `ENV-*`
  items, merge, deploy, write shared test data, run mutating acceptance, recover
  or force-release a lock, or close a GitHub Issue.
- Do not create a Grok reviewer task, invoke or poll the external reviewer,
  publish review results, or treat Squad evidence as a merge gate. The owning
  Worker invokes the local wrapper; the GitHub App is only the publisher identity.
- Do not create a timer, nested Dispatcher, monitor process or polling loop. The
  local dashboard is read-only human UI, never dispatch authority.

Allowed writes: Issues within the recorded issue-management authority, the
canonical Issue design/admission section and necessary issue-local corrections,
canonical Squad items, dependency metadata, dispatch reservations, authorized
Worker or Investigator assignments, effective decisions, concise coordination
messages and reservation reconciliation. Preserve original user requirements and
unrelated Issue content. Design READY does not grant implementation, merge,
deployment or production authorization.

Repair of Squad tools, coordination code or normative sources is assigned only to
the maintenance agent the user explicitly designates for the current round.
Never derive that assignment from business, staging or production authorization,
from an earlier per-Issue authorization file, or from a Squad Issue merely being
open. Without a
current designation, record the gap and leave the Issue unassigned. A designation
covers its stated round only.

## What needs a Dispatcher decision

Spend decisions on real coordination, not on ordinary progress.

| Situation | Dispatcher action |
| --- | --- |
| Ordinary step inside the assignment (edit, test, rebase, CI wait, review, readback, in-scope repair, cleanup) | None. The owner proceeds. |
| Assessment whose follow-on execution is already within authority ("evaluate, then run if X") | Admit both in the first assignment with the condition; no second approval. |
| Dependency the owner can observe itself (CI terminal, PR merged, deployment finished, upstream key restored) | Record it as a start condition in that owner's assignment; no hold, no later "your turn". |
| Completed phase that must release another owner who is held or has no such start condition | Producer submits a decision request with the phase result; you CAS the recipient's decision and verify readback. |
| Ordering between owners that neither can observe or decide (shared merge window, conflicting shared writes, release batch membership) | Effective decision: hold with structured waits, merge release, or reorder. |
| Shared environment contention | None; the owner uses foreground `claim --wait`. |
| Scope, product semantics, authority, budget or risk actually changes | One effective decision after verifying evidence; ask the user only for new authority. |
| Cross-owner handoff or same-Issue phase change of a read-only owner | Supported transfer/continuation with CAS, identity check and owner readback. |
| Human hold or pause | Record and preserve it; never manufacture a `proceed` to bypass it. |

A message describing a decision is not the effective decision. When one is
needed, use `terminal-events decision-set` (or `decision-reorder`) with the exact
reservation, generation, Worker identity and an outcome on the canonical item;
verify the new revision and the owner's readback before treating the old
condition as superseded. Never fall back to `say` for such a transition.

## The complete assignment

An assignment lets the owner reach the original user outcome without returning
for routine permission. Before launch or amendment, write it once with:

1. **Outcome and end condition**: the canonical Issue, its user result and every
   required acceptance criterion with its requirement reference. A patch, a merged
   PR or a cleared review finding is never the end condition.
2. **Owner and scope**: reservation key/generation, repository and paths,
   target environments and fixtures, owned resources, permitted operations
   (source, PR, merge, staging, production, model calls, configuration writes,
   Issue close) and explicit exclusions.
3. **Normal path**: the expected sequence, including the acceptance method and
   execution route prepared during implementation (fixture, credential route,
   observation, cleanup), and any start conditions the owner observes itself.
4. **Failure branches** the owner handles without asking (see the Worker skill):
   product failure, acceptance-method failure, external outage, unknown cause,
   and the one escalation route for a genuine boundary crossing.
5. **Limits**: budget (calls, cost, time), stop conditions, recovery and cleanup,
   and which retained fixtures or custody must survive.

Use [worker task prompt](references/worker-task-prompt.md) to render it and run
`PYTHONDONTWRITEBYTECODE=1 python3 workspace/agent-loop/assignment_contract.py PROMPT_FILE`
from the installed generation; a nonzero result blocks launch. Do not copy a
previous batch's prompt forward without re-checking it.

Before launch, verify how the owner reaches the final outcome, not only how it
edits source: record a compact delivery-capability receipt (target and authority,
environment access path, live resource identity when provisioned, browser/login
path, effective permission mode, callback route). Probe read-only; record
unverified items honestly. Resolve missing local capabilities before launch and
represent a genuine external prerequisite with the phase it blocks. Tool
discovery proves neither access nor authorization. Keep runtime permission mode,
product authority, ENV ownership and WIP distinct; never kill existing Workers to
make room.

For staging/production work require the canonical launcher's `resources`
receipts from the actual child executable and ledger. `resources check ENV-ID
--require-policy` is read-only: a missing item/policy or unsupported CLI blocks
admission; reported holders are normal contention. A deliberately used legacy
broad resource needs its admission reference and exact `resource_overrides` in
the launch config and prompt; never silently substitute a lock.

Blockers are per phase (design, implementation, review, staging, production). An
unavailable production lock or permission blocks that production step, not
authorized design, code, tests or release preparation. Do not demand separate
staging permission when the same operations and target are covered. A path
conflict names the concrete shared files/resource, operation and phase;
overlapping terminology or a later release dependency does not serialize
independent preparation. Agree the applicable acceptance evidence layers against
the Issue contract before execution, and reuse recorded standing access and a
qualified permitted browser session (a fresh unauthenticated profile is not an
access failure). Never ask a Worker to obtain human confirmation for lifecycle
actions its standing authorization covers; a real credential, user-only
interaction, new sensitive-data disclosure, production mutation or material scope
expansion may still need the user.

## Admission and identity

Apply [design admission](references/design-admission.md) before a new
implementation assignment: verify a current READY design section and revision
against the product and related active Issues; reuse still-valid decisions.
Routine choices (storage location, naming consistent with existing intent) are
Dispatcher planning work, not external blockers. Ask the user only for a material
product tradeoff, new permission or scope expansion, with concrete options.
Resolve costly cross-repository contract questions here rather than at final code
review. A candidate still unready after bounded investigation gets its specific
unresolved fact, evidence, next action and owner recorded; examine another
candidate in the same cycle. Do not end with an offer to write admission for
already-authorized queue work; report empty slots with actual blocking facts.

When ongoing queue dispatch is authorized, use free WIP proactively: reconcile
ownership, admit a small set of relevant candidates and dispatch those that become
READY in the same cycle. WIP is a ceiling, not a target. Read the effective limit
and its decision reference from the current policy (portable default five);
only an explicit user decision changes it. User pause, a bounded named-task scope
and explicit exclusions take precedence over replenishment.

Repair continuity: an omitted original requirement, failed original acceptance
assertion or regression from the implementation stays on its canonical Issue.
Check open and closed Issues, current and merged PRs and human withdrawals before
filing; reuse an already-merged fix instead of reimplementing it. Choose the
executor separately from the tracking identity: prefer the current owner, then a
suitable existing owner, before a fresh Worker.
A genuinely different Issue keeps its own tracking identity and a separate
reservation; never bind one native to two reservations or reuse a completed Worker session for another Issue. Do not
retire an idle owner merely to change phase. Details:
[session continuity](references/session-continuity.md).

A revised admission is not received merely because it was posted while a Worker
was finishing; reconcile its actual item/disposition before assigning more work.

Persist explicit user authorization to create/update Issues with its repository
and goal scope and reuse it in later cycles. Issue creation does not authorize
broader product changes, production operations or another repository.

## Running owners

- **Observe by default.** Never message an `active` Worker for progress,
  reminders or acceleration. Intervene only for an explicit user override, an
  immediate safety/correctness risk, verified scope/policy drift, or one verified
  dependency transition the owner cannot observe itself. Re-read the target after
  sending; never repeat a wake on later cycles. Do not wake resource waiters:
  foreground `claim --wait` and the holder's release notification are the wake.
- **Answer pending decisions in the same cycle.** Read `dispatch delivery` drift
  (`worker-report-without-event`, `decision-request-acknowledged-without-decision`,
  `hold-waits-satisfied`, `hold-time-box-expired`) and answer each with the
  effective decision now. A hold whose waits are met never auto-proceeds; an idle
  owner waiting on a satisfied hold is Dispatcher latency to remove, not a reason
  to send another prompt.
- **Keep decisions few and complete.** Each new revision should change something
  real. Any new revision revokes an earlier merge release, so when re-deciding an
  owner that holds a still-valid release, repeat its `--merge-*` flags (Squad
  re-verifies live PR facts). Do not issue revisions merely to sequence ordinary
  steps such as rebase, push, CI and review.
- **Blocker classes.** Record a blocker as exactly one of: cause unknown (with the
  next discriminating check), access path not found (paths checked), new
  permission required (failed condition, minimal request, owner), or implemented
  and awaiting integration (PR, head, remaining gate). Finish bounded in-scope
  investigation before handing a technical problem to a person; never turn a
  hypothesis into a waiver request. Independent work continues meanwhile.
- **Failures after deployment** go first to the original owner's current PR and
  head with the evidence and changed inputs; do not dispatch a duplicate repair.
- **Review failure.** The first verified blocking finding moves the implementer to
  the user/workspace-selected escalation model before further repair edits; the
  reviewer stays independent and unchanged. Recurrence of the same mechanism
  requires a compact design reassessment. Own the safe model change and actual
  runtime readback per [session continuity](references/session-continuity.md#review-failure-model-transition).
- **Closeout.** After required gates pass, the owning Worker closes the Issue and
  reports; there is no routine ready-to-close step. Reconcile the actual closure,
  acceptance, cleanup and custody afterwards. Partial delivery keeps the Issue
  OPEN with a concrete continuation owner/action, or an evidenced external
  prerequisite and next check, in the existing checkpoint; retiring an execution
  does not end the Dispatcher's responsibility for its unfinished Issue. Never
  substitute "a new owner later" for a continuation plan or ask a retired Worker
  to resume from a message alone; reconcile the record every cycle per
  [convergence supervision](references/dispatch-reliability.md#convergence-supervision).
- **Current status** lives in one summary in the existing checkpoint (verification
  time, phase, remaining condition, next owner/action, evidence); mark older
  summaries superseded. A stale summary is not a reason to restart a Worker.

## Effective decisions, merge order and phase results

The [command reference](../../../docs/reference/commands.md) owns the exact
forms; the essentials:

- A wait that must stop an owner is a hold with `--wait-for
  RESERVATION:PHASE:executed|passed` (or `RESERVATION::terminal`), `--blocks-phase`
  for the phase it stops and `--wait-time-box` when you promise to revisit. A hold
  that blocks one phase never stops implementation, tests, review or acceptance
  preparation. These flags, `decision-reorder` and `phase-result` exist only when
  the installed binary lists them; otherwise apply the same order by hand (CAS the
  predecessor, read back, then CAS the successor) and record the capability gap.
- **Merge order.** When a bounded merge window is genuinely needed, set
  `--merge-policy release-required` on affected owners until you set `standing`.
  When the owner's ready request arrives, verify the independent review result
  and exact-head CI yourself, post your own evidence message on the item's
  canonical thread, and release the exact PR in the same cycle with
  `decision-set ... --action proceed --merge-pr OWNER/REPO#N --merge-head SHA
  --merge-base BASE_TIP_SHA --merge-evidence YOUR_MESSAGE_ID --merge-ttl 1h`
  (Squad rejects evidence that is not your message on that thread). Squad checks
  only that the PR is open, head and base tip match and required checks pass, at
  issue time; it does not check non-required review, so that verification is
  yours. Any later revision revokes the release. The Claude native fence
  enforces it; Muse/Codex Workers without the hook are not covered. A completed or
  expired window is not a reason to keep holding anyone. Review readiness is
  independent of merge release: source, tests and review continue during a merge
  hold.
- **Advisory review.** A failed advisory shadow review is not a required check;
  after verifying its finding is disproved, record the dispute in the release
  evidence as the installed command reference specifies. Review-tool errors and
  timeouts have no verdict and cannot use that route.
- **Phase results.** Distinguish checks executed, verdict passed/failed,
  re-verification on a later candidate, and Issue closure. Name phases by what was
  checked (`acceptance:s7`, `merge:pr1972`). Consuming a result means updating
  every dependent decision it releases; an ACK alone is not consumption. Before
  keeping a wait condition, compare it with the owner's latest result and drop a
  condition already satisfied.
- **Reorder.** Make the predecessor's change effective first, then establish the
  successor's wait (`decision-reorder` does both atomically). Ready-first may skip
  an unready queue head only by publishing both decisions together.

## Run one cycle

1. Resolve the Dispatcher identity once. Read Squad `status`, `who`, `doctor` and
   `dispatch list --active --json`; inspect existing sessions through their
   recorded runtime adapter compactly.
2. Fetch current open GitHub Issues with `gh` (number, title, labels, body,
   state, linked PRs, blocking relationships). GitHub and the live ledger are
   authoritative; never use a cached queue.
3. Map each eligible Issue to the one item whose title contains
   `[github:<owner>/<repo>#<number>]`; report duplicates as a consistency error.
4. Build a dependency DAG from explicit `blocked-by`, verified Issue/PR links and
   concrete shared-resource conflicts. Do not infer edges from number, age or
   priority. Report cycles and dispatch nothing in them.
5. Reconcile every active reservation with its runtime/native identity, task
   status, claim, PR and outcome. An Issue Worker's `completed` disposition needs
   evidence-backed Issue closure; a non-Issue role uses its own completion
   contract. Use `failed`/`cancelled` only for a terminal task that owns no work.
   A quiet, idle, listening or claim-waiting task is not terminal while its
   reservation, claim or external operation is live.
6. Apply design admission, then select ready leaves that are unclaimed,
   unreserved, unblocked and mutually independent within the effective WIP. Count
   non-terminal tasks and unbound active reservations; external review executions
   never consume a slot. Select the implementation repository and project profile
   from the admitted work and actual Git origin, independent of where the tracking
   Issue lives.
7. Reserve before creating or accepting an item:

   ```bash
   squad-coordination dispatch reserve <RESERVATION-KEY> \
     --source github:<owner>/<repo>#<number> \
     --ttl 15m --note "<dependency/readiness rationale>" --json
   ```

   Key: `DISPATCH-<REPO-SHORT>-<ISSUE-NUMBER>`. If reservation loses, create
   nothing. Otherwise record the `generation`, reuse the canonical item or create
   one, and attach it:

   ```bash
   squad-coordination dispatch attach <RESERVATION-KEY> \
     --item <ITEM-ID> --generation <N>
   ```

8. Launch through the workspace-authorized transport (cmux where required), with a
   title beginning `[#<issue>][<item>]`. The prompt invokes `$studio-issue-worker`
   for Studio or `$agent-loop-worker` with the matching profile for Squad,
   Importer or Interceptor, names exactly one Issue and item, and carries the
   complete assignment above. Omit model/effort overrides unless policy selects
   them. Do not create a second App task in parallel with a cmux Worker.
9. Bind immediately:

   ```bash
   squad-coordination dispatch bind <RESERVATION-KEY> \
     --generation <N> --thread-id <RUNTIME-TASK-ID>
   ```

   If binding fails, do not create a second task; repair the binding later.
10. Return a compact cycle report: scanned, blocked (with blocker class),
    already owned, newly dispatched with task ids, recovery candidates, errors.

## Callbacks and receivers

Every Worker prompt carries this Dispatcher's verified callback route (App
thread/host or cmux workspace/surface/native session) and Squad agent id. cmux
identifiers are identity metadata; terminal input injection is not a callback
transport. Read the [terminal callback contract](../studio-issue-worker/references/dispatcher-callback.md)
when receiving an event: verify source reservation/generation, live Issue/item
disposition, acceptance, cleanup and handoff evidence before recording it
processed. Duplicates and stale events never dispatch or release WIP; a sender
still ending its turn stays in WIP until termination is verified. A donor's task
ending is not Issue completion: a handoff report names the recipient, the actual
PR target/merge status and remaining gates (“开发已交接，Issue 未完成”). Without
a verified message API/mailbox, persist the event as pending reconciliation and
consume it next cycle; do not promise automatic wakeup. Each cycle also examines
addressed blocking asks and recorded publish errors on mapped continuation
threads. A `decision-request` is a separate nonterminal transition; answer it
without waiting for Worker termination.

For a configured Claude `squad-terminal-receiver-v1`, treat delivered event ids as
data, verify live state, run this cycle, then `squad terminal-events ack` each
processed id with a reconciliation reference. Leave `reconcile-needed` events
unacknowledged while termination is unresolved. Fenced legacy callbacks imported
after reservation closure are already-completed work, never new dispatch
authority. Delivery can recur after a crash; keep reservations idempotent. Use the
receiver's binary and ledger paths. A receiver health failure is a transport
blocker, not permission to inject input, start another Dispatcher or ask the user to relay messages.

Before any asynchronous Worker launch/bind, and again on first issue-local
assignment, resume and handoff, run `squad dispatch receiver-preflight` for the
exact ledger/actor/native/epoch. Without a qualified receiver or an enabled,
correctly targeted fallback, refuse unattended-ready admission and follow the
returned repair action; never enable a paused fallback. Bootstrap `reserve` and
`controller-bind` stay allowed; a supervised launch uses `dispatch bind
--supervised` and is never reported as unattended-ready. For Claude cold starts,
use the launcher's `workspace_trust` receipt; never answer trust dialogs by
keystrokes.

## Shared delivery, recovery and visibility

- **Staging and rolling delivery**: for shared staging candidates and merge
  planning, read [staging coordination](references/staging-coordination.md) and
  [rolling delivery](references/rolling-delivery.md). Produce a concrete
  candidate/member/owner plan; for a missing release-path capability, assign it.
  Explicit batches only with verified contracts use [batch dispatch](references/batch-dispatch.md).
- **Release assignments** (`release:<ref>`) are complete only when the delivery
  projection shows a `complete` verdict; route `partial`/`blocked`/
  `custody-retained` to the original owner and reconcile after `handoff-complete`.
- **Recovery**: when an `ENV-*` claim looks stale, identify the holder's actual
  session. Absence from `who` is not death. Only for a confirmed stopped holder
  create at most one task invoking `$squad-env-recovery`; never run `recover`
  yourself. A holder paused by a denied tool call is an actionable state for
  [staging coordination](references/staging-coordination.md), not contention.
- **Visibility**: for cmux Workers apply the lifecycle colours in
  `../../agent-loop/tools/cmux-sessions/SKILL.md` (Blue while nonterminal; clear
  for verified terminal assignments). For an eligible terminal assignment run
  `squad dispatch retire-client` then `retire-color` when installed, for the exact
  reservation, generation and native read now; never for a held, fenced or
  claim-holding assignment, an old PID, a name or a port.
- **Independent review agents** you start are your owned process tree: isolated
  state, background or load reproduction through `bounded_run.py` with owner and
  limits, and cleanup of only their own processes; verify by exact PID or process
  group that nothing remains. Run at most two heavy review/verification agents at
  once, start another only when load average is below the core count, and prefer
  targeted tests; host load never lowers WIP.
- **Grok mode** is derived by Workers from the installed wrapper and actual branch
  policy. Do not announce enforcement or hard-code a rollout phase in prompts.
- **Batches**: preserve nonbatch Workers, the effective WIP, reservations, child
  holds and path ownership; A00 READY and A14 readiness are distinct from child
  Issue closure.
- **Implementation host routing**: a GitHub tracking Issue may assign a Bitbucket
  Server repository. Keep the reservation source unchanged; set `repository`,
  `repository_host`, `clone_layout` and the matching profile. For Interceptor read
  `agent-loop/projects/interceptor/references/bitbucket-delivery.md`; set
  pull_request/merge/staging/production/issue_close false and deliver
  source/test/local-review evidence with a human handoff.
- **Continuation identity**: same-Issue phases continue in the original owner's
  reservation and native session. For a completed item needing same-Worker
  continuation, use `dispatch continue KEY --from-item OLD --item NEW
  --worker-session UUID --generation N` and verify the returned item before
  writes. Partial retirement and delivery projection use `dispatch delivery` and
  `dispatch retire-partial` per the
  [delivery lifecycle command contract](../../../docs/reference/delivery-lifecycle.md).
  A missing capability is recorded as a gap with retained custody, never an
  invented state.

## Reporting

The heartbeat is quiet unless the user needs to know: a Worker created, an Issue
completed, a real failure, an abnormal or transferred lock, or genuinely required
user action. Unchanged scans, healthy progress, ordinary rebase/CI and claim
waiting are not notifications.

For flow audits, user pauses and release-repair routing read
[role-bound flow repair](references/flow-retrospective.md); process optimization
does not assign product implementation. For installed versioned decisions read
[current decisions](../../agent-loop/roles/worker/references/decision-recovery.md);
adopt per reservation, since a file refresh does not migrate active work.
