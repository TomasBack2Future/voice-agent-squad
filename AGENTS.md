# Repository agent guide

Read [Contributing](docs/contributing.md), [architecture](docs/architecture.md)
and the [documentation index](docs/README.md) for the assigned area.
These are source-repository instructions, not a generated task queue.

## Scope

- Work on the assigned change only; preserve unrelated work and worktrees.
- This repository builds the coordination CLI/MCP/plugin. Source work does not
  authorize installing the binary, restarting a daemon, modifying a user's
  database or changing another product's deployment.
- Keep source checkout, selected work ledger and installed runtime separate.
  Use isolated temporary state for tests, never live claims or customer data.
- Follow actual repository review policy. Do not add task assignment, scheduler
  roles, current queue snapshots or environment-specific delivery rules here.

## Implementation

- Preserve atomic SQLite transitions, fenced ownership/recovery and dispatch
  idempotency; include negative-path and race tests.
- Keep CLI and MCP contracts aligned. Database migrations are additive and tested.
- Release binaries use pure Go, `CGO_ENABLED=0`; race tests need CGO enabled.
- Use focused failing tests for fixes and review the complete diff. Report
  unavailable or unrun tests explicitly. Never commit credentials or private logs.
- Use small direct changes, descriptive commit subjects with feat/fix/test/docs/
  perf/refactor/chore prefixes, at most 72 characters and no Co-Authored-By lines.

## Checks and documentation

Run `go vet ./...`, `CGO_ENABLED=1 go test -race ./...`,
`CGO_ENABLED=0 go build ./...` and `golangci-lint run` as applicable.
Release changes also need the [distribution gates](docs/environments-and-ci.md).
Update behavior, tests and the owning topic document together.

The optional `squad scaffold agents-md` command generates adoption snapshots
for consumers that choose that feature. Its renderer/check-mode tests remain
required, but **do not run it over this maintained source-repository file**.
This repository's AGENTS.md deliberately contains no live status or task list.
