# Parallel review readiness

Use this after author self-review and before any environment claim. Parallel
execution changes scheduling, not merge authority.

## One final full-diff review

A review invocation always receives one complete pull-request diff from the
frozen base to the frozen head. It does not review commits independently. The
normal target is one invocation for the final complete diff, not one invocation
after every push.

Do not freeze merely because implementation code compiles. First close the
Issue-specific companion set: source, regression tests, CI/classifier wiring,
schemas and fixtures, deployment/configuration, documentation, operator skills,
acceptance tooling and rollback material that the touched behavior requires.
Mark an inapplicable companion explicitly rather than silently omitting it.

Before sampling, create and validate a review admission receipt containing the
repository and PR, exact base/head, hashes of the complete diff and frozen PR
body, clean-worktree and remote-head verification, passed fast gates, the
required/completed companion sets, and evidence that no known correction
remains. Use `scripts/delivery-check.mjs` with `action: review`. A prose claim
that the head is stable is not admission evidence.

Only one review may be in flight for a repository/PR. Reconcile or cancel/join
that owned invocation before another can start, even when the next invocation
would use a different head. A valid result or timeout on the same tuple is never
resampled; the existing materially repaired pre-sampling exception remains the
only same-tuple retry.

## Start and join

Before freezing, apply the coupled-invariant and repeated-finding guidance in
[delivery quality](delivery-quality.md). Inspect total diff size and generated
evidence share against the installed review input capability. Keep concise
human-readable evidence plus complete hashed artifacts; never omit code or
truncate a diff to fit. If the complete input cannot be retrieved, record a
pre-sampling operational failure and arrange a supported complete retrieval or
cohesive PR split. No sample is useful without a complete input.

Consult durable review status for the exact repository/PR/base/head before
invocation. A running, timed-out or valid completed sample cannot be retried just
because its callback or publication was missed. Reconcile the existing attempt;
only the existing materially repaired pre-sampling exception allows retry.

1. Pass applicable fast local checks: formatting/`git diff --check`,
   compilation or type checking, and targeted regression tests. Complete the
   companion audit, stabilize the whole change, rebase to the latest observed
   target, push once, and verify that the local clean head equals the remote PR
   head. Freeze repository, PR, base/head, complete-diff hash, PR-body hash and
   chosen effort in the admission receipt. Keep the PR description concise while
   preserving requirements, compatibility constraints, tests and rollback
   evidence. The wrapper still receives the complete diff; never truncate it.
2. Choose `--reasoning-effort medium` for ordinary changes, or `high` for
   security/authentication, schema/data migration, concurrency/locking, or
   deployment/rollback changes. Choose before sampling. Pass the same effort to
   `squad-grok-review doctor` and the review invocation; retain the default
   user-selected 20-minute timeout for new invocations. Do not restart an
   existing review or resample a timed-out head merely to use the longer cap.
   Do not change the user's global Grok configuration or
   the owning Codex Worker's model/effort.
3. After doctor succeeds, launch one local wrapper invocation for that frozen
   input as a managed asynchronous process, keeping its process/session handle.
   While it runs, execute/watch full required CI and integration tests on the
   same revision. Reuse existing workflow runs and already-required isolated
   builds; do not start duplicate builds or a GitHub-hosted waiting job.
   No separate reviewer Agent, raw Grok call, or untracked detached process.
4. Treat the receipt as a write barrier. The owning Worker may collect existing
   evidence and prepare acceptance,
   read-only SLS log queries, and rollback notes in a separate scratch artifact.
   Do not edit, commit, push, rebase, or change the frozen PR description; do not
   claim ENV, mutate shared test data, or deploy while waiting. Other ready
   Workers may use staging.
5. Join both outcomes deterministically. Collect the wrapper exit code and SHA-bound Check,
   rather than treating process start or a green CI result as completed review.
   Exit 0 is a valid approved review, 2 is a blocking verdict to verify, and 1
   is an operational failure with no approval. Use managed completion waits or
   blocking CI watchers instead of model-driven polling. Keep a retained join
   handle or completion callback that resumes the Worker; do not end at an idle
   prompt with anonymous background shells and assume their exit will advance
   the lifecycle.
6. Before joining ENV wait and again after acquisition, re-read head/base,
   mergeability, required CI, mode, App identity and applicable Check. Required
   CI must be green; enforced review must be App-pinned success for this exact
   tuple. In shadow mode record failures and verify findings without inventing
   a new mandatory branch gate. Inactive mode never excuses a required Check.
   Do not hold ENV while CI or enforced review is still pending.

## Changed input and failures

- If any omission, test result or verified finding requires a correction,
  invalidate the receipt and cancel/join the obsolete owned invocation before
  editing. Mark it `superseded`, return to implementation, collect all known
  corrections, repeat the companion audit and fast gate, then freeze one new
  final head. Do not launch a replacement while further corrections are known.
  Never cancel another Worker's run or global workflows. Cancellation is not a
  clean review.
- A base or head change makes the old review stale, including patch-identical
  rebases. The wrapper rejects publication when the tuple changes mid-review.
  If an external change is detected while sampling, promptly cancel/join the
  owned reviewer to avoid wasting the remainder of its token/time budget. Then
  refresh admission before one review of the updated complete input; do not
  treat the old Check or matching patch text as approval of the new tuple.
- Timeout, invalid output, authentication/argument error, and publication
  failure are distinct operational outcomes, not approval. Do not lower effort,
  extend timeout, add a no-op commit, or blindly resample the same input to get
  a passing verdict. A pre-sampling retry requires a materially repaired cause;
  a timeout after sampling is not covered by that exception.
- Record safe `failure_stage`/`failure_kind`, effort, reviewer duration,
  total duration, input/output/reasoning token counts when available, and exact
  head/base. Missing usage after timeout is unknown, not zero cost. The local
  status `reviewer_duration_ms` measures the CLI invocation; `duration_ms`
  includes surrounding review workflow overhead. Never publish raw model
  output, SDK errors, prompt text or hidden reasoning.
- Also record invocation count, `stale`/`superseded` count, commits after freeze,
  and wasted reviewer duration/tokens. Normal delivery should use one invocation;
  an additional invocation should correspond to a verified correction on a new
  final head, not incremental cleanup discovered after premature admission.
