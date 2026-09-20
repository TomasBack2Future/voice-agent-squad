# Studio local Agent Loop

Status: maintained integration contract, not a request to start work.
The configured `squad-coordination` wrapper supplies task identity and selects
the shared ledger. Discover that configuration; do not assume the source
checkout, outer workspace or an old absolute path is the ledger.

## Roles and ownership

| Role | Owns | Must not do |
| --- | --- | --- |
| Dispatcher | One finite reconciliation per authorized heartbeat; canonical reservations and missing independent worker dispatch | Implement product changes, claim ENV, merge/deploy, close product issues |
| Worker | Exactly one assigned canonical issue/primary item through evidence-backed delivery | Scan/pick another issue or bypass checks |
| Environment recovery | A verified stopped holder's interrupted operation through safe recovery and fenced transfer | Steal a live claim or merge new product work |
| Local reviewer | SHA-bound observations/check via approved wrapper | Write code, own an issue/environment, merge or deploy |
| Human monitor | Read-only loopback observation | Claim/release/dispatch actions, mutation APIs or exposure of hidden reasoning/secrets |

Normal delivery has two exclusive ownership classes: primary work and the
target environment merge/verification lock. Do not add a third lock for the same
delivery. The current local names are `ENV-001` for Studio staging and `ENV-002`
for production; production requires explicit authorization. Local arbitration
does not replace hosted branch policy, deployment concurrency or cluster locks.

Observe current identity/status/claims before mutation. Reuse a canonical item;
only a successful atomic claim grants ownership. Do implementation, deterministic
tests and required review outside ENV. When genuinely ready to deploy, validate
current head/base/checks, wait using the supported blocking claim mechanism,
and validate again after acquisition. Hold ENV across exact-revision rollout,
acceptance and any necessary rollback; release only on a verified safe terminal
path. Never force-release ENV because a task appears quiet or unregistered.

Dispatch reservations provide scheduler idempotency, not product ownership.
Do not replace a quiet non-terminal worker or wake an active task for status.
The configured product-worker WIP limit is five; existing non-terminal workers
and unbound active reservations count toward it. The only periodic scheduler is
the dedicated dispatcher; a reconciliation is finite, not a never-ending goal.

The detailed operating skill and target repository runbook own exact commands,
acceptance windows and evidence. This page preserves boundaries without copying
secrets or embedding machine-specific skill paths. Verify actual repository
review policy: no implicit P0/high-risk reviewer quota, and an enforced
App-pinned check cannot be bypassed with a ledger completion flag.

## Documentation arrangement

Each repository must carry its own `AGENTS.md`, README and documentation index.
Git-root discovery and isolated worktrees must not require an outer directory's
instructions. An optional [workspace template](../workspace/README.md) maps
repositories but is not the only copy of safety-critical rules.

Keep four kinds of information separate:

- Agent instructions: concise working constraints and routing.
- Topic docs/contracts: current architecture, interfaces, tests and operations.
- Dated observations: source SHA, evidence, environment state and unknowns.
- Task/dispatch state: live ledger and task system, never reusable instructions.

Update one owner per fact in the same change. Archive old plans as history rather
than silently promoting them to runbooks. Skills package repeatable procedures;
installed skill copies are not automatically updated by moving a repository.
An Agent Loop refactor must explicitly plan binary/config/skill/automation
migration and active claim/worktree preservation; this docs change performs none.
