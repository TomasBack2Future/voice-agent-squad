# Runtime-independent roles

Muse, Claude and Codex may each perform Dispatcher, Worker, Deployer, Reviewer
or Investigator work. The role supplies authority and completion requirements;
the runtime supplies execution capabilities. A provider name is neither a role
restriction nor evidence that the selected client can complete the assignment.
CLI, desktop App and supervised protocol hosts are separate execution surfaces.

## Equal admission, scoped to the operation

Use the same required capabilities for the same role, operation and custody mode
across all three runtimes. Verify the selected executable path/version, native
session and actor, actual model/effort and user-selected permissions, loaded role
skill/profile revision, ledger binding, and owned external operations. Record
these in existing startup/checkpoint evidence, not a second ledger. `--help`,
executable presence, skill discovery and a static receipt do not prove execution.

| Role | Required responsibility and operation-specific capability |
| --- | --- |
| Dispatcher | One controller; current reservation/decision authority; verified event reception and handling; bounded reconciliation |
| Worker | Exact assignment and claim; source/test/delivery tools for authorized phases; supervised renewal and decision/outcome route |
| Deployer | Release skill and frozen candidate; protected ENV/execution fencing; operation supervision, acceptance and recovery |
| Reviewer | Independent bounded review; exact input revision, allowed evidence and verdict contract; no implementation or release authority |
| Investigator | Bounded evidence question and permitted access; supported cause or explicit uncertainty; no implied repair or deployment authority |

Do not run `squad go`, pick another item, or acquire a primary claim merely
because a generic loop skill was loaded. A dedicated role follows its own
assignment. The Worker assignment schema and launcher remain Worker-only; do not
relabel a Deployer or Investigator to fit that schema. Use the role's actual
contract and supported executor. An absent adapter is an implementation gap with
a concrete owner and acceptance criteria, not a permanent provider exclusion.

For asynchronous work, verify both directions: event producer, durable ledger,
recipient/native/controller binding, safe transport, recipient handling, and the
authorized downstream result. Persisted, delivered, handled and completed are
separate states. A supported Worker-to-Dispatcher route does not prove the reply
route, and a CLI probe does not qualify App delivery. Do not inject terminal
input or introduce another scheduler to bridge an unavailable route.

## Hooks, review and custody

Claude plugin hook metadata and `Task`/`Bash` tool names are adapter hints, not
portable execution guarantees. Without qualified hooks, explicitly read the
addressed mailbox and current decisions at startup/resume, before a consequential
transition, and before closure. Use a supported session-owned receiver for idle
wake and supervisor for renewal; manual checks are not unattended wake support.
Do not add periodic model prompts or silently disable required conflict checks.

Use the repository's actual review policy. When independent local review is
required, any qualified runtime may host the independent reviewer with the same
briefing, input tuple, read-only scope and evidence standard. `superpowers` is
one optional client integration, not a universal prerequisite. A specifically
required managed model/Check is a separate policy: local review cannot substitute
for it, and changing the author's runtime does not change that policy.

Do not equate renewal, stopping renewal, a turn interrupt, process silence or
an event ACK with persistent exclusion of a stale writer. When a custody mode
requires execution fencing or takeover, require equivalent proof for Claude,
Codex and Muse. Preserve existing fail-closed guards until a reviewed adapter
meets that contract; another runtime's weaker path is not a precedent for bypass.
Verify actual local writer joins and retain external-operation supervision.
Do not stop or migrate existing sessions merely to apply a new admission rule.

## Adoption and failure reporting

Permission names are runtime-specific. Preserve the user's actual selected mode
on launch and supported resume; verify it in effective runtime state. Full Access,
YOLO or bypass mode never grants role, environment or disclosure authority.
Never silently downgrade the model/permission selection to fit a schema.

An unavailable operation blocks that phase, not independent authorized work.
Name the failed condition, actual installed revision, next discriminating check,
repair owner and verification needed. Reuse existing Issue/PR owners before
filing. Distinguish source, reviewed/merged, installed, session-adopted and effective
states. Installation and takeover remain separate authorized operations; a skill
link refresh does not change a running session. Pauses and existing custody persist.

## Portable command and review selection

`runtime_entry.py` supplies a client-neutral CLI binding for all three runtimes.
Pass the native Squad binary, selected ledger, actual actor and native session;
it removes inherited foreign session identities. `capabilities` reports source
support separately from native qualification. `listen` uses deferred delivery;
`handled` consumes the recipient's explicit handling result and records delivery
then ACK through the fenced Squad API. An exit code or wake alone is not a
handling result. The helper neither generates model turns nor creates a scheduler.

The `worker` entry selects the existing launch adapter. Claude's portable entry
requires explicit model/effort and the event executable; its receipt is
`launch-checked`, not proof of effective model/permissions or claim-loss write
exclusion. Legacy direct configurations retain their existing invocation.
Codex and Muse custody guards remain in force. A shared App is not a CLI target.

The bounded reviewer supports explicit `--backend grok|claude|codex|muse`.
Non-Grok selection requires an explicit model, uses distinct Check names and
retains the shared PR admission history. Choosing another backend does not reset
an attempt, refund a consumed input or impersonate a required Grok Check. See
[the review command reference](../../docs/proposals/grok-required-review-gate.md#native-review-backends).
