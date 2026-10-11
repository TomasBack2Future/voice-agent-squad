# Claims and coordination

## What a claim is

A claim is an atomic, exclusive lock on one item by one agent on one machine. The claim record lives in `~/.squad/global.db` (SQLite) and is acquired via `BEGIN IMMEDIATE` — if two agents race for the same item, exactly one wins and the other gets a clean "already claimed by agent-XXXX" error. Released claims move to `claim_history` so a peer can see who held what when.

A claim is a CLI command, not a frontmatter field. The item file's `status:` is rewritten at close-out (when `squad done` runs), not at claim time.

## Why files + DB hybrid

Item content is **per-repo, durable, git-committed.** It lives in `.squad/items/<ID>-<slug>.md` and travels with the codebase. That's where the AC is, where the body is, where the rollback plan is.

Claims, chat, and file touches are **machine-local, operational, ephemeral.** They live in `~/.squad/global.db` and are recreated on the next register. Trying to merge claim rows in git would be a nightmare — two agents claim simultaneously on different machines, both commits "win," and now you have a phantom claim that no one holds. The hybrid keeps git diffs about behavior changes and the DB about who's doing what right now.

## Multi-agent rules

- **One claim per agent per repo** by default. The configuration knob (`agent.claim_concurrency` in `.squad/config.yaml`) can lift it, but the default is 1 because a single agent juggling multiple claims is usually thrashing.
- **Heartbeat keeps claims live.** `squad tick` and `squad progress` renew held claims for the acting agent in the selected ledger. Merely posting chat, running tools, or receiving mailbox events does not renew them. `claim --wait` renews the waiting agent's held claims on each wait-lease refresh. The canonical Claude launcher supervises the native client and sends a fenced `squad heartbeat` every 30 seconds without consuming messages or prompting the model. A claim past `hygiene.stale_claim_minutes` (default 60 min) is still reported by `squad doctor`.
- **Active dispatch ownership is reconciled, not expired away.** Hygiene does not auto-delete the canonical claim of a reserved/dispatched assignment, or claims of an agent with an unexpired wait lease. A dead client stops automatic heartbeat; its active dispatch remains visible for Dispatcher reconciliation. After terminal reconciliation, ordinary stale claims can be reclaimed. Process liveness proves neither task progress nor safe completion.
- **`squad force-release <ID>`** removes an ordinary stuck claim after an operator verifies it is abandoned. Protected `ENV-*` claims are different: hygiene never auto-reclaims them. Use the fenced `squad recover` flow after independently verifying the holder task and external operations.
- **File-touch tracking** warns (does not block) when you start editing a file that another agent's claim is touching. `squad touch <path>` declares an active touch; `squad untouch <path>` releases it. The opt-in pre-edit hook automates this against Edit/Write tool calls.

## Waiting without model polling

Most claim conflicts should still make an agent choose other ready work. When
the item is a required shared resource and useful work is exhausted, the agent
can wait without repeatedly spending model tokens:

```bash
squad claim ENV-001 --intent "run exact-revision acceptance" --long --wait
```

The first attempt is the same atomic claim as usual. If another agent holds the
item, Squad registers a non-exclusive, item-scoped listener and blocks inside
the CLI process. `squad release` sends an immediate loopback wake signal. The
waiting process then rechecks ownership and retries the atomic claim. A quiet
fallback check covers releases that did not originate from the CLI, including
stale-claim hygiene.

Waiting is not ownership. It must not be used to reserve a shared environment
before external prerequisites such as approval, current-base validation, or CI
have passed. Revalidate those prerequisites after `--wait` returns and before
the first mutation.

## Protected environment recovery

Stopping a Codex task can interrupt its cleanup handler while a deployment or
rollback continues outside Codex. For that reason, stale `ENV-*` claims are
reported but never deleted by automatic hygiene.

After independently verifying the holder task is stopped and recording the
state of workflows, rollout, exact revision, and environment health, a recovery
operator can atomically transfer the claim:

```bash
squad recover ENV-001 \
  --from agent-abcd \
  --holder-session 0199... \
  --reason "holder task stopped during rollback verification" \
  --evidence "task stopped; workflow terminal; staging revision 9917d417 ready" \
  --confirm-holder-stopped
```

Recovery is a compare-and-swap on the expected holder and generation. It never
makes the environment free between owners. The new generation is written to
`claim_recoveries`; a late release from the displaced holder is rejected. The
recovery owner may only make the environment safe, verify an exact revision,
and release it—not merge unrelated work.

## Durable dispatch reservations

A caller can reserve a canonical source before creating its item or task.
Only the reservation winner creates the item, attaches it, and binds the
returned generation to the created task:

```bash
squad dispatch reserve DISPATCH-FEAT-501 --source github:owner/repo#501 --json
squad dispatch attach DISPATCH-FEAT-501 --item FEAT-501 --generation 1
squad dispatch bind DISPATCH-FEAT-501 --generation 1 --thread-id 0199...
```

This reservation is not work ownership. The eventual task owner must still win
the normal atomic item claim. It closes the race between concurrent callers
that observe the same unclaimed source. Reserving before item creation prevents
duplicate canonical items and tasks. Unbound reservations expire; bound
reservations remain durable until the caller reconciles them with `dispatch close`.
Scheduling cadence and task-role policy are supplied by the consumer, not this API.

## Lifecycle states

```
filed (.squad/items/) ──┬─► claimed ──► in-progress ──► review ──► done (.squad/done/)
                        │       │              │
                        └─► blocked            └─► released (back to ready)
                                │
                                └─► (resolved) ──► claimed
```

Command per transition:

| From | To | Command |
|---|---|---|
| filed | claimed | `squad claim <ID> --intent "..."` |
| claimed | released | `squad release <ID>` |
| claimed | review | `squad review-request <ID>` |
| any open | blocked | `squad blocked <ID> --reason "..."` |
| blocked | claimed | `squad claim <ID>` (re-claim) |
| any open | done | `squad done <ID> --summary "..."` |

`squad reassign <ID> @new-owner` is shorthand for "release + ping new owner in chat."

## Cross-machine claims

The global DB is **machine-local**. If you switch laptops, your claim does not follow you — register on the new machine, then re-claim. Items themselves do follow (they're in git), so the work is portable; only the operational state needs re-creating.

Cross-machine claim sync is **out of scope for v1.** v2 may add it (the design doc has a section on this). For now: one machine = one claim namespace.

## Per-claim worktrees

When two agents claim into the same working tree, their pending edits flow into each other's test runs — a sibling's WIP can produce a misleading green that doesn't validate either claim's code in isolation. The structural fix is one git worktree per claim.

Opt in per claim:

```bash
squad claim FEAT-123 --intent "..." --worktree
```

or globally for the repo, in `.squad/config.yaml`:

```yaml
agent:
  default_worktree_per_claim: true
```

What `--worktree` does:

- Provisions `<repoRoot>/.squad/worktrees/<agent>-<item>/` on a fresh `squad/<item>-<agent>` branch forked from the current HEAD.
- Records the absolute path in the `claims.worktree` column so `squad done` and `squad handoff` can tear it down without re-deriving.
- Prints a `cd <path>` nudge after claim — that's where you do the work.

Teardown happens automatically:

- `squad done <ID>` runs `git worktree remove --force` then `git branch -D squad/<item>-<agent>` if no commits landed. Commits leave the branch in place — the user (or a follow-up PR) owns the merge.
- `squad handoff` does the same for every claim it releases.
- Cleanup failures are warnings, never block the close-out. Stranded directories surface in `squad doctor` as `worktree_orphan`.

The `--worktree` flag is opt-in for this ship. Solo flows are unaffected — the column is empty, the cleanup path is a no-op.

## Common races and how they resolve

- **Two agents claim simultaneously.** SQLite `BEGIN IMMEDIATE` serializes the transactions; one commits, the other gets `unique_constraint`-equivalent and the `squad claim` command exits with a clear "already claimed by X" message. No corruption, no torn state.
- **Agent crashes mid-claim.** No release runs, so the claim stays open with a stale heartbeat. Ordinary claims can be force-released after confirmation. `ENV-*` claims require the fenced recovery flow above and are never auto-reclaimed.
- **Claim across worktrees in the same repo.** Each worktree's `.squad/items/` may differ if the items are in different branches, but the DB is shared. Claiming the same `<ID>` from two worktrees still races against the DB, so only one wins — even if the other worktree doesn't have that file checked out.

## See also

- [squad-vs-agent-teams.md](squad-vs-agent-teams.md) — claim semantics compared to agent-teams' file-locked tasks.

## Opt-in resource scopes and deadlock checks

A ledger can register protected resource definitions through
`squad resources define policy.json` or MCP `squad_resources_define`. Definitions
map item IDs to a group, default coverage and a permitted service scope. The
[Studio adoption policy](../../workspace/resources/README.md) keeps ENV-001/002
and their legacy broad coverage while adding independent service resources.

`claim --scope SERVICE` (MCP `scope`) narrows only a registered service. Empty
scope uses the registered default; `*` covers the group. Same item IDs remain
exclusive, and claims conflict when group coverage overlaps. The accepted scope
is stored with ownership and history, retained through fenced recovery, and never
reinterpreted from changed policy. Policy updates require affected claims/waiters
to be idle. SQLite triggers enforce insert conflicts for older clients too.

`claim --wait` records its request with a 30-second lease renewed at most every
10 seconds, even with notification fallback disabled. The atomic registration
rejects a wait-for cycle and reports its agent/request/blocker chain. Granting a
claim checks the graph again. Repeated acquisition of an overlapping owned claim
fails immediately. Release notifications wake related waiters; notification loss
or non-CLI releases are recovered by bounded rechecks. Cancellation removes the
wait record; a crashed wait's lease expires without releasing any held claim.
`squad doctor` reports live wait cycles, including those introduced by older
clients. It never resolves a cycle by deleting protected ownership. Old binaries
cannot record waits and therefore cannot provide full deadlock detection; upgrade
active clients before enabling scoped automation.

Scope policy does not confer deployment authority and is local to one ledger.
Use a consistent ordering for multi-resource acquisition; no automatic scope
upgrade, multi-item atomic acquisition or shared read-lock protocol is provided.

## Replacing stopped clients

Successful takeover receipts and audit messages describe only the committed
transaction. Events observed by a rolled-back attempt are discarded before a
retry, including events acknowledged or replaced between attempts.

This legacy operation is rejected atomically once the repository has any
controller binding or retirement record. It must not bypass the current
controller actor/native/epoch or move one reservation out of a bound cohort.
Use the existing complete-inventory controller handoff for Dispatcher custody;
that operation deliberately preserves Worker claims and sessions. A new native
Worker replacement under that protocol uses the separate supervised source
handoff below when its prerequisites are qualified. Do not use a legacy
takeover as a fallback.

### Controller-bound source Worker handoff

`dispatch worker-handoff` preserves the existing controller and transfers one
ordinary source claim plus its reservation to a fresh registered Worker. The
original owner must authorize the exact request digest in a canonical message.
The original native and tools must already be joined through a reconciled source
execution pin. A quota error, process absence alone, or an operator assertion
cannot replace that pin. Legacy CLI clients instead require original-native
stop preparation, actual host join observation, persistent native fence and
current-controller supervisory attestation under exact human authority. No pin
is fabricated. App clients without a qualified native-stop executor remain
unsupported. Protected ENV, other retained claims, active pins, ambiguous custody,
unhandled original events and changed ownership fences reject the transition.

The transaction preserves the effective hold, increments claim and reservation
generations, records an immutable receipt and publishes a real replacement
decision event. It never grants `proceed` or invents business completion.
Heartbeat renewal does not change ownership; changed actor, claim generation or
claim acquisition time does. Terminal continuation retains the original canonical assignment; changing
to a different continuation item is unsupported. The explicit legacy terminal
route verifies same-item original release history before creating a new claim. Dispatcher custody remains unchanged.

Handle all original-generation results before handoff. The replacement reads
and handles the transferred hold through its actual native receiver; the current
controller separately releases eligible work using the existing decision CAS.
The fresh Muse source launcher can adopt a dirty retained worktree only with the
immutable handoff receipt and a hash-bound original joined writer record. It
compares the entire original assignment and authorization, preserving repository,
branch, base ancestry, profile and worktree. The legacy variant verifies the
immutable stopped-custody receipt and actual retained source snapshot instead
of a pin-backed writer record. An explicitly retained terminal ordinary phase
can reacquire its claim at a new generation only with exact original release
history. Same-native resume remains separate.

`dispatch takeover --request handoff.json` is an operator recovery operation,
not ordinary scheduling. The JSON supplies `reservation`, `expected_dispatcher`,
`dispatcher_session`, `expected_worker_session`, `expected_generation`,
`new_dispatcher`, `confirm_dispatcher_stopped`, `reason` and `evidence`.
A Worker replacement additionally supplies `new_worker_session`,
`expected_holder`, `expected_claim_generation`, `new_holder` and
`confirm_worker_stopped`. MCP exposes the same object as `squad_dispatch_takeover`.

First independently stop the exact old native clients and their lease supervisors,
and inspect their external operations. A model quota error is not stopped-client
proof. Recent registered heartbeats or claim renewals reject replacement. Protected
resource custody must first be resolved through `recover`; active execution pins
also block the claim transition. Never impersonate the old agent or edit the DB.

The transaction compares the expected reservation and ordinary claim fences,
transfers the canonical claim without a free interval, increments both generations,
and preserves the current decision (including a hold) in the new epoch. Old
heartbeats, event publications and reservation closure are rejected. Worktree,
intent and canonical item remain unchanged. The result includes an audit message
and pending old events requiring explicit handoff reconciliation; it does not
acknowledge them or declare the work complete. A replay with the old fence fails;
after a lost response inspect the reservation and audit before retrying.

Without the replacement fields this transfers only Dispatcher custody. It keeps
the still-running Worker's generation/session intact and reroutes unprocessed
Dispatcher receipts. The operator must inventory all affected reservations and
keep a new writer inactive until every necessary binding is read back.
### Session identity isolation

An explicit `SQUAD_AGENT` takes precedence. Otherwise, a session selected by
`SQUAD_SESSION_ID` or the supported terminal session variables reads only its
own persisted identity. A missing or unreadable per-session file derives a new
session actor; it must not borrow the legacy unscoped `agent-id.txt`. Sessions
without a session key retain that legacy behavior. This applies equally to
Claude, Codex and Muse; launchers still supply explicit sanitized native/actor
bindings. Native provider variables are not an ownership transfer mechanism.

This changes identity resolution only. It does not migrate or recover an existing
claim, reservation or environment. A session that previously relied on the global
fallback must resolve its actual custody before writing under a new identity;
use explicit existing actor binding only when its ownership is independently
verified. Never copy an actor name merely to make a failed ownership check pass.
