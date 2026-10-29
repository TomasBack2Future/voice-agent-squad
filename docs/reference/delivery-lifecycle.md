# Delivery state and explicit closeout

These commands reuse the selected repository's existing messages, claims,
attestations, reservations and terminal receipts. They do not add a scheduler,
a second state ledger, an approval step or a production execution grant.
CLI and local MCP expose the same Go operations. Check the installed executable's
help before using them; updating skills does not install a new binary.

## Current state

`squad dispatch delivery KEY [--issue-observation FILE]` returns independent
Issue, item, reservation, decision, retained claims and latest command-result
fields, plus recorded operation phase/revision, resume action and evidence reference.
`unresolved_operations` names every current unresolved request on that exact
assignment (bounded to 100), even when a newer request has already exited.
`owned-operation-unresolved` flags that drift independently of the latest result.
Missing operation provenance remains `unknown`; caller-provided phase references
are not proof of downstream handling or deployed revision. MCP: `squad_dispatch_delivery` (`reservation`, optional `issue`).
An observation is `{ "source_ref":"github:owner/repo#123", "state":"OPEN",
"verified_at":1790000000 }`, obtained by an authorized read of that exact Issue.
It is caller-supplied evidence, not acceptance or closure authority. A missing
or older-than-five-minute observation is `unknown`; mismatched source or a
future timestamp is rejected. Ledger item IDs are scoped by repository identity.

Stable drift codes distinguish an open Issue with a completed reservation,
a closed Issue with active reservation, a terminal reservation retaining claims,
and pending delivery on a blocked item. The latest attestation is a command
exit, never proof of native session death or business acceptance. This read
operation does not mirror files, rewrite messages or release resources.

## Resumable closeout

`squad closeout --request FILE` (local MCP `squad_closeout`, `request`) takes:

```json
{"reservation":"DISPATCH-PRODUCT-123","generation":1,
 "worker_session":"native-session","expected_decision":2,
 "request_key":"final-acceptance",
 "body":"Exact revision and acceptance, logs, cleanup, retained records and limitations."}
```

Invoke only after assigned acceptance/logs/cleanup and safe resource release.
The original Worker needs custody and its effective decision must permit work.
No routine Dispatcher close approval is added. The sequence is:

1. Existing `done` verification and evidence gates; no force/skip flag.
2. Upsert one marked GitHub closeout comment using the current gh connection.
3. Read/close the exact GitHub Issue.
4. Submit one deduplicated `issue-closed` outcome through `terminal-events submit`.

Only the Dispatcher reconciles its reservation afterwards. Local item done is
an intermediate result: a failed GitHub step leaves Issue delivery unfinished.
Each step writes intent/result receipts to existing messages. Reusing a request
key with changed content is rejected; same-request retries probe external state
before repeating an effect. A response may be lost after an external write;
retry the identical request. Do not blindly create another request or comment.
The comment marker and GitHub APIs provide convergence, not a distributed atomic
transaction. Concurrent matching markers are diagnosed rather than deleted.

Steps are bounded to 60 seconds; a still-live intent holds a two-minute lease.
If configured local gates need longer, run existing `squad done` as an owned
tracked task, verify its terminal result, then retry closeout; the completed
local step is adopted. An interrupted wrapper does not prove external work
stopped. Reconcile owned operations before retrying an unresolved live intent.
GitHub output is bounded and stderr/credentials are not copied into receipts.

## Partial delivery and continuation

Keep the Issue OPEN and the unfinished item blocked. Publish `blocked`, have its
original Dispatcher handle it, and safely release/reconcile all Worker claims
and active execution custody. The original owner may invoke
`squad dispatch retire-partial --request FILE` (local MCP
`squad_dispatch_retire_partial`). The request contains `expected` (the full exact
reservation returned by list), `issue` (fresh matching OPEN observation), and
`blocked_outcome` (the original Worker's handled blocked event).

This is an audited transition from dispatched or erroneously completed to the
existing failed state, labelled partial delivery. A dispatched assignment requires
its current hold. A completed assignment is already excluded by current-decision
lookup and new-operation admission; do not manufacture a new work decision to
retire it. Both paths require matching native/generation/actor, blocked canonical
item, handled original outcome, no retained Worker claims/active execution pins
and no other claimant of the canonical item. Audit and the exact one-row
reservation update commit together; an unapplied update rolls back the audit. It does not close the Issue or invent a done
item. A new authorized continuation can use existing reserve/attach/bind with
next generation and the same canonical item. A process exit or Issue observation
alone never qualifies retirement. Unmanaged external operation custody still
requires the original owner's verification; absence from this ledger is not
proof that those operations stopped. Remote mutation remains disabled.

## Owned command result delivery

`squad operation run --request FILE` (local MCP `squad_operation_run`, `operation`)
wraps a command explicitly, with identity/custody and decision checks:

```json
{"identity":{"reservation":"DISPATCH-PRODUCT-123","generation":1,
 "worker_session":"native-session","expected_decision":2,"request_key":"cleanup-1",
 "phase":"cleanup","revision":"exact-revision","resume_step":"inspect cleanup result"},
 "kind":"test","argv":["go","test","./internal/example"],"timeout_seconds":600}
```

Optional `phase`, `revision` and `resume_step` are bounded single-line references
saved with the operation and included in its request digest. Changed provenance
under the same key is rejected. Use a client-native tracked background task for a long command. No permanent
watcher is created. A durable start precedes execution; exit records reference
captured attestation evidence and enqueue `operation-exit` for the original
Worker's qualified native receiver. Timeout preserves a failed result even when
the execution context is cancelled. Quiet failure evidence cannot deduplicate
against quiet success. Process custody and evidence persistence are separate:
`not-started` proves launch did not occur; `exited-without-evidence` retains the
observed exit code when evidence saving fails. Both still require original-owner
result handling, and neither supplies missing acceptance evidence. After handling,
they do not leave phantom live custody blocking closeout or partial retirement.
A corrected, already-authorized command uses a new key; the failed key never
runs again. `result-unavailable` remains unresolved when a started process has
no proven join. `exec.ErrWaitDelay` also retains this state: the direct child
may have exited while a descendant still holds its pipes. ACK does not remove
that unresolved custody. A successful command result is not proof that an
independent background service or external workflow stopped; its original owner
still verifies that custody. A missing result is never converted to success. On Unix cancellation targets the process group created
by that invocation; Windows descendant supervision is unqualified.

`squad operation status --request IDENTITY.json` (local MCP
`squad_operation_status`, `request`) reads the saved record. A start without a
terminal result is unresolved and refuses automatic rerun. A changed command
under the same request key is rejected. Changed generation/native/custody cannot
receive an old exit. The original Worker may still deliver/handle its recorded
operation exit after the same reservation reaches completed. This reconciliation
permission does not reopen execution or deliver a new work decision. Results remain durable when routing needs reconciliation.

The Worker reads its effective decision and real result, updates the next phase
or blocker in its existing checkpoint, then ACKs the event with that reference.
Delivery/ACK never closes an Issue, changes the decision, releases ENV or retries
an external workflow. A pause/hold remains effective even when an exit arrives.
Native end-to-end qualification is per installed runtime; protocol/subprocess
fixture tests alone do not qualify a new client combination. Arbitrary command
execution and closeout are local-only, excluded from the remote service surface.

New lifecycle mutation entrypoints also require the caller's actual native
session environment to match the assignment (or current owner-controller
binding for partial retirement). A worktree's persisted actor fallback alone
supplies no native authority. Unbound restored shells can inspect state but
cannot use these mutations. Missing launcher binding is a concrete capability
gap, not a prompt to copy another session's environment or bypass its fence.
