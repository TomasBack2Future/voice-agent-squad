# Claims and coordination

## What a claim is

A claim is an atomic, exclusive lock on one item by one agent on one machine. The claim record lives in `~/.squad/global.db` (SQLite) and is acquired via `BEGIN IMMEDIATE` — if two agents race for the same item, exactly one wins and the other gets a clean "already claimed by agent-XXXX" error. Released claims move to `claim_history` so a peer can see who held what when.

A claim is a CLI command, not a frontmatter field. The item file's `status:` is rewritten at close-out (when `squad done` runs), not at claim time.

## Why files + DB hybrid

Item content is **per-repo, durable, git-committed.** It lives in `.squad/items/<ID>-<slug>.md` and travels with the codebase. That's where the AC is, where the body is, where the rollback plan is.

Claims, chat, and file touches are **machine-local, operational, ephemeral.** They live in `~/.squad/global.db` and are recreated on the next register. Trying to merge claim rows in git would be a nightmare — two agents claim simultaneously on different machines, both commits "win," and now you have a phantom claim that no one holds. The hybrid keeps git diffs about behavior changes and the DB about who's doing what right now.

## Multi-agent rules

- **One claim per agent per repo** by default. The configuration knob (`agent.claim_concurrency` in `.squad/config.yaml`) can lift it, but the default is 1 because a single agent juggling multiple claims is usually thrashing.
- **Heartbeat keeps a claim live.** Every `squad tick`, `squad milestone`, `squad thinking`, etc. updates the `last_touch` timestamp on your active claim. A claim with no activity past the configured `hygiene.stale_claim_minutes` (default 60 min) is flagged by `squad doctor` as stale.
- **`squad force-release <ID>`** removes an ordinary stuck claim after an operator verifies it is abandoned. Protected `ENV-*` claims are different: hygiene never auto-reclaims them. Use the fenced `squad recover` flow after independently verifying the holder task and external operations.
- **File-touch tracking** warns (does not block) when you start editing a file that another agent's claim is touching. `squad touch <path>` declares an active touch; `squad untouch <path>` releases it. The opt-in pre-edit hook automates this against Edit/Write tool calls.

## Waiting without model polling

Most claim conflicts should still make an agent choose other ready work. When
the item is a required shared resource and useful work is exhausted, the agent
can wait without repeatedly spending model tokens:

```bash
squad claim ENV-001 --intent "run exact-revision acceptance" --long --wait
```

The first attempt is the same atomic claim as usual. If another agent holds the
item, Squad registers a non-exclusive, item-scoped listener and blocks inside
the CLI process. `squad release` sends an immediate loopback wake signal. The
waiting process then rechecks ownership and retries the atomic claim. A quiet
fallback check covers releases that did not originate from the CLI, including
stale-claim hygiene.

Waiting is not ownership. It must not be used to reserve a shared environment
before external prerequisites such as approval, current-base validation, or CI
have passed. Revalidate those prerequisites after `--wait` returns and before
the first mutation.

## Protected environment recovery

Stopping a Codex task can interrupt its cleanup handler while a deployment or
rollback continues outside Codex. For that reason, stale `ENV-*` claims are
reported but never deleted by automatic hygiene.

After independently verifying the holder task is stopped and recording the
state of workflows, rollout, exact revision, and environment health, a recovery
operator can atomically transfer the claim:

```bash
squad recover ENV-001 \
  --from agent-abcd \
  --holder-session 0199... \
  --reason "holder task stopped during rollback verification" \
  --evidence "task stopped; workflow terminal; staging revision 9917d417 ready" \
  --confirm-holder-stopped
```

Recovery is a compare-and-swap on the expected holder and generation. It never
makes the environment free between owners. The new generation is written to
`claim_recoveries`; a late release from the displaced holder is rejected. The
recovery owner may only make the environment safe, verify an exact revision,
and release it—not merge unrelated work.

## Durable dispatch reservations

A Dispatcher reserves a canonical Issue source before creating its Squad item
or Worker task. Only the winner creates the item, attaches it, and binds the
returned generation to the created task:

```bash
squad dispatch reserve DISPATCH-STUDIO-501 --source github:owner/repo#501 --json
squad dispatch attach DISPATCH-STUDIO-501 --item STUDIO-501 --generation 1
squad dispatch bind DISPATCH-STUDIO-501 --generation 1 --thread-id 0199...
```

This reservation is not work ownership. The Worker must still win the normal
atomic Issue claim. It only closes the scheduler race in which two periodic
cycles both observe an unclaimed Issue. Reserving before item creation prevents
duplicate canonical items as well as duplicate Workers. Unbound reservations
expire; bound reservations remain durable until the Dispatcher
reconciles them with `dispatch close`.

## Lifecycle states

```
filed (.squad/items/) ──┬─► claimed ──► in-progress ──► review ──► done (.squad/done/)
                        │       │              │
                        └─► blocked            └─► released (back to ready)
                                │
                                └─► (resolved) ──► claimed
```

Command per transition:

| From | To | Command |
|---|---|---|
| filed | claimed | `squad claim <ID> --intent "..."` |
| claimed | released | `squad release <ID>` |
| claimed | review | `squad review-request <ID>` |
| any open | blocked | `squad blocked <ID> --reason "..."` |
| blocked | claimed | `squad claim <ID>` (re-claim) |
| any open | done | `squad done <ID> --summary "..."` |

`squad reassign <ID> @new-owner` is shorthand for "release + ping new owner in chat."

## Cross-machine claims

The global DB is **machine-local**. If you switch laptops, your claim does not follow you — register on the new machine, then re-claim. Items themselves do follow (they're in git), so the work is portable; only the operational state needs re-creating.

Cross-machine claim sync is **out of scope for v1.** v2 may add it (the design doc has a section on this). For now: one machine = one claim namespace.

## Per-claim worktrees

When two agents claim into the same working tree, their pending edits flow into each other's test runs — a sibling's WIP can produce a misleading green that doesn't validate either claim's code in isolation. The structural fix is one git worktree per claim.

Opt in per claim:

```bash
squad claim FEAT-123 --intent "..." --worktree
```

or globally for the repo, in `.squad/config.yaml`:

```yaml
agent:
  default_worktree_per_claim: true
```

What `--worktree` does:

- Provisions `<repoRoot>/.squad/worktrees/<agent>-<item>/` on a fresh `squad/<item>-<agent>` branch forked from the current HEAD.
- Records the absolute path in the `claims.worktree` column so `squad done` and `squad handoff` can tear it down without re-deriving.
- Prints a `cd <path>` nudge after claim — that's where you do the work.

Teardown happens automatically:

- `squad done <ID>` runs `git worktree remove --force` then `git branch -D squad/<item>-<agent>` if no commits landed. Commits leave the branch in place — the user (or a follow-up PR) owns the merge.
- `squad handoff` does the same for every claim it releases.
- Cleanup failures are warnings, never block the close-out. Stranded directories surface in `squad doctor` as `worktree_orphan`.

The `--worktree` flag is opt-in for this ship. Solo flows are unaffected — the column is empty, the cleanup path is a no-op.

## Common races and how they resolve

- **Two agents claim simultaneously.** SQLite `BEGIN IMMEDIATE` serializes the transactions; one commits, the other gets `unique_constraint`-equivalent and the `squad claim` command exits with a clear "already claimed by X" message. No corruption, no torn state.
- **Agent crashes mid-claim.** No release runs, so the claim stays open with a stale heartbeat. Ordinary claims can be force-released after confirmation. `ENV-*` claims require the fenced recovery flow above and are never auto-reclaimed.
- **Claim across worktrees in the same repo.** Each worktree's `.squad/items/` may differ if the items are in different branches, but the DB is shared. Claiming the same `<ID>` from two worktrees still races against the DB, so only one wins — even if the other worktree doesn't have that file checked out.

## See also

- [squad-vs-agent-teams.md](squad-vs-agent-teams.md) — claim semantics compared to agent-teams' file-locked tasks.
