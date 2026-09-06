# Commands

Every subcommand the `squad` binary ships, by group. Each entry shows the synopsis (auto-generated from `squad <verb> --help`), one-line description, and an example.

> **Using Claude Code?** Almost every command listed here is also exposed as an MCP tool with the same name (`squad_<verb>`). Describe what you want — *"claim FEAT-001"*, *"mark this done"* — and Claude calls the tool for you. The CLI form below is the canonical reference and what Claude calls for you behind the scenes; you only need to type these yourself for scripts, CI, or when you'd rather work in a shell. See the **MCP** section at the bottom for the tool surface.

To regenerate this page from the binary itself: `squad <verb> --help`.

## Identity

### `squad register`

Register this agent in the squad global database. Zero-arg form derives a session-stable id and display name from your terminal session — no flags needed for the common case. Pass `--as <id> --name "..."` only when you want explicit overrides (scripted setups, log-readable names).

```bash
squad register                                # session-derived id and name
squad register --as agent-blue --name "Alice" # explicit override
```

Idempotent; re-running updates the display name and bumps `last_tick_at`. Most agents reach this command via `squad go`, which calls register internally before claiming.

### `squad whoami`

Print the agent id this session resolves to.

```bash
squad whoami
```

### `squad who`

List registered agents with status, current claim, and last tick.

```bash
squad who
```

## Items

### `squad new`

Create a new item file under `.squad/items/`. New items default to `status: captured` — filed in the inbox, not yet eligible for `squad next`. Pass `--ready` to file straight to `status: open` if the body already passes the [Definition of Ready](../concepts/intake.md#definition-of-ready); the command refuses with an error if it doesn't.

```bash
squad new feat "add the export button"
squad new bug "race in the cache flusher"
squad new feat "kafka migration" --priority P0 --area infra --estimate 4h --risk high
squad new chore "rotate keys" --ready                # skip the inbox; must pass DoR
```

Flags (each falls back to `defaults.<key>` in `.squad/config.yaml`, finally to a built-in default):
- `--priority P0|P1|P2|P3` (default: P2)
- `--area <tag>` (default: `<fill-in>`)
- `--estimate 30m|1h|4h|1d` (default: 1h)
- `--risk low|medium|high` (default: low)
- `--ready` (default: off; file as `open` instead of `captured`)

### `squad inbox`

List captured items — items filed but not yet promoted to `open`. Captured items don't appear in `squad next` and can't be claimed.

```bash
squad inbox                       # everything captured in this repo
squad inbox --mine                # only items you captured
squad inbox --ready-only          # only items that already pass DoR
squad inbox --rejected            # log of rejected items (separate flow)
```

Each line shows the id, kind, title, age, and DoR status (which rules pass / fail).

### `squad accept`

Promote a captured item to `open`. Runs the [Definition of Ready](../concepts/intake.md#definition-of-ready) check; refuses with the violations listed if the item isn't ready. Accepts multiple ids in one call.

```bash
squad accept FEAT-001
squad accept FEAT-002 BUG-007 TASK-014
```

On success, the frontmatter is rewritten to `status: open`, `accepted_by` and `accepted_at` are set, and the item shows up in `squad next`.

### `squad reject`

Permanently drop a captured item. Deletes the file and appends a row to `.squad/inbox/rejected.log` with the id, title, reason, agent, and timestamp. The `--reason` flag is required — there is no anonymous reject.

```bash
squad reject BUG-001 --reason "duplicate of BUG-007"
squad reject FEAT-009 TASK-022 --reason "merged into FEAT-014"
```

There is no un-reject. To re-file rejected content, use `squad new`.

For sharpening a captured item, use the dashboard's "Send for refinement" button (range-select passages → inline comments → claude redrafts) or the MCP `squad_auto_refine_apply` tool.

### `squad intake`

Substrate for the structured-interview flow that the `/squad:squad-intake` slash command drives. Most users won't invoke these directly — they're documented for scripting and debugging.

```bash
squad intake new <idea...>                        # open a new-mode session
squad intake refine <item-id>                     # open a refine-mode session against an existing item
squad intake list                                  # list open sessions
squad intake status <session-id>                   # show transcript and remaining required fields
squad intake commit <session-id>                   # commit a drafted bundle (file the items)
squad intake cancel <session-id>                   # abandon a session
```

Sessions are durable across restarts; running `intake new` or `intake refine` with an existing pending session resumes it. The bundle written by `intake commit` is structurally validated — incomplete bundles are rejected loudly with the missing fields named.

### `squad ready`

Inspect Definition of Ready status without changing state. Useful in edit loops while you flesh out a captured item.

```bash
squad ready --check FEAT-001               # one item
squad ready --check FEAT-001 BUG-007       # several
squad ready --check --strict               # exit non-zero on any violation (CI use)
```

Exit 0 with no violations; exit 0 with violations printed unless `--strict`.

### `squad next`

List ready items in priority order. Excludes items already claimed unless `--include-claimed` is passed.

```bash
squad next                    # default top of stack
squad next --limit 10         # show more
```

An item is "ready" when its `blocked-by:` list is empty (or all references are done) and no one currently holds the claim.

### `squad status`

Show claimed / ready / blocked / done counts for this repo.

```bash
squad status
```

### `squad dump-status`

Emit `STATUS.md` from current DB and items state.

```bash
squad dump-status > STATUS.md
```

## Specs and epics

### `squad analyze`

Decompose an epic's items into parallel streams. Reads the epic frontmatter, walks the items that reference it, and writes `.squad/epics/<epic>-analysis.md` containing the stream list, file globs, dependency edges, and a parallelism factor.

```bash
squad analyze auth-rework
```

Prints the absolute path to the analysis file on success. Decomposition is deterministic: same inputs produce the same streams.

### `squad epic-new`

Create an epic scaffold under `.squad/epics/<name>.md` with `spec`, `status`, and `parallelism` frontmatter. The `--spec` flag is required and must reference an existing spec slug; the command fails if the spec file does not exist.

```bash
squad epic-new auth-rework --spec auth
```

Prints the absolute path to the created file on success. Names must be kebab-case.

### `squad spec-new`

Create a spec scaffold under `.squad/specs/<name>.md` with `title`, `motivation`, `acceptance`, `non_goals`, and `integration` frontmatter ready to fill in.

```bash
squad spec-new auth "rebuild authentication around OIDC"
```

Prints the absolute path to the created file on success. Names must be kebab-case; titles are free-form.

### `squad decompose`

Draft a set of captured items from a spec. Reads `.squad/specs/<spec>.md`, asks the model to suggest a parallel decomposition, and writes one captured item per chunk under `.squad/items/` with `parent_spec: <spec>` set in frontmatter. Triage the drafts via `squad inbox --parent-spec=<spec>`.

```bash
squad decompose auth-rework
squad decompose auth-rework --print-prompt    # echo the prompt for debugging
```

Drafts are captured, not open — each one runs through the [Definition of Ready](../concepts/intake.md#definition-of-ready) on `squad accept`. The slash-command equivalent is `/squad:squad-decompose <spec>`.

See [recipes/decomposition.md](../recipes/decomposition.md) for the full workflow.

## Onboarding

### `squad go`

Onboard or resume in one step: init the workspace if `.squad/` is absent, register the agent if not already registered, find the top ready item, claim it, print its AC, and flush the mailbox. Idempotent — running twice does not claim two items; resumes the existing claim instead.

```bash
squad go
```

No flags — the command takes no arguments. The recommended single entry point for both first-time and resume cases. Equivalent to `init --yes` + `register` + `next` + `claim` + `tail` chained, but with the resume semantics built in.

### `squad claim`

Atomically claim an item.

```bash
squad claim FEAT-001 --intent "wire the export button to the API"
```

Exit non-zero if another agent already holds it. Add `--touches path1,path2` (comma-separated) to declare files you'll be editing so peers see the overlap. Use `--long` to apply the 2h long-running threshold instead of `hygiene.stale_claim_minutes`.

When you need that exact item instead of choosing different work, use
`--wait`. Squad registers an item-scoped loopback listener and keeps the retry
loop inside the CLI process, so the calling agent does not need to wake the
model and poll status repeatedly:

```bash
squad claim ENV-001 --intent "deploy the verified release" --long --wait
```

`--wait-timeout` defaults to 2h. `--wait-fallback` defaults to 30s and only
recovers a missed notification inside the CLI; unchanged fallback checks emit
no output. A normal `squad release` wakes waiters immediately. Acquisition is
still an atomic claim, so simultaneous waiters produce exactly one winner.

### `squad release`

Release your claim on an item.

```bash
squad release FEAT-001
```

### `squad done`

Run the `verification.pre_commit` gate from `.squad/config.yaml`, then release the claim, rewrite frontmatter to `status: done`, and move the file to `.squad/done/`. Pass `--skip-verify` to override the gate locally.

```bash
squad done FEAT-001 --summary "shipped, all tests green"
squad done FEAT-001 --skip-verify --summary "trivial doc fix"
```

### `squad blocked`

Mark item blocked: release claim + status: blocked + ensure `## Blocker` section exists.

```bash
squad blocked FEAT-001 --reason "waiting on auth.proto from API team"
```

### `squad reassign`

Transfer your claim by releasing it and @-mentioning the new owner. The new owner still has to run `squad claim FEAT-001` themselves — reassign is consent-based.

```bash
squad reassign FEAT-001 --to agent-bob
```

### `squad force-release`

Admin: forcibly release someone else's ordinary stuck claim. Requires `--reason` for the audit trail. Do not use it for `ENV-*`; use `squad recover` so ownership is transferred without a free-lock gap.

```bash
squad force-release BUG-042 --reason "agent-blue offline >2h, no response"
```

### `squad recover`

Atomically take over a protected `ENV-*` claim after independently verifying
that its Codex holder task is stopped. The expected holder is a compare-and-swap
guard; task/session and external-state evidence are retained in the recovery
audit. A recent registered holder heartbeat causes the command to refuse.

```bash
squad recover ENV-001 \
  --from agent-blue \
  --holder-session 0199abcd \
  --reason "task stopped after rollout" \
  --evidence "task stopped; Actions terminal; exact staging SHA ready" \
  --confirm-holder-stopped \
  --json
```

### `squad dispatch`

Maintain durable, generation-fenced reservations around Worker task creation.
Reservations prevent duplicate periodic dispatch but never replace the Worker's
normal Issue claim.

```bash
squad dispatch reserve STUDIO-501 \
  --source github:TomasBack2Future/voice-agent-studio#501 \
  --ttl 15m --json
squad dispatch bind STUDIO-501 --generation 1 --thread-id 0199abcd
squad dispatch list --active --json
squad dispatch close STUDIO-501 --generation 1 --state completed \
  --note "issue closed after staging acceptance"
```

### `squad handoff`

Post a handoff brief and release any held claims. The brief is structured: each `--shipped/--in-flight/--surprised-by/--unblocks` is repeatable.

```bash
squad handoff \
  --shipped FEAT-001 \
  --in-flight FEAT-002 \
  --note "EOD wrap-up; back tomorrow"
```

Or pipe the note from stdin: `git log --oneline | squad handoff --stdin --shipped FEAT-001`.

#### `--propose-from-surprises`

Auto-draft one gotcha-kind learning proposal per surprise so end-of-claim findings don't die in chat. Two modes:

- With explicit `--surprised-by` flags: one proposal per supplied string.
- Without `--surprised-by`: mines this agent's held-claim chat history for every `stuck` post and every `fyi` post whose body contains a surprise keyword (`surprise`/`surprised`/`didn't expect`/`turns out`/`wait`). Near-duplicates are deduped by lowercase substring containment.

Each proposal lands under `.squad/learnings/gotchas/proposed/`. Title is the first 80 chars of the surprise body; slug is derived via the same kebab-case helper as `squad learning quick`; area is inferred from the held claim's frontmatter `area`. The `## Looks like` section is pre-filled with the raw surprise body verbatim.

```bash
squad handoff --note "EOD wrap" --propose-from-surprises
squad handoff --surprised-by "modernc returns nil for empty BLOBs" --propose-from-surprises
squad handoff --propose-from-surprises --dry-run    # preview, write nothing
squad handoff --propose-from-surprises --max 3      # cap at 3, warn if more
```

The handoff itself proceeds even when zero candidates are found — `no surprises to propose from` prints to stderr and the command exits 0.

## Chat

### `squad tick`

Show new messages since last tick and advance the read cursor. Diagnostic-only in normal operation — chat is delivered continuously via the `Stop` listen + post-tool-flush + user-prompt-tick hooks. Reach for `squad tick` when you suspect a hook miss or want to advance the cursor explicitly.

```bash
squad tick
```

### `squad thinking` / `milestone` / `stuck` / `fyi`

Typed verbs. All post to your active claim's thread by default; `--to <thread>` overrides (`--to global` for the team-wide channel, `--to FEAT-001` for a specific item thread).

```bash
squad thinking "leaning toward suspending the producer rather than throttling"
squad milestone "AC 1 green, moving to AC 2"
squad stuck "cannot reproduce locally — seeing fresh patterns?"
squad fyi "touching shared.go in a way that will conflict with mid-pool work"
```

### `squad ask`

Ask a question of a specific agent.

```bash
squad ask @agent-blue "did the deep-copy change merge yet?"
```

### `squad say`

Plain chat — escape hatch when no verb fits.

```bash
squad say "lunch break, back at 14:00"
```

### `squad review-request`

Request review on an item, optionally mentioning a reviewer.

```bash
squad review-request FEAT-001 --mention agent-bob
```

### `squad progress`

Report progress on a held item. The percentage is positional (0–100); the note is optional. Only the agent currently holding the claim can report progress on it.

```bash
squad progress FEAT-001 60 --note "AC 2 of 4 done"
```

The report is written to both the `progress` table (source of truth for "latest pct") and the `messages` table (so it shows up in `squad tail` and `squad history`).

### `squad tail`

Print recent messages, optionally streaming new ones.

```bash
squad tail                     # messages from the last 30 minutes
squad tail --since 1h          # last hour
squad tail --follow            # stream
squad tail --thread global     # only the global channel
```

### `squad history`

Print all messages for an item, in time order.

```bash
squad history FEAT-001
```

### `squad standup`

Per-agent digest over a lookback window: what shipped, what was lost (reclaimed or force-released), the currently-open claim (with age + intent), stuck signals you posted, and the file touches you still hold. Defaults to a 24-hour window.

```bash
squad standup                   # last 24h, human-readable
squad standup --since 1w        # custom window
squad standup --json            # machine-readable for scripting / dashboards
```

The digest is read-only; it doesn't mutate the DB.

### `squad archive`

Roll old chat messages into a per-month archive DB.

```bash
squad archive --before 30d         # keep last 30 days; older messages roll into ~/.squad/archive/
```

## File touches

### `squad touch`

Declare files you are editing on a specific item so peers see the overlap.

```bash
squad touch FEAT-001 internal/cache/flusher.go internal/cache/flusher_test.go
```

Signature: `squad touch <ITEM-ID> <path>...`. The first argument is the item ID the touches belong to (usually your active claim); the rest are paths.

### `squad untouch`

Release file touches; no paths releases all touches for this agent.

```bash
squad untouch                                      # release all
squad untouch internal/cache/flusher.go            # release one
```

### `squad touches list-others`

List active file touches held by agents OTHER than the current one. Used by the `pre-edit-touch-check` hook to warn before edits collide.

```bash
squad touches list-others
squad touches list-others --json
```

## Evidence

### `squad attest`

Record a verification artifact (test/lint/build/typecheck/manual/review) into the evidence ledger. Items with `evidence_required: [...]` in their frontmatter need an attestation per kind before `squad done` will close them out (without `--force`).

The item id is taken either as a positional argument (matches every other claim verb's convention) or via `--item`. Both forms work; passing both with conflicting values is an error.

```bash
# Test, lint, typecheck, build — squad runs the command and stores stdout+exit.
squad attest FEAT-001 --kind test     --command "go test ./..."
squad attest FEAT-001 --kind lint     --command "golangci-lint run"
squad attest FEAT-001 --kind build    --command "go build ./..."

# Review — write findings to a file first; squad reads, hashes, and stores it.
squad attest FEAT-001 --kind review \
    --reviewer-agent agent-helper \
    --findings-file /tmp/review.md

# Manual — record an out-of-band verification (compliance sign-off etc).
squad attest FEAT-001 --kind manual --command "compliance review e-mailed 2026-04-26"

# --item form (still supported; useful when scripting):
squad attest --item FEAT-001 --kind test --command "go test ./..."
```

| Flag | Required when | Meaning |
|---|---|---|
| (positional) or `--item` | always | Item id; the attestation is scoped to one item. |
| `--kind <kind>` | always | One of `test`, `lint`, `typecheck`, `build`, `review`, `manual`. |
| `--command <cmd>` | always except `kind=review` | Shell command to run; squad captures stdout and exit code into the ledger. |
| `--findings-file <path>` | `kind=review` | File whose body becomes the review record. |
| `--reviewer-agent <id>` | `kind=review` | The agent id of the reviewer. |

The attestation is stored under `.squad/attestations/<hash>.txt` (committed) and indexed in `~/.squad/global.db` (operational). Records are deduplicated on `(repo_id, item_id, kind, output_hash)` — re-running an identical attestation is a no-op.

## Learning

R5 learning artifacts live under `.squad/learnings/{gotchas,patterns,dead-ends}/{proposed,approved,rejected}/`. `squad learning` is the management surface; the underlying files are plain markdown so peers can review them via PR.

### `squad learning propose`

Stub a new learning artifact under `proposed/` with the kind-specific section headers. The command prints the file path; edit the stub to fill in the body. Learning kinds are `gotcha` (this looked like X but is Y), `pattern` (this works), `dead-end` (we tried X, it didn't work because Y).

```bash
squad learning propose gotcha retry-on-503 --area "http-retries" --title "retry on 503"
squad learning propose dead-end dont-mock-the-db --area "db" --title "do not mock the db"
```

### `squad learning quick`

Frictionless one-line capture. Auto-derives the slug from the one-liner, defaults `--kind` to `gotcha`, and infers `--area` from the most recently closed item in this repo (falls back to `general`). Use this when the surprise is fresh and the ceremony of `propose` would let it slip; edit the stub later.

```bash
squad learning quick "interface{} in claims store breaks Go 1.25"
squad learning quick "use channel-of-done to fan out workers" --kind pattern
```

| Default | Source |
|---|---|
| `kind` | `gotcha` (override with `--kind {gotcha,pattern,dead-end}`) |
| `slug` | derived from the one-liner (lowercase, kebab, max 60 chars) |
| `title` | the one-liner verbatim |
| `area` | most-recently-modified item under `.squad/done/`, else `general` |
| `paths` | `internal/<area>/**` (same default as `propose`) |

If the derived slug already exists, `quick` walks `slug-2`, `slug-3`, … through `slug-9` before giving up — a one-liner that collides nine times is too generic. The stub body carries a `> captured via squad learning quick` marker so a reviewer can spot proposals that still have placeholder sections to fill in. Suppress the follow-up reminder with `SQUAD_NO_CADENCE_NUDGES=1`.

### `squad learning list`

List learning artifacts. Filter by area, state, or kind.

```bash
squad learning list                          # all states, all kinds
squad learning list --state proposed
squad learning list --kind dead-end --area auth
```

### `squad learning approve`

Promote a proposed learning to `approved/`. The slug is the directory name under `proposed/`.

```bash
squad learning approve retry-on-503
```

### `squad learning reject`

Archive a proposed learning under `rejected/` (preserved for audit; the proposal directory and body are kept).

```bash
squad learning reject not-a-real-pattern --reason "duplicates retry-on-503"
```

### `squad learning agents-md-suggest`

Propose a unified-diff change to `AGENTS.md` based on an approved learning. The diff goes into `.squad/learnings/agents-md/proposed/` for human review.

```bash
squad learning agents-md-suggest --learning retry-on-503
```

### `squad learning agents-md-approve`

Apply a proposed `AGENTS.md` diff via `git apply`; on success archive the proposal.

```bash
squad learning agents-md-approve <id>
```

### `squad learning agents-md-reject`

Archive a proposed `AGENTS.md` change under `rejected/` without applying.

```bash
squad learning agents-md-reject <id> --reason "policy already covers this"
```

### `squad learning triviality-check`

Internal helper consumed by the `stop-learning-prompt` hook: reads `git diff --numstat` from stdin and prints `trivial` or `non-trivial`. Not normally invoked by hand.

## Hygiene

### `squad doctor`

Diagnose stale claims, ghost agents, orphan touches, broken refs, and DB integrity.

```bash
squad doctor                  # report findings; exit 0 either way
squad doctor --strict         # exit non-zero when findings exist (CI use case)
```

The default exit code is 0 even with findings — the output is diagnostic, meant for the user to read and act on. Pass `--strict` only in CI where you want a failing build on any finding.

## Statistics

### `squad stats`

Operational statistics for the current repo: verification rate, claim p99 latency, WIP-violation count, reviewer disagreement rate, and a daily series for each. Pure read-side aggregation over the existing tables; no new state.

```bash
squad stats                              # human summary
squad stats --json                       # full pretty-printed snapshot
squad stats --tail --interval 5s         # NDJSON stream until SIGINT
squad stats --window 1h                  # last hour only (default 24h)
squad stats --window 0                   # unbounded
```

The same data is exposed at `/api/stats` (when `squad serve` is running) and `/metrics` for Prometheus scrape — see [recipes/prometheus.md](../recipes/prometheus.md).

## Multi-repo

### `squad workspace status`

Per-repo summary table across every repo registered in the global DB.

```bash
squad workspace status
squad workspace status --repo current
squad workspace status --repo id1,id2,id3
```

### `squad workspace next`

Top P0/P1 ready items across every repo (lower-priority items aren't shown — drill into a single repo with `squad next` for those).

```bash
squad workspace next --limit 10
```

### `squad workspace who`

Every registered agent across every repo, with current claim and last tick.

```bash
squad workspace who
```

### `squad workspace list`

Every known repo with origin URL and last-seen-at.

```bash
squad workspace list
```

### `squad workspace forget`

Remove a repo from the global DB (after deleting it locally).

```bash
squad workspace forget <repo_id>
```

## GitHub integration

`squad pr` is the parent for the GitHub-pull-request integration; the leaf commands below are the verbs you'll usually invoke.

### `squad pr-link`

Record a pending PR ↔ item mapping (run before `gh pr create`). Prints the hidden HTML marker to embed in the PR body.

```bash
squad pr-link FEAT-001
squad pr-link --write-to-clipboard FEAT-001
squad pr-link --pr 42 FEAT-001                     # append to existing PR via gh pr edit
```

### `squad pr-close`

Archive the squad item linked to a merged PR. CI-only — invoked by the auto-archive workflow.

```bash
squad pr-close 42
squad pr-close 42 --repo-id "owner/repo"
```

## Plugin and hooks

### `squad install-plugin`

Install the squad Claude Code plugin to `~/.claude/plugins/squad/`.

```bash
squad install-plugin
squad install-plugin --uninstall
```

### `squad install-hooks`

Install or update squad's Claude Code hooks in `~/.claude/settings.json`.

```bash
squad install-hooks                               # interactive
squad install-hooks --yes                         # accept defaults (six hooks ON)
squad install-hooks --yes --pre-commit-pm-traces=on    # tune individually
squad install-hooks --status                      # what is installed
squad install-hooks --uninstall                   # remove all squad-managed entries
```

Default-ON hooks (`--yes`): `session-start`, `user-prompt-tick`, `pre-compact`, `stop-listen`, `post-tool-flush`, `session-end-cleanup`. Default-off (opt-in): `async-rewake`, `pre-commit-pm-traces`, `pre-edit-touch-check`, `stop-learning-prompt`, `loop-pre-bash-tick`. See [hooks.md](hooks.md) for the full list and what each does.

## Real-time transport

Three commands together implement squad's chat real-time delivery. Normal users don't invoke them directly — the hooks installed by `squad install-hooks` do — but understanding what they do helps when something looks wrong.

### `squad listen`

Block until a peer message wakes this session; emit a Claude Code decision-block JSON envelope on wake. Bound by the `stop-listen` hook to a loopback TCP listener.

```bash
squad listen --instance my-session --bind 127.0.0.1:0 --max 24h
```

| Flag | Default | Meaning |
|---|---|---|
| `--instance <id>` | derived from env | Stable session identifier; the row in `notify_endpoints` is keyed on this. |
| `--bind <addr>` | `127.0.0.1:0` | Bind address for the loopback listener. Must be loopback. |
| `--fallback <dur>` | `30s` | Fallback re-check interval when no wake arrives. |
| `--max <dur>` | `24h` | Hard exit after this duration with no wake. |

### `squad mailbox`

Print pending mailbox as a Claude Code hook envelope; exit 0 if empty. Used by the `user-prompt-tick`, `post-tool-flush`, and `pre-compact` hooks to inject pending peer chat as `additionalContext`.

```bash
squad mailbox                         # default: additional-context format, UserPromptSubmit event
squad mailbox --event PreCompact
squad mailbox --format text           # plain text instead of JSON envelope
```

### `squad notify-cleanup`

Drop `notify_endpoints` rows for a given instance. Called by the `session-end-cleanup` hook so peer senders stop dialing a dead listener port.

```bash
squad notify-cleanup --instance my-session
```

## MCP

### `squad mcp`

Run an MCP server over stdio (Claude Code transport). Registered in `~/.claude/plugins/squad/.mcp.json` by `squad install-plugin`; not normally invoked by hand.

```bash
squad mcp
```

Exposes the full agent-driving surface to Claude Code so MCP-only callers don't have to shell out:

- **Lifecycle:** `squad_register`, `squad_whoami`, `squad_next`, `squad_claim`, `squad_release`, `squad_done`, `squad_blocked`, `squad_progress`, `squad_review_request`.
- **Chat:** `squad_say`, `squad_ask`, `squad_handoff`, `squad_tick`.
- **Coordination:** `squad_force_release`, `squad_reassign`, `squad_archive`.
- **Inspection:** `squad_get_item`, `squad_list_items`, `squad_status`, `squad_who`, `squad_history`.
- **Touch tracking:** `squad_touch`, `squad_untouch`, `squad_touches_list_others`.
- **Evidence:** `squad_attest`, `squad_attestations`.
- **Learning:** `squad_learning_propose`, `squad_learning_list`, `squad_learning_approve`, `squad_learning_reject`, `squad_learning_agents_md_suggest`, `squad_learning_agents_md_approve`, `squad_learning_agents_md_reject`.
- **PR integration:** `squad_pr_link`, `squad_pr_close`.

See `~/.claude/plugins/squad/.mcp.json` for the registered command line.

## Server

### `squad serve`

Start the squad dashboard (HTTP + SSE).

```bash
squad serve --port 7777 --bind 127.0.0.1
```

## Scaffold

### `squad init`

Scaffold a squad workspace in the current repository: writes `.squad/`, `AGENTS.md`, and a CLAUDE.md managed block. Asks ≤3 questions.

```bash
squad init
squad init --yes                  # accept all defaults
```

## Diagnostics

### `squad version`

Print the squad version.

```bash
squad version
```
