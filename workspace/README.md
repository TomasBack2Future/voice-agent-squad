# Optional Studio workspace layer

The sibling-checkout workspace can contain:

```text
studio/
  AGENTS.md                      # optional routing from this template
  README.md                      # local workspace map
  voice-agent-studio/AGENTS.md    # independently versioned entry
  voice-agent-squad/AGENTS.md     # independently versioned entry
  convoai-studio-importer/AGENTS.md
  interceptor/AGENTS.md
```

The optional [Agent Loop package](agent-loop/README.md) supplies a versioned
Worker skill, Studio project profile, bounded assignment schema and checkpoint
schema. Adopting it is an explicit workspace action. Its optional `skill_sync.py`
installer can register local Git hooks to synchronize committed skills into
`.agents`, `.codex` and `.claude`; it does not migrate a live ledger.

[AGENTS.md](AGENTS.md) is the source-controlled outer-workspace template.
Copy it deliberately when creating a workspace; this repository does not install
or overwrite an existing workspace file automatically. Relative paths in that
template refer to its eventual outer directory, not this template folder.

Each repository owns its own instructions because a repository-root agent or
an isolated worktree may not load parent-workspace instructions. Do not create
an enclosing Git repository simply to make instructions inherit. Do not commit
sibling checkouts, credentials, private state or live task queues here.

Before a physical migration inventory source checkouts, linked worktree common
directories, user WIP/stashes, installed tool and skill paths, external tool configuration, saved app projects and automation prompts. Moving the source
directory alone changes none of those references. Coordinate active owners
before switching/removing their checkout. The local migration audit is separate
from this portable documentation template.

## Maintained coordination skills

`coordination-skills/squad-dispatcher` and `coordination-skills/studio-issue-worker`
are the canonical sources of the existing machine-level coordination skills.
They preserve the established delivery references and scripts. Install them as
one commit-pinned package so sibling references resolve; user skill entries are
links or generated installs, never independently maintained copies.

The Dispatcher owns product/design admission in the canonical Issue before a
new implementation assignment. It checks product consistency, related Issue
conflicts, scope/compatibility and observable acceptance. Investigator is used
only for missing facts; no separate Designer stage is introduced. The detailed
contract is [design admission](coordination-skills/squad-dispatcher/references/design-admission.md).
Issue editing is allowed only for the scoped decision and necessary corrections;
implementation, ENV ownership, merge, deployment and closure remain outside the
Dispatcher role. Workspace transport and authorization policies still apply.

Both the Studio Worker and portable Agent Loop Worker consume the Issue section
and revision. The v1 envelope accepts an optional `design_admission` object with
`section`, `revision` and `status: READY`; new dispatches must supply it. Optionality
preserves old assignments. Schema validation checks only shape; the Dispatcher
and Worker must verify the current Issue and evidence. No database admission
field or automatic runtime enforcement is claimed. Existing sessions are not
restarted; only verified actual scope conflicts warrant a targeted correction.
