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

Each repository's AGENTS.md owns portable development rules; topic documents own
architecture and runbooks. Workspace-specific working policy belongs in this
outer file and the explicitly selected versioned package under
`voice-agent-squad/workspace/agent-loop`, not in product repository documents.
Keep live task state out of maintained documentation. Source work does not
authorize package installation, shared-environment changes or active-task
migration.

For an Agent Loop Worker, require one schema-valid assignment envelope and select
one role skill plus one project profile by ID/version. The canonical Issue owns
requirements; do not copy the Dispatcher's conversation into the Worker prompt.
Load phase-specific references only when that phase begins, and resume from a
schema-valid checkpoint rather than transcript replay. Data Analyze is historical
migration evidence, not a runtime dependency of this workspace.
