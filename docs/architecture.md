# Architecture

This repository builds the Squad CLI, MCP server and optional local UI/plugin.
It is a local coordination tool, not an application deployment controller.
The module path retains its upstream identity; the fork's Git remote identifies
delivery ownership. Do not change module imports just to rename the checkout.

| Source boundary | Responsibility |
| --- | --- |
| `cmd/squad/` | Cobra commands, argument validation and orchestration |
| `internal/items/`, `specs/`, `epics/` | Versioned work definitions and readiness/dependencies |
| `internal/store/` | SQLite schema and migrations |
| `internal/claims/`, `dispatch/` | Atomic ownership, fenced recovery and dispatch reservations |
| `internal/chat/`, `listener/`, `notify/` | Durable coordination messages and wakeup transport |
| `internal/attest/`, `learning/` | Command evidence and reviewed durable learnings |
| `internal/hygiene/` | Diagnosis and bounded cleanup; protected ENV ownership is not ordinary stale-claim cleanup |
| `internal/mcp/` | MCP transport over coordination operations |
| `internal/server/`, `tui/` | Local presentation/API; capabilities depend on configured mode |
| `internal/scaffold/`, `plugin/` | Adoption templates, generated entry points, hooks and optional agent integration |

## Durable state

Work definitions and their history belong in the selected repository's `.squad/`
files. Operational claims, agents, messages, reservations and generations use
the configured local SQLite store. They are not reconstructed from a Markdown
queue snapshot or from a browser's cached view. See
[database schema](reference/db-schema.md) and [claims](concepts/claims-and-coordination.md).

## Installation and process view

```text
Source checkout -> build/release artifact -> optional local installation
                                            ├── CLI process
                                            ├── MCP process (client-launched)
                                            └── optional local dashboard daemon
Selected repository .squad/ <-> coordination operations <-> local SQLite store
```

The source checkout, selected ledger and installed runtime are separate.
Moving source does not move runtime data, update client configuration or restart
an installed process. There are no Kubernetes charts or application Pods in
this repository. Distribution is described in [CI and environments](environments-and-ci.md).
Tests use isolated state; never substitute the live database.

## Repository instructions versus optional generated output

The checked-in AGENTS.md is a stable, hand-maintained source contributor guide;
CLAUDE.md points to it. Neither file contains live queue state or specifies a
consumer's development workflow.

`squad scaffold agents-md` retains its optional ledger snapshot behavior for
consumer compatibility. Do not run it over this source repository's guide.
Renderer, CLI drift/no-write and hook tests continue to cover the product
feature independently; the repository guide has a separate stability test.
Large architecture and operating contracts belong in topic documents. Live
claims and task status remain operational data, not contributor instructions.
