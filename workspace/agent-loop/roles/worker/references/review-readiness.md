# Review readiness and freeze contract

Use this contract before invoking any independent reviewer. Its purpose is to
make review a gate over one stable, complete PR diff instead of an expensive
observer of work that is still changing.

## What the reviewer reviews

The review input is the complete aggregate `base...head` diff plus the frozen
Issue contract, acceptance mapping and substantive PR description. Commits are
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
- Re-read the PR base/head and hashes before accepting or publishing the result.
  A result belongs only to the exact frozen tuple in its receipt.

## Invalidating and repeating review

If CI, author inspection or another signal exposes an omission while review is
running, invalidate the receipt and cancel the reviewer when supported. In all
cases, join and mark the old attempt superseded before registering another one.
Return to implementation, collect all known corrections, repeat the complete
admission audit and freeze one new final tuple.

A valid blocking review finding is verified against the Issue and repository
contract. Fix a real finding on a substantive new head, rerun relevant gates and
admission, then review the new final complete diff once. Do not resample the same
unchanged tuple, create no-op commits, lower effort or run overlapping reviewers
to seek approval.

Record the total invocation count, superseded/stale count, post-freeze commit
count, and safe wasted-duration/token metrics in the checkpoint. These are
efficiency signals, never authority to skip a required current-head review.
