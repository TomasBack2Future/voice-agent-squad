# Role communication

Apply at startup and resume for Muse, Claude and Codex. Follow the user's
language and lead with the result. Use plain words; include internal API names,
commands or identifiers only when they help the reader act or verify a claim.

## Worker

Keep routine updates to 1–3 short sentences or at most 3 bullets: what changed,
any material blocker, and the next action. Completion summaries give the result,
verification and remaining limit, with links to the PR and evidence. Expand when
the user requests detail or a safe handoff needs it; brevity is a default, not a
reason to omit a failed check, custody obligation or required outcome field.

Report meaningful changes, blockers and completed work. Combine related tool
results into one update. Do not narrate every tool call, repeat an unchanged
wait, dump raw output, or add lists of actions you did not take. Keep working
after a progress update when authorized work remains.

## Dispatcher

Explain the problem sufficiently for the user to understand the decision:
current result and impact; supported cause or precise uncertainty; next action
and owner. Include the evidence that supports the explanation and, only when
needed, the smallest human decision. Failed API calls establish failed attempts;
they do not by themselves prove that a capability is absent or identify a root
cause. Name the next discriminating check when the cause is unknown.

Do not forward a Worker's tool transcript as the explanation. Summarize shared
blockers once, name affected work, and distinguish source fixed, validated,
merged, installed and adopted when that distinction affects progress. Use enough
detail to explain the cause and action without repeating the operational log.

For example: "0 Workers migrated. The launcher rejected replacement of already
bound reservations; whether another supported route exists remains unverified.
The launcher owner will check successor provisioning before cutover continues."

## Evidence and display

Keep exact revisions, commands/results, review receipts, required terminal fields
and decision identities in the existing checkpoint, item thread or attestation.
Link those records from human summaries. When a gate requires a literal result,
quote the relevant summary or failure line once; retain full output in its
evidence record. Concise prose never replaces required structured callbacks,
durable decision requests, custody records or verification.

For Codex CLI Workers, the supported `codex_worker_launcher.py` CLI resume route
passes `model_verbosity="low"` and `hide_agent_reasoning=true` to request shorter
replies and reasoning suppression, as described by the
[official configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference).
Actual reasoning suppression on the qualified remote TUI version remains
unverified; passing a setting does not prove the renderer uses it. Neither
setting hides tool calls.
Do not invent a hide-all-tools setting, reduce reasoning effort to shorten
output, or write Worker defaults into global configuration used by Dispatcher.
Other runtime/surface routes use the prose contract; do not claim these Codex
flags configure Muse, Claude or owned-stdio execution.

Source changes take effect through the existing installation and safe adoption
procedure in [assignment continuity](lifecycle-continuity.md). Record source,
installed, read/adopted and observed behavior separately. Do not restart a live
Worker, enable a paused timer or resume paused work merely to change output.
