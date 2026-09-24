# Session continuity and outcome ownership

Read when admitting a long-running assignment, amending its scope, diagnosing
verified execution drift or coordinating an authorized takeover. This is a
role protocol, not new CLI functionality or permission to replace active tasks.

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
| Existing monitor | Read-only observation and deduplicated meaningful notifications; no routine progress messages to the executor |

A discovered source defect needs an explicit repair owner and integration owner.
Do not launch another Worker unless dispatch authority covers it. The Deployer
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

## Authorized takeover

1. Identify the old native session, task, reservation owner/generation, claims,
   execution pins and external operation IDs. Verify it cannot continue writing
   by the authorized stop/fencing path. A dead local process does not terminate
   its Actions; assign supervision of any still-running operation.
2. Freeze a handoff receipt: current target and baseline, effective user
   decisions, evidence, known unknowns, operations, remaining acceptance and
   next action. The destination must be able to read this before taking custody.
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
