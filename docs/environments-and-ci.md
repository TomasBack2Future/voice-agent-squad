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

### Required check and hosting prerequisite

`gate` (the job name, reported by GitHub Actions, integration id 15368) is the
one stable required check; the required-status-check context is exactly `gate`.
It runs even when other jobs fail, requires every job the scope selected to
succeed, and permits `skipped` only for jobs the scope did not select. A selected
job that was skipped, cancelled or failed fails the gate.

**It is not enforced today.** `main` has no branch protection and no rulesets,
so nothing requires `gate` before merge. Enforcing it is a hosting setting owned
by the repository owner. [The ruleset draft](ci-main-ruleset-draft.json) is
prepared for that owner to apply through the repository settings or the rulesets
API; this repository's automation never applies it, never bypasses `gate`, and
does not treat repository-admin access as authorization to change protection.

### Lint tool pin

`lint` installs the release pinned in `scripts/ci/golangci-lint.pin` (version
and SHA-256), caches it, and runs `golangci-lint config verify` followed by
`golangci-lint run`. `config verify` downloads a JSON schema from
`golangci-lint.run`; only a schema-load failure with a network-class cause, and
a transient download error (timeout, connection reset, HTTP 5xx), are retried,
at most three attempts. An exhausted budget, a checksum mismatch, an invalid
configuration and any unrecognised failure fail immediately. To upgrade, change
the version and its SHA-256 from the release's `checksums.txt` together.

### Remote receiver and updater regressions

The `remote` job runs the real remote CLI/MCP acceptance and the updater
permission and rollback tests (`deploy/`). It is selected by any Go change,
`deploy/`, `scripts/test_remote_service.py` and every unknown path; fixtures and
a workflow contract test keep path scoping from skipping it.

### Measurement limits

The first speed samples (three full-scope runs and two probe runs) are not
sufficient p50/p90 evidence. When every check runs, sharding raises total runner
minutes (28–29 versus about 19.7 per run before); the saving is wall time and the
work avoided on small changes, not cost. No monetary saving is claimed for
standard public-repository compute.

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

## Recovery regression gate

The `recovery-smoke` job is separate from release packaging. It runs the
[recovery contract matrix](recovery-smoke.md) on isolated CLI/SQLite fixtures,
fails on empty or skipped required suites, and uploads its JSON report. Runtime,
workspace and Go/store changes select it; main and manual full runs include it.
A green contract run does not qualify native clients or all roles. Native
qualification is explicit and reports missing runtime/role coverage as blocked.
