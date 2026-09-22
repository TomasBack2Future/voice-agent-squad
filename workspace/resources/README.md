# Studio resource policy

This is an opt-in adoption package, not the installed ledger. Preserve existing
ENV-001/ENV-002 IDs, item histories and claim/recovery generations. Never copy
these templates over live item files or migrate active claims automatically.

| Item | Default coverage | Explicit narrow coverage |
| --- | --- | --- |
| ENV-001 | All registered staging services (legacy) | `--scope studio` |
| ENV-002 | All registered production services (legacy) | `--scope studio` |
| ENV-003 | Staging external Importer | No flag required |
| ENV-004 | Production external Importer | No flag required |
| ENV-005 | Staging Feedback | No flag required |
| ENV-006 | Production Feedback | No flag required |

Importer covers its deployment, YAML/env, subscriptions/generations and SQLite
state ownership. It does not cover Studio's internal Go Import Worker. Feedback
covers its independently deployed service and owned configuration/state. Verify
Feedback's actual deployable boundary before enabling that route; a frontend
feedback component change remains a Studio change.

Studio scope covers API/Worker releases, migrations, routing and shared acceptance
requiring a fixed Studio revision. A normal authenticated API request or ordinary
business Session write does not by itself require Studio environment ownership.
An acceptance operation that needs both a fixed Studio revision and an independent
service revision requires both resources. Prepare all prerequisites first, acquire
in lexicographic item-ID order, and revalidate after acquisition. Do not upgrade a
held narrow claim to a wildcard; release only after the current operation is safe,
then acquire the required coverage again. Same-owner overlapping acquisition fails
rather than waiting on itself. Shared read locks are not part of this revision.

## Rollout

1. Build/test the source and separately authorize installation. Upgrade the CLI,
   MCP processes and wrappers that will use scoped calls. Preserve the old interface.
2. Verify the selected ledger, new ID availability and Feedback deployment identity.
   Create the four new item files from reviewed templates. Amend existing ENV-001/002
   prose with this table; retain all history and existing item metadata.
3. Wait until affected claims and waiters are idle. Back up the ledger, then run
   `squad resources define /absolute/path/to/studio.json` in the selected ledger.
   Definition changes fail while affected resources are held or awaited. This
   command registers policy only; it does not create item files or deploy services.
4. New Studio calls use `squad claim ENV-001 --scope studio --wait` (ENV-002 for
   authorized production). New service calls use their independent IDs. Old calls
   with no scope remain broad and conflict with every service in that environment.
5. Sync the versioned skills/profile through the existing skill installer. Do not
   edit generated installed packages or silently migrate active tasks.

The SQLite insert guard also protects against older binaries that omit the new
columns. Keep the new schema and triggers when rolling back a binary. Restoring
an old database schema while scoped callers are active is not a safe rollback.
Older CLI/MCP processes do not understand `--scope`, structured resource conflicts
or deadlock waits: they fail safely on insert conflicts, but may require upgrade
for automatic waiting. Do not claim behavioral parity with an unmodified binary.

No automatic timeout cleanup releases ENV ownership. Wait leases expire after
30 seconds without renewal; this removes only a dependency edge from deadlock
analysis. Stopped-holder recovery still verifies external operations and preserves
fenced ownership. New tasks must use the same ledger; distinct ledgers do not
coordinate one environment.
