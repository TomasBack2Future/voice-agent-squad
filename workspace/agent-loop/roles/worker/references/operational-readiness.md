# Operational readiness

## Startup and delivery boundary

Before launching the implementation session, the launcher runs the package's
`worker_preflight.py` with the assignment, selected profile, actual runtime and
client-visible skill paths. Include every skill/tool needed by the assignment,
including production skills only when production is authorized. The check reads
Git identity and files; it neither launches a model nor claims work. Resolve a
missing skill or changed base once in the assignment before launch, rather than
making the Worker repeatedly rediscover the mismatch.

For a new Claude Worker, pass `--launch-config` to preflight and use the
canonical `claude_worker_launcher.py` as the actual workspace command. Validate
`schemas/claude-launch.schema.json` separately from the assignment; do not add
runtime ids or new authorization keys to the envelope. The launch probe removes
inherited session identity and executes the same coordination read as startup.
A local files/tools-only receipt leaves launcher access unverified. Resolve a
failed child probe before creating the session; do not diagnose it from a
successful command in the Dispatcher's differently configured parent shell.

Record whether the Issue delivers source, a merged change, staging availability
or production availability, together with its observable acceptance target. If a
user-facing feature is split into source and deployment items, the parent delivery
remains incomplete until the requested environment works. A loopback test does
not establish a deployed route.

Executable discovery is not permission or authentication. For a GitHub source
repository, check access through `gh` and, when managed PR review applies, use
`squad-grok-review doctor --repo OWNER/REPO` (optionally `--pr NUMBER`) before
expensive work. A doctor result without a repository says `not_checked` for
repository access. This reads through the reviewer App identity without sampling
or publishing; it cannot prove permission to write a Check. If the installed
binary lacks this capability, report that limitation; do not assume a global
doctor proves repository access or install a binary implicitly. For a non-GitHub
source, verify the assigned origin and use its project profile's local review
readiness checks; use `gh` only for the GitHub tracking Issue. Do not require PR
publication credentials for Interceptor's local review and human handoff, or
infer push permission from successful read access.

## Execution capability and autonomy

The startup receipt above verifies local files/tools only. Before the affected
phase, separately verify actual environment access, target identity, browser
execution/login availability, selected runtime permission mode and a supported
Dispatcher notification route. Do not claim these passed from executable
presence. Resolve routine local gaps within authority; record external gaps
with the exact phase they block.

Own authorized commands, browser interactions and result capture. Do not
assign shell/DevTools execution or agent-to-agent message delivery to the user
when supported tools can perform it. A user-only login/MFA interaction may be
necessary; browser-managed authentication is compatible with never exporting
cookies/tokens. A failure of one workflow is not proof that the operation must
be manual. Check another documented authorized mechanism, without bypassing
an approval denial or expanding scope. Escalate a concrete missing decision or
access prerequisite once, with evidence and the smallest human action needed.

## One shared blocker, one repair owner

When a shared CI, release or contract failure blocks the item, read its existing
Squad history and active repair claims before changing that surface. Declare
actual touched paths through `squad touch`. The Dispatcher assigns one canonical
blocker and repair owner and records affected items' dependencies. The existing
atomic claim on that canonical item supplies exclusion; free-text intent or
empty file touches do not. A Worker must not invent a second repair item, claim
another assignment or independently fix a blocker owned by a peer. Report the
failure, matching run/revision and existing PR once to the Dispatcher and continue
independent authorized work. Dependencies apply at the affected phase, not
necessarily to all implementation.

Include a bounded failure fingerprint: affected phase/environment, component and
immutable component revision, stable error code, normalized failed condition and
run/trace evidence. Match those inputs before reusing an incident; differing revisions
need explicit evidence of the same cause. Never combine no-change, timeout, storage
and upstream errors merely because one pipeline produced them. The canonical item
records the repair claim, affected items/phases and verified resume condition. Reuse
its history across Workers; changing the Worker does not reset a retry budget.

For a deterministic failure, do not launch another unchanged attempt. For a transient
failure, follow the stricter existing policy or at most one safe retry when no budget
exists, with terminal/idempotence evidence. Unknown cause requires bounded diagnosis.
After a verified repair, rerun affected assertions and required final gates, retaining
unaffected valid evidence. Use the delivery helper's `shared-failure` action where
available; its advisory result neither creates a claim nor proves a repair.

## Evidence before declaring a blocker

Record the blocked operation/phase, configured target, observed result, run/request
evidence, bounded recovery already checked, next action and its owner in the existing
checkpoint. Separate an observed failure from an unknown cause. Verify the failed
attempt used the intended protocol/endpoint, resource identity and actual API/schema
contract; an HTTP failure does not prove the configured HTTPS route is unavailable,
and an unverified field name does not prove a migration is needed. Use one cheap,
authorized read-only probe or documented alternative before transferring the blocker.
Do not probe a mutating path without authority or route around a denial.

An explicit permission boundary or user-only login/MFA is evidence in its own right;
no forbidden attempt is required. Cite that boundary and request only the smallest
human action. Otherwise, a failed tool path or missing binary alone does not establish
an external/user-only prerequisite. Use `blocker` in the existing delivery helper
where available; keep independent authorized work moving while its affected phase
waits. No new status template or human message relay is required.

## Release preparation before the environment lock

Use the selected project's read-only preparation capability before claiming ENV:
verify candidate SHA, successful CI **and actual required artifacts**, immutable
image accessibility through the deployed registry path, target kubeconfig/context,
required Secret names/key presence, runner/tools and component bootstrap/storage
prerequisites. Check presence without publishing Secret values. Collect failures
in one receipt instead of discovering one prerequisite per failed rollout.

Run new-component first-publication, unchanged-source/revision-stamp and cold
rollback-tool-cache tests before final review. Separate preparation from mutation;
revalidate receipts and resource ownership immediately before deployment.

Do not silently relax a latest-main deployment policy. A prepared immutable
candidate may survive main advancement only when the project contract explicitly
allows it and provenance checks bind the workflow, tools, images and revision.
Otherwise use an explicitly coordinated merge window and requalify as required.

If preparation fails while holding ENV, release only after verifying there has
been no environment mutation and no external operation remains live, or after
the required safe recovery. Keep a durable checkpoint naming the blocker and
resume gate. Never release a lock merely because a timeout elapsed.

## Evidence and completion across runtimes

When the ledger differs from the source checkout, run command attestations with
`squad attest ITEM --work-dir /absolute/owned/worktree --kind test --command '...'`.
The command executes there; evidence stays in the selected ledger and its hashed
output includes the execution directory. Verify the expected suite actually ran.
On older binaries, explicitly change directory inside the recorded command;
never count a successful test run from the coordination repository as evidence.

A runtime approval denial is separate from task authorization and CI/review.
Preserve the evidence and report the specific denied action once. Do not disguise
or retry equivalent commands to bypass the denial. Use the existing approved
merge path, or obtain the required bounded user decision.

Persist terminal outcome in the canonical Squad item thread using `squad say
--to ITEM ...`: include reservation key/generation, Worker session, outcome,
PR/revision, evidence references, released/retained resources and live external
operations. The Dispatcher reconciles this durable event and closes its own
reservation after independent checks. A client-specific callback is an optional
wakeup, not the only record: never require a Claude Worker to call a Codex-only
tool. An unavailable wakeup must leave an explicit pending-reconciliation receipt,
not cause repeated messages or a fabricated completed reservation.

For a new-package pilot, compare time to first effective edit, compactions,
review/stale attempts, external/approval wait, lock occupancy and acceptance time.
Keep measurements local to the assigned task; no provider history or billing
collection is required. Use checkpoints rather than transcript replay.
