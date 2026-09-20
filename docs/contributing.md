# Contributing

Read [AGENTS.md](../AGENTS.md) for repository scope and
[architecture](architecture.md) for implementation boundaries. A contributor
can build and test this repository without adopting a task scheduler, plugin,
shared ledger, or generated instruction snapshot.

## Development workflow

1. Start a scoped branch from current `origin/main` for the assigned change.
2. Use isolated temporary state for database, claim, scaffold and plugin tests.
   Never point development tests at an installed user's state.
3. Add focused failing tests for behavior fixes; preserve negative/fail-closed
   coverage and atomic transition/race checks.
4. Run the relevant gates, review the complete diff, then open a focused PR with
   its purpose, behavior/documentation impact and actual test results.
5. Satisfy the real protected-branch/review policy before merging. A source PR
   does not install a new binary or migrate any live database.

## Local gates

```bash
go vet ./...
CGO_ENABLED=1 go test -race ./...
CGO_ENABLED=0 go build ./...
golangci-lint run
```

[CI and distribution](environments-and-ci.md) lists the exact workflow triggers,
platform matrix and release checks. A feature branch without a PR may not run
hosted CI; report local and hosted evidence separately.

## Documentation ownership

AGENTS.md is the stable source working guide; CLAUDE.md points to it. README
introduces the product, the documentation index routes readers, and reference
pages own CLI/MCP/schema contracts. Update the owning page with behavior changes.
Do not commit live queue snapshots, last-run outcomes or development-agent role
orchestration as repository instructions.

The optional product command `squad scaffold agents-md` and its adoption hooks
still support generated snapshots for opted-in consumers. Do not regenerate the
source repository's AGENTS.md. Changes to that product feature must preserve its
renderer, CLI check mode, negative drift/no-write and hook tests.

## Conventions

- Small, direct Go implementations; release builds must support `CGO_ENABLED=0`.
- CLI/MCP parity, additive tested database migrations, bounded redacted output.
- No secrets, private task data or machine-local paths in fixtures or docs.
- Commit prefixes: feat, fix, test, docs, perf, refactor, chore. Subject at most
  72 characters; no Co-Authored-By lines or PM IDs in source identifiers.
- No telemetry, usage collection or crash-report upload.

## Releases

The release workflow publishes version-tagged artifacts through GoReleaser.
Tagging, installing a build, restarting the dashboard or altering plugin/client
configuration are separate delivery actions, not documentation validation.
For optional adoption examples see [recipes](README.md) and the command reference.
