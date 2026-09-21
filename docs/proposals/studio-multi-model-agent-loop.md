# Studio Multi-Model Agent Loop — Design Draft

This is a proposal for an opt-in workspace operating model, not an activated
workflow, a repository contribution requirement, or a deployment instruction.
It is stored with Squad because it proposes coordination capabilities and an
adoption package. Product repositories must not inherit these role policies.
Approval of this document is separate from authorization to implement, migrate,
start sessions, collect usage, publish changes, or deploy.

The design builds on the Data Analyze practice while removing its dependence on
one project, one model harness, and duplicated installed instructions. All
layouts, new schemas, adapters, thresholds, and rollout stages below are proposed
unless explicitly identified as existing behavior. No live task queue,
subscription balance, environment health snapshot, or release status belongs here.

The [optional bootstrap package](../../workspace/agent-loop/README.md) implements
only new-session Codex route selection. The scheduler, role skills, quota admission,
handoff and context enforcement below are not activated by that helper.
Muse is deferred by design decision and is not an initial candidate or prerequisite.

Reading guide: [roles](#4-role-contracts),
[skills and project boundaries](#6-skills-project-contracts-and-repository-layout),
[Issue/PR/Review context contracts](agent-loop-context-contracts.md),
[cmux](#9-cmux-integration-without-modifying-cmux),
[model policy](#10-model-and-effort-policy),
[usage admission](#11-p0-subscription-and-usage-awareness), and
[migration gates](#14-p0-prerequisites-and-migration-gates).

## 1. Outcomes and boundaries

The intended outcomes are:

- Complete work through verified delivery, not just code generation or PR creation.
- Support multiple model providers without changing ownership or safety rules.
- Give each role instance a separate, visible session with bounded permissions.
- Make subscription capacity, token usage, actions, failures, and waiting visible.
- Reuse generic roles across projects through explicit project contracts.
- Improve the workflow through reviewed evidence, not autonomous policy rewriting.

There are six primary roles: Investigator, Dispatcher, Worker, Reviewer,
Production Deployer, and Recaper. Protected environment recovery is a restricted
execution mode, not an unrestricted seventh role. Role isolation requires a
separate execution context, not universal cmux hosting: Investigator and Recaper
may run outside cmux while retaining the same identity, audit and budget rules.

Squad owns coordination decisions and their audit trail. cmux owns terminal
presentation and supported session integrations. Git hosting, CI, Kubernetes,
and other deployment systems remain authoritative for their external operations.
A model can propose an action; it cannot replace an atomic ownership decision or
an external system's actual result.

Non-goals for the first version include modifying cmux, training models, adding
a second Worker-dispatch scheduler, automatic credential/account rotation, automatic production
release, and distributing a local SQLite coordination authority across hosts.

## 2. Lessons from Data Analyze

The inspected practice supplies useful delivery constraints, but not a complete
provider-neutral implementation.

| Prior practice or implementation | Preserve | Change or explicitly validate |
| --- | --- | --- |
| Workspace instructions plus project delivery skills | Central policy and project-specific acceptance contracts | Move role behavior into canonical generic role skills; keep product contributor guides portable |
| Atomic primary-work and environment claims | Exclusive ownership, fencing, and protected recovery | Bind ownership to a stable work identity and explicit execution attempts |
| Dispatch reserve, attach, launch, bind | Idempotency and bounded reconciliation | Add provider/session binding and recovery from ambiguous launches |
| One Issue per Worker; waiting Workers count toward WIP | End-to-end accountability and honest capacity accounting | Permit qualified model changes through a controlled handoff |
| Frozen Grok review input and SHA-bound publication | Independent review, deterministic gates, no approval shopping | Expose the review job in cmux without inventing another owning Worker |
| Project-local and user-installed copies of role skills | Existing useful contracts | Resolve one canonical source; inspected same-named copies differ |
| Codex-specific launch/identity wrappers | Explicit identity propagation | Remove hard-coded ledger location and Codex-only session assumptions through an adapter |
| A loopback, read-only human monitor | Observation without mutation authority | Add safe event/usage projections; do not silently turn that monitor into a command endpoint |
| Squad operational statistics and review measurements | Existing durations, outcomes, and review evidence | Implement missing per-attempt usage and subscription observation before relying on them |

Important implementation boundaries:

- The inspected coordination wrapper selects the old Data Analyze ledger and
  derives identity from Codex task/session variables. Merely moving source
  directories does not migrate that runtime or make other providers compatible.
- Dispatch reservations currently bind a worker thread string. A typed
  provider/harness/session/cmux binding is proposed, not already implemented.
- Squad's per-item token estimation currently reports `unavailable`; its
  transcript-byte hook is explicitly unwired. Existing statistics are not proof
  of complete token accounting.
- Review-specific safe token/cost/duration observations do not establish
  account-wide subscription availability.
- The old Python observation monitor and Squad's native dashboard are different
  surfaces. Each needs its own security and capability review.

Implementation anchors: [dispatch reservations](../../internal/dispatch/reservations.go),
[claims](../../internal/claims/claims.go),
[protected recovery](../../internal/claims/recover.go),
[identity](../../internal/identity/identity.go),
[statistics](../../internal/stats/schema.go),
[token estimation](../../internal/stats/tokens.go), and
[review status](../../internal/grokreview/status.go).
Historical workspace inputs include the Data Analyze outer `AGENTS.md`,
its role skills, coordination wrappers, and read-only monitor. They are migration
evidence, not instructions to copy into product repositories.

## 3. Architecture and authority

```text
Workspace policy
  + generic role skill
  + project profile and capability skills
  + qualified runtime/model/account policy
                       |
                       v
               Squad coordination
      work identity / claims / reservations
       fenced transitions / durable events
              |                  |
              v                  v
       Runtime adapters     Read-only projections
              |             usage / recap / control panel
              v
       Role-selected execution host
       cmux surfaces OR app/headless job runner
       isolated sessions / bounded review and recap jobs
              |
              v
       Git / CI / deployment controllers
       independently verified external outcomes
```

The diagram describes dependencies, not permission inheritance. UI events,
model messages, and terminal titles never grant ownership. A cmux process exit
does not prove that its CI run, deployment, or data operation has stopped.

Authority is layered:

1. User authorization and protected repository/environment policy define scope.
2. Squad atomically assigns primary-work ownership and environment ownership.
3. Role and project contracts constrain permitted operations.
4. The runtime adapter carries identity and enforces its supported tool boundary.
5. External systems enforce their own credentials, concurrency, and admission.
6. Observers report evidence; they do not infer a successful mutation from intent.

A local ledger is only authoritative among its cooperating clients. Remote
machines must not each create an independent ledger for the same protected
environment. Cross-host coordination requires a separately designed authority;
CI/deployment-side locks remain mandatory regardless of local Squad claims.

## 4. Role contracts

One role skill defines one role. Multiple Worker instances are allowed, but
each instance owns one primary work item and has its own provider session.

| Role | Input and output | Permitted scope | Must not do |
| --- | --- | --- | --- |
| Investigator | A bounded incident/question → evidence, reproduction, deduplicated Issue proposal or authorized Issue | Read logs, source, CI and permitted browser surfaces; create/update an Issue when that write is authorized and coordinated | Fix product code, claim an environment, deploy, or dispatch Workers |
| Dispatcher | Eligible Issues and explicit dependencies → validated DAG, reservations, launch/reconciliation decisions | Atomically reserve work, allocate qualified capacity, launch missing sessions, reconcile bindings and terminal outcomes | Implement product work, reinterpret silence as failure, merge, deploy, or recover an environment |
| Worker | One assigned canonical item → tested PR and exact-revision staging acceptance | Own implementation, local tests, review corrections, PR lifecycle, guarded staging delivery and cleanup | Scan for another Issue, bypass review, deploy production, or take over another live writer |
| Reviewer | Immutable review request → structured findings and a tuple-bound result | Read the frozen context through the approved review mechanism; produce review evidence | Edit source, own the implementation claim, merge, deploy, or widen tool access |
| Production Deployer | Approved release manifest and production authorization → verified release or rollback | Own one release item, acquire the production environment gate, tag/publish/deploy/validate when authorized | Infer release authority from elapsed time, change count, staging success, or available quota |
| Recaper | One daily evidence window → delivery/efficiency recap and deduplicated improvement Issues | Read Issue, dispatch, delivery and sanitized usage/action evidence; create evidence-backed Issues in configured repositories | Dispatch work, interrupt Workers, modify skills/policy/code, close another owner's Issue, claim environments, or deploy |

The Investigator uses a primary investigation item before coordinated external
writes. Duplicate detection and Issue creation need an idempotent receipt; a
failed network response is not permission to create a second Issue.

The Dispatcher runs finite reconciliation cycles. A single Worker-dispatch
scheduler or explicit operator wake triggers those cycles; cmux hooks and a UI
refresh must not become additional dispatch schedulers. Recaper has a separate
once-daily trigger that never launches Workers or invokes dispatch as a side
effect. Monitoring an active Worker does not authorize injecting
a prompt. Only a relevant dependency transition, an explicit user override, or
a concrete safety problem justifies a bounded intervention.

The Reviewer is a role but need not be a long-running chat. For Grok, retain a
supervised one-shot process with captured completion/exit status and a separate
cmux surface. The approved wrapper freezes review input, restricts tools, and
delegates publication to its narrowly scoped publisher.

Recaper owns one bounded daily recap run, not the Issues or Workers it observes.
It uses a stable window/run key and coordinated ownership before publishing
Issues. It distinguishes workflow inefficiency from legitimate waiting, reports
missing evidence explicitly, and creates actionable improvements rather than
applying them. See [the Recaper contract](#recaper-daily-run-contract).

Protected recovery uses a stopped-holder protocol: identify the holder and
external operations, verify that takeover is safe, compare-and-swap ownership
with a new generation, restore or validate one exact revision, and release only
on a verified safe terminal path. It cannot advance unrelated product work.

## 5. Stable identity, sessions, and ownership

The model session is an execution attempt, not the identity of the work.

A proposed binding record contains:

- Project ID, canonical source reference, primary item ID, and role instance ID.
- Attempt ID, parent attempt, handoff reason, and reservation/ownership generation.
- Provider, harness, resolved model ID, effective effort, and opaque account alias.
- Provider-native session ID and adapter version.
- Canonical skill/profile versions or content digests.
- Worktree path and branch identity, where applicable.
- Execution host type and durable host-run ID; for cmux-hosted attempts only,
  workspace, pane, and surface UUIDs plus the observed cmux lifecycle.
- External operation receipts: PR tuple, CI run, review job, release/image revision.
- Last durable checkpoint and its redacted evidence references.

These are separate identifiers. Do not use a terminal title, PID, display index,
or a fabricated Codex thread ID as the cross-provider primary key. Short cmux
references are convenient for an interactive command but are not durable IDs.
For non-cmux execution, cmux fields are absent by design, not an error or reason
to create a placeholder terminal. Provider identity, checkpoint, events and
usage remain required independently of the presentation host.

Changing provider creates a new attempt while retaining the canonical work
identity. If an old attempt may still write, the new attempt must not start
mutating. Resume, hibernation, terminal restoration, and model fallback all
revalidate ownership before any write.

Record these outcomes independently:

- Session exited or became idle.
- Implementation and PR preparation completed.
- Review and deterministic gates passed.
- Exact staging revision was accepted or safely rolled back.
- Issue/release was resolved and owned resources were cleaned up.

An idle or exited session does not imply any later outcome.

## 6. Skills, project contracts, and repository layout

The proposed source of generic roles is an optional Squad adoption package.
It must not become this repository's contributor policy.

```text
voice-agent-squad/
  docs/proposals/studio-multi-model-agent-loop.md
  workspace/agent-loop/                 # bootstrap exists; role package below is proposed
    skills/
      investigator/SKILL.md
      dispatcher/SKILL.md
      worker/SKILL.md
      reviewer/SKILL.md
      production-deployer/SKILL.md
      recaper/SKILL.md
    schemas/                           # role/profile/event/binding contracts
    templates/                         # provider-neutral context contracts/renderers
    adapters/                          # harness-specific integration contracts

<workspace>/
  AGENTS.md                            # thin workspace routing and safety policy
  README.md                            # workspace map, not a live queue
  .agents/
    project-profiles/<project>/        # versioned capabilities and target references
    skills/                            # canonical project capabilities or managed links
  <product repositories>/             # independent Git histories and portable guides

<runtime state>/                       # separate from every source checkout
  coordination ledger / bindings / redacted events / checkpoints
```

The exact packaging location is a decision to approve, not a request to create
these folders now. Runtime databases, session logs, private deployment overlays,
and credentials must not be committed into this layout.

### Separation of concerns

- **Role skill:** lifecycle, inputs/outputs, ownership requirements, forbidden
  actions, error classification, and completion criteria.
- **Project profile:** repositories, dependency/resource keys, commands, CI gates,
  review policy, architecture/runbook links, staging/production acceptance,
  deployment coordinates, rollback contracts, and credential references.
- **Project capability skill:** reusable operations such as querying sanitized
  logs, releasing a component, or validating a project-specific import. Roles
  invoke these capabilities; they do not embed duplicate deployment procedures.
- **Runtime adapter:** model/session launch, effort validation, supported tools,
  checkpoint/resume, event normalization, safe usage observation, and exit state.
- **Context contracts:** portable Issue/PR fields plus frozen review requests and
  runtime handoff checkpoints. Use the [companion design](agent-loop-context-contracts.md)
  to preserve scope, stable acceptance IDs, decisions and evidence across models;
  repository templates must not embed role routing or Squad ownership mechanics.
- **Workspace AGENTS.md:** selects those contracts and identifies their precedence.
  It does not contain a second copy of every role.
- **Product AGENTS.md and docs:** explain contribution, product architecture,
  operation, and testing independently of the development Agent Loop.

In particular, a product execution worker or product task dispatcher is not a
development Worker or Dispatcher role. Necessary product terminology remains in
product documentation; development-agent orchestration and Squad procedures do not.

A provider-specific discovery file such as `CLAUDE.md` should be a thin pointer
or generated loader where the harness supports it. Discovery behavior must be
tested per harness; an identical filename does not guarantee identical loading.

### Canonical skill resolution

Inventory project-local, user-installed, and plugin-provided copies before
migration. For each logical skill, record its canonical source, version/digest,
owner, supported clients, and installation mapping. Resolve exactly one version.
Conflicting same-named copies must produce a visible configuration error, not
silent precedence or a concatenation of instructions.

Use managed links or reproducible packaging where supported. Do not maintain
hand-edited copies for Codex, Claude, Muse, and Grok. Do not delete an installed
copy until all consumers are identified and the replacement has passed a smoke
test. Any future local usage collection also requires an explicit policy review:
the repository's current contributing policy does not authorize telemetry or
usage collection merely because this draft proposes it.

## 7. Dispatch, DAG, and launch protocol

Dependencies are typed edges, not Issue-number ordering. At minimum distinguish
implementation prerequisites, reviewed-interface prerequisites, and
accepted-deployment prerequisites. An open PR is not evidence that a deployment
dependency is satisfied. Reject cycles and surface missing acceptance contracts.

The proposed launch protocol extends existing reservations:

1. Resolve one canonical item and an explicit dependency/resource contract.
   Validate its approved Issue contract, stable acceptance IDs and required
   context before admitting implementation; a brief intake Issue is not yet ready.
2. Check WIP, permissions, qualified capabilities, and fresh budget admission.
3. Atomically reserve the canonical dispatch key and attach the item.
4. Persist a launch intent and idempotency key before creating a terminal/session.
5. Launch once in the role's explicit execution host; collect a provider-session
   handshake. For a cmux-hosted Worker, resolve the target surface explicitly.
6. Bind that attempt and durable host IDs to the reservation generation; include
   cmux IDs only for cmux-hosted attempts.
7. The Worker atomically acquires its primary-work claim before mutation.
8. Reconcile completion using ownership and external evidence, not just exit code.

A crash between steps 4 and 6 produces an ambiguous launch. Reconcile the launch
receipt and existing session before retrying; never blindly create another
Worker. An unbound live reservation still consumes WIP.

Begin with the prior maximum of five non-terminal product Workers, subject to
lower budget and resource limits. Waiting, idle, restoring, and review-waiting
Workers count. A role's session count and review-job concurrency are separate
limits. A Dispatcher cycle fills only remaining capacity; it never starts five
additional Workers simply because the configured limit is five.

## 8. Delivery gates and recovery

### Worker lifecycle

Assignment → ownership → isolated worktree → implementation/local gates →
stable PR tuple → full CI and independent review → environment admission →
exact-revision staging deployment → acceptance → safe release of environment →
Issue resolution → worktree cleanup.

Review and full CI should overlap after the fast local gates and author review.
During sampling, freeze the substantive input and PR base/head tuple. A valid
blocking review is addressed with verified fixes, not a different reviewer or
repeated sampling. A stale tuple, timeout, or process failure is not approval.

Freeze the approved Issue contract and required source/evidence snapshots with
the PR input, following the [context contract](agent-loop-context-contracts.md).
A link alone cannot convey requirements to a tool-disabled reviewer. Bind review
validity to substantive context/policy identity as well as base/head: same-SHA
requirement changes must not reuse an old success. This stronger validation is
proposed, not implemented by the current SHA-only tuple check. Keep repository
auto-close behavior separate from the final accepted-delivery decision.

Before entering an environment wait, and again after acquisition:

- Re-read the PR base/head, mergeability, deterministic checks, and review policy.
- In enforced mode, verify the required successful Check and its publisher identity.
- Confirm the target environment/resource identity and a usable rollback path.
- Confirm that the attempt still owns the primary item and current generation.

Do not hold an environment lock while waiting for routine CI or review.
Deployment must bind the merge/release revision to an immutable image and the
actual runtime. Acceptance checks exact revision, mixed revisions, workload
readiness, functional outcomes, and project-required logs or data-integrity checks.

Classify failures using evidence. Roll back product/deployment failures,
unknown health, mixed revisions, or potential data risk. A demonstrably isolated
harness/configuration failure on a healthy exact revision may use the existing
bounded repair policy: one substantive attempt or 60 minutes, whichever occurs
first, unless the project contract is stricter. Never reset this window by
restarting the verifier.

Cleanup occurs only after safe completion: preserve dirty/untracked work, retain
needed evidence, and remove only the verified owned worktree. A failed cleanup
is recorded separately; it must not erase the delivery result or another task's work.

### Environment identity

Use a stable resource key resolved to explicit kubeconfig/context, namespace,
release, platform, values/overlay references, and deployment authority. A shell
alias or internal cluster nickname is not an environment contract.

Migration must map old and new names to the same protected resource. Creating
a second apparently free lock for the same staging or production environment
would defeat exclusivity. Keep old protected ownership intact until a controlled
handoff verifies external operations.

### Production lifecycle

Production is a separate authorized release item, not the Worker's next step.
Its immutable manifest identifies the accepted changes, component versions,
images/digests, schema compatibility, deployment ordering, validation, and rollback.

The Production Deployer assesses a release batch against explicit criteria,
then requests or uses the applicable explicit production authorization.
It acquires the production gate only when release prerequisites are ready.
Tagging, publishing, configuration changes, deployment, and validation must
each remain within that authorization. A failed production rollout retains
protected ownership until a safe revision is verified.

## 9. cmux integration without modifying cmux

cmux's UI hierarchy is window → workspace → pane → surface. A terminal surface
hosts a process; a provider conversation/session is a different object. “One
session per role” means a separate provider session for each role instance,
not one global terminal for all Workers and not a requirement to use cmux for
every role. Hosting is a role/runtime choice, separate from model selection.

| Role | Proposed default host | Visibility requirement |
| --- | --- | --- |
| Investigator | Interactive app or bounded job outside cmux | Investigation identity, evidence, usage and outcome in Squad; qualify tools on its actual host |
| Dispatcher | cmux terminal | Visible bounded-cycle session and durable dispatch bindings |
| Worker | cmux terminal per instance | Owned work, provider attempt and worktree bound to its surface |
| Reviewer | Supervised one-shot job displayed in cmux | Frozen review tuple, progress and safe final result |
| Production Deployer | cmux terminal | Release identity, protected ownership and exact external-operation receipts |
| Recaper | Scheduled bounded job outside cmux | Daily run/window identity, recap, Issue receipts and its own usage in Squad |

A proposed visible arrangement is:

```text
cmux window
  Control workspace
    Dispatcher surface   → one dispatch session, finite cycles
  Issue workspace: <canonical item>
    Worker surface       → one owning attempt
    Reviewer surface     → bounded supervised review process/output
  Release workspace: <release item>
    Production surface   → one authorized release attempt

Outside cmux
  Investigator session   → on-demand investigation → evidence / Issue
  Recaper daily job      → bounded evidence window → recap / improvement Issues
  Both report identity, usage and outcomes to the same Squad authority
```

The Reviewer remains a separate role even when displayed beside its Worker.
Names are for humans; cmux bindings use UUIDs and canonical item/attempt IDs.
Non-cmux roles remain visible through the control panel, without terminal
allocation or a dependency on cmux availability.

### Existing integration versus proposed adapters

| Runtime | Upstream cmux integration documented | What still needs qualification |
| --- | --- | --- |
| Codex | Agent hooks/session restoration; cmux computer-use MCP integration | Installed-version compatibility, exact session binding, permissions, model/effort and usage APIs |
| Claude Code | Agent hooks/session restoration; cmux computer-use MCP integration | Installed hooks/settings, effective launch permissions, usage-field availability and stable binding |
| Grok | Agent lifecycle/feed/resume integration | Headless review wrapper coexistence, sanitized exit/result publication and exact tuple binding |
| Muse (deferred) | Historical research only | Excluded from initial qualification and automatic fallback |

The existing CLI exposes discovery, workspace/surface operations, session
observations, events, status/progress, and notifications. Read-only probes from
an authorized cmux-launched process include:

```sh
cmux identify --json
cmux capabilities --json
cmux tree --all --json
cmux sessions list --json
```

These are capability checks, not a launch script. Resolve the installed binary
and use explicit targets. No `cmux session create` abstraction is assumed.

A catalog plugin is not required merely to run the CLI. Native agent hooks,
MCP integrations, provider plugins, and shell wrappers are different mechanisms.
A missing catalog search result does not disprove native integration.

cmux hooks report observed lifecycle such as running, idle, or needing input.
These observations cannot release claims or close Issues. Event replay requires
boot/sequence identity and deduplication; replay gaps require reconciliation.

Browser automation and desktop computer use are distinct capabilities.
A model choice such as “Sol” grants neither. Verify the tool transport, browser
scope, macOS permissions, and a non-sensitive test before admitting browser work.
Do not bypass cmux socket restrictions or use another session's credentials.

Qualification of cmux-hosted roles must happen in an authorized cmux session. Static documentation
and installed CLI help alone are not an end-to-end integration test. If the
calling process is denied socket access, report an unavailable capability;
do not bypass the restriction to complete qualification.
Investigator and Recaper instead qualify their selected host directly. Their
browser/tool capability must not implicitly depend on cmux-injected tooling.

Additional safeguards:

- Inspect effective wrapper flags; native integration must not silently expand
  tool permissions or trust bypasses.
- Treat optional AI naming as metered overhead; leave it off unless approved.
- Treat hibernation/restoration as a session interruption, not ownership release.
  Memory-pressure handling can interrupt a process; external operations may continue.
- Do not stream raw terminal buffers, prompts, or hidden reasoning into the panel.
- Keep the existing cmux implementation unchanged; adapters use its supported CLI,
  hooks, and MCP contracts.

References: [agent hooks](https://github.com/manaflow-ai/cmux/blob/main/docs/agent-hooks.md),
[computer use](https://github.com/manaflow-ai/cmux/blob/main/docs/computer-use.md), and
[event stream](https://github.com/manaflow-ai/cmux/blob/main/docs/events.md).
These track upstream development; qualification must pin the installed version.

## 10. Model and effort policy

The following is an ordered candidate policy derived from the proposed
preferences, not a benchmark result or a claim of current account availability.
Codex/OpenAI and Codex/sub2api are separate service routes of one runtime.
Qualify provider, model, account/bucket and host independently; do not assume
shared capabilities or independent quotas. Muse is excluded from this first phase.

| Role or work class | First candidate | Next candidates, in order |
| --- | --- | --- |
| Investigator | Codex Sol, high for diagnosis; medium for bounded log triage | Claude Opus high |
| Dispatcher | Codex Sol high on a qualified route | Claude Opus high |
| Worker, routine | Claude Opus medium | Codex Sol medium |
| Worker, difficult or high-risk | Claude Opus high, or max only if supported and justified | Codex Sol xhigh |
| Reviewer | Grok 4.6 medium for ordinary work, high for security/concurrency/deployment risks | No automatic substitute for an enforced Grok gate; Claude/Codex may be evaluated in shadow only |
| Production Deployer | Claude Opus high | Codex Sol high, separately qualified for the release contract |
| Recaper | To be selected after evidence-grounded recap and Issue-deduplication qualification | Ordered model/effort candidates remain a decision; prefer deterministic aggregation and a bounded analysis budget |

Resolve aliases to actual model IDs, supported efforts, tool capabilities, and
account/billing modes at launch. Record the resolution. Unsupported effort is
a configuration error, not permission to silently choose a lower effort.
Do not equate differently named effort levels across providers.

The Dispatcher benefits from reliable state reconstruction and sufficient
context, not unlimited accumulated chat. Keep a compact DAG/resource checkpoint
and retrieve evidence on demand; do not feed every terminal transcript into it.
Polling, counting, deadline checks, and event aggregation should be deterministic.

Model qualification uses representative tasks with independently checked
outcomes: diagnosis accuracy, ownership compliance, test quality, verified
review findings, recovery correctness, latency, and measured consumption.
Difficulty/risk cohorts must be separated; a cheap model on easy work is not
evidence that it is best for production recovery.

A successful browser/tool smoke test is a hard capability requirement for a
task that needs those tools. A model that reasons well but lacks the qualified
transport is ineligible for that task.

The current [Grok review command](grok-required-review-gate.md) remains the
reference for its supported review contract. An alternate model must not publish
a success pretending to be the required reviewer or bypass an enforced Check.

## 11. P0: subscription and usage awareness

Usage visibility is an admission dependency, not a dashboard added after rollout.
Keep three concepts separate:

1. **Subscription capacity:** provider/account limit windows and reset times.
2. **Token consumption:** provider-reported per-call/session usage with provenance.
3. **Cost estimate or bill:** explicit currency, pricing source, and billing mode.

A subscription is not a pool that can always be converted into tokens or dollars.
A client cost estimate is not necessarily an invoice or incremental subscription
charge. Several models or sessions may share one account-level capacity bucket.

### Proposed provider observations

| Provider/runtime | Available evidence to qualify | Limit of that evidence |
| --- | --- | --- |
| Codex | App-server account rate-limit reads/updates, model discovery, thread token-usage events | Verify installed protocol support; account buckets are not automatically per-item budgets |
| Claude Code | Status-line JSON can expose model/effort, five-hour/seven-day limit windows, token/context and estimated-cost fields | Availability depends on version/auth; context usage is not cumulative billed usage |
| Grok | CLI usage reporting and wrapper-level review usage observations | Token/cost history does not prove subscription remaining capacity |
| Muse (deferred) | Historical observations retained separately | Not a blocker for initial non-Muse admission |

Each observation includes provider, opaque account alias, bucket identity,
measurement time, source, confidence, window/reset metadata, and availability.
Unknown, unsupported, stale, and exhausted are different states. Missing data
is never zero consumption or unlimited capacity.

Normalize token records by provider request/event ID. Distinguish deltas from
cumulative counters and cache/reasoning subfields from independent totals.
Do not sum cumulative session counters repeatedly or double-count nested fields.
Expose coverage: measured, estimated, and unavailable attempts.

Codex interface references:
[app-server](https://developers.openai.com/codex/app-server) and
[usage/pricing](https://developers.openai.com/codex/pricing).
Prefer documented multi-bucket rate-limit data when available; validate the
installed protocol rather than assuming every latest interface exists locally.

Claude references:
[status-line fields](https://code.claude.com/docs/en/statusline) and
[cost semantics](https://code.claude.com/docs/en/costs).
Treat client-reported estimates and actual billing as separate measures.

### Admission and graceful degradation

First filter candidates by authorization, role policy, capabilities, risk, and
review requirements. Only then rank qualified candidates by available capacity,
measured reliability, expected completion cost, and latency.

Provisional settings for qualification are a two-minute freshness limit,
a warning below 20% remaining capacity, and no new routine work below 10%.
These are configurable starting hypotheses, not provider guarantees. Providers
without percentage windows need an explicit equivalent admission rule.
Reserve qualified capacity for finishing or safely recovering in-flight work.

If quota is exhausted or uncertain:

- Stop admitting new work that depends on the unavailable budget.
- Use a qualified alternate only at a safe checkpoint and within the same scope.
- If no candidate is safe, park the item with its ownership and recovery state
  intact; report the real constraint without creating another session.
- Unknown quota blocks unattended mutating admission unless an explicit bounded
  operator exception defines the budget and recovery route.
- Never buy credits, consume reset credits, change billing mode, or rotate
  credentials/accounts automatically.

A quota fallback cannot lower an enforced review requirement, retry a blocking
verdict under another model, or change release scope.

### Model-swap protocol

1. Persist the work/role/skill contract and exact repository/PR revision, with
   approved Issue contract revision, context digest and review/evidence references.
2. Capture owned claims/generations, pending external operations, completed
   evidence, next authorized step, and usage/switch reason.
3. Quiesce the old writer and independently verify its process/external state.
4. Perform an explicit ownership handoff where required; bind the new attempt.
5. Rehydrate authoritative context without relying on the old chat; confirm scope,
   acceptance criteria, decisions and next permitted action, then revalidate gates
   before writing. Missing or conflicting required context blocks resumption.

While a protected environment is held, do not use an ordinary provider fallback
as a takeover. Continue with the verified owner or use the protected recovery
protocol. Loss of model availability is not evidence that deployment stopped.

## 12. Squad events and the control panel

The existing claims, reservations, milestones, verification, and statistics
provide a foundation. The following normalized event/binding capabilities are
new work, not existing CLI flags or schema guarantees.

A proposed event envelope includes event ID, schema version, occurrence and
observation times, canonical item, role instance, attempt, producer sequence,
causation/idempotency key, ownership generation, safe model/usage metadata,
external-operation references, result classification, and redacted evidence links.

Useful event families include:

- Dispatch reserved, launch requested, session bound, launch reconciled.
- Claim acquired, handoff completed, protected recovery entered, claim released.
- Local gate completed, PR tuple frozen, review started/completed/invalidated.
- Environment wait/acquisition, deployment started, acceptance and rollback result.
- Budget observed, admission deferred, fallback proposed/completed.
- Recap window reserved, aggregation completed, finding deduplicated,
  improvement Issue published/reconciled, daily run completed/deferred.
- Issue resolved, cleanup completed/deferred, attempt ended.

Coordinate state transitions and their event outbox transactionally.
Assume at-least-once delivery and deduplicate projections. A dropped UI event
must not lose ownership; a replayed hook must not repeat an external write.
An audit event should reference the external receipt, not replace verification
of that receipt.

### Control panel scope

Version one is read-only: show canonical work, dependencies, owner/attempt,
execution host and optional cmux location, review/CI tuple, environment ownership, blocking reason,
usage coverage, reset windows, and safe outcome trends.
Investigator and Recaper appear through durable run records even without a
terminal. Recaper's authorized Issue publication is a role operation, not a
mutation endpoint added to the read-only panel.

The old read-only monitor stays read-only. Any later command surface is a
separate approved capability with authentication, explicit targets, permission
checks, compare-and-swap transitions, and an audit receipt. “Stop” must
distinguish stopping a local model from cancelling an external operation.
Never provide a convenience action that force-unlocks a protected environment.

Keep the panel loopback-only initially. Do not expose prompts, hidden reasoning,
raw terminal output, customer transcripts, environment dumps, tokens, private
keys, or credential file contents. Define retention and local access controls
before enabling collection. No external telemetry upload is proposed.

## 13. Efficiency, daily recap, and supervised improvement

Measure the complete delivery, including failed attempts and review overhead.
Do not reward a low token count obtained by skipping validation.

| Metric | Definition or guard |
| --- | --- |
| Delivery completion | Accepted deliveries / eligible assigned work, with an explicit observation window |
| Lead time | Queue, active implementation, CI, review, lock wait, deployment and acceptance durations shown separately |
| Consumption | Input/output/cache/reasoning usage with provider semantics and measured-data coverage |
| Action efficiency | Tool/model calls, meaningful external writes, retries and redundant repeated actions |
| Review value | Verified findings and correction cycles; false positives require author evidence, not mere disagreement |
| Delivery safety | Rollbacks and failed acceptance, classified by product, infrastructure, harness, or unknown cause |
| Switching overhead | Handoffs, context rebuild, invalidated reviews, duplicated attempts prevented |
| Capacity health | Stale/unknown observations, admission deferrals, reset windows and recovery reserve |
| Back-and-forth | Repeated identical failures or A→B→A changes without new evidence, excluding legitimate waits |

Compare models within role/difficulty/risk cohorts and show sample counts.
Missing usage is a visible limitation, not a zero-cost success.

### Recaper daily-run contract

Recaper runs once per day as a bounded job, outside cmux by default. Deterministic
aggregation prepares its input; a qualified model evaluates evidence and authors
the recap and actionable improvement Issues. It does not continuously poll or
serve as a second Dispatcher. Scheduling requires a configured time, time zone,
destination, retention and analysis budget; this draft creates no automation.

Its input covers:

- Issue handling: intake, completion and reopening, lead time, unresolved work,
  acceptance outcomes and reasons for blockage.
- Dispatcher behavior: dependency decisions, duplicate/prevented dispatch,
  capacity allocation, unnecessary wakeups and token/action overhead.
- Worker behavior: delivery completion, testing, review correction, retries,
  tool actions, environment waits, failed acceptance, rollback and cleanup.
- Whole-loop usage: tokens and actions by role/model/attempt and accepted
  delivery, including failed attempts, review, investigation and Recaper itself.
- Budget health and coverage: subscription constraints, missing/stale data,
  estimates versus measurements, and actionable efficiency opportunities.

The proposed run protocol is:

1. Reserve one project/window run key atomically and freeze the evidence cutoff.
   Use a configured calendar-day window with explicit start/end and time zone;
   store UTC boundaries. Retrying that window resumes the same run rather than
   publishing a second daily recap. Define bounded missed-run catch-up at setup.
2. Aggregate redacted authoritative events and Git-hosting receipts. Snapshot
   non-terminal work without changing its ownership or interpreting silence as
   failure. Separate legitimate waits and task difficulty from avoidable waste.
3. Analyze within a bounded token/action budget. Each finding identifies the
   observed behavior, evidence, impact, hypothesis/confidence, proposed change
   and measurable acceptance criteria. A missing-data finding is not proof of
   poor model or Worker performance.
4. Search existing open and relevant closed Issues before publication. Use a
   stable problem fingerprint independent of run date to prevent daily copies.
   Link an existing matching Issue in the recap; create an Issue only for a new,
   evidence-backed actionable problem. Recurrence after closure needs explicit
   regression evidence, not automatic reopening.
5. Publish within a configured repository allowlist and per-run Issue limit.
   Put workflow/skill/coordination improvements in the configured workflow
   repository, and product defects in their owning product repository. Persist
   publication intent and receipt; on an ambiguous response, reconcile before
   retrying. Partial publication resumes from receipts without duplicate Issues.
6. Record the recap, evidence coverage, created/reused Issue links, deferred
   findings, own usage and completion status. A run with no justified finding
   creates no Issue. Recap artifacts belong in runtime/report storage, not live
   status sections in maintained repository documentation.

Issue creation is not priority approval, task dispatch or implementation
authorization. Improvements enter normal triage and delivery gates. Recaper
does not edit model routing, skill instructions, WIP limits or safety policy.
No Issue is created in this documentation-only task.

Its own consumption is recorded immediately and summarized in the appropriate
later evidence window, without recursively re-running a recap to analyze itself.
Observe outcomes of previously proposed improvements to evaluate whether they
actually reduced failure or consumption; do not measure success by Issue count.

Existing
[learning and retrospective commands](../reference/commands.md) can supply an
approval path, but a new daily multi-provider recap is not already implemented.
A human or designated authorized maintainer approves a versioned skill/policy
change, canaries it, measures results, and can roll it back. The loop never
rewrites its own permissions, merge gates, or production authority.

## 14. P0 prerequisites and migration gates

Documentation preparation, independent review, and an authorized production
baseline are prerequisites, not achievements asserted by this draft.
All agreed P0 workstreams must be addressed before unattended adoption: legacy
migration/rollback boundaries, installed runtime interfaces, subscription/usage
admission, role-appropriate cmux or non-cmux hosting, Squad ownership/recovery,
cross-model Issue/PR/Review continuity, and source-faithful Studio
deployment/acceptance contracts. None is silently
deferred to a later dashboard phase. Each needs verified evidence or an explicit
decision excluding an unqualified runtime from the initial candidate pool.

### Gate A: clean, source-faithful workspace

- Define workspace routing, repository ownership, and canonical skill sources.
- Keep product AGENTS/README/docs in English and independent of role orchestration.
- Remove live status/queue snapshots from maintained guides.
- Audit architecture and deployment documentation against current source.
- Document staging and production separately using explicit target coordinates,
  secret references, acceptance, CI Actions capabilities, and rollback paths.
- Independently review the documentation and record its exact revisions,
  unresolved findings, and disposition outside maintained architecture pages.
- Map existing Issue/PR templates to the shared context contract without losing
  project safety fields. Define stable acceptance IDs, amendment authority,
  evidence mapping and post-acceptance closure semantics before implementation.

Studio's deployment review must explicitly account for:

- Frontend, API, control plane, and internal import-worker deployments.
- API/business Interceptor placement and which containers share a Pod.
- Execution-worker fleet placement, sidecar Interceptors, identity and scaling.
- Relations between independent Helm charts, release ordering, and contracts;
  no umbrella-chart dependency should be invented where none exists.
- The difference between a chart-supported worker deployment and a release
  workflow that intentionally uses an externally managed worker fleet.
- The separate Importer service, its persistent state and deployment mechanism;
  it must not be confused with Studio's internal import worker.
- Production Compose/Helm/external-fleet choices as applicable to the actual
  target, not as an assumed universal deployment topology.

### Gate B: verified environment baseline

Use the existing approved delivery procedure for any prerequisite release.
Resolve active operations and ownership first. An actual production release
needs explicit authorization, an immutable manifest, protected ownership,
validation, and rollback readiness. Neither this draft nor clean documentation
authorizes that release or proves staging/production healthy.

### Gate C: canonical skills and provider adapters

Prove deterministic skill discovery, effective permissions, identity propagation,
model/effort resolution, checkpoint/resume, and P0 usage admission.
Pin tested runtime versions and retain a rollback mapping for installed skills.
Do not copy a live database to create a parallel coordination authority.

Drain or explicitly transfer existing work before switching the active workspace
runtime. Keep legacy active claims and external operations reachable. Moving the
ledger path is an optional, separately controlled migration, not a prerequisite
for moving source. Verify one authority per protected resource throughout.

### Gate D: isolated role-host qualification round

Run synthetic tasks with isolated coordination state and no shared deployment
mutation. Real model calls, if needed, use an explicitly bounded test budget.

| Test | Required observation |
| --- | --- |
| Every role loads its skill | One canonical version, role-specific permissions, correct project profile |
| Each candidate runtime starts/resumes | Stable work identity, new attempt where appropriate, valid provider/host binding and cmux IDs only when applicable |
| Investigator and Recaper run without cmux | Identity, tools, usage, outcomes and permission boundaries work with no cmux socket or surface |
| Recaper trigger is duplicated or a run restarts | One window/run identity; no duplicate recap publication or improvement Issues |
| Recaper has incomplete data or no actionable finding | Coverage limitations reported, no unsupported optimization claim or quota-filling Issue |
| Recaper publication response is lost | Reconcile intent/receipt before retrying; remain within target and Issue-count limits |
| Crash between launch and bind | Reconciliation finds or safely rejects the existing launch; no duplicate writer |
| Quota low/exhausted/unknown/stale | Correct admission/fallback/parking behavior and visible coverage |
| Browser/computer-use permission denied | Explicit capability failure; no bypass or unrelated tool escalation |
| Worker interrupted while external operation runs | Claim retained; external state verified before recovery |
| Review timeout, blocking result, or stale head | No fabricated success, no approval shopping, no environment admission |
| Fresh model starts without old chat | Reconstructs approved scope, ACs, decisions, failed approaches and next authorized action from durable artifacts |
| Missing, conflicting or oversized review context | Explicit incomplete state; no silent truncation or fabricated approval |
| Same-SHA substantive requirement change | Old review cannot authorize merge; context-aware publication/admission detects drift |
| cmux event replay, restart, or hibernation | Idempotent observation and ownership revalidation |
| Conflicting skill installations | Visible conflict; no silent shadowing |
| Cleanup with dirty or foreign worktree | Data preserved and cleanup deferred |

### Gate E: staged adoption

1. Approve this design and resolve the blocking decisions below.
2. Implement and test identity, provider qualification, and usage admission first.
   Include context assembly, validation and cold-start handoff before multi-model
   dispatch; passing a template lint is not proof of preserved context.
3. Pass the isolated role-host/skill round, including non-cmux execution and
   Recaper deduplication fixtures, plus independent safety review.
4. Deliver one explicitly assigned low-risk staging Issue end to end.
5. Expand to multiple Workers within the existing WIP and budget caps.
6. Run Recaper on accumulated evidence with draft-only Issue output first;
   after deduplication and evidence quality pass review, enable its configured
   once-daily run and bounded Issue publication. Daily reporting can begin
   before multi-Worker expansion; neither mode dispatches implementation work.
7. Qualify a separate production release workflow with explicit authorization.

Do not enable the new Dispatcher while the old scheduler can dispatch the same
queue. Keep rollback to the previous runtime possible without losing canonical
items, ownership generations, or external-operation evidence.

## 15. Decisions required before implementation

1. Which Codex/OpenAI and Codex/sub2api models and account aliases qualify, and
   which quota buckets are shared? Muse qualification is deferred.
2. Are the candidate orders/efforts acceptable, and what risk classes require a
   stronger model or prohibit automatic handoff?
3. Is the first control panel strictly read-only as proposed? Any later command
   surface needs a separately reviewed scope.
4. Should the canonical role package ship with Squad, or be a separate versioned
   package? Which installation mappings should each harness consume?
5. Should the existing ledger remain in place during adoption, or be migrated
   after all active work is drained? What is the exact single-authority mapping?
6. What subscription freshness/threshold rules, recovery reserve, test budget,
   and exception approval apply? Unsupported quota sources are a launch blocker
   for unattended mutation unless a bounded exception is explicitly approved.
7. What release-batch criteria and approval boundary should Production Deployer
   use, and what independently verified baseline satisfies the P0 prerequisite?
8. What model/effort order, daily time/time zone, catch-up policy, Issue target
   mapping, publication limit and analysis budget should Recaper use?

## 16. Acceptance of the design versus activation

This draft is ready for design review when its contracts are internally
consistent, source references resolve, and implemented versus proposed behavior
is clearly distinguished. Acceptance does not mean all adapters exist, all
models are qualified, subscriptions are measurable, cmux is configured, or
production has been deployed.

Implementation should be split into independently reviewable work packages:
canonical contracts and skill packaging; Issue/PR/Review templates, context assembly
and handoff validation; identity/launch reconciliation;
provider usage/admission; cmux bindings; safe event projections and recap;
project qualification; and staged adoption. Dependencies between those packages
must be explicit, with usage and safety prerequisites preceding unattended work.
