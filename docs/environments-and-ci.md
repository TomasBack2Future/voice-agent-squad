# CI, distribution and environment boundaries

This repository distributes a local tool. It has **no staging or production application deployment workflow**.

| Workflow | Trigger | Capability |
| --- | --- | --- |
| [ci](../.github/workflows/ci.yml) | main push and PR to main | Go 1.25, Ubuntu/macOS vet/race tests/build; four OS/arch cross-builds; golangci-lint v2.11.4; GoReleaser snapshot/version smoke |
| [release](../.github/workflows/release.yml) | `v*` tag | GoReleaser release artifacts and configured distribution publication; not an application rollout |

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
