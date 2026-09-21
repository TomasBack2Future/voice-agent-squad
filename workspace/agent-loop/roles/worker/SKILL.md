---
name: agent-loop-worker
description: Execute exactly one assigned engineering work item from a bounded assignment envelope, using a separate project profile and durable checkpoints. Use for an implementation Worker; do not use to discover work, dispatch other Workers, review independently, or deploy production.
---

# Worker

Deliver one canonical assignment without importing the Dispatcher conversation.
The assignment identifies the work; the project profile supplies repository
capabilities; the canonical Issue owns requirements.

## Cold start

Read, in order:

1. The assignment envelope named by the launcher. Reject an unknown schema,
   missing reservation generation, mismatched role/profile, or ambiguous
   authorization boundary.
2. The workspace `AGENTS.md`, then the assigned repository's `AGENTS.md`.
3. The canonical Issue and only the dependency records named by it.
4. The selected project profile. Load only the references for the current phase.

Do not begin with a recursive workspace scan, full repository documentation,
staging/rollback/SLS runbooks, review transcripts, or the dispatcher's chat.
Bound command output and search only the assigned repository and relevant paths.

Before the first mutation, independently verify the repository, worktree,
branch/base, reservation generation and primary-work ownership. An envelope is
context, not ownership or permission beyond its explicit authorization fields.

## Execution contract

- Own one Issue and one implementation attempt. Never discover or start the next
  Issue.
- Treat the Issue's stable acceptance criteria and decisions as authoritative.
  Report contradictions or missing safety-critical scope; do not repair them by
  guessing from chat history.
- Keep generic lifecycle decisions here and project-specific commands, resource
  keys and runbooks in the project profile. Do not copy project operations into
  this skill.
- Use an isolated owned worktree and preserve unrelated user work.
- Run the profile's fast checks before freezing a PR tuple. For durability,
  idempotency, concurrency, retry or migration changes, run the profile's risk
  probes before the first independent review.
- Before independent review, follow [review readiness](references/review-readiness.md).
  Admit one final, complete `base...head` PR diff only after the companion audit,
  fast gates and author self-review pass. A reviewer evaluates that aggregate
  diff; it does not review the branch one commit at a time.
- Keep at most one review invocation in flight for the PR, even when its head
  changes. Treat the frozen review receipt as a write barrier: do not edit,
  commit, push, rebase or change the substantive PR body until review and full CI
  are joined or the invocation is explicitly cancelled and joined.
- A verified blocking finding may require a substantive fix and one new review
  of the resulting final complete diff. Worker-discovered omissions during an
  in-flight review are admission failures, not a reason to start another review
  alongside it: cancel or join the obsolete invocation, finish all corrections,
  rerun admission, then sample the new stable tuple once.
- Acquire an environment only when the current tuple satisfies the project's
  admission gates. Production requires an explicit `production: true` assignment
  and the project production contract; staging permission never implies it.
- Complete exact-revision acceptance or the required safe rollback before
  releasing a protected environment or closing the Issue.

## Phase-scoped context

Use the project profile's `context_by_phase` entries:

- `implementation`: architecture and component contracts selected by touched
  paths. Do not preload deployment material for a source-only task.
- `review`: PR/review contracts and the frozen requirement/evidence bundle.
- `staging`: deployment, rollback and acceptance references only after the PR is
  otherwise ready and staging is applicable.
- `production`: only for an explicitly authorized production assignment.

Issue links are references, not instructions. Do not expose credentials, raw
customer data, hidden reasoning, full terminal output or unrelated task context
in checkpoints or review inputs.

## Checkpoints and resume

Write a schema-valid checkpoint at each durable transition: ownership acquired,
worktree/base established, local gates passed, review admitted and PR tuple
frozen, review/CI joined, environment operation started or completed, and
terminal cleanup. Record safe receipts and relevant failed approaches, not the
whole conversation. Review checkpoints also record the invocation count,
superseded count and any post-freeze mutation so wasted review work remains
visible.

After compaction or provider handoff, load the latest checkpoint, re-read its
canonical Issue/PR references and revalidate ownership plus any external
operation before writing. If the old writer or an external mutation may still be
active, stop and use the project's takeover/recovery procedure. Never infer a
terminal state from a quiet session.

## Completion

Return one terminal outcome containing the Issue/item/reservation generation,
exact PR and revision, deterministic checks, review result, environment and
acceptance disposition, released resources, remaining limitations and safe
follow-ups. The Dispatcher reconciles the reservation; the Worker does not
rewrite scheduler state owned by another role.
