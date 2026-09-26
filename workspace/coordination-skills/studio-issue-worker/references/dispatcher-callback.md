# Worker terminal callback to the originating Dispatcher

Read at assignment and immediately before the final Worker response. This is a
single event-driven notification, not a timer, webhook service or new scheduler.
It does not replace claim release notifications to resource waiters.

## Bind the recipient

The assignment must provide `dispatcher_agent_id`, reservation key/generation,
and a runtime-tagged callback route. For `codex-app`, record the actual
`dispatcher_thread_id` and `dispatcher_host_id`. For `cmux`, record the
Dispatcher workspace UUID, surface UUID and native agent session id as identity
metadata, not as proof of a safe message transport. Verify
endpoint identity read-only at launch; a native Claude session UUID must never
be passed as an App thread id. Keep these fields in the assignment's existing
launcher/coordination metadata, not unsupported envelope schema fields.

Send only to that exact origin or an explicitly rebound successor in the ledger.
Do not infer transfer from a new visible Dispatcher. Owner-only reservation
closure still belongs to its recorded owner or an authorized supported transfer;
a callback route does not confer that ownership.

For an older task without these fields, resolve its original dispatch message
and reservation's reserved_by identity to a verified task once. An agent id is
not a task id. If unresolved, record `callback-unroutable` in the item and final
answer; use existing reconciliation when configured. Do not guess a recipient,
scan raw session conversations or block an otherwise safe closure on a missing route.

## Trigger and order

Send one event after the durable outcome is recorded and cleanup/claim handling
is finished, immediately before ending the current turn:

- `issue-closed`: exact acceptance/cleanup passed, GitHub Issue is closed, Squad
  item done, all owned claims released, all owned external operations terminal.
- `handoff-complete`: batch offer/ACK/committed release/recipient receipt are
  verified, donor has no claims/external operations. Keep the child Issue open
  and its reservation/batch-owned hold active; this is not runtime acceptance.
- `blocked`: a genuinely new blocker causes the Worker to end, after recording
  exact missing prerequisite, safe revision and actual retained/released claims.
  An unresolved operation or ENV claim must be reported explicitly, never as
  safe completion. Do not release unsafe ENV solely to emit this event.

Routine CI, review, percentage progress, resource waits and repeated unchanged
blockers emit no callback. A blocked event does not release WIP or authorize a
replacement. A later distinct resolution/closure is a new event.

Use a stable event id:
`worker-terminal-v1/<reservation-key>/<generation>/<worker-thread-id>/<kind>/<outcome-ref>`.
The outcome ref is the durable closure, handoff-receipt or blocker-transition
Squad message id; reuse it when repeating the same outcome, never a timestamp
generated on each attempt.

1. Inspect the owned item's callback records for that event id. If already sent,
   do not resend. Record `callback-intent` with the event id and exact recipient.
2. Use a verified message transport once. For `codex-app`, call
   `send_message_to_thread` with the recorded thread/host and compact prompt;
   preserve model/effort. For a cmux-hosted agent, the durable Squad event is
   the delivery record. Wake the receiver only through an already configured,
   verified message API or mailbox that does not touch terminal input. cmux
   workspace/surface ids alone do not provide such a transport. Never use
   `cmux send`, `send-key`, paste or Enter for background terminal callbacks,
   even if a screen snapshot appears empty: input can change after inspection.
   Never inspect, save, clear, restore or submit a user's draft to make room.
   If no safe wakeup transport is available, record `pending-reconciliation`
   with the full sanitized payload in Squad and use the existing reconciliation
   cycle when it next runs. Do not claim a wakeup was scheduled or delivered.
3. Record `callback-sent` on confirmed transport success. On explicit failure or
   uncertain delivery record `callback-failed` or `callback-unknown`, preserve
   the payload for heartbeat reconciliation and explain it in the final answer.
   Never blind-retry an ambiguous send or create a second task/automation.
   Use the existing bounded reconciliation/heartbeat fallback when configured;
   do not invent a watcher or ask the user to ferry the event. If no wakeup
   exists, report notification pending honestly while preserving durable state.
4. Finish the user-visible final response without waiting for a Dispatcher ACK.
   A send success is transport acceptance, not proof that dispatch finished.
   Durable persistence alone is not `callback-sent`.

## Installed Squad terminal receiver

Use the installed receiver executable from coordination metadata, not a stale
PATH wrapper. After writing the sanitized outcome on the canonical item thread,
submit it with the structured command (MCP equivalent: `squad_terminal_events_publish`):

```sh
squad terminal-events publish --reservation KEY --generation N \
  --worker-session NATIVE_ID --kind issue-closed --outcome MESSAGE_ID
```

For a reservation explicitly migrated to versioned decisions, first read
`terminal-events decision-get` with that identity and add `--expected-decision REV`
to terminal/blocker publications. Follow [current decisions and recovery](../../../agent-loop/roles/worker/references/decision-recovery.md).

Use `handoff-complete` or `blocked` for those outcomes. The command derives the
recipient/item from the reservation, validates generation, binding, message
ownership and task custody, and returns a stable event id with state `pending`.
A rejected publish is an actionable contract error; do not relabel it transient.
Old Workers may still write canonical/global event prose: the receiver checks
both, including already-completed reservations, against the same identity facts.
A canonical `done` record also emits `reconcile-needed` even if the handwritten
callback is missing. This is a reconciliation hint, never proof of acceptance.

For a design conflict, record one `ask` on the canonical thread mentioning the
Dispatcher, or publish `decision-request` referencing that message. A canonical
addressed `ask` is discovered automatically for older Workers. These requests
are nonterminal: retain the assignment, continue independent work, and do not
claim that WIP was released. The Dispatcher records its revised Issue decision
and publishes `decision-resolved` referencing its own canonical-thread message;
For an adopted assignment, use `decision-set` instead of a standalone
`decision-resolved` publish so updating the revision and its wake is atomic.
The ledger routes the reply to unambiguous assignment custody, including the
same Worker after claim release; receiving it does not grant a new claim. New Claude launch
configs set `event_executable` to the verified receiver binary so the canonical
launcher installs a session-owned native receiver for the Worker too. Do not
claim automatic reply delivery for an old Worker without such a receiver; retain
the durable decision and use only an explicitly authorized, safe issue-local
correction path. Never clear or submit a user draft, and never create a replacement.

Recipients run `squad terminal-events ack <event-id> --note <reference>` only
after handling the transition. Decision requests need a recorded decision/reply,
not Worker termination; replies need the assigned Worker to read the revision.
For terminal observations, if the Worker is still ending or termination is
ambiguous, leave the event unacknowledged: the existing receiver retries after
two minutes. Do not consume the only wakeup and then wait for a nonexistent timer.
A duplicate reminder may contain an old processing snapshot; read current ledger
state before diagnosing failure. Capture the command's actual exit status, never
`$?` after piping to `head`. Delivery/ack do not close reservations or release claims.

## Payload

Include only sanitized identifiers and evidence pointers, no logs, credentials,
customer payloads or hidden reviewer reasoning:

- protocol `worker-terminal-v1`, stable event id and kind;
- originating Dispatcher and Worker task ids/host, Worker agent id;
- repo/Issue/primary item, reservation key/generation, batch/manifest if relevant;
- actual GitHub/Squad disposition, PR and exact head/merge/runtime revision;
- delivery mode/role and Issue delivery state separately from task-turn state;
  for handoff, exact recipient task/primary, PR target branch/merge status and
  per-child pending functional/performance/SLS/cleanup gates plus next action;
- acceptance/cleanup evidence or explicit missing criteria; handoff receipt ids
  for a transfer; released AND retained claims; owned external-operation state;
- `worker_turn_state: ending` (not yet proven terminal);
- “Invoke $squad-dispatcher for one bounded reconciliation cycle. Verify this
  event against live GitHub, Squad, reservation binding and task state; reconcile
  only authorized ready dependencies within WIP. Do not infer acceptance or
  initiate production. No reply/wake-up to this Worker is needed.”

The receiver validates rather than trusting event prose. Dispatcher sees the
send before the Worker's final response can complete: it must confirm the source
turn ended using a compact App task snapshot or bounded cmux session/screen
inspection matching the recorded transport (optionally one bounded wait <=30s).
If still active or ambiguous, preserve WIP and leave the terminal observation
unacknowledged for the existing receiver retry; do not poll, stop the Worker or dispatch into a guessed slot.
Deduplicate processing against the exact event id and generation in the ledger.
An already-processed event is a no-op, not a new dispatch or acknowledgement loop.
A verified notification triggers the existing bounded scheduler; it grants no
merge/deploy/recovery authority and creates no new recurring automation.

For `handoff-complete`, report “开发已交接，Issue 未完成” with the recipient
and remaining gates. Do not present donor task completion as Issue completion or
integration-branch merge as a main/staging release. Preserve legacy callback
compatibility by verifying missing reporting fields from the live handoff record;
never invent acceptance or a recipient from a missing field.
