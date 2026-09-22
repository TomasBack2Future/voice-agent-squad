---
name: cmux-sessions
description: Launch, observe and narrowly control cmux terminal or agent sessions with the cmux CLI. Use for Dispatcher-to-Worker session creation, explicit cross-session routing, bounded screen reads, or a user-authorized session correction; do not use Computer Use when socket automation is available.
---

# cmux sessions

Use cmux as live-session transport. Squad remains authoritative for work
ownership, reservations, dependencies and environment locks.

## Preflight

Use the bundled binary when `cmux` is not on `PATH`:

```sh
CMUX_BIN=/Applications/cmux.app/Contents/Resources/bin/cmux
"$CMUX_BIN" ping
"$CMUX_BIN" capabilities --json
"$CMUX_BIN" identify --json
"$CMUX_BIN" tree --all --json
```

An external Dispatcher may mutate sessions only when capabilities reports
`access_mode: automation`. `cmuxOnly` means the process is outside the trusted
socket boundary; do not bypass it. Use Computer Use only when the user requests
it or the CLI capability is genuinely unavailable.

## Runtime permission mode and resume

Record and honor the user's selected runtime permission mode in the launcher.
For Claude, explicit user authorization for YOLO selects
`--permission-mode bypassPermissions`; `auto` can still deny commands and
`--allow-dangerously-skip-permissions` only enables an option. Verify the
installed CLI contract and then the actual process arguments/status bar.
Do not enable bypass implicitly or in response to a denial without user
approval. This setting does not expand task scope, WIP or ENV authority, and a
Dispatcher's permission mode does not automatically propagate to its Workers.
Pass each newly launched session's authorized mode explicitly.

When asked to change a running session's mode, inspect live work first. Preserve
its native session id, claims, external-operation state and assignment. Stop
the local client as authorized and resume the same id (Claude: `--resume ID
--permission-mode MODE`); do not send the original assignment again or create a
replacement reservation. Verify the old process exited before resuming and
check the effective mode afterward. An external rollout continues independently
of stopping the client and must remain supervised on resume.

## Create and bind one Worker

Run the selected package's `worker_preflight.py` before session creation, using
the actual client's skill entries and required executable names. Resolve missing
capabilities or stale assignment identity before paying for a Worker startup.
This local receipt does not replace Squad ownership or runtime approval checks.

Create a new workspace with an owned worktree and a reviewed launcher:

```sh
"$CMUX_BIN" new-workspace \
  --name '[#981][STUDIO-084] short title' \
  --cwd /absolute/owned/worktree \
  --command /absolute/reviewed/launcher.sh \
  --focus false
```

Read the returned identifiers or resolve them once with `tree --all --json`.
Persist the workspace UUID, surface UUID and native agent session ID with the
matching dispatch reservation. The Worker title prefix and assignment identity
must agree. Never reuse another Issue's workspace or native session.

For terminal notifications, record a runtime-tagged callback route at launch:
cmux workspace UUID, surface UUID, native session id and Squad agent id, or an
actual App thread/host id. Resolve the endpoint read-only before dispatch; do
not send a dummy prompt to test it. A Claude UUID is not a Codex App thread id.
Missing transport must be recorded as pending reconciliation, not delegated to
the user to copy messages.

## Observe without disturbing

```sh
"$CMUX_BIN" sessions list --json
"$CMUX_BIN" read-screen --workspace WORKSPACE --surface SURFACE --lines 80
```

Use explicit targets and bounded output. A quiet or idle screen is not terminal
while a reservation, claim, CI/deployment operation or environment lock is live.
Do not focus, flash, move, close, kill or replace a healthy Worker for monitoring.

## Send a bounded intervention

Input is allowed only for the initial assignment, an explicit user override, an
immediate safety/scope correction, one verified dependency transition, or one
deduplicated terminal event to its explicitly assigned Dispatcher:

```sh
"$CMUX_BIN" send --workspace WORKSPACE --surface SURFACE 'message'
"$CMUX_BIN" send-key --workspace WORKSPACE --surface SURFACE enter
"$CMUX_BIN" read-screen --workspace WORKSPACE --surface SURFACE --lines 40
```

Never send routine progress questions, acknowledgements or resource-wait
reminders. Verify the title/IDs immediately before sending and read the screen
once afterward. Durable decisions and outcomes belong in Squad/checkpoints, not
only in terminal scrollback.
