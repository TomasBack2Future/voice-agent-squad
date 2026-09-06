---
name: squad-loop
description: The operating loop every session follows — register, tick, pick, work, checkpoint, test, review, commit, close. Use whenever you start or resume work on a squad-managed repo.
allowed-tools:
  - Bash
  - Read
  - Edit
  - Task
paths:
  - ".squad/**"
disable-model-invocation: false
hooks:
  PreToolUse:
    - matcher: Bash
      hooks:
        - type: command
          command: "${CLAUDE_PLUGIN_ROOT}/hooks/loop_pre_bash_tick.sh"
---

> **Reference:** `AGENTS.md` is the full operating manual — loop, parallel dispatch, time-boxing, handoff, chat cadence, anti-patterns. Skills with their own `paths:` glob (e.g. `squad-handoff.md`) auto-load when you touch a file matching their glob, surfacing the relevant depth without you needing to remember it.

# The squad loop

Every session — yours, every coworker's, every parallel agent's — follows the same eight steps. The point of the loop is to make progress with no ceremony, leave the workspace better than you found it, and never break anything for the team. Skip a step and you produce work that other people have to clean up.

## When to use this skill

Invoke this skill when starting a session, resuming after a pause, or any time you find yourself about to "just start coding" without going through the loop. If you are unsure whether you are in the loop, you are not — read this skill and start at step 1.

## The eight steps

1. **Run `squad go`.** It registers a session-stable id, claims the top ready item, prints AC, and flushes any unread chat. Idempotent — re-run to resume the same claim. If `squad go` says `no ready items`, fall through to step 2.
2. **Pick an item.** Continue an in-progress claim if you have one. Otherwise: `squad next` lists the top of the ready stack. Pre-flight check: every entry in the item's `blocked-by:` must already be `done`. Cross-repo references mean you are in the wrong repo session — skip.
3. **Claim atomically.** `squad claim <ID> --intent "one sentence"`. If exit 1, somebody else holds it — pick another. When the exact item is a required shared resource and no useful work remains, use `squad claim <ID> --wait` once instead of waking the model to poll status and history. The DB is the live lock; waiting is non-exclusive and the item file's `status` field updates at close-out, not now. On P0/P1 or `risk: high` items the CLI prints a one-line nudge suggesting `squad ask @<peer>` before you start — treat it as a real prompt, not styling annoyance; high-stakes work going wrong burns peer time too. Add `--worktree` (or set `agent.default_worktree_per_claim: true`) when running parallel sessions so peer WIP can't bleed into your test runs.
4. **Work the item.** Read the item end-to-end. Verify every `file:line` reference against current code. If the acceptance criteria name concrete failures, write RED tests FIRST (see `squad-premise-validation`). TDD is the default, not a suggestion.
5. **Checkpoint at every meaningful chunk.** New file, new test, new abstraction, ~30–60 min of focused work — pause and re-read the AC. Has scope crept? Is the diff still the smallest possible? File any new BUG/DEBT discoveries now while they are fresh.
6. **Test before claiming done.** Scoped tests during iteration; full suite once before commit. Paste the actual output (see `squad-evidence-requirement`). Bare assertions are worth zero.
7. **Code review, every item.** Even one-line fixes. Spawn `superpowers:code-reviewer` with the diff and the item file. Verify each finding — do not perform-agree. See `squad-code-review-mandatory`.
8. **Commit, mark done, move on.** `squad done <ID> --summary "one-line outcome"`. Move the item file to `.squad/done/`. Update the status board if you were on it. Pick the next item. (Chat is delivered continuously by hooks — no manual tick needed before commit.)

## Why this works

The loop is a forcing function for visibility and accountability. `squad go` keeps the team unblocked — registration, claim, mailbox flush in one shot. Atomic claim prevents two agents from doing the same work twice. Premise validation prevents you from "fixing" bugs that do not reproduce. Evidence-gated done means the next session can trust your "done." Mandatory review catches what self-review misses. The eight steps together are how a squad of agents actually finishes things instead of meandering.

## Anti-patterns to avoid

- Starting to code before claiming. The DB is the lock; without a claim, you can collide with a peer.
- Skipping `squad go` because "I know what I'm doing." Continuous hooks need a registered identity; without `squad go` you may post under a stale id.
- Marking done without re-reading the AC. Each box must map to a test you wrote or a step you actually performed.
- Treating review as ceremony. Review costs ~30 seconds and catches what you cannot see in your own diff.
