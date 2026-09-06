# Recipe: Multi-agent parallel Claude sessions

## Why this works

Atomic claims live in `~/.squad/global.db`, a SQLite file with `BEGIN IMMEDIATE` semantics. When two agents race for the same item, exactly one wins; the other gets a clean "already claimed" error and picks something else. No corruption, no torn state.

The dashboard at `squad serve` shows live which agent holds what, what they last said, and where the file-touch overlaps are.

When several agents need the same shared resource, do not make each model run a
status/history loop. Once a candidate is actually eligible to use the resource,
run one blocking claim instead:

```bash
squad claim ENV-001 --intent "deploy current green head" --long --wait
```

Squad waits inside the CLI and wakes on release. Only the process that wins the
atomic claim returns successfully; other waiters remain blocked until a later
release or their timeout.

## Setup: three terminal panes, one repo

Open Claude Code in each pane (each pane is its own session) and ask:

> *"Claim the top ready item and walk me through it."*

Claude calls `squad_next` + `squad_claim` in each session, and each session lands a distinct agent id automatically. Squad keys the agent identity off the session env — first one of `SQUAD_SESSION_ID`, `TERM_SESSION_ID`, `ITERM_SESSION_ID`, `TMUX_PANE`, `STY`, `WT_SESSION` that's set — and terminal apps usually set one of these per pane, so collisions don't happen out of the box.

The terminal-equivalent in each pane:

```bash
cd ~/dev/your-project
squad go              # registers a session-derived id and claims the top ready item
```

If you want explicit names for log-readability, override on first run from a terminal: `squad register --as agent-blue --name "Blue"`. Subsequent claims (whether through Claude Code or `squad go`) resume against that id. Otherwise let the derived id stand.

If you're scripting against multiple agents from a single shell (no terminal session), set `SQUAD_SESSION_ID` explicitly per process:

```bash
SQUAD_SESSION_ID=blue squad go
SQUAD_SESSION_ID=green squad go
SQUAD_SESSION_ID=violet squad go
```

Without a session key, every shell shares one persisted agent-id file at `~/.squad/agent-id.txt`, and the most recent registration wins. Set a session env var (`SQUAD_SESSION_ID`, `TERM_SESSION_ID`, `ITERM_SESSION_ID`, etc.) to opt into a per-session file at `~/.squad/agent-id.<hash>.txt` so each shell keeps its own identity.

## Coordination

In Claude Code, ask:

- *"Who's working on what right now?"* → calls `squad_who`.
- *"Ask @agent-green if I should rebase."* → calls `squad_ask`.
- *"What's ready across all my projects?"* → calls the workspace queries.

Or in a terminal:

```bash
squad who                                    # who is registered, current claim, last tick
squad tick                                   # diagnostic sweep — chat is normally delivered continuously by hooks
squad ask @agent-green "should I rebase?"    # directed question
squad workspace status                       # cross-repo when you have multiple
```

## File-touch warnings

Declare files you're editing on your active claim so peers see the overlap. Claude does this automatically when you ask it to claim and touch files (`squad_touch`), or you can do it yourself:

```bash
squad touch FEAT-001 internal/cache/flusher.go
# ... edit ...
squad untouch internal/cache/flusher.go      # release when done; or `squad untouch` for all
```

The first arg is the item ID the touches belong to; the rest are paths.

If you install the opt-in `pre-edit-touch-check` hook, the warning fires automatically when you Edit/Write a file another agent is touching. The hook only warns; it does not block. The right move when warned:

1. Stop and read what the peer is doing (`squad tail --thread <ITEM>` to see their thread).
2. If the work overlaps, post `squad ask @agent-NAME "I'm about to touch X — conflict?"` and wait.
3. If the work doesn't overlap (different functions in the same file), proceed.

## Stale claims

If a peer crashes or walks away mid-claim, the heartbeat goes stale. After `hygiene.stale_claim_minutes` (default 60), `squad doctor` flags it:

```
$ squad doctor
stale claims: 1
  BUG-042  agent-blue   last_tick 2h17m ago    → squad force-release BUG-042 --reason "agent-blue gone"
```

Take over with:

```bash
squad ask @agent-blue "stealing BUG-042, ok?"   # courtesy ping (skip if peer is unreachable)
squad force-release BUG-042 --reason "agent-blue offline >2h"
squad claim BUG-042 --intent "..."
```

The `claim_history` table records the takeover with the reason, so future audits can see what happened.

## Per-claim worktrees (recommended for parallel)

Each claim gets its own checkout, so concurrent agents never share pending edits during a test run. Two ways to enable:

```bash
squad claim FEAT-123 --intent "..." --worktree
```

or set the default in `.squad/config.yaml` once:

```yaml
agent:
  default_worktree_per_claim: true
```

After the claim, `cd` into the printed path:

```
claimed FEAT-123
  tip: cd into the isolated worktree: cd /path/to/repo/.squad/worktrees/agent-blue-FEAT-123
```

`squad done` and `squad handoff` tear the worktree down automatically. If teardown fails (uncommitted edits in the checkout, etc.), the close-out still succeeds — the warning is surfaced and `squad doctor` will flag the stranded directory as `worktree_orphan` so you can clean it up later. See [recipes/recovering-from-orphan-worktrees.md](recovering-from-orphan-worktrees.md).

## Manual worktree variant

If you want one Claude Code session per branch (rather than one per claim), you can still drive it manually:

```bash
git worktree add ../your-project-feat-a feat-a
git worktree add ../your-project-feat-b feat-b
```

Open Claude Code in each worktree. Each worktree's CWD has its own `.squad/items/` (because the items are per-branch), but they all share `~/.squad/global.db`. Claims, chat, and touches still coordinate across worktrees correctly.

If the same item ID exists in two branches with different bodies, claiming locks the ID — but the agent in the other worktree will see different acceptance criteria. Avoid this; either keep items on `main` and merge updates promptly, or rename when the AC genuinely diverges.

## Two machines, one repo

Items follow git like normal — both machines pull and push the same `.squad/items/` files. **Claims do not cross machines.** Each machine has its own `~/.squad/global.db` and its own claim namespace. Coordinate at the agent level: "I'm working on FEAT-001 from my laptop today" gets posted to chat, not enforced by the lock.

This is by design for v1. Cross-machine claim sync is a v2 design problem (the design doc has a section on it).

## Anti-patterns

- **Claiming and walking away** past `hygiene.stale_claim_minutes` without ticking. Heartbeat handles brief absence; long absence stales the claim.
- **Editing a file without `squad touch`** in a multi-agent session. Peers can't see your overlap and the touch-check hook can't help.
- **Force-releasing without checking.** The peer might be 30 seconds from posting a milestone. Try `squad ask @agent-NAME` first.
- **Treating `claim` as a property.** It's a CLI command and a DB row, not a frontmatter field. The item file's `status:` updates only at `squad done`.
- **Model-driven claim polling.** Repeatedly running `status`, `history`, and
  `claim` when nothing changed wastes context. Use `claim --wait` after the
  external readiness gates have passed.
