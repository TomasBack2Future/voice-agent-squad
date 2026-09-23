# Worker task prompt contract

Every dispatched Worker prompt must be self-contained and contain:

- runtime-tagged callback routing: actual App thread/host ids or cmux
  workspace/surface/native session ids, `dispatcher_agent_id`, plus the existing
  reservation key/generation; cmux ids identify a session but do not supply a safe
  message transport. Use a verified message API/mailbox for wakeup; otherwise
  persist the event as pending reconciliation in Squad. Never use terminal input
  injection for background callbacks. Verify the selected transport, never pass a native
  Claude UUID to an App thread API; require
  the Worker to record them and read the maintained
  `studio-issue-worker/references/dispatcher-callback.md`. After durable closure,
  accepted batch transfer or a new terminal blocker, persist one deduplicated
  `worker-terminal-v1` event for this Dispatcher before ending and notify only
  through the safe transport when available, never to another
  Worker or a guessed task. No callback for routine progress/resource waiting;
  delivery failure falls back to the durable ledger/heartbeat, not a retry loop;

- the compact delivery-capability receipt: explicit target/authority, actual
  environment access path and live resource identity when available, applicable
  browser/login path, user-selected effective permission mode and callback
  route; distinguish verified, external prerequisite and not-yet-checked;
- ownership of authorized restart, browser replay and evidence collection:
  no manual shell/DevTools/output-relay chores when available tools support
  execution. Escalate only the concrete user-only interaction or verified
  decision/access blocker using the delivery-quality contract;
- “Invoke `$studio-issue-worker` and process exactly one Issue.”
- repository, canonical GitHub Issue URL/reference, canonical Squad item id,
  dispatch reservation key, and reservation generation;
- the canonical Issue design-section locator and its explicit revision, READY
  disposition, and any referenced dependency decisions; no copied design body;
- a requirement to read and verify that design revision and repository
  instructions before product mutation. If stale, contradictory or missing,
  report one concrete admission blocker to the Dispatcher; do not redesign the
  product implicitly or request redundant human permission;
- a requirement to read `studio-issue-worker/references/delivery-quality.md` and
  run its applicable read-only preflights using verified evidence: complete
  work package, typed dependencies, successful samples, review tuple/history,
  and exact retained-resource cleanup. Repeated same-family verified findings
  trigger a bounded owner design sweep, not another blind correction round;
- purpose-based Python boundaries from the maintained Worker skill: retired
  business runtime stays frozen, while necessary current-system tests and tools
  are covered by the named Issue; preserve path ownership and data-safety gates.
  Updated workflow contracts must run in applicable CI, not only locally;
- a requirement to atomically claim the canonical item and use foreground
  `claim --wait` if another task owns it;
- standing authorization for implementation, tests, PR, guarded merge, staging,
  rollback, Issue closure, and external communication, with no additional human
  confirmation after required gates pass;
- strict Issue isolation: the Worker never performs, confirms, or asks the user
  about another Issue's acceptance or data mutation; dependency context is
  limited to identifiers and resolved/unresolved state;
- a requirement to finish issue-local implementation, rebase, tests,
  deterministic CI, and Grok review while another Worker holds the target ENV;
  the assigned delivery mode determines which phases wait on ENV; candidate mode
  prepares merge/CI/images/prefetch outside ENV only after capability activation;
- the phase-aware local Grok contract: after quick local checks/self-review the Worker runs
  `squad-grok-review doctor`, then invokes `squad-grok-review` once for the
  substantive frozen head alongside full CI, using repository, PR, mode and
  explicit risk-appropriate effort with the user-selected 20-minute cap; run on
  the host according to current tool permissions (omit unsupported escalation
  fields), and doctor must validate GitHub/Grok auth, CLI contract, model, and
  session storage before sampling; it never creates a reviewer Agent, calls raw
  Grok, manually publishes, or bypasses the result; shadow is observational,
  while enforced App-pinned success is required before `ENV` or merge;
- a requirement to verify Grok findings rather than blindly accept them, fix
  valid findings on a substantive new head, and record evidence for invalid
  findings without approval-shopping;
- a requirement to keep the Issue claim through final staging verification;
- a requirement to select ENV resources by component and operation from the project
  profile, preserving ENV-001/002 legacy calls; narrow Studio scope and independent
  Importer/Feedback resources require the installed resource policy. Acquire a
  cross-service set in lexicographic ID order; a deadlock is not permission to
  release another task's claim;
- the explicit staging delivery mode and verified repository capability reference;
  legacy mode claims after PR readiness and before merge, while activated
  staging-candidate-v1 claims after terminal CI/images/prefetch, before deployment;
  ENV-002/production requires an explicitly assigned production task. Release
  ENV only after verified safe acceptance or recovery, never while unsafe;
- a requirement to read maintained `references/staging.md` before deployment:
  resolve existing documented access/context/known shell alias before asking for
  credentials, and record per-Issue post-deploy SLS evidence or justified
  not-applicable disposition; empty/denied queries are not passes;
- a requirement to classify post-deploy acceptance failures before rollback:
  retain the candidate and `ENV` for one bounded repair/retry only when the
  exact environment is proven healthy, unmixed, and data-safe and the failure
  is isolated to harness/configuration or transient infrastructure; otherwise
  roll back, including on unknown state or repair timeout;
- a requirement to report terminal evidence in Squad and the task response;
- “Do not discover, reserve, dispatch, or start another Issue.”

Template:

```text
Invoke $studio-issue-worker. Process exactly <ISSUE-REF> using Squad item
<ITEM-ID>; dispatch reservation <RESERVATION-KEY> generation <N>. Read the Issue, workspace AGENTS.md, and
repository instructions first. Read the canonical Issue design section
<DESIGN-SECTION-LOCATOR>, revision <DESIGN-REVISION>, admitted READY. Verify the
revision and acceptance scenarios before product edits. Product naming, entry
points, scope or cross-Issue contract changes return to the Dispatcher with
evidence; routine implementation choices remain yours. Continue independent
authorized work while a decision is pending. Own this one Issue end to end: atomically claim
the canonical item, reproduce, implement, test, and open/shepherd the PR. This
task is standing authorization for the exact named Issue: do not ask for or
wait for additional human approval before a policy-compliant merge, the staging
ENV claim and deployment, acceptance, rollback, Issue/PR updates, or Issue
closure. When all required gates pass, merge, validate the exact deployed
revision, roll back safely on failure, release all locks, and close the Issue
only after acceptance. Production is outside this default authorization unless
assigned by a separate explicit production task. Use blocking claim --wait for
contention. The assigned staging mode is <legacy|staging-candidate-v1>, with
capability evidence <reference or not-activated>. Legacy mode acquires ENV before
merge. Activated candidate mode prepares main CI/images/prefetch outside ENV.
Another Worker's ENV claim blocks independent deployment/shared writes; explicitly
assigned read-only batch acceptance can run against its protected tuple. Complete
useful implementation, tests and policy-required review before waiting.
Do not perform, confirm, or ask the user about another Issue's acceptance or
data mutation; mention dependencies only by identifier and resolved/unresolved
state. Do not discover, reserve, dispatch, or start another Issue. Record
sanitized evidence and leave a precise handoff only if an actual external gate
blocks progress. Do not request generic confirmation for actions covered by
this standing authorization. Ask only for a concrete missing credential,
user-only interaction, new sensitive-data disclosure, production mutation, or
material scope expansion.

Apply the maintained Worker's purpose-based Python boundary: current
Issue tests and tools need no language-only approval; legacy runtime changes
remain frozen. Verify changed workflow contract tests are actually run by CI.

Read references/delivery-quality.md. Before the applicable transition, verify the
work package and companions, dependency phase, successful samples, exact review
tuple/history, and cleanup custody with scripts/delivery-check.mjs. Retain fixtures
marked retain across handoffs. After two verified same-family findings, complete
one owner-led invariant and consumer sweep before submitting a new review head.

Grok review is local and read-only; the GitHub App is only its publication
identity. After quick local checks and self-review, freeze the PR/base/head and
run `squad-grok-review doctor` outside ENV. Start one managed review alongside
full CI with explicit risk-appropriate effort and a 20-minute cap. Use the host
execution boundary permitted by the current tools; omit unsupported escalation
fields. Keep the frozen review input unchanged. Require doctor to report GitHub and
Grok authentication, CLI contract, configured model, and session storage
healthy. If it succeeds, invoke the wrapper once for the substantive head with
`--repo <owner/repo> --pr <N> --mode shadow`, or use `--mode required` when the
protected branch requires the App-pinned `grok-review` Check. The wrapper
discovers publisher configuration itself; do not assemble App ID, installation
ID, or private-key flags in the Worker. Do not create a reviewer Agent, invoke
raw Grok, manually publish a same-name status/Check, use admin bypass, or use
no-op commits to resample. A pre-sampling operational failure may be retried on
the same SHA only after its reported cause is materially repaired. Shadow
findings are observational; when enforced, require success outside ENV for the
current head. Verify each finding; fix valid findings on a substantive new head
and record counter-evidence for invalid findings without approval-shopping.
Revalidate and record the authorizing Check Run after acquiring ENV.

An acceptance command failure is not automatically a deployment failure.
Classify product/deployment, harness/configuration, transient runner/network,
or unknown from exact-revision and environment-health evidence before choosing
rollback. When the candidate is proven healthy, unmixed, and data-safe, retain
the candidate and `ENV` for bounded transient retries or one substantive
acceptance-harness repair attempt (default maximum 60 minutes) and retry the
acceptance. Roll back immediately for product failure, degraded or unknown
health, mixed revisions, possible data/availability impact, an unclassified
failure, or an exhausted repair window. Never let a verifier exit code alone
select rollback.

Before deployment/recovery read the maintained Worker references/staging.md.
Resolve existing documented cluster access, named aliases/functions and explicit
context before reporting credentials missing; never print credentials or bypass
a recovery revision guard. Before closure record exact-release functional tests,
bounded sanitized SLS evidence (or specific not-applicable reason), cleanup and
safe lock release. Missing required log evidence stays pending. Ordinary ENV
contention means foreground claim --wait, not a final response or new Worker.
```

## Batch prompt override (explicit opt-in only)

For an authorized batch task append `delivery-mode: batch-v1`, exact manifest
revision, batch ID, role (`developer` or `integration-release`), primary release
item/reservation generation, child Issue/item/reservation generation, integration
branch, permitted paths, dependency/CI evidence, and final acceptance owner.
Invoke the maintained Worker skill and require its `references/batch-delivery.md`.
These scoped instructions supersede only the ordinary single-Issue ownership and
independent deploy/closure steps above: developers stop at an acknowledged frozen
handoff and never independently deploy; the sole integration owner accepts the
transfer before editing, holds primary plus ENV at release, and owns exact-SHA
per-child acceptance/closure. Keep batch-owned holds and active reservations until
acceptance, and count paused/nonterminal tasks in WIP. Missing handoff receipt
blocks a second writer. A00 READY is not closure; A14 waits on applicable
implementations/evidence, not closed children. Do not release other holds or
create new Workers from a child assignment.

At startup state the role and exact final acceptance owner. A developer's final
handoff must say “开发已交接，Issue 未完成”, link the recipient task and child PR,
state the PR's actual target/merge status, list pending acceptance/log/cleanup
gates and next action, and update its task title without losing Issue/item/PR.
The integration owner continues accepted contributions through the authorized
release and per-child acceptance; no generic “PR created, done” endpoint.

After quick local gate/self-review, freeze PR/base/head. Run doctor and one
managed squad-grok-review with explicit high effort for batch coordination/CI or
release changes, alongside full deterministic CI; use the user-selected 20-minute
cap and actual inactive/shadow/enforced policy. Run on host using current tool
permissions; omit unsupported escalation fields. Keep reviewed input frozen and
retain exact tuple/process/exit evidence. No raw Grok, fake checks, no-op retries,
reviewer tasks, hosted review waiters or bypasses. Actual branch policy remains
authoritative on both integration and main.

Design admission also applies to batch dispatch: one shared product decision
may be referenced by several children; each child must have clear scope and
acceptance ownership. Batch READY or an existing implementation handoff never
substitutes for a current design decision.

For delivery optimization, include the candidate contract reference and exact
activation evidence in the prompt/coordination metadata, not new strict envelope
keys. A live migration must state the old/new mode, owner, next allowed action
and acknowledgment location; reloading the skill alone is insufficient.
