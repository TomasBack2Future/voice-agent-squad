# CI, distribution and environment boundaries

Source audit: 2026-09-20 at `174672c0e2e2d7f0634535caf3dc14694ab11028`.
This repository distributes a local tool. It has **no Studio staging or
production application deployment workflow**.

| Workflow | Trigger | Capability |
| --- | --- | --- |
| [ci](../.github/workflows/ci.yml) | main push and PR to main | Go 1.25, Ubuntu/macOS vet/race tests/build; four OS/arch cross-builds; golangci-lint v2.11.4; GoReleaser snapshot/version smoke |
| [release](../.github/workflows/release.yml) | `v*` tag | GoReleaser release artifacts and configured distribution publication; not a Studio rollout |

Release builds use `CGO_ENABLED=0`; race tests explicitly enable CGO. A feature
branch push alone does not trigger the main/PR-only CI workflow. Do not create
a release tag or install the built binary just to validate documentation.

Read-only hosting observation on September 20: main
[CI run 34385566049](https://github.com/TomasBack2Future/voice-agent-squad/actions/runs/34385566049)
at the baseline above succeeded. That proves the named run, not the version
currently installed on any developer's machine. This change has not restarted
the daemon, changed plugin installation, or migrated a live database.

## Staging and production interaction

The CLI arbitrates local ownership of `ENV-001` (Studio staging) and `ENV-002`
(Studio production) when configured for that shared ledger. Those are resources
held by an authorized worker, not deployment destinations for Squad itself.
The monitor remains loopback-only and read-only in the Studio integration.

Environment coordinates, immutable images, workflow inputs, validation and
rollback are maintained by each target repository's environment/deployment
documents. A Squad claim is necessary local ownership where configured, not
permission to bypass CI, a protected branch, production authorization, secret
scope or external locks. A fork's source update does not change the running tool.
