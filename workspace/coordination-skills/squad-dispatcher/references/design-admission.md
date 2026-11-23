# Dispatcher-owned design admission

The Dispatcher owns the product decision and cross-Issue coherence before
implementation starts. Use its existing global view; do not add a mandatory
Designer role, another review committee or an extra human approval round.
Investigator supplies missing facts. Worker implements the admitted decision.

## Decide with bounded context

For a new feature or changed behavior, inspect the relevant existing user flow,
terminology, entry points/routes and owning contracts. Compare related active
Issues, not just touched files: separate implementations may still duplicate a
product concept or disagree about shared behavior. Cite concrete evidence and
distinguish observations from proposed decisions. A detailed Issue or passing
technical tests does not by itself prove the product design is coherent.

For a small defect with established correct behavior, a short statement of that
behavior, evidence and acceptance is enough. Reuse an adequate existing spec by
reference. Do not force every Issue through an investigation or generate a new
design document to satisfy a template.

When evidence is missing, use one bounded Investigator assignment containing
the specific question, relevant Issue/repository, evidence expected and stopping
condition. Reconcile existing ownership before dispatch; do not create a duplicate
investigation or implementation Worker. Use the installed reservation/session
mechanism and count nonterminal investigations in the same WIP budget. If that
runtime cannot represent the role, record the gap rather than disguising an
Investigator as an implementation Worker. Investigation returns evidence to the
same Issue; the Dispatcher makes or revises the product decision.

Ask the user only for a material product tradeoff that cannot be resolved from
existing intent/authority. Ordinary naming consistency, existing behavior and
routine implementation decisions do not create new approval requirements.

## Routine feature decisions

Within authorized queue work, missing admission is a task for this Dispatcher.
For editable defaults or similar configuration features, resolve storage owner,
persistence lifetime, precedence/override behavior, compatibility and observable
acceptance from the existing product model before READY. Record the chosen design
and rationale; do not require a user decision merely because implementation has
alternatives. Ask only when the alternatives materially change product intent
and existing evidence cannot resolve them. Do not dispatch an implementation
Worker to make an unresolved cross-Issue product decision on its own.

## Canonical Issue decision

Maintain a clearly delimited `## Design / admission` section in the existing
Issue body. Preserve the original report, requested outcomes and unrelated
sections. Read the latest body before an update; if it changed, reconcile rather
than overwrite another author's changes. Do not treat GitHub body editing as an
atomic compare-and-swap. Verify the result after writing. Use `gh` with a body
file, and avoid rewriting the Issue on every unchanged dispatch cycle.

Record proportionate detail:

- Revision and disposition: `READY`, `needs-investigation` or `needs-decision`.
- User outcome and non-goals; existing behavior for a small repair.
- Product consistency: chosen terms, entry point and user flow; evidence of
  overlap/conflict checks against existing functionality.
- Issue consistency: related work, explicit dependencies, shared decisions,
  implementation boundaries and compatibility/migration requirements.
- Acceptance: observable user scenarios, including relevant error/recovery
  behavior; required environment capabilities and unresolved prerequisites,
  stated in the Verification block below.
- Evidence references and remaining questions. Blocking questions preclude READY.

### Verification block

READY requires this block inside the section. It is the Issue standard: an owner
and a reviewer can both tell from it what is being fixed, how it is checked and
what the result means.

```markdown
### Verification (v1)

Fix: <the failing behaviour, where it lives, the input or state that fails today>
Reproduce locally: <command or steps that show the failure on the current revision>
Tests that must fail before the fix: <named unit/contract tests to add, with the
case each covers>

| # | Criterion | Blocks close | Layer | How to verify | Pass | Not pass |
| - | --------- | ------------ | ----- | ------------- | ---- | -------- |
| 1 | <the user-visible outcome on a path in use> | yes | staging | <what a local client or owned browser reads on the staged candidate, which existing data> | <exact observation> | <what a failure looks like> |
| 2 | ... | no | unit | <test command> | ... | ... |
| 3 | ... | no | local | <how the candidate revision is run as an owned process; the request or UI steps against it> | ... | ... |
| 4 | ... | yes | production | <why staging data cannot prove it; what is read after the deploy> | ... | ... |
```

Rules for the block:

- **One row per original criterion**, quoting its source. Every criterion a
  person wrote gets a row; none is left out. Criteria that one unit test covers
  together may share a row. The block is the closing contract.
- **Must pass.** `Blocks close` is `yes` for one to three rows: the ones that
  show the Issue's user-visible outcome on a path in use. A path is in use when
  users, clients or jobs reach it in production once this change is released,
  with nothing else to enable; a new feature that the release turns on is in
  use. For a change that stays behind a flag or a role that is off, the `yes`
  row is the existing behaviour unchanged on staging, and the new behaviour is
  `no` rows. Every other row is `no`: it is checked at `unit` or `local`, or at
  `staging` when staging already runs its path, its result is recorded, and it
  does not hold the close. Defensive negatives, protocol and input matrices,
  "every case again from the UI in production" and the behaviour of a path
  nobody runs are `no` rows. When more than three rows would be `yes` the Issue
  holds several outcomes: split it, each part with its own block. Keep the
  marks a person wrote. Otherwise choose them yourself and say in the block
  that you did; when you mark a criterion a person wrote as `no`, say so in one
  line the next time you show the list for the user, and do not wait for an
  answer.
- **Where the path exists.** A row is verified in the environment its layer
  names, and only where its path already runs there. A path that is not enabled
  (a flag that is off, a service role that is not configured, no consumer) is a
  `no` row at `unit` or `local`. Nobody builds a temporary production path to
  exercise it: a release, a catalog or configuration entry, a credential, a
  container or a host change made only so that a test can run is not a
  verification step. An owner who believes one is needed puts it on the list
  for the user with its cost before anything is built.
- **Lowest layer that proves it.** `unit`: a committed unit or contract test.
  `local`: the candidate revision run by the owner as an owned process, before
  merge. `staging`: the deployed candidate with data that exists on staging.
  `production`: only when staging data cannot prove the criterion, with that
  reason written in the row; it is a read after the production release, under
  the production authorization the assignment names, and grants none itself.
  At every layer the check is run from a local client, existing CLI or owned
  browser. Anything logic, boundary values, serialization
  between a producer and its consumer, or old persisted shapes can express is a
  `unit` row and a test that fails first; it is not left to be found on staging.
- **Pass and not pass are observations**, written so two readers reach the same
  verdict: a value, a status, a count, a rendered state. "Works", "no errors"
  and "as expected" are not criteria. Not run, not triggered and zero traffic
  are not pass.
- **Verify locally with what exists.** The method is a local command, an
  existing client or CLI, or an owned browser. A new acceptance tool or workflow,
  and a check run as a cluster Job, are never the method for one Issue. A
  product defect still gets its regression test in the product PR.
- **Able to overturn a wrong fix.** A row that carries the original defect names
  three things: the original failing input or state (the paused retry already on
  the page, the payload at the size that broke), the real consumer or surface
  that must show the result (the client that reads the response, the panel the
  user reads; not a neighbouring element, and not a test double of the
  producer), and in Not pass the observation that would prove the fix wrong.
  Evidence that would look the same under a wrong implementation is not
  evidence: a test that asserts the response shape the code happens to return, a
  measurement of the element next to the broken one, a fresh object when the
  defect was in an existing one. For an Issue admitted before this rule, add
  only what is missing when it next enters repair or acceptance.
- **Constructible now.** Name the fixture or data each row uses and confirm it
  exists or can be created within authority. A `yes` row nobody can construct
  is resolved here, before launch: choose an equivalent observable that proves
  the same criterion, or ask the user once with the options and what each
  costs. `unit` rows always run. A `no` row at another layer that cannot be
  constructed within authority is recorded as not verified with the reason.
  Neither is discovered after deployment, and no agent lowers a `yes` row by
  itself.
- **A proposal, not a campaign.** While the product is what failed, a `yes` row
  is repaired and rechecked as often as it takes. A fixture, read path or test
  data the owner can build within its authority is preparation, not a reason
  to propose. When the method has failed twice, or the next attempt needs a
  release, a shared-environment change or a credential made only for the test,
  the owner stops and proposes once: close with row N unverified, what
  verifying it would cost, and what stays unproven. It goes on the list for
  the user; only the user accepts it. An accepted row is reported as "not
  verified, waived by the user on <date>", never as passed.
- **What verification finds.** A `yes` row that does not pass keeps the Issue
  open whatever the cause. When the Issue's change caused it, it is repaired
  on the Issue. When something else did (another component, a model or
  provider), the owner reports the cause and sends the one proposal above; it
  does not repair the other component. A `no` row that fails is run once on the
  base revision. If it passes there, the change broke it: repair it before the
  close. If it fails there too, it was there before: report it once with that
  evidence, it gets its own Issue and does not hold this close. Anything else
  noticed on the way that no row covers is reported once the same way. A model
  or provider quirk is recorded, and fixed only when a path in use hits it.
- **Closure follows the layers.** The owner closes the Issue when every `yes`
  row passes at its layer or carries the user's waiver, every `no` row has a
  recorded result (passed; failed on the base revision too and reported; not
  verified, with the reason) and cleanup is done. A change that reaches a
  deployed component has at least one `yes` row at `staging`, so it is never
  closed on unit and local evidence alone; a local run is not live acceptance.
  Rows at `unit`, `local` and `staging` do not wait for a production release;
  only `production` rows do.
- **Revision.** Changing a criterion, its mark, its layer or its pass condition
  increments the block's revision with the reason and authority. At admission
  the marks are yours. After launch, moving a row from `yes` to `no` is the
  user's decision, reached through the proposal above. An owner corrects a
  wrong command, fixture or test name itself and records the correction. A
  wrong or unreachable criterion, layer or pass condition is one decision
  request; the owner does not reinterpret it.
- **Blocks without marks.** In a block that has no `Blocks close` column every
  row blocks. For each open Issue with such a block, propose the marks of all
  its rows in one item on the list for the user, with the cost of each row
  still unverified; every row keeps blocking until the user's answer is
  recorded as a revision of the block.

A small defect needs two or three rows; do not pad the table.

These dispositions belong to this section; they are not assumed to exist as
Squad enum values or GitHub labels. Do not confuse product/design READY with
release readiness. A verified downstream environment dependency can remain
pending while independent implementation proceeds; an unresolved product
boundary or contradiction cannot.

The Issue section is the decision authority. A referenced spec can supply detail
but must not become a conflicting second status/decision source. Use an explicit
revision such as `d1`; increment it when product decisions or acceptance change.
Assignments carry the Issue/section locator and revision, not the full text.
For Agent Loop envelopes use `design_admission: {"section": "Design / admission",
"revision": "d1", "status": "READY"}`. This field describes the referenced Issue
decision; it does not replace live verification or confer execution authority.
Preserve a brief supersession reason/evidence when replacing a decision.

Example: an existing `/reports` area already means evaluation comparisons.
A proposed issue-submission page must explicitly resolve terminology and entry
point overlap before READY. Similar-looking URLs are evidence of possible user
confusion, not proof of an HTTP routing collision. The final naming choice comes
from the product context; do not hard-code a universal replacement route.

## Dispatch and change handling

Before reserving a new implementation Worker, verify the READY revision against
the latest relevant Issue requirements, active dependencies and product evidence.
Check superseding merged changes and recorded user decisions, not only the
original linked PR. Resolve conflicts in favor of the current authorized contract
and cite the supersession before READY. In an acceptance-only assignment, distinguish
an obsolete assertion from a new product requirement: the Dispatcher may correct
an assertion to match already-authorized semantics without another human approval;
it may not weaken an invariant or choose new product behavior this way.
Refresh affected decisions when those inputs change; do not reread the whole
product on every sweep. For several children sharing a design, reference the
same decision and give each child bounded scope and acceptance ownership.

Keep conceptual conflicts separate from scheduling edges: fix inconsistent
semantics in the design; add a dependency only when evidence establishes an
actual prerequisite or shared-resource constraint. Mere similar names or nearby
Issue numbers do not justify serialization.

The Worker reads the assigned revision before product mutation. Missing/stale
admission or contradictory requirements trigger one concrete Dispatcher decision
request. New product semantics, entry points, scope or cross-Issue API behavior
need a design revision; ordinary algorithms, code layout and fixes within the
accepted behavior remain Worker decisions. Continue independent authorized work
while waiting. Design correction does not renew or expand execution authority.

For active Workers, do not retroactively mass-restart, revoke reservations or
send routine "reload skill" messages. Apply this contract to new assignments.
If an actual design conflict affects active work, record the decision first,
then send one verified issue-local scope correction at a safe boundary, retaining
valid implementation and existing claim/ENV safety rules.

On completion, verify acceptance against the admitted revision as well as the
technical evidence. Track post-dispatch product-decision changes and acceptance
rework when available; the goal is less rework, not more documents or roles.

## Acceptance capability and resource admission

Before READY, identify the concrete test Project/environment, the API entrypoint
being accepted, a verified fixture and its termination criteria, and the scoped
credential capability for both execution and cleanup. Reuse a known-good fixture
by immutable reference; do not share secret material in assignment prose. A
successful low-level work-template request does not prove the page/public API
contract. Missing fixtures are explicit preparation work with a bounded owner,
not a reason for each Worker to rediscover all configurations.

Classify operations by their effects. Creating credentials, imports, fixtures,
Simulation/Evaluation runs or Sessions is a write even when called "acceptance".
Model the same test Project as a shared resource; serialize conflicting writes
and require the applicable ENV claim before the first write. Independent source
or read-only work may overlap within the user's WIP. For retained artifacts,
decide custody/retention before dispatch; do not promise deletion absent an API.

A missing cleanup scope is an admission gap to resolve using already-authorized
credential tooling. It is not automatically a user-only chore. Escalate only a
verified authority/access gap or a material new decision, with the exact evidence.

Require a compact executable acceptance plan before environment admission, using
[acceptance readiness](../../../agent-loop/roles/worker/references/acceptance-readiness.md).
An AC list alone does not establish runnable fixtures, supported retry states,
access or a usable evidence path. Delegate bounded preparation outside ENV;
reuse valid checks and avoid another approval round for routine implementation.

Bind that plan to the configured API origin/Project, exact candidate and fixture
provenance. Verify execution and cleanup capabilities separately. A retained fixture
needs a fresh state read; a fixture that must be created under ENV needs a prepared
supported creation path and eligible expected state, not a fictitious existing ID.
Include bounded completion assertions, cleanup or retained custody, and any actual
login/MFA prerequisite. Use the `acceptance-readiness` delivery preflight before ENV
admission. Missing readiness blocks that affected phase; independent implementation
may proceed with a named preparation owner and resume condition.
