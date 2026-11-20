# Review readiness and freeze contract

Use this contract before invoking any independent reviewer. Its purpose is to
make review a gate over one stable, complete PR diff instead of an expensive
observer of work that is still changing.

## Trigger review at readiness

The first complete, stable implementation that passes the companion audit,
fast local gates and author self-review is ready for formal review. "Final"
means no known implementation correction remains for this attempt; it does not
mean merge permission, the whole batch, full CI or an environment is ready.
Start the owning Worker's managed review immediately on that frozen tuple,
in parallel with full CI. A merge hold, ENV queue or merge-only predecessor
must not defer review. An unresolved implementation/interface dependency that
makes the diff incomplete still blocks admission; record that specific reason.

For cross-repository interfaces, authentication, migrations or similarly costly
contracts, resolve the design/contract risks during design admission, before
implementation. This bounded design check does not approve code and does not
add a mandatory second model review to ordinary changes. A design decision that
needs another owner goes through the existing decision route, not a new reviewer
session. The full-diff code review remains bound to the exact PR tuple.

Record `review_ready_at`, `review_started_at`, and the reason for any delay in
checkpoint evidence. Reconcile an existing invocation before sampling; do not
turn repeated scheduling checks into repeated reviews. Before merging, recheck
exact revision and current required gates; earlier timing does not make stale
review evidence valid. Keep the existing single-flight and write barrier.

For a `human-pr` profile, the same readiness and freeze rules apply before a PR
exists: use the clean local base/head, complete-diff hash and frozen contract
file/hash in place of the PR body. The single-flight key is the assigned repository
and branch. Use managed local review and return local evidence for human PR
creation; never invent a PR number or remote approval.

## What the reviewer reviews

The review input is the complete aggregate `base...head` diff plus the frozen
Issue contract, acceptance mapping and substantive PR description, including the
original failing case from the Issue's Verification block. Ask the reviewer to
check once, against that case, that the tests and evidence would fail if the
fix were wrong or absent. Commits are
authoring history, not separate review units. Do not start one review for each
commit or use an approval for an earlier head as evidence for a later head.

## Admission audit

Before freezing the tuple, finish the profile's fast gates and author
self-review. Inspect every applicable companion surface for the complete change:

- implementation source and generated artifacts;
- targeted tests, negative paths and risk probes;
- CI/classifier routing and required workflow coverage;
- schemas, migrations, fixtures and compatibility behavior;
- deployment/configuration manifests and runtime wiring;
- user/operator documentation, project profiles and maintained skills;
- acceptance tooling, observability evidence and rollback behavior.

Mark each surface complete or not applicable with a reason. Resolve all known
corrections before review. A clean worktree alone is not readiness.

For a repair, apply the [critical failure conditions](acceptance-readiness.md#design-verification-from-behavior-not-from-the-patch)
before this declaration: original failed state, relevant maximum scale and real
producer/consumer boundary. Include their existing evidence or precise gap in
the review input. A small happy-path fixture or mock-only pass cannot discharge
a required condition that it does not exercise.

Create a durable admission receipt containing the repository and PR, base and
head SHAs, complete-diff hash, substantive PR-body hash, clean-worktree and
remote-head verification, completed fast gates, required/completed companion
surfaces, resolved known corrections, and the observation that the tuple was
stable at admission. Reject a receipt with missing or inconsistent fields.

## Single flight and write barrier

- Allow at most one in-flight independent review for a repository and PR,
  regardless of head SHA. Register the attempt before starting the reviewer.
- After admission, the receipt is a write barrier. Do not edit files, commit,
  push, rebase, force-push or make a substantive PR-body change while review is
  running.
- Start the managed review and full deterministic CI concurrently, retain their
  process handles or callbacks, and deterministically join both. An idle terminal
  or anonymous background process is not a joined result.
- Every pushed head starts this loop again. Before switching to other work or
  ending a turn, record the exact head, each CI run and attempt, the review
  attempt, the native background task that will notify you, and the next action
  for success and for failure. Parallel work is fine; losing the follow-up is
  not. A `gh run watch` covers one run; on its notification read every required
  check and the review for the head, confirm the head is still current, then
  merge or fix. A watch exiting is not a merge.
- Re-read the PR base/head and hashes before accepting or publishing the result.
  A result belongs only to the exact frozen tuple in its receipt.

## Invalidating and repeating review

If CI, author inspection or another signal exposes an omission while review is
running, invalidate the receipt and cancel the reviewer when supported. In all
cases, join and mark the old attempt superseded before registering another one.
Return to implementation, collect all known corrections, repeat the complete
admission audit and freeze one new final tuple.

A valid blocking review finding follows the repair protocol below. Rerun relevant
gates and admission, then review the new final complete diff once. Do not resample the same
unchanged tuple, create no-op commits, lower effort or run overlapping reviewers
to seek approval.

Record the total invocation count, superseded/stale count, post-freeze commit
count, and safe wasted-duration/token metrics in the checkpoint. These are
efficiency signals, never authority to skip a required current-head review.

## Repair after a blocking finding

Verify the finding against the original Issue requirements and the reviewed
revision before accepting it. A valid blocking finding is a demonstrated defect
that prevents the assigned outcome or violates a required repository contract;
the review command failing is not itself such a finding. Timeouts, tool/provider
errors, nonblocking suggestions and disproven false positives do not trigger an
implementation-model change. An unresolved finding needs one discriminating
check, not automatic acceptance or dismissal.

At the first valid blocking finding in the continuing canonical repair, switch
the **implementer** to the escalation model selected by the user/workspace before
the next repair edit. Do not change the independent reviewer's model, mechanism
or verdict contract. Use the existing Dispatcher transition and readback route
under [session continuity](../../../../coordination-skills/squad-dispatcher/references/session-continuity.md#review-failure-model-transition).
If no escalation model/authority is selected, request that one decision; never
invent a fallback. Already using the selected model satisfies the model gate.
Another PR or resume for the same unfinished outcome does not reset this rule.

Record the review tuple, verified finding and invariant, current/selected model
and observed transition in the existing checkpoint. Preserve owner, native
identity, branch, uncommitted work, valid evidence and consumed review attempts.
Join the review and honor its write barrier; do not kill an in-flight operation
to switch models. A queued request or model name in a title is not adoption.

Before editing, map the original failure and the conditions the repair could
break to checks in the existing acceptance mapping. For concurrency/retry code,
consider a committed operation whose response is lost, duplicate requests and
cancel/execution interleavings; exercise each relevant sequence and its durable
downstream result. Explain inapplicability by the actual contract. A test that
only mirrors the finding is insufficient when the change affects other states.
Use the [behavior-first verification contract](acceptance-readiness.md#design-verification-from-behavior-not-from-the-patch);
this does not require a new acceptance tool or a blanket full-suite rerun.

If the same causal mechanism or violated invariant recurs after a repair,
including a repair-induced regression, stop incremental patches. Keep the same
owner and code and prepare a compact brief: original requirements/non-goals,
current complete diff and exact revisions, failing event order, affected state
transitions/invariants, prior attempted fixes and still-valid evidence. Under the
selected escalation model, reassess the state transitions, commit boundaries and
producer/consumer contract before choosing one coherent in-scope repair. Record
why it fixes the mechanism and how the original and affected conditions will be
rechecked, then implement and review the new complete diff. Already using that
model still requires this reassessment. Similar wording or a repeated tool error
alone is not recurrence. Do not reset review budgets, discard valid work, launch
a replacement owner or widen the product scope to achieve a clean context.
