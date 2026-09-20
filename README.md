# squad

This is the Voice Agent Squad fork. Start with [AGENTS.md](AGENTS.md),
[the architecture map](docs/architecture.md), [Studio integration](docs/studio-agent-loop.md)
and [CI/distribution boundaries](docs/environments-and-ci.md).
The upstream installation/tutorial examples below describe the generic Squad
product, not the configured local Studio dispatcher or its shared ledger.

[![CI](https://github.com/zsiec/squad/actions/workflows/ci.yml/badge.svg)](https://github.com/zsiec/squad/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

> Project management for software work done with AI coding agents.

Squad gives Claude Code the durable coordination layer it lacks on its own — atomic claims, typed chat verbs, file-touch tracking, an evidence ledger, and a multi-agent dashboard. The full squad CLI surface is exposed as MCP tools, so Claude does the squad work for you. You describe what you want; squad's plumbing makes it happen.

> ⚠️ **Status:** under active development, pre-1.0.

## Quick start

**Step 1.** Install the `squad` binary:

```bash
brew tap zsiec/tap
brew install squad
```

Or via `go install` if you'd rather build from source:

```bash
go install github.com/zsiec/squad/cmd/squad@latest
```

**Step 2.** Install the Claude Code plugin from inside any Claude Code session:

```
/plugin marketplace add zsiec/squad
/plugin install squad@squad
/reload-plugins
```

Restart Claude Code (or `/reload-plugins`) so the always-on hooks, skills, and MCP server load. Squad's tools then become available to every Claude Code session in any repo. The first MCP boot also installs the dashboard daemon as a per-user system service and opens http://localhost:7777 in your default browser — once. No `squad serve` step.

That's it for setup. The first time you ask Claude to do squad work, it claims and walks the loop:

> *"Claim the top ready item and walk me through it."*

Claude calls `squad_next` to find the priority pick, `squad_claim` to lock it, prints the acceptance criteria, and flushes any pending peer chat into your context. You start working. When the work is done, say so:

> *"Mark this done with summary 'shipped retry logic'."*

Claude calls `squad_done`. If the item declared `evidence_required: [test, review]`, Claude first records each verification with `squad_attest` — capturing the command output, the exit code, and a content hash so the proof survives. The item moves to `.squad/done/` and the next one's ready.

That's the whole loop. You never run a squad command yourself.

> If you prefer typing, `/squad:work` is the slash-command equivalent of "claim and walk".

## Your first real item

The example walks the loop on a no-stakes item. To file actual work, three slash commands cover the capture surface:

- **`/squad:squad-capture <one-line description>`** — fast capture. Claude infers the type (`bug` / `feat` / `task` / `chore` / `debt` / `bet`), drops the item in your inbox, and you triage later. Use this when you're mid-task and don't want to context-switch.
- **`/squad:squad-intake <starting idea or item id>`** — structured interview. Claude asks one focused question per turn — area, acceptance criteria, scope, non-goals — drafts a bundle (item, or spec + epics + items if the idea is large), and confirms before committing. Pass an existing item id to refine an undercooked stub instead of starting fresh.
- **`/squad:squad-decompose <spec>`** — explode an existing spec into 3–7 captured items linked to the parent. Use this after writing a spec for a multi-week initiative.

Captured items land in `status: captured` — visible in `squad inbox`, **not** in the ready stack. Capture fast, shape later. To triage, ask Claude:

> *"Accept FEAT-007."* — runs the Definition of Ready check (area set, ≥1 acceptance-criterion checkbox, real title or problem). Passing items flip to `open` and become claimable.
>
> *"Send FEAT-007 for refinement"* — opens the dashboard's range-selection composer; select passages, attach inline comments, claude redrafts the body in place.
>
> *"Reject FEAT-007 — duplicate of FEAT-003."* — deletes the file; the reason is appended to `.squad/inbox/rejected.log`. Re-file from scratch if you change your mind.

Full walkthrough: [docs/recipes/triage.md](docs/recipes/triage.md). The reasoning behind the two-state model: [docs/concepts/intake.md](docs/concepts/intake.md).

## Beyond the quick start

A claim → work → done loop is the whole shape of squad. The next layer of the surface is what makes it durable past one session.

**Items live in your repo, not in a tracker.** Every item is a markdown file under `.squad/items/<TYPE>-<NN>-<slug>.md` with YAML frontmatter (priority, type, evidence-required, blockers). They're git-tracked, so the queue travels with the repo and code review covers the queue too. See [docs/concepts/the-loop.md](docs/concepts/the-loop.md).

**Chat is durable and typed.** Squad's chat verbs (`ask`, `say`, `fyi`, `milestone`, `stuck`) write to a SQLite-backed bus that outlives any session. A teammate's question yesterday is still in your inbox today. The plugin's hooks deliver pending chat at session-start, between tool calls, and before context compaction — no polling. Concepts: [docs/concepts/chat-cadence.md](docs/concepts/chat-cadence.md).

**Multiple agents on one repo, cleanly.** Atomic SQLite `BEGIN IMMEDIATE` claims mean two Claude Code sessions can't both grab the same item — exactly one wins, the other gets a clean error. File-touch tracking warns before peers collide on the same file. The hands-on walkthrough is at [docs/recipes/multi-agent-parallel-claude-sessions.md](docs/recipes/multi-agent-parallel-claude-sessions.md).

**Evidence-gated done.** Items can declare `evidence_required: [test, review]` in frontmatter. `squad_done` refuses to close them without an attestation per kind, and each attestation captures the command, exit code, stdout, and a content hash. The ledger lives at `.squad/attestations/`. The reasoning behind it: [docs/concepts/the-loop.md](docs/concepts/the-loop.md).

**Multi-repo views.** Squad keeps an operational DB at `~/.squad/global.db` covering every repo on the machine. Ask Claude *"what's ready across all my projects?"* and the workspace queries surface a unified ready stack and chat history. Concepts: [docs/concepts/multi-repo.md](docs/concepts/multi-repo.md).

**Live dashboard.** http://localhost:7777 — live SSE feed of who-has-what, item flow across repos, and an Insights panel charting verification rate, claim p99 latency, and WIP-cap violations over time. The first MCP boot installs the dashboard as a per-user system service (launchd on macOS, systemd-user on Linux) and opens the page in your browser; subsequent sessions detect a binary upgrade and transparently restart the daemon on the new version. The same data is at `GET /api/stats` and `GET /metrics` (Prometheus exposition). Recipe: [docs/recipes/prometheus.md](docs/recipes/prometheus.md).

> **Power-user opt-outs.** `SQUAD_NO_AUTO_DAEMON=1` skips the auto-install entirely (use `squad serve` manually if you want the UI). `SQUAD_NO_BROWSER=1` skips only the auto-open but still installs the daemon and writes the welcome sentinel. Both are read on every MCP boot — set them in your shell rc to make the choice persistent.

**When things go wrong.** Ask Claude to run `squad_status` for a quick health check or `squad_doctor` for the full diagnostic — stale claims, ghost agents, orphan touches, broken refs, DB integrity. Common failure modes and recovery paths are at [docs/troubleshooting.md](docs/troubleshooting.md).

**Without Claude Code.** Squad ships a full CLI for scripting, CI, and power-user use. `squad init`, `squad go`, `squad attest`, `squad doctor`, every chat verb. Install the binary on its own:

```bash
go install github.com/zsiec/squad/cmd/squad@latest
```

The complete command reference is at [docs/reference/commands.md](docs/reference/commands.md).

**Comparing to other tools.** Squad's nearest neighbor is Claude Code's own experimental [agent-teams](https://code.claude.com/docs/en/agent-teams), which is great for ephemeral single-session coordination. Squad is for work that outlives the session — multiple days, multiple machines, durable history. The full decision matrix is at [docs/concepts/squad-vs-agent-teams.md](docs/concepts/squad-vs-agent-teams.md).

## Full documentation

[docs/README.md](docs/README.md) is the entry point — concepts, references, recipes, troubleshooting, contributing — all linked from there.

## License

MIT — see [LICENSE](LICENSE).
