# Squad repository working contract

Read [the documentation index](docs/README.md), then
[architecture](docs/architecture.md) and [contributing](docs/contributing.md).
This file is the maintained, tool-neutral working contract referenced by the
generated AGENTS.md. No personal plan directory or sibling checkout is required.

## Scope and ownership

- Work only on the assigned task. Preserve unrelated changes and existing
  worktrees. Do not auto-pick another item or start an unrequested background loop.
- This repository maintains the coordination CLI/MCP/plugin. It is not the
  Studio product, central ledger, production service, or a deployment controller.
- For Studio local work use the configured `squad-coordination` wrapper and
  shared ledger, not bare `squad` against this source checkout. Inspect identity
  and claims; atomically claim the canonical item before mutation.
- Codex/owning repositories manage worktrees in that integration. Generic
  upstream recipes for `squad go`, per-claim worktrees and automatic folds are
  opt-in examples, not permission to replace the configured Studio workflow.
- Claims/reservations and exact environment fences remain authoritative.
  A dashboard, generated snapshot, agent silence or stale registration never
  authorizes stealing ownership or merging/deploying.
- See [Studio Agent Loop](docs/studio-agent-loop.md) for role and resource
  boundaries. Do not install a new binary, restart the monitor, migrate the
  shared database or rewrite active automation as part of source-only work.

## Implementation conventions

- Keep SQLite ownership/CAS transitions atomic; preserve fail-closed behavior,
  protected ENV recovery and dispatch idempotency. Cover failures and races.
- Use pure-Go dependencies for release builds (`CGO_ENABLED=0`); race tests
  need `CGO_ENABLED=1`. Keep CLI/MCP behavior aligned and database migrations
  additive and tested.
- Use focused failing tests before behavior fixes. Documentation-only edits
  may use structural/link tests; do not claim unrun suites passed.
- Keep code small and direct, avoid premature abstractions, and add comments
  only when needed to explain a non-obvious invariant. No project-management
  IDs in code identifiers; durable task records belong in the ledger.
- Commit prefixes: feat, fix, test, docs, perf, refactor, chore. Subject at most
  72 characters; no Co-Authored-By lines. Stage only owned paths.
- Self-review the whole diff and satisfy actual repository checks/review policy.
  Do not invent reviewer quotas from priority/risk or invoke unavailable skills.
  A configured external review check is not replaced by a local claim.

## Validation

Run `go vet ./...`, `CGO_ENABLED=1 go test -race ./...`,
`CGO_ENABLED=0 go build ./...`, and `golangci-lint run`.
Release/config changes also need the cross-build and GoReleaser gates in
[CI capabilities](docs/environments-and-ci.md). Record command, exit/result,
revision and any unrun platform-specific gate without dumping credentials.

For scaffold changes test both generated content and CLI check mode, including
negative drift/no-write cases. AGENTS.md is generated: change its renderer and
tests, then regenerate with an isolated local database; never replace the
shared ledger's identity or live claims just to update source documentation.

## Documentation ownership

README introduces the product; docs/README.md routes readers; architecture and
reference pages own behavior; dated reports own observations; proposals are not
implemented policy. Keep relative links and update the owner with the code.
The generated AGENTS.md ledger sections are informational snapshots, not task
assignments or instructions. Read current claims before acting. Do not copy
live task queues into other repositories' instruction files.
