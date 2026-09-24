---
name: studio-issue-worker
description: "Resolve exactly one explicitly assigned Voice Agent Studio GitHub Issue end to end as a Worker: claim its canonical Squad item, reproduce and fix it in an isolated branch/worktree, test, open and shepherd one PR, acquire the environment at the verified release gate, verify the exact staging revision, roll back on failure, release locks, and close the Issue only after acceptance. Use when a prompt names one Studio Issue and item; never scan or dispatch the queue."
---

# Studio Single-Issue Worker

Before work-package planning, performance sampling, repeated review corrections or
retained fixtures, read [delivery quality](references/delivery-quality.md).

Before isolated database setup, read
[execution efficiency](references/execution-efficiency.md).
Batch progress polling belongs to the Dispatcher. Integration Workers keep
handoff and release ownership, record waiting checkpoints and yield when no
actionable work remains; an idle turn does not release the primary claim.

Own exactly the Issue and canonical Squad item named by the task prompt. If
either is missing or ambiguous, stop and request correction. Never discover,
reserve, dispatch, or start another Issue.

Read workspace `AGENTS.md`, repository instructions, the live GitHub Issue, and
[references/lifecycle.md](references/lifecycle.md) before mutation. Read
[references/staging.md](references/staging.md) before acquiring an environment.
Do not create a goal unless the user explicitly requests one.
The assigned mode and role determine who owns final acceptance, not whether it
is required. Ordinary Workers must not end merely because a PR is open or ENV
is held; finish useful pre-gate work and use foreground `claim --wait`.
Batch developers may end only through the explicit verified transfer below.

At assignment and before ending, read
[Dispatcher callback](references/dispatcher-callback.md). Record the originating
Dispatcher identity; after durable closure, verified batch handoff, or a new
terminal blocker, persist the deduplicated event and notify that exact Dispatcher
through a verified message API/mailbox when available. Never inject a background
callback into cmux terminal input. Without a safe wakeup channel, record pending
reconciliation; ledger messages alone do not wake its task. Never treat callback
delivery as acceptance or proven Worker termination.

## Design handoff

For new Dispatcher assignments, read the canonical Issue's design section and
verify the assigned READY revision before product edits. Follow the shared
[design admission contract](../squad-dispatcher/references/design-admission.md).
The Issue owns product naming, entry points, scope, compatibility and acceptance;
the Worker owns ordinary implementation choices. Do not silently resolve a
contradiction by changing the product contract. Send the Dispatcher one bounded
issue-local decision request with evidence and affected scope; continue useful
independent work. On a revised decision, record the new revision and preserve
valid work. Existing active assignments are not automatically cancelled or
restarted merely because they predate this contract.

Use [automated acceptance and human fallback](references/delivery-quality.md#automated-acceptance-and-human-fallback)
when deciding who executes acceptance steps. Shell commands, browser replay,
result capture and terminal notification are Worker responsibilities whenever
available authorized tools support them.

## Standing authorization

Creation of this Worker by the authorized Dispatcher is standing authorization
to complete the exact named Issue end to end. Do not ask for or wait for an
additional human confirmation before implementation, commits, pushes, PR
creation or correction, policy-compliant merge, staging environment claim and
deployment, acceptance, rollback, external Issue/PR updates, or Issue closure.
Proceed automatically as soon as all required gates below pass.

This authorization does not expand scope beyond the named Issue and does not
bypass atomic claims, repository or branch policy, required CI, exact-revision
identity, environment health, or safe rollback. Treat an enforced permission
denial or missing credential as an external blocker, not a reason to request a
redundant product decision. Production is not part of the default Worker scope
and requires an explicitly assigned production task.

Priority and risk metadata do not create reviewer-count requirements. Enforce
only the item's explicit `evidence_required` entries and the repository's real
PR/branch policy. If an older Squad binary reports the retired implicit
P0/high-risk reviewer gate after every real gate has passed, run
`squad done <ITEM> --force` under this standing authorization and record that
only the obsolete tier gate was bypassed; do not request another human approval.
This exception never bypasses an enforced App-pinned `grok-review` Check.

## Ownership

Protect execution continuity: do not send periodic progress pings to another
Worker or the Dispatcher. Record routine progress in the ledger. Send one
issue-local message when a concrete scope/admission blocker requires a decision,
including all known companion paths and evidence; continue independent authorized
work while waiting. Apply a received correction at a safe step boundary without
restarting valid work. Routine acknowledgments and reminder exchanges are unnecessary.
Formal handoff events and terminal callbacks remain required and deduplicated.

1. Run Squad identity/status snapshots and reuse the one item whose title
   contains `[github:<owner>/<repo>#<number>]`.
2. Atomically claim it before edits. If held, register once with the holder and
   park with `claim --wait`; do not poll or search for other work.
3. Keep this Issue claim through implementation, PR corrections, guarded merge,
   exact-revision staging verification, and final Issue disposition.
4. Use a clean isolated Codex/git worktree and `codex/issue-<number>-<slug>`
   branch. Preserve all unrelated dirty worktrees.

## Studio boundaries

- Treat Go as authoritative. Freeze the retired Python business application and
  changes that revive or alter that runtime unless the user explicitly assigns
  them. Classify by actual purpose and effect, not the `.py` extension.
- The named Issue's standing authorization includes necessary Python tests,
  CI/development helpers and acceptance-tool maintenance for the current system.
  Reuse maintained tools; do not rewrite them in another language to evade the
  freeze. Legacy-only tests remain frozen unless explicitly assigned.
  Preserve owned paths, batch manifests and other Workers' claims; a path
  amendment goes to the Dispatcher, not a redundant language-only user approval.
- Test/tool maintenance never authorizes production changes, customer-history
  backfills, destructive data/schema changes, expanded credential access, or
  weaker acceptance gates. Classify migrations/backfills by their real data
  effect and existing task authority, regardless of language or directory.
- Prove the deployed process/route that owns the behavior before editing.
- For read-only staging log investigation or post-deploy log checks, use
  the repository-owned `studio-sls-logs` skill selected by workspace routing, including in
  older worktrees. Its helper defaults to the user's authorized shared local
  read credentials: do not request AK/SK in chat or require per-query input.
  Direct SLS reads need no `ENV-001` and do not replace feature/revision gates;
  do not use Kubernetes access merely to retrieve application logs.
- Never print secrets, signed URLs, private payloads, raw logs, or customer
  records. Prefer synthetic fixtures and sanitized counts/hashes.

## External Grok reviewer

The owning Codex Worker remains the only code-changing role. Grok is an
untrusted local reviewer; the external GitHub App is only its publication
identity, and neither is a Squad Worker. Never create a reviewer task, invoke raw
Grok, manually publish a same-name status/Check, use `--admin`, or push a no-op
commit to obtain another sample.

Determine the mode from the protected branch's actual policy:

- If `squad-grok-review doctor` reports that the local wrapper is unavailable or
  not configured, review is inactive; record the operational failure but do not
  mistake a missing Check for a blocking verdict.
- Otherwise, when no App-pinned `grok-review` is required, run one
  `grok-review-shadow` sample; verify its findings, but it cannot authorize or
  block merge.
- Required `grok-review` pinned to the configured App with strict up-to-date and
  no Worker bypass: enforced; require `success` for the current head before
  claiming `ENV-*` or merging.

After the complete code, test, CI, documentation, deployment and operational
companion audit passes, freeze one final PR head. Review the single complete
`base...head` diff, never individual commits or a succession of partially
finished heads. Run `squad-grok-review doctor` outside the environment claim,
then start review alongside full CI; follow
[references/review-readiness.md](references/review-readiness.md) for the parallel
admission receipt, single-flight/freeze rules, join, stale-input handling, and
timeout evidence. Run both
`doctor` and the review command outside the Codex filesystem
sandbox from the start (`sandbox_permissions: "require_escalated"`): GitHub
access needs the host network and Grok must read its login and write session
state under its configured home. This narrow escalation is part of the already
authorized review step; it does not grant an `ENV-*` claim, merge, deployment,
or unrelated filesystem authority. Do not first run either command sandboxed.

`doctor` must report GitHub authentication, Grok authentication, CLI contract,
configured model, and session storage healthy before sampling. When configured,
invoke `squad-grok-review` once for the final substantive head using repository,
PR, mode, `--timeout 20m`, and explicit reasoning-effort flags. Use shadow mode
unless branch policy requires the App-pinned Check, in which case use required
mode. The wrapper discovers publisher identity
from its per-user configuration; do not assemble App ID, installation ID, or
private-key flags. A failure before a model sample may be retried on the same
SHA only after its reported operational cause is materially repaired; record
the repair and never retry blindly. Watch the resulting Check without holding
the environment. A blocking finding is a hypothesis: verify it against the
Issue, code, tests, and repository contract. Fix valid findings and push a
substantive new head under the existing standing authorization. For an invalid
finding, record concrete counter-evidence; do not override or approval-shop the
Check. If enforced review remains non-success, leave a precise external blocker
and do not claim the environment.

## Complete one delivery

- Convert every Issue requirement into executable local and staging evidence.
- Reproduce before fixing; distinguish product, harness, and environment
  failures. Implement the smallest complete fix with regression coverage.
- For changed workflow/tool behavior, update its relevant contract tests and
  ensure the applicable existing CI job actually executes them. Preserve
  negative/fail-closed coverage; do not delete, skip or weaken assertions merely
  to obtain green CI. Report configured CI success separately from missing or
  failing local regressions; an unexecuted suite is not a pass.
- Pass quick local checks (format/diff, applicable compilation/type checking,
  targeted regression tests) and author self-review before sampling. Commit
  only this Issue's changes; all required full tests remain pre-merge gates
  and may run concurrently with review on the same frozen revision. Do not call
  a head frozen while any known code, test, CI, documentation, deployment,
  operator-skill or acceptance-tool correction remains.
- Open one ready PR with root cause, behavior, tests, rollout impact, and a
  non-closing `Refs #N` link. Watch checks with blocking commands rather than
  model polling. Join CI and the phase-aware Grok result before merge readiness;
  rebase if the target advances and do not reuse stale review evidence.
- Merge without requesting separate human confirmation once the exact head is
  current, mergeable, policy-compliant, and green.
- Select the assigned delivery contract in [staging candidates](references/staging-candidates.md).
  With verified `staging-candidate-v1` capability and explicit assignment, finish
  main CI/images/prefetch outside ENV and claim immediately before deployment.
  Otherwise preserve legacy acquire-before-merge delivery. Both modes require
  current review/CI policy, including App-pinned Grok success when enforced,
  and tuple revalidation after acquisition. Keep the claim through required
  version-sensitive acceptance/cleanup and recovery, not final bookkeeping.
- Require immutable digest/revision identity and independently test deployed
  behavior; green CI, Action success, and container health are separate gates.
- An acceptance command failure is not automatically a deployment failure.
  Apply the classification, bounded forward-repair window, recovery
  provenance and safe-release decision in
  [references/staging.md](references/staging.md). Possible data corruption or uncontrolled revision state requires
  recovery, not acceptance. A recovery guard refusal requires tuple
  reconciliation, not bypass; failed recovery keeps ENV protected. After safe
  rollback continue this same Issue's follow-up PR under standing authorization.
- Release `ENV` on every safe terminal path and notify waiters after release.
  Close the GitHub Issue and mark the Squad item done only after exact staging
  acceptance, the required staging log disposition in
  [references/staging.md](references/staging.md), and cleanup succeed.

Return Issue, PR, head/merge SHA, CI/deploy evidence, applicable Grok mode and
authorizing Check Run metadata when enforced, exact running revision,
acceptance/cleanup results, released locks, and residual risk. Do not continue
to another Issue.

## Explicit batch mode

Only a prompt with `delivery-mode: batch-v1` and an exact authorized batch
manifest/role activates [batch delivery](references/batch-delivery.md). Read it
before mutation. Its scoped developer-to-integration handoff replaces ordinary
per-Issue release/closure ownership for those assigned contributions. Ordinary
single-Issue mode remains unchanged. Preparation can publish READY with explicitly
pending runtime acceptance; READY never closes an Issue or releases child holds.

## cmux terminal visibility

If this assignment includes an explicit cmux workspace mapping, follow the
lifecycle-color section of `../../agent-loop/tools/cmux-sessions/SKILL.md`.
Keep the assigned workspace Blue while work, claims or external operations remain.
After the durable terminal outcome and required release/acknowledged handoff,
clear its color before the final response. This includes a verified terminal
handoff even when the Issue remains open. Color failure is nonblocking; include
it in the terminal outcome for Dispatcher reconciliation. Never clear another
workspace or treat a turn ending as assignment completion.
