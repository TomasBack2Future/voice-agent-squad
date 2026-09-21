# Workspace preparation and migration boundary

This is a preparation contract, not a migration command or live inventory.
Keep four independent repositories; do not create an enclosing Git history.

```text
studio/
  AGENTS.md                     thin routing, safety and selected contract versions
  README.md                     repository map and documentation links
  .agents/
    project-profiles/           installed links to approved versioned profiles
    skills/                     installed links to approved versioned role skills
  .worktrees/                  optional new owned worktrees
  voice-agent-studio/
  interceptor/
  convoai-studio-importer/
  voice-agent-squad/
    workspace/agent-loop/      this opt-in package

Separate protected runtime storage
  ledger / checkpoints / redacted observations / evidence receipts
User-level configuration
  provider definitions / credential references / qualified per-client settings
```

Only the package files explicitly checked in are implemented. A reference Worker
skill, Studio profile and assignment/checkpoint schemas now live under
`voice-agent-squad/workspace/agent-loop/`; the entrypoint does not install them
into `.agents`, activate dispatch or migrate runtime state.
Provider-specific discovery must load the selected workspace policy explicitly:
an isolated repository/worktree may not discover its former parent's AGENTS.md.
Repository contributor rules remain self-contained and free of role orchestration.

## Before any physical or runtime move

Prepare a private inventory with source, owner, destination, dependency,
disposition, evidence and rollback for each category:

| Category | Read-only evidence and move gate |
| --- | --- |
| Source repositories | Remote/default branch, current branch/SHA, dirty/untracked work, stashes, pending PRs; preserve/review before switching |
| Linked worktrees | Git common directory and registered path, existence, branch/head, owner and external-operation references; a count is not a deletion list |
| Skills | Canonical source/version, installed copies/links, consumer and discovery precedence; resolve conflicts before replacing anything |
| Runtime wrappers | Executable/source version, ledger selection and provider/session assumptions; source relocation does not migrate the installed command |
| App projects and sessions | Saved root, worktree/attempt binding and still-active operations; do not rewrite App internals to emulate relocation |
| Schedulers and reservations | Actual trigger source, target queue, generation and completion receipts; silence/notLoaded is not terminal |
| Claims and environments | Canonical resource mapping, owner/fence and external operation; no duplicate authority or automatic protected-lock cleanup |
| Evidence and private state | Retention/access policy, exact revision and location; never commit credentials, raw logs or customer data |

Keep the existing ledger in place initially. Resolve old and new workspace paths
to the same authority; do not copy the live database into a second active ledger.
Existing worktrees remain where their owners expect them. Move, archive or remove
one only after verifying ownership, recoverable changes and terminal external
operations. Do not run blanket `git worktree prune` or delete an old directory
because source checkouts have moved.

## Cutover order

1. Publish source/documentation changes as independent PRs. Preserve existing
   documentation branches and local research; merging needs separate approval.
2. Select one canonical versioned role package and project capability source.
   Keep provider secrets outside repositories; only safe logical route references
   belong in portable project profiles.
3. Qualify per-session entry, instruction discovery and permissions on the actual
   execution host. Investigator/Recaper need no cmux dependency.
4. Resolve the current architecture/deployment/CI contracts and complete a dated
   [baseline receipt](baseline-receipt.md). Installed skills referring to retired
   topology must not override the selected release's source contract.
5. Reconcile legacy reservations and protected ownership. Prove the old scheduler
   cannot dispatch the same queue before activating a replacement.
6. Run a single explicitly assigned staging delivery with durable context and
   exact-revision acceptance; only then expand concurrency and recurring roles.

After that pilot, remove the old Data Analyze role/profile copies from discovery
only when every active consumer resolves the new package and the old scheduler
cannot launch the same queue. Historical ledgers and receipts may remain retained;
they are not a runtime import path for new assignments.

Rollback is to the last qualified source/configuration package with the same
ledger and preserved external receipts, not restoring an old database over live
claims. No step here authorizes a production rollout, lock recovery or cleanup.
