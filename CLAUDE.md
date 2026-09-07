# CLAUDE.md — squad repo

## For agents starting a session

1. Read this file end-to-end.
2. Read `docs/README.md` and `docs/adopting.md` to understand what the binary does and how a user adopts it.
3. If you're working from a phase plan, read it under `/Users/zsiec/dev/switchframe/docs/plans/` (gitignored locally; not part of this repo). Otherwise the user will provide context inline.

## Codebase orientation

```
cmd/squad/                       # binary entry; one file per cobra subcommand (135+ files, incl. tui.go)
internal/store/                  # SQLite layer; schema.sql + additive migrations
internal/items/                  # item file format, lock, walk, rewrite
internal/claims/                 # atomic claim ledger
internal/chat/                   # typed chat verbs + bus
internal/touch/                  # file-touch tracking
internal/hygiene/                # stale-claim sweeps, doctor checks
internal/identity/               # agent id derivation
internal/repo/                   # repo discovery + global DB path
internal/workspace/              # multi-repo views
internal/scaffold/               # `squad init` templates
internal/attest/                 # evidence ledger (R4)
internal/learning/               # learning artifacts (R5)
internal/notify/                 # notification endpoints
internal/prmark/                 # PR linkage
internal/epics/, internal/specs/ # spec/epic hierarchy (R3)
internal/stats/                  # operational statistics (R7)
internal/listener/               # real-time chat transport (R1)
internal/server/                 # dashboard HTTP + SSE; web SPA at internal/server/web/
internal/mcp/                    # MCP server for Claude Code (R6)
internal/installer/              # plugin install state machine
internal/skills/                 # skill frontmatter parser (test scaffold)
internal/tui/                    # bubbletea TUI (`squad tui`)
internal/tui/client/             # HTTP+SSE client — sole storage-layer access for TUI
internal/tui/views/              # per-view tea.Model implementations (12 views)
internal/tui/components/         # shared table, palette, statusbar
internal/tui/daemon/             # OS-specific service installers (launchd / systemd-user)
internal/tui/theme/              # lipgloss styles
plugin/                          # Claude Code plugin (manifest, hooks, skills, commands) (R6)
templates/github-actions/        # CI templates emitted by `squad init`
docs/                            # README, adopting, concepts, recipes, reference, troubleshooting
.github/workflows/               # CI workflow
```

`go.mod` pins Go 1.25. CI matrix: ubuntu-latest + macos-latest × Go 1.25. Pure Go, no CGO. Release artifacts target `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`.

## Conventions (the load-bearing section)

### TDD is the default, not a suggestion

Failing test → minimal implementation → passing test → commit. One TDD micro-loop = one commit. Skip TDD only for: pure refactors with full existing coverage, exploratory spikes that won't merge, one-off scripts. If you skip it, justify the skip in the commit message.

### Pure Go, no CGO

Must build with `CGO_ENABLED=0` for every supported os/arch. Use `modernc.org/sqlite`, never `mattn/go-sqlite3`. The release matrix is `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64` — nothing ships if any of them break.

### Commits

Prefixes: `feat:`, `fix:`, `test:`, `docs:`, `perf:`, `refactor:`, `chore:`. **No `Co-Authored-By` lines.** Subject ≤72 chars, lowercase after the prefix, imperative mood. Body explains WHY, not WHAT — the diff shows what changed. Frequent small commits beat one big one.

### No PM traces anywhere in code

No backlog IDs, no ticket tags, no PM vocabulary in identifiers, comments, or commit messages. Squad enforces this against its users — squad has to follow it on itself, doubly. If you need a stable reference, use a file:line, a test name, or a function name, not a ticket.

### Minimum comments, default to none

Delete anything that restates code. Only keep a comment when the WHY is non-obvious — a hidden constraint, a workaround for a specific bug, a subtle invariant that would trip a future reader. Comments describing the current task or callers rot immediately and belong in commit messages or PR descriptions, not in the source.

### No future-work TODO comments

If it is worth doing, file it as an item (`squad new bug "..."` or similar). If it is not worth filing, it is not worth a comment. TODOs in the tree are a failure mode — they accumulate forever and nobody reads them.

### No defensive checks for impossible states

Trust internal invariants. Validate only at system boundaries: user input, external APIs, disk, network. A nil-check on something that cannot be nil is noise.

### No premature abstraction

Three similar lines is fine. Wait until you have three real callers before extracting. A wrong abstraction costs more than some duplication.

### TUI import boundary

The TUI's view modules (`internal/tui/views/**`) must NOT import storage-layer packages (`internal/store`, `internal/items`, etc.) — only `internal/tui/client` may speak to the wire. Enforced by `TestImportBoundary` in `internal/tui/architecture_test.go`.

### Evidence requirement before claiming done

Every "tests pass" / "build green" / "CI green" claim must be backed by the actual output line pasted into the conversation. Bare assertions are worth zero. The author and the reviewer both need to see the green line. This is non-negotiable — a silent pass is indistinguishable from a fabrication.

### Lint before every commit

CI runs `golangci-lint run` and rejects PRs on gofmt / staticcheck drift. Run it locally before commit (or at minimum before `squad done`). `gofmt -l . | xargs gofmt -w` auto-fixes the formatting issues; staticcheck issues need manual attention. The squad-done verification gate runs `golangci-lint run` so an item with lint debt cannot reach `done` — but that gate is a safety net, not the first line of defense. Catching it locally saves the round-trip.

### Code review before every commit

Every meaningful change goes through `superpowers:code-reviewer` before commit. Even one-line fixes. The cost is about thirty seconds; the cost of a bug reaching a user is hours of their time and yours. Review catches what tests do not.

This repository workflow does not make priority or risk an implicit CLI review
quota. Projects that require review evidence declare it in
`evidence_required`; hosting-service approval requirements remain enforced by
their branch policy.

### Use the right skill before coding

- `superpowers:brainstorming` for unclear features, ambiguous requests, or anything creative.
- `superpowers:systematic-debugging` for bugs, test failures, unexpected behavior.
- `superpowers:test-driven-development` for implementing any feature or bugfix.
- `superpowers:dispatching-parallel-agents` when you have three or more independent sub-problems.
- `superpowers:executing-plans` when working through a phase plan.

Do not rationalize past the skill — if it might apply, invoke it.

### TaskCreate threshold

Use `TaskCreate` when a task is estimated at ≥2h or has ≥3 sub-steps. Skip it on small linear work — the overhead of maintaining the list outweighs the visibility.

### Amend policy

Never `git commit --amend` without explicit approval. One narrow exception: removing PM-trace mistakes from a local unpushed commit before anyone sees it. Otherwise always create a new commit — amending published history is destructive.

### Switchframe-ism scrubbing

The reference implementation at `/Users/zsiec/dev/switchframe/server/cmd/sf-team/` is read-only inspiration. Lift code patterns, then scrub every Switchframe-ism per the master plan checklist: `sf-team` → `squad`, `~/.switchframe-team` → `~/.squad`, `SF_TEAM_SESSION_ID` → `SQUAD_SESSION_ID`, all broadcast vocabulary (on-air, MXL, SCTE, captions, mosaic, server/output/, etc.) removed or replaced with generic placeholders. A grep for `switchframe`, `sf-team`, `sf_team`, or `on-air` against this repo must return zero matches outside of documentation that references the migration explicitly.

## Operating manual

How every session goes. Make progress with no ceremony, never break what you wouldn't defend in review.

### Mental model

- Items: `.squad/items/` (markdown, YAML frontmatter + body). Done → `.squad/done/`.
- Board `.squad/STATUS.md` is a pointer; items are the contract.
- Claims, chat, file touches: `~/.squad/global.db` (machine-local).
- Plans: `docs/plans/` (gitignored).

### Resume a session

Run `squad go`. It inits, registers, claims the top ready item, prints AC, flushes the mailbox. Idempotent. The plugin's `/work` does the same.

```bash
squad go
```

If it says `no ready items`, ask the user, `squad new`, re-run. If you have an in-progress claim, `squad go` resumes it.

### Pick an item (if not using `squad go`)

Priority: (1) in-progress claims you hold; (2) newly unblocked items; (3) ready items by priority (P0→P3) then smallest estimate; (4) user asks (file with `squad new` if >1h).

```bash
squad next
squad claim <ID> --intent "one sentence" [--touches path1,path2]
```

`claim` exit 1 means already claimed — pick another. The DB is the live lock.

### Worktree workflow

When `squad claim` provisions a per-claim worktree (default per `agent.default_worktree_per_claim`), `cd` into it and stay there for code edits, tests, and commits. The worktree's own branch keeps your `git status` clean from peer agents working in shared territory — the alternative is repeated staging-leak when peers' WIP files appear modified next to yours and end up bundled into your commit. **Exception:** run `squad done` from the parent main checkout, not the worktree. The auto-merge step that folds your branch into `main` needs the parent on `main` to do the merge; if you stay in the worktree you'll get `fold: parent repo is on "<branch>", expected "main"` and the close fails halfway. Easiest pattern: `cd $REPO_ROOT && squad done <ID>` when ready to close.

### Work the item

1. Read end-to-end. AC is the contract.
2. **Verify every `file:line` reference against current code.** Item bodies rot — fix the item first if stale.
3. **TDD by default.** Failing test → minimal impl → green → commit. Skip only for spikes, fully-covered refactors, or one-offs. Never silently.
4. AC names concrete failures? Write **RED tests first** against unfixed code. If they pass unmodified, reclassify or close no-repro.
5. **Stay chatty.** Post at the cadence moments below — silence on a held claim erases the *why*.

#### Anchor checkpoints (≥1d items)

Long items drift. Every meaningful chunk (~30–60 min of focused work), pause and check:

1. **Re-read the item's `## Acceptance criteria`.** Does the current diff progress toward each line, or has scope crept?
2. **Re-read `docs/plans/<id>.md`** (if you wrote one). Is the next concrete step still right?
3. **Did anything new surface that should be a separate item?** File the BUG/CHORE now while it's fresh.
4. **Are you still working on the smallest possible diff that meets the contract?** If sprawling, split: land the minimum first, file the rest as follow-ups.

Mentions and file conflicts arrive continuously through hooks — address any that surface before the four questions above. A correction at hour 2 is much cheaper than at hour 6.

### Test before claiming done

Run verification from `.squad/config.yaml`. Scope iteration by package; full suite once before commit. Evidence requirement applies — paste the actual output line.

### Code review (mandatory, every item)

Even one-liners. Spawn `superpowers:code-reviewer` with diff + item file. Verify each finding — don't perform-agree. Push back with evidence when wrong. File follow-up items for out-of-scope findings. Blocking issues open → leave `status: review` and stop.

For external review: `squad review-request <ID> [--mention @reviewer]`. Brief the reviewer with the item file path, the diff, the specific concerns, and an output format (prioritized findings: Critical / High / Medium / Low with file:line).

**Premise-validation latitude.** Tell the reviewer: "If the claimed failure seems dubious, empirically verify against the pre-fix code. If the bug doesn't reproduce, report back so we can reclassify the item."

**Working-tree hygiene.** "If you patch any file to verify behavior, restore it. Do not leave `.bak` files or scratch edits behind."

### Commit and close

```bash
git add <files>
git commit -m "fix: <concise summary>"   # or feat: / docs: / refactor: / chore:
```

Subject ≤72 chars, lowercase after prefix, imperative. **Never reference item IDs in commit messages.**

```bash
squad done <ID> --summary "one-line outcome"
```

Releases claim, archives item to `.squad/done/`, posts to global + thread, auto-untouches. Update `.squad/STATUS.md` if you were on it.

#### Quality bar before commit

Tests passing is necessary but not sufficient. **Don't mark an item done until you'd defend the code in review.** If you're embarrassed by it, refactor it or split the dirty bits into a follow-up CHORE.

Concrete signals to check before committing:

- **No commented-out code.** Delete it. Git remembers.
- **No `TODO`, `FIXME`, or "future work" comments.** If it's worth doing, file an item.
- **No defensive checks for things that can't happen.** Trust internal invariants; only validate at system boundaries.
- **Minimum comments. Default to none.** Only WHY when genuinely non-obvious.
- **No project-management traces in code.** No backlog IDs in filenames, identifiers, comments, or commit messages.
- **No premature abstraction.** Three similar lines beats a wrong abstraction.
- **No half-finished implementations.** End-to-end against the AC, or it's not done.
- **Acceptance criteria literally checked off?** Re-read the item file's `## Acceptance criteria`.

If you genuinely can't meet the bar this session, set `status: review` instead of `done`, write what's wrong in the resolution notes, and file the follow-up.

### Filing a new item

`squad new <type> "<title>" --priority P[0-3] --area <subsystem>` scaffolds the file. Types: `bug`, `tech-debt`, `feature`, `chore`, `task`.

ID prefixes for this project:
- `BUG-NNN`
- `FEAT-NNN`
- `TASK-NNN`
- `CHORE-NNN`

If P0/P1, add to the Ready section of `.squad/STATUS.md`. Numbers are monotonic per-prefix. Half-baked thoughts? File them anyway with `priority: P3` and `status: open`; triage later.

#### Done contracts (per type)

| Type | Done means |
|---|---|
| **bug** | A test reproducing the original bug exists in the repo and now passes. |
| **tech-debt** | No behavior change AND a named metric improved with before/after numbers. |
| **feature** | Acceptance criteria all checked. UI: end-to-end verified. |
| **chore / task** | The thing is done. Verify by running it. |

If you can't write a done-contract assertion for the item, the AC are too vague — sharpen them before coding.

### Multi-agent dispatch

**Default to parallel.** If 2+ ready items are in different subsystems with no shared state and no dependency between them, dispatch in parallel. Sequencing them out of habit wastes wall-clock time.

Subagents can't see chat — before spawning, the parent must bake any unaddressed mentions into the sub-brief. Continuous hooks deliver chat to the parent, so the freshest state is whatever is already in your context.

**Do not** dispatch parallel agents for items in the same file/directory (merge conflicts), items where one's findings might change another's approach, or exploratory work (a single focused investigation is faster).

**Test gates are not symmetric.** Children run only their package-scoped tests during TDD. They must NOT run the full suite. The parent is the integration gate; after cherry-picking parallel diffs, the parent runs the full suite **before** dispatching code review.

After agents return, **verify their work.** Read the actual diff before reporting "done." Trust but verify — agent summaries describe intent, not always reality.

### Handoff between sessions

If a session ends mid-item:

- Leave the item `status: in_progress`.
- Add a `## Session log` section to the item file with: what I did, what's left, gotchas / dead-ends to avoid, next concrete step.
- If you've changed code that isn't yet tested or committed, leave a note in `.squad/STATUS.md` under In Progress with `(uncommitted on branch X)`.
- `squad release <ID> --outcome released` if truly handing off.

#### End-of-session brief (every session)

Before signing off, post a 3-bullet summary:

1. **Shipped:** items closed (link by ID + one-line outcome). If nothing closed, say "nothing closed."
2. **In flight / queued:** what's `in_progress` or `review` and where it is.
3. **Surprised by:** anything the next session should know that isn't in an item file or commit message. Skip if genuinely nothing.

### Escalation / blocked

If you hit something you can't resolve:

- Set `status: blocked`.
- Add `## Blocker` section: what blocks, what's needed, who/what could unblock.
- Move out of In Progress → Blocked in `.squad/STATUS.md`.
- Pick the next item and continue. Don't sit on a blocked item.

### Time-boxing exploratory work

Some items have unclear scope. These can become black holes. Time-box them: **default exploration cap is 2 hours** of focused work. If you're 2h in and still don't understand the problem, **stop and write up what you know** — hypothesis space tried, ruled-out causes, evidence collected, what's still unknown — then either escalate, spawn a parallel agent on the most promising remaining hypothesis, or set `status: blocked`.

Don't quietly extend the cap. Long unsuccessful sessions are a signal, not a setback.

### Chat cadence

The backlog is durable; chat is where the team stays in sync while that durable state is being changed. Post often, post small, post honestly. Peers reading later (human or agent) should be able to reconstruct your thinking, not just your commits.

**Verbs.** Use the shortest one that fits. All route to your current claim thread by default; pass `--to <ID>` or `--to global` to override. All accept `@agent` mentions.

| Verb | When |
|---|---|
| `squad thinking <msg>` | Sharing where your head's at — *before* committing, when a plan is still forming. |
| `squad milestone <msg>` | A checkpoint: AC green, phase done, test landing. |
| `squad stuck <msg>` | You're blocked — others can jump in. |
| `squad fyi <msg>` | Heads-up — direction change, surprise, discovery. |
| `squad ask @agent <msg>` | Directed question to one agent. |
| `squad say <msg>` | Plain chat — escape hatch when no verb fits. |
| `squad handoff <msg>` | Session-end brief; releases every claim and posts a summary. |

**Cadence.** Post on claim, on direction change, on AC complete, on commit, on surprise, on blocker, on session pause. **Too much?** If the post is just "starting" / "resuming" / "still working" with no new information, cut it. The goal is *visibility into non-obvious state*, not a change log.

**Recognition.** Anchor thanks to the specific behavior (e.g. *"the orphan-ref grep was the catch I'd have missed"*, not *"great work!"*). Generic cheer dilutes the audit log and primes sycophancy in reviewer-agent roles. The `squad-chat-cadence` skill carries the full rule.

### Anti-patterns (the load-bearing ones)

- **Don't claim "done" without running tests.** Bare assertions are worth zero.
- **Don't ship past blocking review.** Code review is mandatory.
- **Don't perform-agree with the reviewer.** Verify each finding.
- **Don't `--no-verify` on commits.** Fix the cause.
- **Don't skip TDD silently.**
- **Don't reference item IDs in commit messages or code.** Backlog lives in `.squad/items/`; code does not.
- **Don't write `TODO` / `FIXME` comments.** If it's worth doing, file an item.
- **Don't add comments that restate code.** Only WHY when non-obvious.
- **Don't grind silently between claim and done.** Silence on a held claim erases the *why* behind the diff and wastes peer wall-clock.
- **Don't open more than one in-progress claim per session** unless using parallel agents.
- **Don't dispatch parallel agents on related work.** They will conflict.
- **Don't extend a time-box silently.** Default exploration cap is 2h.
- **Don't ship without an evidence paste.**

### When in doubt, ask

Risky actions (deploy, push to main, force-push, edit secrets, drop a database) — **stop and ask**. Once is cheap; an unwanted action is expensive.

Low-risk small calls (file naming, comment placement) — pick a default that matches surrounding code and proceed.

### Learnings (durable, write-gated)

Cross-item lessons (gotchas, patterns, dead-ends) go in `.squad/learnings/`. Flow: **propose → human approve → auto-load**.

- `squad learning propose <kind> <slug> --title ... --area ...` writes a stub (`gotcha`, `pattern`, `dead-end`); fill its headers.
- `squad learning approve <slug>` synthesizes approved entries into `.claude/skills/squad-learnings.md`, auto-loaded on matching paths.
- `squad learning reject <slug> --reason ...` archives under `rejected/`.

Agents don't write `AGENTS.md` directly — it's generated from the ledger by `squad scaffold agents-md`. To suggest hand-edited contract changes, propose a diff against `CLAUDE.md` (or the relevant skill) instead.

## Reading order for new agents

In priority order, stopping when you have enough context:

1. **This CLAUDE.md** — conventions and bootstrap stance.
2. **Master plan** — `/Users/zsiec/dev/switchframe/docs/plans/2026-04-24-squad-master-plan.md`. Cross-cutting conventions, phase index, execution model.
3. **Current phase plan** — `/Users/zsiec/dev/switchframe/docs/plans/2026-04-24-squad-phase-NN-<topic>.md`. Task-by-task instructions with paste-in code.
4. **Design doc** — `/Users/zsiec/dev/switchframe/docs/plans/2026-04-24-squad-extraction-design.md`. Every architectural decision with rationale. Read when you need the WHY behind a phase instruction.

All four documents live in `/Users/zsiec/dev/switchframe/docs/plans/`. They are gitignored in that repo and must not be committed from this repo either — `.gitignore` already excludes `docs/plans/`.

## Updating this file

Update this file when codebase structure or conventions change materially. The conventions section is load-bearing — every contributor reads it. Keep the codebase orientation in sync with the actual tree.

<!-- squad-managed:start -->
# squad (managed by squad)

This block is owned by `squad init`. Edits inside the markers may be
overwritten on `squad init` re-runs. Edit OUTSIDE the markers freely;
those sections are yours.

## For agents starting a session

Read these in order before doing any work:

1. **`AGENTS.md`** — operating manual: how to pick work, work it, commit, update, hand off
2. **`.squad/STATUS.md`** — what's in flight, what's ready, what's blocked
3. The relevant item under `.squad/items/<ID>-*.md` — the acceptance criteria are the contract

Then take the one-command path:

```bash
squad go
```

`squad go` registers a session-stable id, claims the top ready item, prints
its acceptance criteria, and flushes any unread chat. It's idempotent —
re-run it to resume the same claim and re-flush the mailbox.

## Loop summary

- Pick top item: `squad next`
- Claim: `squad claim <ID> --intent "<one sentence>"`
- Stay visible: `squad thinking <msg>` / `squad milestone <msg>` / `squad stuck <msg>`
- Close: `squad done <ID> --summary "<outcome>"`

## Conventions

- TDD by default. Failing test → implementation → passing test → commit.
- Commits: `feat:` / `fix:` / `test:` / `docs:` / `perf:` / `refactor:` / `chore:`.
- No backlog IDs in commit messages, filenames, or code identifiers.
- Quality bar in CLAUDE.md "Quality bar before commit" — tests passing is necessary but not sufficient.

<!-- squad-managed:end -->
