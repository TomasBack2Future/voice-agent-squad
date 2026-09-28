# Interceptor source delivery and manual PR handoff

The GitHub Issue is the requirements/dispatch source; implementation lives at
`git.agoralab.co/ipt/interceptor`, default branch `master`. Set the assignment's
`repository_host` to that host and use this profile. Preserve independent Git
history, worktree and claims. Do not change Studio as part of this assignment.

PR creation, publication and merge are human-operated for this lane. Dispatch
with `source_mutation=true`, `pull_request=false`, `merge=false`, `staging=false`,
`production=false`, `issue_close=false`. Do not ask for Bitbucket API credentials,
create a PR, post review comments or dispatch Jenkins. Source authority does not
implicitly authorize a push: retain the local branch unless push is explicitly
authorized. Shared-environment writes remain separately authorized operations.

Read the repository AGENTS.md and touched-area docs. Use a `codex/*` branch from
current `origin/master`. Run targeted tests, then `./regression/ci.sh` (both root
and nested modules). Missing Linux/cgo/WeText prerequisites remain a named
validation blocker, never a partial pass labeled as complete.

## Local independent review

Use the selected package's managed `squad-grok-review` local mode. Freeze a clean
worktree, explicit 40-character base SHA and an external Markdown contract file
containing the tracking Issue, requirements, interface decisions and acceptance
mapping. Keep the file stable throughout review and retain its hash in evidence.

```sh
squad-grok-review doctor --provider local-git --repository-host git.agoralab.co \
  --repo ipt/interceptor --worktree /absolute/owned/worktree \
  --base-sha BASE_SHA --description-file /absolute/evidence/contract.md \
  --reasoning-effort high
squad-grok-review --provider local-git --repository-host git.agoralab.co \
  --repo ipt/interceptor --worktree /absolute/owned/worktree \
  --base-sha BASE_SHA --description-file /absolute/evidence/contract.md \
  --reasoning-effort high --timeout 20m
```

No hosting API/PR or Bitbucket token is used. Keep the existing Grok home/config;
ordinary changes can use medium effort, auth/concurrency/migration risks high.
Start at the first complete stable implementation after local fast gates and
self-review; merge holds and ENV waits do not block it. Follow the generic
Worker's single-flight, write-barrier and deterministic join contract. Exit 0
is an approved local sample, 2 blocking findings, 1 an operational failure.
It is local evidence, never a remote approval or permission to merge.

## Human handoff

Deliver one checkpoint containing the tracking Issue/item/reservation, repository
and host, local branch, exact base/head and full-diff/contract hashes, test
commands/results, local review findings/status, and unresolved blockers. Include
a proposed PR title/body as a local file plus any interface dependency ordering.
Do not mark the Issue fully delivered solely because this handoff is ready.
Record the agreed source-phase handoff and release/retain ownership through the
normal authorized lifecycle, keeping any subsequent integration/acceptance work
explicit. The human creates the PR and handles native repository approvals and
merge; the Worker does not emulate these with a GitHub check.
