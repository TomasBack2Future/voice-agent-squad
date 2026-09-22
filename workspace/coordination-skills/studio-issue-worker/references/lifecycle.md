# Single-Issue lifecycle

At startup bind the originating Dispatcher callback identity from the assignment
per [dispatcher-callback.md](dispatcher-callback.md). After step 8's durable
outcome and safe claim handling, send its deduplicated terminal callback before
the final response; do not wait for a reply or start the next Issue.

1. Verify the exact Issue is open, assigned/eligible under the task prompt, and
   still relevant to current `main`.
2. Record requirement-by-requirement local and staging acceptance evidence.
3. Reproduce and identify the authoritative Go path.
4. Implement in one isolated branch/worktree; add negative-path regression
   coverage and run required tests.
5. Publish one non-closing Issue-linked PR and pass current-base, mergeability,
   review-policy, and required-check gates. After the quick local gate and
   author self-review, freeze the PR input and run doctor, then one local
   review concurrently with full CI under the workspace's inactive, shadow, or
   enforced mode. Read [review-readiness.md](review-readiness.md) for effort,
   managed parallel execution, and joining the exact-revision results. Pass
   repository, PR, derived mode, and explicit reasoning effort. Run both commands with
   narrow `require_escalated` execution outside the Codex filesystem sandbox so
   GitHub networking and Grok session persistence work; do not hold `ENV-*`.
   Require doctor to validate GitHub/Grok authentication, CLI contract, model,
   and session storage before sampling. In enforced mode, wait for its App-pinned
   Check; verify and correct valid findings on a substantive new head, never by
   raw-Grok invocation, same-name publication, admin bypass, or no-op resampling.
6. Under the Worker's standing authorization, acquire the environment only at
   release readiness. Revalidate the exact head/base and the authorizing App
   Check when enforced, record its Check Run audit pointer, merge with an
   exact-head guard, follow the exact merge SHA to staging, and perform
   independent acceptance without waiting for separate human confirmation.
7. Apply the failure-classification and bounded-repair decision in
   [staging.md](staging.md); a verifier exit code alone never selects rollback.
   Restore the last accepted exact revision when that decision requires it,
   verify recovery before releasing ENV, and continue the same Issue with a
   follow-up PR. A failed/ambiguous recovery retains ENV and is a safety blocker.
8. Close the Issue only after deployed acceptance, the staging log evidence
   disposition in [staging.md](staging.md), and cleanup; otherwise leave
   a precise, evidence-backed blocker while retaining or safely releasing claims
   according to workspace policy.
