# Contributing

For this fork, first read [the working contract](../CLAUDE.md) and
[Studio integration](studio-agent-loop.md). The generic examples below are
opt-in adoption instructions; Studio uses its configured wrapper/shared ledger,
Codex-managed worktrees, actual branch policy and independent PR lifecycle.
Do not run `squad go`, install hooks/binaries, auto-fold to main, or create extra
reviewer agents merely because an upstream example below does so.

Squad uses squad. If you contribute, you'll work the same loop the docs describe.

For day-to-day item work in this repo (claim, chat, done, attest), drive squad through Claude Code the same way users do — *"claim BUG-042 and start"*, *"mark this done with summary X"*. The CLI commands below are useful when you want to test the local build of the binary or script something; otherwise let Claude call the MCP tools.

## Setup (one-time)

```bash
git clone https://github.com/zsiec/squad
cd squad
go build ./cmd/squad                                # confirms the build works
go test -race ./...                                 # confirms tests pass

# Scaffold .squad/ if this clone doesn't have one yet, then onboard.
# In Claude Code: ask "initialize squad here and claim the top ready item".
# In a terminal:
./squad go                                          # idempotent: init + register + claim + AC + mailbox
```

Optional: install the plugin and hooks against this repo (and yes, you can dogfood squad on squad).

```bash
./squad install-plugin
./squad install-hooks --yes
```

## Pick something to work on

In Claude Code: *"What's ready to work on in this repo?"* (Claude calls `squad_next`.) Or file a new item: *"File a bug for the race in the cache flusher."* (Claude calls `squad_new bug "..."`.)

The terminal equivalents:

```bash
./squad next                            # top of the ready stack
./squad workspace next                  # cross-repo if you've got more
ls .squad/items/                        # browse manually
./squad new bug "race in the cache flusher"
./squad new feat "expose --json on whoami"
./squad new chore "ItemPath helper missing for cmd/squad/done.go"
# (DEBT, INFRA, etc. work too — add the prefix to .squad/config.yaml first.)
```

## Work the loop

In Claude Code: *"Claim BUG-042 with intent 'stop the leak; add regression test'."* Then do the work — Claude can also call `squad_milestone`, `squad_review_request`, `squad_attest`, and `squad_done` for you as you progress. The full loop in CLI form:

```bash
./squad claim BUG-042 --intent "stop the leak; add regression test"
# ... read AC end-to-end ...
# ... if AC names a concrete failure, write the RED test FIRST against current code ...
# ... minimum impl ...
go test -race ./...
golangci-lint run                       # CI runs this; you should too locally

./squad milestone "AC 1 green"
./squad review-request BUG-042
# ... spawn superpowers:code-reviewer with the diff ...
# ... address findings ...
./squad done BUG-042 --summary "leak fixed; regression test green"

git add .squad/ <the actual files>
git commit -m "fix: stop the cache leak"
```

## Conventions

- **Commits:** prefixes `feat:`, `fix:`, `test:`, `docs:`, `perf:`, `refactor:`, `chore:`. Subject ≤72 chars, lowercase after the prefix, imperative mood. Body explains WHY. **No `Co-Authored-By` lines.**
- **No PM-trace IDs in code.** Item IDs go in filenames under `.squad/items/`, not in identifiers, comments, or commit messages.
- **Comments:** default to none. Only keep a comment if removing it would confuse a future reader.
- **No future-work TODO comments.** File an item instead.
- **Pure Go, no CGO.** `CGO_ENABLED=0` for every supported os/arch.
- **TDD by default.** Failing test → minimal impl → passing test → commit. Skip only for pure refactors with full coverage.

The full convention list lives in [the repo's CLAUDE.md](../CLAUDE.md).

## Submit a PR

```bash
git checkout -b fix-cache-leak           # branch name doesn't carry the item ID
git push -u origin fix-cache-leak

# Embed the squad-item marker in the PR body so the auto-archive workflow runs:
./squad pr-link BUG-042 > /tmp/marker.txt
gh pr create \
  --title "fix: stop the cache leak" \
  --body "$(cat <<EOF
## Summary

Replaced the broken handler with a less-broken handler.

## Test plan

- [x] go test -race ./...
- [x] golangci-lint run

$(cat /tmp/marker.txt)
EOF
)"
```

CI runs on push: `go test -race ./...`, `go vet`, `golangci-lint`, build for all four os/arch combos. Get a green run before requesting review.

When the PR merges, the [auto-archive workflow](recipes/github-actions.md) reads the marker and moves the item to `.squad/done/` with a generic commit message — so the merge commit on main stays free of PM IDs.

## Releases

`main` is always shippable. Maintainer cuts a `vX.Y.Z` tag; goreleaser builds and publishes binaries. Homebrew tap PR is auto-opened.

## Scope

- **v1**: the binary, plugin, hooks, multi-repo, GitHub Actions integration, evidence ledger, statistics, agent-teams interop docs, full reference docs.
- **v1.1**: more hooks, web UI auth, additional importers.
- **v2**: cross-machine global.db sync, REST API, more integrations.

The current state of v1 lives in `.squad/items/` of this repo.

## The no-telemetry pledge

Squad will never phone home. No usage stats, no crash reports, no "anonymous" anything. Telemetry PRs are closed without merge. Adoption is measured externally (stars, Homebrew counts) — not by spying on users.

## Code of conduct

Be kind. Disagree on code, not on people. Maintainers reserve the right to remove anyone making it less fun.
