# Session continuity and outcome ownership

Read when admitting a long-running assignment, amending its scope, diagnosing
verified execution drift or coordinating an authorized takeover. This is a
role protocol, not new CLI functionality or permission to replace active tasks.

Apply [runtime-independent roles](../../../agent-loop/runtime-compatibility.md)
for Muse, Claude and Codex. The following role boundaries apply independently
of the selected runtime; the Worker envelope does not cover every role.

## Assignment and responsibility

Keep the assignment stable and short: requested outcome, exclusions, role,
canonical skill/profile, current checkpoint location and verified callback route.
Separate launch-time source identity from the current authorized target. Put
changed decisions in the current checkpoint with evidence and superseded decision
IDs; keep history separately. Do not send another full assignment on resume or
require the user to restate an authorization already recorded for the same task.
The owning role defines its checkpoint format; the ledger owns actual custody.

| Role | Responsibility |
| --- | --- |
| Dispatcher/coordinator | Scope/dependency decisions, assignment identity, session routing and reservation reconciliation; no production writes or ENV recovery |
| Deployer/integration owner | One release's current state, execution, acceptance and operational closure under its own skill and authority |
| Repair Worker, when assigned | Bounded source fix and delivery evidence; does not acquire the Deployer's production authority |
| Reviewer | Bounded verdict under the actual review contract; does not own the release |
| Investigator | Bounded evidence collection and diagnosis under assigned access; no implied implementation, ENV or release authority |
| Existing monitor | Read-only observation and deduplicated meaningful notifications; no routine progress messages to the executor |

A discovered source defect needs an explicit repair owner and integration owner.
Do not launch another Worker unless dispatch authority covers it. Squad-tool repair
authority comes only from the user's current designation of a maintenance agent, never
from business, staging or production authorization. The Deployer
may perform an in-scope repair when authorized; record that phase without silently
changing its role or dropping its release responsibility. A PR merge is not
production acceptance. No extra permanent role or timer is required.

At admission, reuse the delivery capability receipt and verify a small read-only
step that actually resolves target/access/ownership when available. A model
response, valid permission flag or executable discovery proves neither task
readiness nor environment access. Unknown access is a phase-specific prerequisite,
not automatically a request for the user to supply discoverable coordinates.

## Health and bounded correction

During the existing reconciliation cycle, inspect progress against the current
phase and external operation, not wall-clock silence. CI, review, rollout, lock
waiting and explicit user pauses retain their normal ownership semantics.

Evidence of drift includes repeatedly requesting the same granted permission,
contradictory current-target records, unsupported root-cause claims, repeated
diagnosis without new evidence after an actionable cause is established, or
abandoning delivery for unrequested cleanup. Distinguish a genuine new scope or
access decision from a redundant question. Record the concrete discrepancy.

For verified scope/correctness drift, publish one bounded correction through the
authorized control-plane path: effective decision, evidence, retained outcome
and next owner/action. Verify uptake through the next checkpoint or bounded
read, not another acknowledgement/progress prompt. Persistent drift after that
correction can justify proposing takeover; it does not authorize killing a
session or stealing locks. An explicit user takeover request still requires
custody verification. Compare providers using comparable evidence; record
handoff overhead separately rather than attributing all delay to a model.

## Continuous ownership

One Issue keeps one reservation, one primary item and one native session until its
own closeout. Waiting for CI, review or an environment, a completed phase, a
temporary block, a pending Dispatcher decision or a model/connection failure does
not end the reservation, start a Worker or change the owner; resume the same
native from the current checkpoint. Release `ENV-*` and other shared resources
at their safe boundary while ownership stays, and keep counting the assignment
in WIP until its terminal reconciliation. A user pause or HOLD is never lifted by
this rule. A different Issue always gets a new reservation, item and native;
`dispatch bind` rejects a native already bound to another reservation, so a
finished or idle session cannot be reassigned to save launch time.

## Shared causes: one repair owner, many acceptance owners

Treat a failure as shared only after comparing failed phase, environment,
component revision, error code or failed condition and trace/regression evidence;
similar text or a similar symptom alone is `unconfirmed`, and a later record that
traces it to a Worker's own fixture or input reclassifies it. A confirmed cause
gets exactly one canonical repair owner: reuse its existing Issue, PR and fix
evidence, and check that owner's current PR/head and whether the fix is merged,
integrated or deployed before creating any new implementation. Every other
affected Issue keeps its own Worker and acceptance gate, records the repair as a
dependency, waits without changing owner, and then reruns its original failing
path. Coordination-tool defects are routed to the designated maintenance agent,
never explained as a business root cause or moved into a business Worker.

## Handoff means ownership transfer

Transfer ownership only for a planned role boundary recorded at admission (for
example a donor-to-integration owner), an original owner proven unable to
recover or to continue safely in its session, or an explicit human reassignment.
Name the receiver, its capabilities, permissions and phase boundary before the
transfer. A completed phase, a shared dependency or a stalled terminal is not a
handoff. Do not call a transfer complete until the receiver's binding, claims and
callback route are read back and the old writer is fenced; there must never be two
effective writers. If no supported transfer exists, report the capability gap and
keep the original owner's custody. Use `worker-handoff`/`takeover` only through
their documented routes; never recreate a worker to bypass a ledger fault.

## State vocabulary

Checkpoints, progress summaries and events use one of these states and never one
for another:

| State | Owner | Meaning | Event |
| --- | --- | --- | --- |
| `phase-complete` | original owner | a phase finished; the owner continues | none (checkpoint/progress summary only) |
| `waiting` | original owner | named dependency or decision, with resume condition | none; `decision-request` only for a real decision |
| `transferring` | old owner until readback | transfer prepared, receiver not yet confirmed | none |
| `receiver-confirmed` | receiver | binding, claims, callback and summary readback verified | `handoff-complete` |
| `task-complete` | closed | Issue acceptance, cleanup and release done | `issue-closed` |

`handoff-complete` is a terminal ownership-transfer event and the donor ends after
it; consumers retire or release the donor. Never publish it for a phase report,
and never publish it twice for the same Worker generation. Phase reports that
must unblock another reservation use a `decision-request` as already required.

## Takeover summary

Keep the takeover summary in the existing checkpoint (no new ledger) and update
it at each material phase change. It holds: exact identities (repo, branch, PR
head, candidate/image, reservation, generation, native, item); confirmed cause,
excluded hypotheses and unknowns; the effective decision revision, authorizations,
pauses and prohibitions; in-flight operations, claims, ENV and cleanup custody;
remaining acceptance, next action and resume condition. A receiver reads the
summary and its references, rechecks volatile facts (head, decision revision,
claims, operation state) and does not redo valid investigation. A decision
revision newer than the summary supersedes it; an older decision never overrides
a newer one.

## Authorized takeover

1. Identify the old native session, task, reservation owner/generation, claims,
   execution pins and external operation IDs. Verify it cannot continue writing
   by the authorized stop/fencing path. A dead local process does not terminate
   its Actions; assign supervision of any still-running operation.
2. Freeze the takeover summary (see above) as the handoff receipt. The
   destination must be able to read this before taking custody.
3. Verify destination capabilities for the actual role, provider/native task ID,
   ledger binding and callback route. Use supported fenced ownership operations;
   never force-register/impersonate the old identity or substitute a handoff note
   for an atomic transition. Protected ENV recovery belongs to its authorized
   recovery owner, not the Dispatcher. User scope persists; holder-bound execution
   credentials do not transfer by copying them.
4. Reconcile reservation and callback ownership as well as task/ENV ownership.
   If no single atomic cross-resource transfer exists, keep the new writer
   inactive until each required binding has been read back. Unsupported rebind
   is a capability gap, not permission to invent a command. The original
   reservation owner either performs supported reconciliation or explicitly
   retains its closure duty; a successor must not impersonate that owner.
5. Record the recipient's readback of target, decisions, operations and next
   action. Resume from the verified phase, not the original assignment. Do not
   restart the old writer or repeat user approval merely to complete handoff.

## Terminal reconciliation

Name the reservation closure owner and callback recipient at launch and refresh
them at takeover. On a terminal event, independently verify the assigned outcome,
external operation status and claim release or acknowledged transfer. Issue
Workers still require evidence-backed Issue disposition; a non-Issue release
uses its own acceptance contract rather than an invented Issue-close gate.
Only the legitimate reservation owner closes it through the supported interface.

Record event acknowledgement and reservation closure evidence in the existing
cycle. If operational delivery is complete but owner-only administration remains,
report those states separately and route the concrete closure obligation to its
owner. Do not reacquire an environment, relaunch deployment or ask the user to
relay messages. Missing callback transport remains pending reconciliation in
the existing cycle; do not claim delivery or introduce a new polling process.
