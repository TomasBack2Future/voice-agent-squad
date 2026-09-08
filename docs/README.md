# squad documentation

## What squad is

Squad is a project-management framework for software work done with AI coding agents. It encodes an operating loop — atomic claims, typed chat verbs, file-touch tracking, hygiene sweeps, web dashboard, and an optional Claude Code plugin — into a single static binary. One binary works for solo and multi-agent setups; multi-agent is a configuration, not a separate product.

## Why

AI agents meander without structure: half-finished branches, redundant work, opaque "I think it's done." Existing PM tools optimize for humans typing in web forms, which is the wrong shape for sessions that come and go. Squad pushes the doctrine into enforceable CLI patterns — the agent files claims, ticks for chat, posts evidence at done — and stays out of the way of solo work.

> **Not sure squad is the right tool?** If your work is one session on one machine and you'll walk away when it's done, [Claude Code's experimental agent-teams](https://code.claude.com/docs/en/agent-teams) is a lighter fit. Squad is the right choice when the work outlives the session — multiple days, multiple machines, or a durable record of who did what. See [concepts/squad-vs-agent-teams.md](concepts/squad-vs-agent-teams.md) for the full decision matrix.

## 5-minute quickstart

The Claude Code path (one shot):

```bash
claude install github.com/zsiec/squad
cd ~/dev/your-project
squad go    # init, register, claim top ready item, print AC, flush chat
```

The binary-first path (CLI-only / scripting / CI):

```bash
go install github.com/zsiec/squad/cmd/squad@latest
squad install-plugin                                  # optional, but recommended for Claude Code users
cd ~/dev/your-project
squad go
```

Both paths converge. `squad go` is idempotent — first run inits `.squad/` and registers a session-derived agent id; re-runs resume the same claim and re-flush the mailbox. Total time: under five minutes from install to first `done`.

## What to read next

- **New?** Walk through [adopting.md](adopting.md) end-to-end.
- **Pick a recipe** for your starting situation: [recipes/](recipes/).
- **Doctrine** behind the loop: [concepts/](concepts/).
- **Lookup** for commands, config, hooks, skills, slash commands, and the DB schema: [reference/](reference/).
- **Review a design proposal?** Start with
  [Grok as a required, read-only PR reviewer](proposals/grok-required-review-gate.md).
- **Snag?** [troubleshooting.md](troubleshooting.md).
- **Contribute?** [contributing.md](contributing.md).
- **Comparing to agent-teams?** [concepts/squad-vs-agent-teams.md](concepts/squad-vs-agent-teams.md).
- **Migrating from agent-teams?** [recipes/migrating-from-agent-teams.md](recipes/migrating-from-agent-teams.md).
