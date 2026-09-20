# Studio workspace routing

This is a multi-repository workspace, not a monorepo. Enter the target repository
and read its root AGENTS.md before work. Each repository's instructions and docs
must remain usable when cloned alone or opened in an isolated worktree.

| Directory | Responsibility | Default branch |
| --- | --- | --- |
| `voice-agent-studio/` | Go/React product, contracts, Helm and release workflows | main |
| `voice-agent-squad/` | Local coordination CLI/MCP/plugin  | main |
| `convoai-studio-importer/` | Independently maintained Python source-to-Studio importer | main |
| `interceptor/` | Go request workflow/forwarding service and companion tools | master |

Do not treat sibling repositories as one Git history, copy credentials between
them, or assume a change to one has updated the others. Use independent branches,
tests and delivery evidence. Preserve unrelated work and active worktrees.

Keep this file short: routing and repository boundaries only. Each repository's
AGENTS.md owns development rules; topic documents own architecture and runbooks.
Live task state and any chosen development-agent orchestration belong outside
product repository instructions. Source work does not authorize installation,
shared-environment changes or active-task migration.
