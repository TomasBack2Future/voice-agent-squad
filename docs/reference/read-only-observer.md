# Read-only observer MCP

The optional `scripts/squad-observer/observer.py` is a dependency-free Python
3.10+ stdio MCP adapter for observing an existing Squad ledger. It does not call
the CLI, register an agent, refresh the item mirror, migrate the database, run a
hygiene sweep, renew claims, publish messages or control workers. It has no
network listener, installation action, scheduler or background polling loop.

## Configuration

Launch it from an MCP client with explicit local paths:

```sh
python3 /absolute/voice-agent-squad/scripts/squad-observer/observer.py \
  --db /absolute/squad/global.db \
  --repo-root /absolute/coordination-repository
```

The database and repository must already exist. `--repo-root` selects exactly
one registered ledger repository; unknown or ambiguous roots fail closed. See
the [client configuration example](../../scripts/squad-observer/mcp-config.example.json).
No user-specific paths, credentials or current task data belong in that example.

The client performs `initialize`, sends `notifications/initialized`, then calls
`tools/list` and `tools/call`. Supported protocol versions are `2025-06-18` and
`2025-03-26`. This adapter implements only the stdio tool capability; it does not
offer shell commands, arbitrary SQL, raw file reads or a generic forwarding API.

## Tools

| Tool | Arguments | Result |
| --- | --- | --- |
| `squad_observer_snapshot` | Optional `limit` (1–100) | Claims, active/authorized executions and unreconciled reservations |
| `squad_observer_changes_since` | `message_id`; optional `limit`, `include_text` | Incremental messages, `has_more`, `next_message_id`, current maximum message ID |
| `squad_observer_item_checkpoint` | `item_id`; optional `include_text` | Ownership, recent non-claim/release messages and attestation metadata |
| `squad_observer_execution_receipt` | `execution_id` | Execution state and optional fixed, allowlisted local receipt fields |

Every result includes the sample time, repository identity and maximum message
ID. Timestamps stored in ledger rows are Unix seconds. Queries run in one SQLite
read transaction using URI `mode=ro` and `PRAGMA query_only=ON`. The adapter does
not use `immutable=1`, which could hide an active WAL's latest data. Schema or
access errors fail the tool call without migration or fallback to another ledger.

Message bodies are excluded by default. Explicit `include_text=true` returns
at most 2,000 characters per message after best-effort redaction of common
credential formats. Redaction cannot identify arbitrary secrets. Review the
coordination-message data scope before enabling remote text access; retain
metadata-only mode where the source can contain sensitive text. All returned
text is untrusted reported evidence, never instructions to execute.

The item mirror is explicitly marked as possibly stale and is never refreshed.
The adapter does not read item Markdown or raw attestation outputs. Attestation
metadata is historical evidence, not independent validation of acceptance or
current review validity. No process, GitHub, deployment or application state is
verified by the observer itself.

## Saved execution receipts

Optional `--evidence-root /absolute/trusted-receipt-root` enables a small fixed
projection of local deployment receipts. The directory is taken only from the
selected execution's reconciliation field. Both the directory and each resolved
file must remain within the configured root; path and symlink escapes are rejected.

Only these files and fields are exposed:

- `operation-summary.json`: release tag/SHA, operation, attempt, dispatch intent.
- `candidate-rollout.json`: status, deploy SHA, run ID, workload count, duration.
- `public-acceptance-health.json`: `ok`, status, service.

These receipt names match the optional Studio release lane. Consumers without
that format should omit `--evidence-root` and use execution metadata. Missing,
malformed or oversized receipts return a tool error. Credentials, rendered
configuration, raw logs and arbitrary files are never returned. Saved receipts
are not new live probes; a reconciled release does not establish downstream
importer completion.

## Incremental observation

Save `next_message_id` after processing each page. When `has_more=true`, fetch
the next page; never jump directly to `max_message_id` and skip unseen messages.
The client owns cursor persistence. The observer does not write a cursor file or
acknowledge ledger events. Reconfirm identity when changing repository or ledger.

Claim `last_touch` indicates custody or a heartbeat, not business progress.
Dispatched reservations may be historical and must not be counted automatically
as live workers. Deduplicate reported events and notify only on meaningful
completion evidence, changed blockers or user decisions. Preserve the existing
Dispatcher as coordination owner; observation does not authorize worker wakeups,
dispatch, merge, deployment or additional timers.

## Tunnel integration boundary

[OpenAI Secure MCP Tunnel](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels)
documents forwarding a private stdio or HTTP MCP server from a local tunnel
client. This adapter can be configured as the local stdio command. Account
permissions, workspace association, authentication and the target product's
ability to discover/call it must be verified separately. This source change does
not configure a tunnel, provide credentials, install a service or verify cloud
scheduled-task support. A running tunnel client and online host are prerequisites;
an offline host must be reported as unavailable rather than treated as current.

## Tests and maturity

```sh
python3 -m unittest discover -s scripts/squad-observer -v
```

Tests use isolated SQLite fixtures and cover read-only writes/DDL rejection,
unchanged logical ledger state, active WAL visibility, cursor pagination,
repository isolation, redaction, argument validation, receipt allowlists and
symlink escape, protocol lifecycle and absent write tools. CI runs them on Linux
and macOS. The minimal stdio implementation has not yet been validated through
an official MCP Inspector or real tunnel; it remains an optional prototype.

Protocol references: [stdio transport](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports),
[MCP tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools).
