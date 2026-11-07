# Executable acceptance and interrupted ownership

Use before environment admission and when an acceptance operation stalls. Reuse
one concise section of the existing assignment/checkpoint, not another approval
form, daemon, role or independent ledger.

## Prepare before holding ENV

Map each required outcome to its command/script or necessary UI interaction,
immutable fixture, observable assertion and evidence location. Verify available
access and a cheap read-only baseline through the intended path. Prepare scripts
and negative-path fixtures before claiming ENV; any fixture creation still needs
its applicable write authorization and lock. For retry/cancel/recovery acceptance,
identify a supported eligible state and how to obtain it; do not discover mid-test
that only failed/cancelled records can be retried. Reuse valid retained evidence.
Separate correctness, environment health, performance objectives and review state.
Existing approved hard gates remain; do not invent latency gates from one sample.

Map each assertion to real-target execution, an applicable committed regression,
retained evidence, or an uncovered condition before starting the journey. Explain
version/configuration applicability and keep required live assertions live. This
is evidence selection within the existing contract, not another approval: ask
only when changing a required criterion or its accepted evidence would change
scope. Do not discover the closure standard at the final report.

Bind the plan to the configured API origin/Project and candidate/fixture provenance.
Check execution and ephemeral-cleanup scope separately, including the actual supported
cleanup method; credential presence is not sufficient. A retained fixture needs a
verified reference/state. When creation requires ENV, prepare its supported command,
definition and eligible expected state before the lock, then verify the created
fixture under ownership before the assertion. Do not require an unauthorized write
to pass preparation or claim that a prepared fixture already exists. For retained
writes, record custody instead of inventing a deletion API. Identify login/MFA or
other human-only prerequisites early and only from verified capability evidence.

Use the existing read-only delivery preflight's `acceptance-readiness` action where
that helper is available; its compact fields are defined by the
[executable fixtures](../../../../coordination-skills/studio-issue-worker/scripts/delivery-check.test.mjs).
This checks evidence completeness, not actual access or permission. Retain the
checked plan in the current checkpoint, and recheck only
changed/stale inputs. Incomplete readiness blocks ENV admission, not independent code
or script preparation; assign one preparation owner and an explicit resume condition.

Use the UI for the user interaction being accepted. Use supported authenticated
API scripts for hashes, frozen metadata, pagination and final-state assertions.
Keep UI authentication in its supported browser context when required; never
export cookies or credentials to simplify a script. For browser-only access,
use bounded asynchronous requests and sanitized results rather than a long
synchronous eval. Choose explicit timeouts and bounded polling appropriate to
the operation; do not treat the browser bridge's timeout as server duration.

## Prepare alongside implementation, not after deploy

Start the acceptance plan when implementation starts and finish it before the
candidate deploys. For every acceptance criterion record the concrete assertion,
fixture, real target and version, the access path qualified by a cheap read,
model-call budget, cleanup and the failure branch (what is retried, what moves
to a later candidate, what keeps the Issue open). Check the target's actual
executability for that assertion before deploy: required model or evaluator
binding, sampling or enablement settings, feature flags, pool or queue read
paths. A criterion that cannot execute on the current target is found during
implementation, not after rollout.

- **No unrecoverable fixtures.** Before creating Sessions, runs, queue entries
  or other writes, prove a supported way to observe them and to cancel or clean
  them up. Without both, do not create them; record the missing capability and
  route it to its owner. Never remove another owner's fixtures to recover.
- **Reuse standing authority.** Apply the latest recorded standing budget and
  access authorizations (for example a per-task model-call cap) and recompute
  older cost or access blockers against them before asking. Count retries in
  the same budget. A standing budget never widens shared configuration,
  production or customer scope.
- **Surface real human decisions early.** When preparation shows a decision
  only the user can make (real-model package, production history, customer
  data, a disputed criterion), raise it once with options and its effect as
  soon as it is known; continue independent work meanwhile. Do not collect such
  decisions for the final report.
- **Unfinished technical preparation is not a waiver.** A missing fixture,
  read path or tool gap is preparation work for its owner. Never ask the user
  to waive a criterion because its preparation is incomplete.

## Design verification from behavior, not from the patch

Derive checks from the user-visible behavior and state transitions the change
promises, then from the first symptom. For retries, idempotency, persistence
and version compatibility, cover by risk: data written by the old code read by
the new code, repeats across time and across deployed versions, resume after a
failure, duplicate operations and real content conflicts (#1977 needed a second
deploy because only the time variation was tested). For UI changes, run the
actual operation sequence in a local browser: cancel, retry, refresh and
re-enter. Query and index behavior is proven on the matching engine version
(integration test) and, where authorized, by a read-only check of the target;
a mock or a different engine version is not evidence for it.

When an acceptance check fails, first try to disprove the failing assertion
with the smallest discriminating check before changing code, schema, DDL or
asking for permissions. Commit a regression only when it guards lasting
behavior; keep valid evidence and repeat only checks a change actually affects.

## Choose the shortest authorized verification path

Pick the cheapest path that proves the assertion on the real deployed target;
a one-off verification does not wait for a repository script, Action or tool PR.

- **Local client, real target.** staging and production may both be verified from
  a local command, temporary script, supported API client or browser. "Local"
  is where the client runs, not a copy or mock: confirm the target URL, the
  served page/service version, the effective configuration and what the
  assertion covers. A mock or local-copy pass is not live acceptance.
- **UI journeys: prefer browser use.** Operate the deployed page directly with
  the client's browser automation or computer-use, reusing a valid browser login
  the task is authorized to use. Do not require a new user, copied profile,
  exported key/cookie or headless-runner workflow. Keep identity isolation when
  the task requires it, never use another person's session or revoked
  credentials, and ask once for login/MFA only when it is truly user-only. API
  reads may confirm backend state, hashes, paging and cleanup but never stand in
  for the UI journey; report what was actually observed.
  Before declaring login unavailable, inspect the existing recorded authorization
  and permitted managed browser profiles, then qualify the selected session with
  a non-secret identity/access check in an owned pane. A fresh unauthenticated
  profile proves nothing about an already-authorized session. Reuse a valid
  shared qualification while its scope and state apply; coordinate cookie-jar
  changes and close only owned panes, without copying authentication state.
- **Three kinds of work stay separate.** A one-off script lives in the task's
  temporary/evidence directory (record its hash or content reference). A product
  defect still gets a meaningful source regression test. A tool is committed only
  for repeat reuse, an explicit task requirement, or a protected execution
  boundary that truly needs a maintained adapter; reuse an existing remote
  executor rather than building one per Issue. Optional tooling follow-up never
  delays closing an Issue whose gates are met.
- **Permissions follow the operation, not the location.** Local does not add an
  approval and does not bypass any: read-only checks, synthetic writes, model
  calls, configuration changes and deployments each keep their own authorization,
  budget, ownership/ENV claim and cleanup. Production verification relies on an
  existing explicit authorization and does not widen it. If a credential or
  network path works only inside a protected remote environment, name that exact
  limit and use the supported remote entry for that case only; it is not a rule
  that every acceptance needs a workflow.
- **One bounded execution scope.** Carry forward the current decision's allowed
  target, owned resources, operations, limits, cleanup and stop conditions.
  Ordinary preparation, assertions, readbacks and cleanup inside that scope
  proceed without a new phase decision for each step. Report progress without
  turning the report into a permission request. A real hold or changed shared
  write, privilege, budget or scope still requires its existing decision route.
  If a method change supersedes an old routine wait, obtain the owner's durable
  replacement once; do not ignore the old decision or rebuild its gate locally.
- **Evidence is light.** In the Issue summary/checkpoint record target and version,
  UTC time, the operation or script reference, expected vs actual, needed
  request/trace IDs, sanitized output or screenshot, cleanup result and what was
  not covered. A passing local run satisfies the gate without a GitHub Action URL, unless the gate itself requires a specific CI job or protected remote execution.
  Scripts and screenshots do not prove a result; the observation does.
  Derive log windows and deadlines from the operation's timestamp/epoch with an
  explicit timezone, not the chat's calendar date. At a material result or wait,
  update the owned Issue summary with the current remaining condition and owner;
  a detailed internal thread is not a substitute for that current projection.

## Diagnose a timeout once, with attribution

Record the operation, UTC window, component revision, expected status/semantic
result, and request/trace ID when available. Distinguish tool cancellation,
client/eval timeout, network/edge wait, server error and unfinished external work.
Correlate the same request with structured server evidence before assigning the
cause to the server, browser or change. Successful unrelated requests do not
exonerate a failed request; empty or truncated logs do not prove absence.

After two no-progress observations, or the existing admission's diagnostic budget,
perform one bounded diagnosis before another attempt. Change the failed condition
or execution method before retrying. Do not repeatedly reopen panes or launch
untracked requests. Reuse completed portions of acceptance and report remaining
assertions explicitly. A healthy revision is not functional acceptance; an
acceptance timeout is not proof that rollout failed. Classify failing control
fixtures before attributing them to the new feature.

For a shared failure, report its run/trace, exact component identity and failed
assertion once, then follow the
[shared-blocker contract](operational-readiness.md#one-shared-blocker-one-repair-owner).
A transient error permits only the existing policy's safe bounded retry
(default at most one when no budget
is specified); verify prior operations are terminal or safely idempotent first.
An unchanged deterministic error needs a verified changed condition before rerun.
Do not discard other accepted assertions or group unrelated error codes as one
cause. An unresolved external mutation never becomes permission for a new attempt.

A review timeout/error has no verdict. Follow actual enforcement policy, retain
the unresolved review status, and repair the operational cause before resampling;
never relabel an empty findings list as approval. Static review does not replace
runtime correctness or transport diagnosis.

## Interrupted while owning an environment

Honor explicit stop and user-rejected tool results. Do not bypass a denial,
automatically resume the rejected operation, or infer user intent from a generic
client cancellation label. If the stop permits a handoff, record the primary/ENV
claims, last verified revision, external operations, remaining assertions and
reason. If execution was stopped immediately, Dispatcher reconstructs this
read-only in its next existing reconciliation cycle; do not require another
Worker tool call to make the pause visible.

Paused with ENV is an actionable coordination state, not ordinary lock contention
or proof of session death. Dispatcher identifies it in the existing cycle and
arranges an authorized resume, handoff or safe owner release after verification.
It never force-releases or resumes on elapsed time alone. Preserve recovery
ownership while an external operation remains unresolved. When writes and
version-sensitive checks finish, the owner releases ENV before final bookkeeping.
Distinguish active work time from interrupted time in the outcome.
