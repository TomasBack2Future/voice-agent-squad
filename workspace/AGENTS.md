# Studio workspace routing

This is a multi-repository workspace, not a monorepo. Enter the target repository
and read its root AGENTS.md before work. Each repository's instructions and docs
must remain usable when cloned alone or opened in an isolated worktree.

| Directory | Responsibility | Default branch |
| --- | --- | --- |
| `voice-agent-studio/` | Go/React product, contracts, Helm and release workflows | main |
| `voice-agent-squad/` | Local coordination CLI/MCP/plugin and Agent Loop integration | main |
| `convoai-studio-importer/` | Independently maintained Python source-to-Studio importer | main |
| `interceptor/` | Go request workflow/forwarding service and companion tools | master |

Do not treat sibling repositories as one Git history, copy credentials between
them, or assume a change to one has updated the others. Use independent branches,
tests and delivery evidence. Preserve unrelated work and active worktrees.

For configured local Studio coordination use the squad-coordination wrapper and
shared ledger; source checkout and ledger are distinct. Read-only investigation
needs no claim. Before mutation verify identity/claims and obtain canonical work
ownership. A source move does not authorize binary installation, active-task
migration, shared-environment changes or protected ENV recovery.

Keep this file short: routing and shared boundaries only. Repository AGENTS.md
owns agent rules; topic documents own architecture/runbooks; dated evidence owns
environment observations; the live ledger owns tasks and claims.
