# Assignment continuity and effective adoption

Use for launching or resuming asynchronous Dispatcher, Worker or Deployer work,
recovering a failed model connection, advancing a phase, and rolling out changed
instructions. This is an operating contract, not a network repair service or a
claim that any client implements automatic supervision. Use the existing
assignment, checkpoint, receiver and reconciliation cycle.

## Admission belongs to the controller

Resolve the responsible Dispatcher and its actual reservation scope before a
new asynchronous role session is created, including sessions requested in a
Codex App chat or launched through a shell. The originating chat hands over the
user's outcome, authority, exclusions and delivery endpoint; it does not reserve
under its own identity and expect another actor to close that reservation.
The Dispatcher creates/reserves/binds through the supported role-specific path.
No healthy controller means a concrete pending handoff, not an orphan launch.
An explicitly authorized standalone task may retain a different closure owner,
but its ownership and return route must be accepted before launch.

For an existing orphan, preserve its native session, work and external operations.
Use supported ownership transfer only after its prerequisites are met. If no
transfer is supported, the current legitimate owner retains closure duty and
records the successor's accepted handoff. Never impersonate the old actor, edit
SQLite to rebind ownership, or restart deployment to repair administration.

Verify the result path in both directions: producer, recipient, durable event,
receiver and handling. When safe wake is unavailable, assign pending delivery to
the existing bounded reconciliation cycle and verify its actual invocation route.
Do not describe a stored message, a cmux workspace or a running Goal as a working
receiver. Event publication failure is delivery work, not a reason to discard the
outcome; reconcile the error, current decision and owner before retrying it.

## Permissions are effective runtime state

Resolve current task choice, then still-valid workspace preference, then runtime
default. Pass the chosen mode explicitly on new sessions and supported resume.
Claude `auto` is not `bypassPermissions`; Muse YOLO and Codex full-access/never
are runtime-specific choices, not interchangeable command-line flags. Read the
installed CLI contract and verify both process configuration and actual runtime
state. Preserve model and effort. Do not infer a child's mode from its parent.

A mismatch is a launch/continuation defect: correct it using existing human
permission at an owned safe boundary, preserve the native session and custody,
and verify the old local writer joined before a replacement client resumes.
Do not interrupt an in-flight mutation merely to reload instructions. Explicit
user pauses and narrower current task decisions remain authoritative. Permission
mode never grants production, disclosure, environment or implementation scope.

## Failed model connections require an outside observer

A Goal preserves intent; its retry loop can terminate with the failed turn.
Separate provider retries, agent-turn continuation and external-operation status.
Use the already-authorized monitor or Dispatcher cycle outside the failed model
session; do not create a second controller, timer or perpetual prompt loop.

Before relying on unattended work, qualify that observer's actual enabled state,
target and supported continuation route in the existing checkpoint. A paused
observer remains paused; report unattended recovery as unavailable rather than
promising it from a skill or adding another timer. A failed turn with an idle ENV
claim is actionable in the existing owner cycle: reconcile external operations
and, if safe and within authority, have its owner release the idle resource.
Never infer safety from elapsed time or release another owner's claim.

On a failed turn, read the last error and the subsequent native execution events.
Classify first-output/stream timeout, connection-refused/reset, proxy unavailable,
rate limit, authentication failure, explicit pause and unknown failure separately.
For a configured local relay, bounded read-only listener/process and endpoint
health checks distinguish a missing local service from a remote failure. A TCP
listener alone is not a healthy model response; mark unproven conditions unknown.
A Claude launch configuration may declare `model_endpoint`, an https origin chosen
by the project or runtime configuration (never a Squad default). Cold start, check
and same-native resume then force that value into the child environment and fail
with an explicit diagnostic when the client's settings layers (config directory,
project, local; these override inherited environment) still name another address,
such as a retired loopback tunnel, or when the endpoint is unreachable over HTTPS.
The launcher never rewrites settings or falls back. A passing check proves the
configured address and reachability, not that a running client uses it: confirm an
actual new model response and its connection separately.

Do not expose credentials, send customer prompts as probes, change providers,
restart a shared relay or alter credentials without the corresponding authority.

When human authority covers transient recovery and the failed condition is ready
for retry, reconcile claims and any already-submitted external operation first.
Resume the same native session from the current checkpoint through its supported
interface. Reattach to existing CI/review/rollout instead of submitting it again.
Deduplicate by native session, failed turn and error fingerprint; use the granted
attempt budget, or one bounded continuation if none is specified. Persist attempts
before dispatch. A new failed turn does not reset the budget for an unchanged
outage. Authentication, user pauses, scope decisions and deterministic failures
are not automatically retried. Exhaustion or unknown transport health goes to a
named repair owner with the failed check and next action; do not loop forever.

Verify new native model/tool execution after resume. A delivered instruction,
active Goal or living process is insufficient. If recovery awaits new authority,
raise that exact missing operation promptly in the existing monitor cycle; keep
independent authorized work moving. Report transport health and task recovery
separately. Instructions alone do not qualify an unattended recovery capability.

## Phase completion and changed inputs

A phase ending advances to its prepared, authorized successor without requesting
unchanged authorization, and the same Worker and native session keep the Issue;
only a planned role boundary, an unrecoverable owner or a human reassignment
transfers ownership (see the Dispatcher's session-continuity reference for the
state vocabulary, shared-cause repair ownership and takeover summary). If a native Goal ends at an intermediate milestone,
retain the assignment as waiting and record the next trigger/owner; do not equate
Goal 100%, PR merge or event ACK with the user's requested endpoint. A genuine new
phase permission remains a named hold. Before closure reconcile Issue disposition,
reservation, primary claim, ENV custody and external operations under their
existing owners; do not reacquire ENV merely for administrative cleanup.

### A result is handed off only by a formal event

Distinguish four things: an ordinary message (context), an input or decision
acknowledgement (transport), a phase result (the checks of a phase ran, with a
verdict) and task completion (Issue gates met). When the next step belongs to
someone else, the owner reports the finished phase in the same call that wakes
that owner: `terminal-events submit --kind phase-result` (or `decision-request`
for a question, `blocked`, `issue-closed`). A plain message is never the
handoff; after a merge release, Squad turns such a report into a
`reconcile-needed` wake as a safety net, not as the contract.

The states after a result are separate: stored but not delivered (pending),
delivered but not handled (redelivered to a restarted receiver), handled but the
downstream decision missing (`decision-request-acknowledged-without-decision`)
and decided but not executed (no owner activity after the decision). A report
that produced no event shows as `worker-report-without-event` in `dispatch
delivery`. A retry of the same request key never repeats its side effects;
merges, deployments, fixtures and model calls are never repeated to re-trigger a
handoff.

An asynchronous operation (CI, review, rollout) is bound to its exact input,
the identity that runs it, the owner who handles its result and the next step.
Starting a watch is not closure: on its notification the owner reads the real
result, confirms the input is still current and acts. Waits stay interruptible
(native background tasks or bounded reads), so a delivered decision is handled
while an operation runs.

### Recovery is proven by the next step, not by connectivity

After a sleep, network or model outage, reachability, a living process and an
online receiver are preconditions only. Recheck, in order: an actual model
call, the native session binding, every external operation's real state, results
completed during the outage and not yet consumed, and the recorded next step.
Consume completed operations instead of resubmitting them. Re-evaluate ordinary
dependency holds against new evidence; explicit human pauses stay paused.
Recovery is complete when the original owner's next step is observed: new owner
activity after the recovery time (`last_owner_activity_at`) and the matching
state change (a new event, decision or phase result). A supervisor that finds a
repairable break hands it to the right owner through the existing route and
verifies the handling; repeating the same report without a handler is not
progress. If no supported route can act, report that capability gap instead of
claiming unattended readiness.

Before freezing delivery, exercise the real invocation boundaries that are
applicable: launcher arguments, workflow/script CLI, event/reply route, effective
configuration and a business trigger. Reuse valid checks. If another authorized
operator changes a relevant input, record the delta and notify the current owner
through the authorized route; repeat only checks whose applicability changed.

## Install, adopt, then observe

Edit one versioned normative source; synchronize exact reviewed files through the
maintained installer into every selected client's discovery roots. Preserve local
working agreements and paused automations. File/link equality is installation
proof only. Do not change a binary, schema, frozen application candidate or
runtime permission simply because its containing package was refreshed.

For authorized live adoption, inventory nonterminal assignments and extant role
sessions, including idle/blocked ones and externally running operations. Send one
complete, scoped adoption instruction by a verified durable route or an explicitly
human-authorized session intervention, respecting unsent drafts. Active owners
adopt at the next safe boundary. A terminated session cannot acknowledge; record
its outcome and bind the new policy to any authorized future resume instead of
reviving completed work. A paused task can acknowledge policy without resuming it.

In existing adoption/checkpoint evidence record source revision and content hashes,
installed client entry, native/actor/reservation generation, actual files read,
effective decisions and mode, unchanged custody, and next authorized action.
Distinguish sent, received, read/adopted and behavior observed. `squad dispatch
receivers` lists which binary each live receiver/MCP process actually runs
against the installed target; a pinned older binary is not adopted until its
owner's supported restart or resume rebinds it. Verify the receipt
and subsequent relevant action; do not declare all sessions effective because
links changed or the Dispatcher acknowledged for everyone. Missing acknowledgments
remain explicit pending adoption with an owner and next safe opportunity.
