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
For Claude cold starts, use the package's `claude_worker_launcher.py` with a
separate schema-valid launch config and pass `--launch-config` to preflight.
The same launcher must pass `--check` with inherited parent identity removed
before creating the workspace. Use native Squad coordination when available;
select the explicit Codex-wrapper compatibility adapter only for an installed
wrapper requiring it. Do not handwrite identity/guard loops. Only a successful
read of an exact unbound reservation is a binding wait; command errors and
invalid output fail immediately with bounded, sanitized diagnostics.

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
These cmux identifiers prove identity, not a safe callback channel. Background
terminal events must be persisted in Squad; use only a verified message API or
mailbox for wakeup. Never inject callbacks with send/send-key/paste/Enter, even
when the input appears empty. Never save, clear, restore or submit a user draft
to deliver an event. Missing safe transport means pending reconciliation, not
a delivered callback or a user message-copying chore.

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
immediate safety/scope correction, or one verified dependency transition.
These permissions do not authorize altering unrelated pending input. If a draft
or command is present or input ownership is uncertain, do not inject or submit.
Background terminal callbacks are excluded; use the durable event path above:

```sh
"$CMUX_BIN" send --workspace WORKSPACE --surface SURFACE 'message'
"$CMUX_BIN" send-key --workspace WORKSPACE --surface SURFACE enter
"$CMUX_BIN" read-screen --workspace WORKSPACE --surface SURFACE --lines 40
```

Never send routine progress questions, acknowledgements or resource-wait
reminders. Verify the title/IDs immediately before sending and read the screen
once afterward. Durable decisions and outcomes belong in Squad/checkpoints, not
only in terminal scrollback.
