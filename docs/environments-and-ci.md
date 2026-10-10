# CI, distribution and environment boundaries

This repository distributes a local tool. It has **no staging or production application deployment workflow**.

| Workflow | Trigger | Capability |
| --- | --- | --- |
| [ci](../.github/workflows/ci.yml) | main push, PR to main, manual dispatch | Scoped pull-request checks and unconditional full validation on main (see below) |
| [release](../.github/workflows/release.yml) | `v*` tag | GoReleaser release artifacts and configured distribution publication; not an application rollout |

## Validation tiers

| Tier | When | What runs |
| --- | --- | --- |
| Local | before pushing | `go vet ./...`, focused `go test -race` for the touched packages, `golangci-lint run`; the full local gates are in [contributing](contributing.md) |
| Pull request | every PR to main | `scope` selects jobs from the changed paths; `gate` aggregates them |
| Full integration | main push, manual dispatch (`full`, default true) | every job unconditionally, including all cross-builds and the GoReleaser smoke, on the merged tree |
| Release | `v*` tag | the release workflow |

`scripts/ci/ci_tool.py` is the single source of the selection rules, race
sharding, test inventory and gate logic; `scripts/ci/test_ci_tool.py` holds their
fixtures. Selection fails open: an unknown path, a change to the workflow,
`scripts/ci/`, `go.mod` or `go.sum`, a missing diff, or any non-PR event selects
every check.

| Changed paths | Jobs selected |
| --- | --- |
| Go sources, tests, embedded assets (`internal/`, `cmd/`, `plugin/`, `reviewer/`, `templates/`) | `static` (vet, build, test inventory), `lint`, `race` (4 parallel shards covering every package and test), `remote`; plus `cross-build` unless only `_test.go` files changed |
| `cmd/squad/main.go`, `.goreleaser.yaml` | `release-smoke` (version injection) and `cross-build` |
| `README.md`, `AGENTS.md`, `CLAUDE.md`, `docs/`, `.squad/` | `doc-contracts`: the Go tests that read those files (link, scaffold-guide, README and spec-parse tests) |
| `workspace/` | `pyloop`, `node`, `doc-contracts` |
| `scripts/squad-observer/` | `observer` |
| `deploy/`, `scripts/test_remote_service.py` | `remote` |

The `race` shards are `cli-1`/`cli-2` (the `cmd/squad` tests split by name),
`server` and `rest` (every other package, computed from `go list`, so new
packages are included automatically). `ci_tool.py inventory` fails when a Go
test directory, Python test or Node test is not mapped to a job, or when any
test would run in zero or two shards.

### Required check

`ci / gate` is the one stable required check. It runs even when other jobs
fail, requires every job the scope selected to succeed, and permits `skipped`
only for jobs the scope did not select. A selected job that was skipped,
cancelled or failed fails the gate. Repository protection that requires this
check is a hosting setting owned by the repository owner; this repository does
not apply it.

### Concurrency

A new push to a pull request cancels only that PR's earlier run. Main pushes,
manual runs and other PRs use separate groups and are never cancelled.

Release builds use `CGO_ENABLED=0`; race tests explicitly enable CGO. A feature
branch push alone does not trigger the main/PR-only CI workflow. Do not create
a release tag or install the built binary just to validate documentation.

## Installation targets and evidence

The tool is installed on a user's machine or chosen CI host, not deployed as a
Studio service. There is no Helm chart, Kubernetes namespace or staging/production
application target in this repository. CLI/MCP/dashboard processes use configured
local state; a new source commit does not change an installed binary or daemon.

A consumer may model external environment ownership as coordination resources.
Resource names and deployment authorization are consumer configuration, not
hardcoded contributor roles or permission to bypass a protected environment.

Record installed-version checks, build/CI results and release receipts in their
respective operational artifacts. Do not maintain a latest CI run or installed
version snapshot in these docs. Never install or restart a tool merely to
refresh a documentation page.
