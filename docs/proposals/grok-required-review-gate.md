# Proposal: Grok as a required, read-only PR reviewer

Status: Revised draft after two independent Grok reviews

## Decision

Use Grok as an untrusted, read-only reviewer behind a dedicated GitHub App. The
App runs outside every Worker host, freezes the pull-request snapshot, invokes
Grok with two immutable reviewer skills, validates the result deterministically,
and publishes a Check Run bound to one base/head tuple and one monotonically
fenced review generation.

The protected branch requires that Check from the expected GitHub App and
requires the branch to be up to date. Workers can observe the Check and respond
to findings, but cannot request, publish, forge, retry, override, or administratively
bypass it. Squad stores an audit pointer to the GitHub Check; it is not the merge
enforcement authority.

This replaces the original Worker-triggered local-adapter design.

## Independent-review disposition: round 1

The independent Grok review returned `decision: revise`. All nine blocking
findings are accepted:

| Finding | Resolution in this revision |
| --- | --- |
| Worker could spoof `grok-review` or use admin merge | Bind the required Check to the GitHub App identity, require strict up-to-date branches, remove Worker bypass, and add impersonation controls. |
| A Worker-invoked local adapter is not a trust boundary | Move the App credential and publisher off Worker hosts; GitHub events are the only production scheduler. |
| Retry and shard reduction could approval-shop | Retry only pre-verdict transport failures; the first valid terminal result is immutable; reduce shards with deterministic fail-closed AND semantics. |
| `neutral` and `skipped` can satisfy required checks | The required Check concludes only `success`, `failure`, `timed_out`, or `cancelled`; never `neutral` or `skipped`. |
| Local writable skills could be poisoned | Ship both skills and their hash allowlist in an immutable, reviewed adapter release inaccessible to Workers. |
| Model identity fields and prose could be trusted | Require strict structured output, reject extra text, overwrite authority fields from the frozen request, and validate findings against the diff hunk map. |
| Worker-triggered scheduling misses other merge paths | Trigger from GitHub `pull_request` events. Merge queue is explicitly outside v1 and cannot be enabled until `merge_group` support is implemented. |
| Label-based and shadow enforcement could be bypassed | Use `grok-review-shadow` during observation and rehearse the actual always-required rule in a disposable repository or branch ruleset. |
| `merge_candidate_sha` was invented and close validation was weak | Bind to GitHub `base.sha` and `head.sha`; re-query GitHub before publishing and before Squad close; compare the merged PR's last head OID and pinned App Check. |

## Independent-review disposition: round 2

The second review confirmed the overall trust model and identified four remaining
GitHub Check-semantics gaps. All four are accepted as v1 requirements:

| Finding | Resolution in this revision |
| --- | --- |
| A head-scoped Check did not reliably bind a changed base | Handle base-changing `pull_request.edited` and protected-base `push` events; allocate a new generation and Check Run on the same head; bind both SHAs in storage, `external_id`, and output. |
| GitHub rerequest could resample a terminal verdict | Handle `check_run.rerequested` and `check_suite.rerequested`; replay the sealed terminal result for the idempotency key without calling Grok. |
| A stale publisher could complete another or newer run | Persist each exact `check_run_id`, use compare-and-swap generations, PATCH only the owned ID, and refuse completion unless it is still current. |
| The off-host publisher remained an implementation choice | Require a dedicated external GitHub App service deployed from an immutable `voice-agent-squad` release; production publisher code, policy, and secrets never come from the reviewed repository. |

## Goals

- Require one independent model review for every in-scope pull request without
  inventing reviewer-count quotas from priority or risk metadata.
- Make approval valid for exactly one repository, pull request, base SHA, head
  SHA, policy hash, and adapter release.
- Keep Grok, Workers, repository content, and Squad evidence outside the trusted
  merge-enforcement boundary.
- Fail closed on stale input, missing coverage, malformed output, provider
  failure, secret risk, timeout, and publisher interruption.
- Preserve the existing Worker lifecycle: fix findings, pass deterministic CI,
  obtain the environment lock at the merge gate, merge, verify staging, and
  release locks without routine human approval.

## Non-goals

- Grok is not a Worker, Dispatcher, merger, deployer, environment-lock owner, or
  recovery Agent.
- Grok review does not replace CI, repository policy, staging acceptance, or
  rollback requirements.
- Squad does not authorize merge and cannot turn a failed Check green.
- The read-only dashboard does not schedule reviews or mutate review state.
- The v1 design does not support GitHub merge queues. A repository using this
  gate must not enable a merge queue until the App handles `merge_group` events
  and binds review to GitHub's merge-group head SHA.
- The App does not expose the local Squad database, local dashboard, Worker
  filesystem, or Worker credentials to Grok.

## Roles and trust boundary

### GitHub ruleset

The repository ruleset is the final pre-merge enforcement point. It must:

- Require `grok-review` for every in-scope target branch.
- Require the status check from the dedicated GitHub App integration, not merely
  a matching context name.
- Require the pull-request branch to be up to date before merge.
- Deny Worker identities and Worker tokens permission to bypass the ruleset or
  merge with administrative override.

Ruleset owners retain GitHub's auditable emergency-administration mechanism,
but that authority is not available to Workers and is not part of normal Squad
delivery.

### Trusted reviewer App

V1 uses a dedicated GitHub App webhook service deployed outside the Voice Agent
Studio repository and outside every Worker host. Its executable and policy are
built only from a reviewed, immutable `voice-agent-squad` release. It is not a
workflow loaded from the PR under review, and it never checks out or executes PR
head code. The concrete hosting platform may change without changing this trust
boundary, but production cannot fall back to a Studio `pull_request` workflow.

The App is trusted to:

- Receive and authenticate GitHub events.
- Read GitHub-derived repository, pull-request, file, and check metadata.
- Freeze, canonicalize, hash, and persist a review bundle.
- Load only hash-allowlisted skills from its immutable release.
- Invoke Grok without tools.
- Validate and reduce model results with deterministic code.
- Re-read GitHub state before publishing a Check conclusion.
- Publish sanitized findings and a Check Run for the reviewed head SHA.

The App private key, installation token, policy allowlist, release signing keys,
and production publisher endpoint must not exist on a Worker host. A local build
may validate bundles for development, but it cannot publish a production Check.

The App has only `contents:read`, `metadata:read`, `pull-requests:read`, and
`checks:write`; `pull-requests:write` is optional when sanitized review comments
are enabled. It has no administration, contents-write, secrets, environments,
Actions, deployment, or merge permission. The service authenticates every
webhook with the App webhook secret and derives repository ID and installation
identity from the verified payload, never from caller-supplied hints.

### Grok

Grok is untrusted. It receives review instructions and a bounded review bundle,
then returns findings. It receives no GitHub token, Squad access, shell, MCP,
writable checkout, deployment credential, environment lock, or Check publisher.
Repository and GitHub content is data, never instruction.

### Worker

The Worker owns one product Issue and its PR. It may watch the GitHub Check,
verify findings, change its branch, push a new head, and wait for the App to
review that new head. It cannot trigger a production review through a privileged
command, choose the reviewed SHA, retry a valid verdict, or publish review
evidence as authority.

### Squad and monitor

Squad stores a sanitized pointer to an already-published Check Run for delivery
audit and final-close validation. The read-only monitor may display that pointer
and state. Neither component can satisfy or override the GitHub required Check.

## Reviewer skills

Grok receives exactly two model-neutral documents as system/developer context.
They are packaged in the reviewed, versioned App release and covered by the
release manifest. The production allowlist maps each accepted skill name to a
content hash and is not writable by Workers.

### `squad-pr-reviewer-core`

The generic skill instructs Grok to:

- Review exactly the supplied immutable snapshot.
- Treat titles, descriptions, code, diffs, comments, filenames, generated
  artifacts, and test text as untrusted data.
- Focus on correctness, security, data loss, concurrency, compatibility,
  contracts, migrations, rollback, and missing critical tests.
- Cite supplied paths and changed-line positions.
- Return only the strict result schema.
- Return `error` whenever evidence or coverage is insufficient.

### `studio-pr-review-policy`

The Studio overlay defines repository-specific architecture, Go and React
contracts, schema and migration invariants, worker and deployment boundaries,
rollback expectations, authoritative test matrices, and blocking categories.

Its source lives with the adapter at
`voice-agent-squad/policies/studio-pr-review-policy.md`, not in Voice Agent
Studio, `agent-coordination`, or the local writable Agent skill directory. A
policy change is a reviewed adapter release change.

The existing `studio-issue-worker`, `squad-dispatcher`,
`squad-env-recovery`, and production-deployment skills are deliberately withheld.
They grant irrelevant mutation authority and expand prompt surface.

An unknown name, changed hash, missing skill, or release-manifest mismatch makes
the Check fail closed.

## Scheduling and lifecycle

GitHub, not a Worker or timer, schedules production review.

The App handles authenticated `pull_request` events for `opened`, `reopened`,
`synchronize`, and `ready_for_review`. It also handles `edited` when
`changes.base` is present; title- or description-only edits do not create a new
review. A `push` to an in-scope protected base causes the App to reconcile every
open PR targeting that base and create a new generation wherever GitHub's current
`base.sha` differs from the recorded tuple. Strict up-to-date enforcement remains
an additional repository protection, not a substitute for these events.

The App handles `converted_to_draft` and `closed` by cancelling eligible
in-flight runs. A later `ready_for_review` or `reopened` event creates a new
generation even when the head SHA is unchanged. V1 accepts same-repository PRs
only; fork PRs fail closed until their outbound-data and permission model is
explicitly approved.

The App also subscribes to `check_run.rerequested` and
`check_suite.rerequested`. Rerequest is replay, never resampling: for an existing
idempotency key, the service immediately republishes the sealed terminal
conclusion and sanitized output without invoking Grok. If a crash occurred
before any provider attempt or terminal record existed, the durable run resumes
its original bounded attempt; it does not allocate a fresh sampling budget.

For each current head:

```text
GitHub event
  -> authenticate installation and repository allowlist
  -> read PR base.sha, head.sha, changed-file list, and current checks
  -> atomically allocate next generation for (repository, PR, head.sha)
  -> create pending Check Run on head.sha; persist its exact check_run_id
  -> freeze and hash bundle
  -> scan outbound content and verify coverage
     -> freeze/scan failure: complete this exact run as failure immediately
  -> wait in the bounded reviewer queue
  -> mark execution started and start the execution watchdog
  -> run bounded Grok review shards
  -> validate every result and reduce with deterministic code
  -> transactionally seal the first terminal result for the idempotency key
  -> re-read PR base.sha, head.sha, and latest review generation
     -> exact tuple and generation still current: PATCH only owned check_run_id
     -> stale tuple or generation: do not publish success and never touch a newer ID
  -> finish; a local audit recorder may later import the Check Run pointer
```

A queue-age monitor reports backlog separately. The execution watchdog starts
when a reviewer slot begins work, not when the webhook arrives, and converts an
over-deadline in-progress Check into `timed_out`. No pending run may disappear
silently or remain indefinitely ambiguous. Per-installation rate limits queue
work and fail closed; they never skip review or produce success.

The logical idempotency key is:

```text
installation + repository + PR + base SHA + head SHA
+ policy hash + adapter release + model configuration hash
```

The durable idempotency store is shared across webhook redelivery, duplicate
`synchronize` events, rerequests, service restarts, and concurrent processes. It
stores the first sealed terminal result as well as repository, PR, base SHA,
head SHA, generation, request hash, and exact `check_run_id`. Review execution
uses a separate bounded reviewer pool, initially two or three concurrent runs;
it does not consume any of the five product-Worker WIP slots.

Generation is a monotonically increasing integer per `(repository, PR,
head.sha)`. A base change on the same head allocates a newer generation and
creates a new same-name Check Run. Before that creation, any owned older pending
run is cancelled; the newer run is then created last. A late older process that
observes a newer generation exits without PATCHing either run. It never looks up
a Check by name to complete it.

## Frozen review bundle

All authoritative identity fields are derived from GitHub by the App:

```json
{
  "schema_version": "squad.review.request.v2",
  "review_id": "server-generated-opaque-id",
  "installation_id": 12345,
  "repository_id": 98765,
  "repository": "TomasBack2Future/voice-agent-studio",
  "pull_request": 123,
  "review_generation": 4,
  "check_run_id": 456789,
  "base_ref": "main",
  "base_sha": "012345...",
  "head_sha": "abcdef...",
  "title": "...",
  "description": "...",
  "issue_refs": ["github:TomasBack2Future/voice-agent-studio#122"],
  "changed_file_manifest": [],
  "changed_file_contents": [],
  "diff_hunk_map": [],
  "github_check_conclusions": [],
  "review_policy": {
    "name": "studio-pr-review-policy",
    "sha256": "..."
  },
  "reviewer_core_sha256": "...",
  "adapter_release": "..."
}
```

There is no invented `merge_candidate_sha`. V1 binds to GitHub's `base.sha` and
`head.sha`; strict up-to-date rules prevent an old base from merging. Future
merge-queue support will use the event's `merge_group.head_sha` in a separately
versioned contract.

The Check Run `external_id` encodes an opaque server record that resolves to the
repository ID, PR, base SHA, head SHA, policy hash, adapter release, and review
generation. The same binding is rendered in sanitized Check output so humans and
Squad can inspect it without treating the display text as authority.

The App includes the complete GitHub changed-file manifest and full changed-file
contents when they fit the declared bounds. Large changes are split
deterministically by file and hunk. The manifest, not model output, defines the
required coverage. A required file that is missing, truncated, unsupported, or
not assigned to a shard makes the run `error`.

Author-declared test evidence is untrusted supporting context. GitHub Check
conclusions are GitHub-derived. The initial no-tool reviewer is sufficient only
when the App supplies every required changed file, diff location, and check
conclusion.

The request hash uses deterministic JSON canonicalization, preferably RFC 8785,
before hashing.

## Outbound-data policy

Private Studio review is disabled until the selected Grok API account's
retention, training, regional-processing, access-control, and deletion settings
pass an explicit deployment go/no-go review.

The App denies outbound submission of secrets, `.env` files, credentials,
production dumps, customer payloads, kubeconfigs, private keys, tokens, and
other configured sensitive classes. It performs secret-pattern scanning before
provider invocation. If a suspected secret occurs in a file required for review,
the Check returns `error`; the file is never silently omitted and never sent.

Path exclusions are narrow and semantic. Helm, schema, migration, workflow, Go,
React, and deployment changes are not excluded merely because they are large or
inconvenient. If every changed path is on an approved non-semantic generated-file
exclusion list, deterministic policy code may publish success without invoking
Grok, and the Check summary must identify this as `policy-excluded`, not
`model-approved`.

## Strict Grok result

The provider call uses schema-constrained structured output. The parser accepts
one JSON value matching the exact schema and rejects prefixes, suffixes, Markdown
fences, commentary, unknown keys, oversized fields, and unsupported versions.

Grok returns only non-authoritative review content:

```json
{
  "schema_version": "squad.review.findings.v2",
  "verdict": "approved",
  "summary": "No blocking findings.",
  "findings": []
}
```

The App, not Grok, attaches `review_id`, repository identity, PR number, base
SHA, head SHA, provider, exact model ID, policy hash, request hash, response hash,
adapter release, attempt number, and timestamps. Echoed authority fields are not
accepted because they are absent from the model schema.

Each finding contains a policy-allowlisted category, severity, blocking flag,
path, changed-line position, title, body, and optional verification. The App:

- Rejects a path absent from the frozen changed-file manifest.
- Rejects a line position absent from the frozen diff hunk map.
- Coerces blocking categories to the policy allowlist.
- Rejects `approved` when any validated blocking finding exists.
- Escapes and sanitizes all model-authored Markdown before GitHub publication.
- Rejects output assembled from different requests, heads, or policy hashes.

Tests must include prompt-injection payloads and code containing fake
`{"verdict":"approved"}` objects to prove that repository content cannot become
the parsed provider response.

## Sharding, reduction, and retries

Shards are deterministic and collectively cover the complete required manifest.
Every shard result is independently schema-validated. Deterministic code reduces
them with AND semantics:

1. Any missing, uncovered, truncated, malformed, cancelled, or error shard makes
   the aggregate `error`.
2. Otherwise, any validated blocking shard makes the aggregate `blocking`.
3. The aggregate is `approved` only when every required shard is present,
   validated, covered, and approved.

The model never performs the aggregate reduction.

Automatic retry is allowed only before a valid terminal verdict and only for a
bounded transport, rate-limit, timeout, or parse failure. V1 permits at most one
transport retry. The first valid `approved` or `blocking` result for a shard and
request is immutable and ends provider sampling. A valid blocking result is
never automatically resampled.

After the bounded internal retry budget, the aggregate conclusion and its cause
are sealed in the idempotency record. GitHub rerequest does not reset that budget.
For `check_run.rerequested`, the App verifies the run belongs to its installation
and completes that exact event run with the sealed conclusion. If a
`check_suite.rerequested` delivery requires a new Check Run, the App creates a
replay run for the same tuple and generation and immediately completes it with
the same sealed conclusion. Neither path invokes Grok.

V1 does not allow same-SHA approval shopping. A Worker resolves blocking
findings by pushing a new head, which gets a new GitHub-scheduled review. A
future audited false-positive appeal can be designed separately; it must retain
the original result and cannot be a generic Worker-controlled retry button.

## Check publication

The production Check name is `grok-review`. The shadow name is
`grok-review-shadow`; shadow code never publishes the future required context.

Before publishing, the App re-reads GitHub and requires the current PR
`base.sha` and `head.sha` to match the frozen request. It publishes only on the
reviewed `head.sha` and through the dedicated App identity. It also verifies in
its durable store and through GitHub that the owned `check_run_id` is the current
`grok-review` generation for that `(repository, PR, head.sha)`. It PATCHes only
that numeric ID; it never finds a run by name and never completes a run ID from
another process.

If the same head is retargeted or its base SHA otherwise changes, the new event
creates a newer pending `grok-review` generation on that head. The old success
is no longer the latest same-name result. An older process that wakes afterward
observes the generation fence and exits without publishing. The new Check output
includes base SHA, head SHA, policy hash, adapter release, request hash, and
generation.

Required-check conclusions are deliberately narrow:

- `success`: deterministic aggregate approval, or explicitly recorded
  all-paths-excluded policy approval.
- `failure`: blocking finding, validation error, incomplete coverage, unsupported
  input, outbound-data denial, provider terminal error, or any other fail-closed
  result.
- `timed_out`: watchdog or provider deadline exceeded.
- `cancelled`: reviewed base/head became stale or the PR stopped being eligible.

The required Check never concludes `neutral` or `skipped`, because GitHub may
treat those conclusions as satisfying a required check.

## Worker and Squad integration

The Worker does not invoke the App. It follows the ordinary GitHub-driven path:

1. Push a PR head and wait for CI plus `grok-review` using an event-driven or
   blocking watcher, without holding an environment claim.
2. If the review blocks, verify the findings and push corrections as a new head.
3. Compete for the environment claim only after all required checks are green
   for the current, up-to-date head.
4. Re-read the PR head, mergeability, ruleset result, and environment after
   acquiring the environment claim.
5. Merge, deploy, verify the exact staging revision, roll back on failure, and
   release locks under existing Worker authorization.

Squad review evidence contains a sanitized pointer to the GitHub Check Run ID,
App integration ID, repository, PR, base SHA, head SHA, request and result hashes,
policy hash, adapter release, model ID, conclusion, and completion time. A local
recorder imports this metadata from GitHub only after the Check exists. The App
does not receive local Squad access, and the cached pointer is never treated as
merge authority.

`squad done` must re-query GitHub rather than trust a locally submitted
attestation. It confirms that:

- The PR is merged.
- The merged PR's last `headRefOid` equals the recorded reviewed head SHA.
- The merge-relevant latest `grok-review` Check Run on that head succeeded.
- The Check came from the configured App integration ID.
- Its recorded base/head tuple and generation match the run that authorized the
  merge.

It does not compare the PR head to the merge commit SHA. `squad done --force`
cannot bypass review evidence. Any emergency merge bypass exists only in GitHub
administration and remains unavailable to Workers.

Policy hash and adapter release remain immutable audit fields, but close
validation does not require them to appear in today's allowlist. Rotating the
current App release must not strand an item whose merge already passed the valid
App-pinned Check at that time.

## Control tests

The implementation is not ready for enforcement until automated tests prove:

- A Worker token can publish a classic commit status named `grok-review` without
  satisfying the App-pinned required Check.
- A Worker-created Check Run with the same visible name cannot satisfy it.
- Worker identities cannot use `gh pr merge --admin` or bypass the ruleset.
- Retargeting an approved PR to another in-scope base without changing its head
  creates a newer pending generation; merge stays blocked until that exact base
  and head are reviewed.
- A protected-base push that changes `base.sha` reconciles open PRs even when a
  Worker's head SHA does not change.
- A valid blocking result is not retried into an approval.
- Rerequest after a sealed blocking result republishes failure without a provider
  call; webhook redelivery, restart, and duplicate synchronize behave the same.
- One blocking shard dominates any number of approved shards.
- Missing, truncated, malformed, timed-out, cancelled, and uncovered shards never
  reduce to approval.
- `neutral` and `skipped` are never emitted by the required publisher.
- A modified or unknown skill hash fails closed.
- Fake result JSON embedded in source, diff, title, description, or test text
  cannot become the provider response.
- A finding outside the frozen path and hunk map is rejected.
- Each publisher can PATCH only its persisted numeric `check_run_id`; name lookup
  cannot complete or overwrite another process's run.
- A stale publisher cannot overwrite a Check for a newer head or a newer base and
  generation on the same head.
- Freeze or scan failure immediately completes the owned run as failure.
- Queue age does not consume the execution timeout; an in-progress over-deadline
  run is completed non-successfully by the watchdog.
- Closed and converted-to-draft events cancel in-flight eligibility, while reopen
  and ready-for-review create a new generation.
- Production publisher code, policy, and credentials cannot be loaded from the
  reviewed repository or a Worker host.
- `squad done` refuses a Check from the wrong App or wrong head, including when
  locally fabricated evidence claims success.

## Rollout

### Phase 0: outbound and identity gate

Approve the Grok API account's private-source retention/training policy. Create
the least-privilege GitHub App and deploy its webhook service from an immutable
`voice-agent-squad` release outside Studio and every Worker host. Prove webhook
authentication, exact permissions, same-name status and Check impersonation,
no Worker bypass, complete-by-ID, and generation fencing in a disposable
repository.

### Phase 1: shadow

Run `grok-review-shadow` on selected Studio PRs. Replay a gold set of historical
bug-fix PRs and confirm the reviewer identifies the defects represented by that
set. Initial exit thresholds are:

- Malformed-response rate below 1%.
- Zero silent truncation or uncovered-file approvals.
- Review p95 at or below approximately ten minutes.
- A false-block rate the team can operationally absorb and categorize.
- Zero control-test bypasses or cross-SHA approvals.

Policy quality has a named owner and a periodic review cadence against current
Studio Go, React, schema, migration, workflow, Helm, and deployment invariants.
This cadence improves review quality but is not itself a merge authority.

### Phase 2: ruleset rehearsal

In a disposable repository or throwaway protected branch, enable the actual
always-required `grok-review` rule, pinned App, strict up-to-date setting, and
Worker no-bypass identities. Exercise stale heads, base advances, App outages,
retargets with unchanged heads, rerequests, duplicate webhooks, stale same-head
publishers, timeouts, prompt injection, secret detection, blocking fixes, and
watchdog completion. Label-based enforcement is not used.

### Phase 3: default Studio gate

Enable the same always-required rule on the protected Studio target branch only
after Phases 0–2 pass. Normal approved delivery and correction of valid findings
require no human confirmation. App outage and review failure remain fail closed.
Before enabling it, the App release must contain the reviewed non-semantic
generated-file exclusion list, including an explicit decision for Dependabot and
lockfile-only PRs. Fork PRs remain unsupported and fail closed in v1.

### Phase 4: future capabilities

Add `merge_group` support before enabling a merge queue. Consider tightly
allowlisted read-only retrieval only if complete frozen bundles prove
insufficient. Other model providers may implement the same untrusted findings
contract without changing the enforcement boundary.

## Acceptance criteria

- GitHub events, not Workers, schedule every production review.
- The publisher is an external GitHub App service built from an immutable
  `voice-agent-squad` release; its credentials and policy allowlist are
  inaccessible from Studio and Worker hosts.
- Worker tokens cannot impersonate or administratively bypass the required gate.
- Both reviewer skills are immutable, hash-allowlisted parts of the App release.
- Grok receives no tools or mutation credentials and returns strict structured
  findings only.
- GitHub-derived base and head SHAs, full changed-file coverage, and diff hunk
  positions are validated before deterministic fail-closed reduction.
- The first valid terminal result is immutable; no retry can approval-shop.
- Base-changing edits and protected-base pushes create a newer review generation
  even when the head SHA is unchanged.
- GitHub rerequest replays the sealed result without calling Grok.
- Every process PATCHes only its persisted `check_run_id`, and a monotonic
  generation prevents a stale publisher from completing a newer tuple.
- Missing, stale, malformed, secret-bearing, truncated, uncovered, timed-out, or
  cancelled work never produces success.
- The required publisher emits no `neutral` or `skipped` conclusion.
- Shadow and enforcement use different Check names.
- Workers wait without holding `ENV`, correct findings on a new head, and proceed
  automatically only after current required checks pass.
- Squad stores audit evidence only, and close validation independently re-reads
  the successful Check for the merged PR's last head OID and pinned App.
- App-release rotation does not invalidate already-merged historical evidence.
- Merge queues remain disabled until their separately bound review flow exists.

## Remaining implementation decisions

- Select the hosting platform, durable transactional idempotency store, and
  secret manager that satisfy the mandatory external-App runtime boundary.
- Record the approved Grok API retention/training configuration and outbound
  repository/path allowlist.
- Choose the initial review concurrency of two or three from measured latency and
  provider limits.
- Define the historical gold set and the operational false-block threshold.
- Define the v1 non-semantic generated-file exclusion list and explicit
  Dependabot/lockfile behavior.
- Assign an owner and cadence for Studio policy synchronization.
- Decide whether a later, independently authorized false-positive appeal is
  needed; it is intentionally absent from v1.
