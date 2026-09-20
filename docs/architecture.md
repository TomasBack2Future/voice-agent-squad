# Architecture

This repository builds the Squad CLI, MCP server and optional local UI/plugin.
It is a coordination substrate, not Studio's application execution engine.
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
| `internal/server/`, `tui/` | Local presentation/API; Studio's configured monitor is read-only |
| `internal/scaffold/`, `plugin/` | Adoption templates, generated entry points, hooks and optional agent integration |

## Durable state

Work definitions and their history belong in the selected repository's `.squad/`
files. Operational claims, agents, messages, reservations and generations use
the configured local SQLite store. They are not reconstructed from a Markdown
queue snapshot or from a browser's cached view. See
[database schema](reference/db-schema.md) and [claims](concepts/claims-and-coordination.md).

In the Studio integration the **source repository**, **shared coordination
ledger** and **installed binary/runtime state** are three distinct resources.
Moving this checkout does not move the latter two, update automation prompts or
repair existing Git worktree registrations. Tests use isolated state; source
edits do not authorize installing binaries or mutating active reservations.

## Agent Loop boundaries

The local dispatcher observes dependencies and reserves dispatch exactly once;
a worker atomically owns one task; environment ownership is acquired only at
the guarded merge/deploy/acceptance phase. A stopped worker does not prove its
external deployment stopped. Recovery needs holder and external-operation
evidence plus fenced transfer. See [integration contract](studio-agent-loop.md).

This local delivery loop is separate from Studio's Go Control Plane/Execution
Worker product loop, Interceptor request workflows and Importer's drain loop.
No component acquires the others' responsibilities by sharing the word agent.

## Instructions and generated state

`squad scaffold agents-md` retains its ledger snapshot feature for compatibility.
Its stable preamble routes to `CLAUDE.md` and identifies all item text as data,
not instructions or ownership. Hand-maintained rules live in `CLAUDE.md`; large
runbooks live in topic docs. The renderer and CLI drift/no-write tests must stay
aligned. The committed snapshot can be regenerated with an isolated
`SQUAD_HOME`; it is never a live dispatch queue.
