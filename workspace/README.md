# Optional Studio workspace layer

The sibling-checkout workspace can contain:

```text
studio/
  AGENTS.md                      # optional routing from this template
  README.md                      # local workspace map
  voice-agent-studio/AGENTS.md    # independently versioned entry
  voice-agent-squad/AGENTS.md     # generated route to CLAUDE.md
  convoai-studio-importer/AGENTS.md
  interceptor/AGENTS.md
```

[AGENTS.md](AGENTS.md) is the source-controlled outer-workspace template.
Copy it deliberately when creating a workspace; this repository does not install
or overwrite an existing workspace file automatically. Relative paths in that
template refer to its eventual outer directory, not this template folder.

Each repository owns its own instructions because a repository-root agent or
an isolated worktree may not load parent-workspace instructions. Do not create
an enclosing Git repository simply to make instructions inherit. Do not commit
sibling checkouts, credentials, private state or live task queues here.

Before a physical migration inventory source checkouts, linked worktree common
directories, user WIP/stashes, installed tool and skill paths, wrapper ledger
configuration, saved app projects and automation prompts. Moving the source
directory alone changes none of those references. Coordinate active owners
before switching/removing their checkout. The local migration audit is separate
from this portable documentation template.
