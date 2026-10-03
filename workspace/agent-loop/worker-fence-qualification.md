# Persistent Codex Worker fence qualification

Installation and Dispatcher receiver success do not qualify Worker execution.
The current Codex adapter has no supported persistent native execution gate;
`require_execution_fence` blocks Worker client/receiver adoption before spawn,
RPC, writer-journal or receiver-lease mutation. Keep this guard until a reviewed
runtime adapter implements and demonstrates the full contract below. A native
execution adapter dependency belongs to the existing installer and native runtime
maintainer; record their independently acknowledged ownership and unavailable
proof rather than closing the gap with an asserted receipt.

## Runnable current negative qualification

Run only from a reviewed source checkout with isolated test state:

```sh
python3 -m unittest discover -s workspace/agent-loop/tests \
  -p test_codex_control_plane.py -v
```

The tests cover rejection before client/RPC creation, unchanged existing
Full Access / never / user selection and custody, mandatory controller epoch,
receiver single-flight, heartbeat transport faults and exact-negative custody
receipts. They prove the **closed adoption boundary**, not persistent fencing of
an already running native Worker.

Inspect a selected installed native executable without starting a server or
sending a business-thread request. Use a task-owned temporary output directory:

```sh
CODEX_BIN=/absolute/selected/codex
TASK_PROOF_DIR=$(mktemp -d)
"$CODEX_BIN" --version
shasum -a 256 "$CODEX_BIN"
"$CODEX_BIN" app-server generate-json-schema --experimental --out "$TASK_PROOF_DIR"
```

Retain the binary hash, generated-schema hash, exact method contract and bounded
isolated native proof. Method discovery alone cannot pass qualification.
`turn/interrupt` concerns one turn; `thread/unsubscribe` concerns one subscriber;
`thread/increment_elicitation` pauses timeout accounting and does not deny direct
input or future tool writes. A quiet UI or timer is not execution suspension.
Never point a qualification probe at a live business endpoint or include private
prompts/data in source or external review inputs.

## Positive adapter admission contract

A future supported native adapter must mediate **every** protected tool and local
writer in the same native session, including tools outside the Squad MCP server.
The installer must prepare its exact native/actor/reservation/claim/controller
pins and retain actual policy and in-flight operation identities. Isolated
qualification must demonstrate:

1. Correct current tuple can admit one writer; a competing admission cannot race
   a custody transition. Stale controller epoch, reservation generation, claim
   generation, released/recovering claim, wrong native/actor and long-lived
   unverified custody all reject before the protected write.
2. A custody-loss transition persists across subsequent turns, reconnect and
   native resume. Neither transport failure nor a one-turn interrupt counts as
   this persistent gate. Never kill a client/server merely for renewal failure.
3. Already admitted **local** writers are actually cancelled and joined, with
   retained handles and terminal outcomes. Original external review/CI/deploy
   operations retain their original owner and are supervised/joined independently;
   do not cancel foreign operations or fabricate process identities.
4. Recovery revalidates the exact original pins and unchanged sandbox/approval/
   reviewer selection before reopening the gate. Replay, stale recovery,
   duplicate controller/receiver and interrupted recovery remain closed.
5. Actual original-native delivery is handled and acked, then protected downstream
   continuation succeeds. CLI publication or receiver enqueue alone is not this
   acceptance evidence.

Record each assertion's actual command/output, binary/source hashes, session/run/
attempt IDs and joins. A local file asserting `qualified: true`, a hook or exposed
method name is insufficient. Until a supported API and positive native execution
proof exist, keep this as an explicit owned dependency; do not add a timer, role,
policy downgrade or unsupported migration as a substitute.

## Controlled activation and rollback inputs

The installer retains a per-native inventory (existing App Dispatcher and every
existing CLI Worker), current selection, actor/reservation/claim/epoch, external
operation handles, writer journal and receiver incarnation. The source Worker
hands over reviewed source hashes and qualification inputs, and does not install
or restart any session. Revalidate each native's actual state immediately before
its separately authorized adoption; do not apply a new-session-only receipt to
existing clients.

If qualification fails, do not start/adopt a Worker: the existing native,
assignment, claim and operations stay with their current owner. Preserve the
actual timer state and unrelated pauses. If a later qualified adoption fails,
rollback closes new writer admission first, joins only the installer's owned new
local writer/receiver, and reads back unresolved operations before any receiver
lease release or same-native resume. Never stop a shared App backend. A controller
rollback needs the normal expected-epoch/full-inventory CAS by its legal owner;
restoring a file or old actor name is not controller authority. Never remove the
private review admission database, occupied flights or consumed recovery roots
when rolling back binaries; an old binary must not be used to evade their gates.

## Exact owned-stdio transport compatibility

The separately selected `owned-stdio` contract is transport preparation only.
It registers CLI `0.159.0-alpha.12.1` with the exact reviewed executable SHA,
440-file experimental protocol identity and immutable installer qualification
receipt in `codex_stdio_contract.py`. There is no general alpha allow-list,
Unix fallback or App attachment. The existing Unix `0.159.2` route and Worker
persistent-fence guard stay closed wherever their original proof is unavailable.

Validate a transport-only config against `schemas/codex-owned-stdio.schema.json`.
Its only supported selection is `gpt-6.1-sol` / OpenAI / medium / priority (Fast),
read-only with network disabled, approval never, reviewer user. Full Access is
unqualified here: never downgrade an existing session to make it fit. Pin the
reviewed qualification receipt and provide its unchanged associated evidence
files and exact generated schema directory. Optional `evidence_directory` relocates
the eight associated files by their unique original basenames; the proof bytes
and all expected file hashes stay unchanged. The schema inventory hash is SHA256
of UTF-8 `json.dumps([{path, sha256}, ...], sort_keys=True, separators=(',', ':'))`,
with all relative JSON paths sorted. Evidence is bounded and rehashed; asserted
completion booleans, altered files and old Unix receipts cannot qualify stdio.

At a separately authorized installer boundary, the original parent supplies its
own already-created `subprocess.Popen` child with exclusive binary stdin/stdout/
stderr pipes. `OwnedStdioRPC(child, config)` never spawns or discovers a host.
The child command must end in `app-server --listen stdio://`. Only the original
parent and IO thread may operate it; registration is locked before any pipe IO.
Executable qualification hashes an opened regular file without executing it,
even for version discovery. A second wrapper, process, thread or changed
pipe/incarnation fails. The bounded JSONL reader drains stderr without exposing
it, rejects partial/malformed/oversized data and never approves host tools.
A transport failure retains the original child handle and marks uncertainty.
It does not terminate a host, join provider work or admit a new sample.

Startup acquisition also binds the exact child to its adapter under that lock.
If pipe setup, initialize or initialized-notification fails after acquisition,
`OwnedStdioStartupError` carries the original adapter as `error.owner`. Original
`KeyboardInterrupt` and `SystemExit` propagate unchanged with that same `owner`
attribute, including interruption immediately after lease publication. Rejection
before acquisition provides no authority to close another wrapper's child.

A failed adapter forbids **all** protocol IO/readiness/handshake retry. Retain
`error.owner` for original-parent/IO-thread bounded `close(timeout=...)` at the
permitted cleanup boundary. Pure custody metadata remains usable. Close verifies
its original pipe handles; it never closes substituted descriptors. Timeout or
interrupted cleanup retains this same handle and lease for another bounded join,
without killing, reinitializing or creating a wrapper. Only actual child wait and
local pipe cleanup release the registry; this does not prove provider termination.
The caller still owns its original `Popen` before acquisition, even when config
or ownership admission rejects the adapter.

`check_owned_stdio(config, rpc, rpc.identity(), worktree)` rechecks the historical
contract, live child identity, **already loaded** native, cwd, current model,
effort and effective policy/Fast tier without selection overrides. It never
loads an absent native. A historical probe native is not the current target.
`deliver_owned_stdio` must run in that same parent/IO thread; a standalone
receiver cannot attach to these pipe descriptors. It preserves the existing
durable event journal with `prepared`, `intent` and `accepted` states. It rechecks
current selection before a queue write, persists `intent` immediately before
wire submission, and verifies the exact returned acceptance ID/client ID/input.
A failed readiness check leaves `prepared` retryable after fresh qualification;
a transport fault during readiness does not imply a queue was submitted. Delivery
acceptance is not current Dispatcher handling or explicit ACK.

On an actual queue write with a lost response, the original intent remains.
The original pipe owner also retains the exact pending native/client ID/input;
fresh readiness alone cannot clear this queue uncertainty. Only exact supported queue/
retained user-input readback matching **both** client ID and immutable text may
join it; ambiguous, missing or truncated history remains uncertain. Never send
again merely because the client ID looks unique. Mock tests do not qualify real
duplicate, stale or lost-response semantics. Child cleanup closes only the
owned stdin and joins only that child; a join timeout retains custody and no
server/client is killed. The returned host join is not remote cancellation.

Run the portable synthetic protocol/negative tests (no Codex/provider invoked):

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s workspace/agent-loop/tests -p test_codex_stdio.py -v
```

The registered original isolated transport proof demonstrates one initial and
one queued turn with actual acceptance, retained input/turn association, idle
wake and joined hosts. It proves neither current Worker adoption nor App host
entitlement, Unix remote transport, persistent all-writer custody, business
handling/ACK or Issue acceptance. Those retain independent platform contracts
and installer qualification gates. This compatibility API does not install,
resume, migrate or activate any existing native session.

For an owned stdio queue write, encoding/size, exact IO ownership and first
writable readiness precede the durable marker. Only the original transport can
issue a typed, exact-operation, single-use not-sent witness before any marker or
possible `os.write`. The locked owning journal must still equal its prepared
record and original parent/native/server incarnation before that witness is
consumed; it does not authorize another owner or reconnect to replay. Another
event requires fresh owner/native/policy readiness. Intent and queue uncertainty
precede every possible first write. Marker exceptions, interrupted write calls
(even with zero counted bytes), partial writes, lost replies and unknown journal
outcomes retain custody/intent and require exact supported readback. There is no
broad-error rollback or inferred absence-based retry.
