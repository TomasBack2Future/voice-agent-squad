# Rolling integration and shared acceptance

Use when the user authorizes rolling preparation/merges and common deployment.
This does not silently migrate ordinary Workers to batch-v1, transfer source
ownership, or authorize production. Keep each active assignment until its owner
acknowledges the versioned transition at a safe checkpoint.

Start the merge plan with the first admitted Issue. Do not wait for all Issues
to be implemented or for a global ready message. Keep a current decision revision,
explicit merge authority, dependencies by phase, exclusive paths, exact PR heads,
review/CI evidence, cutoff and one named common acceptance owner. Preserve prior
revisions as history instead of contradictory live hold/release fields.

Run `workspace/agent-loop/rolling_delivery.py SNAPSHOT.json` on a fresh snapshot
from Squad and GitHub. It is a read-only planner, not another assignment ledger.
The schema is `agent-loop.rolling_delivery.v1`; use the synthetic fixture in
`workspace/agent-loop/tests/test_rolling_delivery.py` for the field contract.
The snapshot records `observed_at`, `batch_id`, `acceptance_owner`, `cutoff_at`,
`policy`, current `decision`, `members`, live `tasks` and `reservations`.

Each member has a canonical `id`, current `state`, `dependencies` (other IDs),
`head_sha`, `ci_head`, `reviewed_head`, `ci_green`, `adopted_decision_revision`,
`merge_blockers` and `exclusive_paths` as applicable. Dependencies outside this
batch must appear with independently verified disposition; unknown/cyclic edges
are rejected. States are design, developing, review-ready, integrated, accepted
and closed. Never promote an implementation based only on an offer or a terminal
message. `task_done` plus `reservation_active` requests reconciliation, not a new
Worker or an automatic reservation close.

A ready member with exact-head gates and integrated predecessors can merge under
its existing ownership and repository policy. Shared paths serialize actual merges;
review and investigation continue. The planner returns the next independent set,
not a promise that those heads will remain green after the first merge. Re-read
before executing. WIP counts paused tasks, external operations, retained claims
and unbound active reservations. Default is five; an explicit user decision may
set a different positive limit, recorded as `policy.wip_limit` and
`policy.user_decision_ref`. Capacity limits new dispatch, not completion of work
already admitted. Do not terminate owners to make the count smaller.

At the cutoff, freeze only integrated contributions with their complete dependency
closure. Unready members move to the next candidate. The combined exact commit
must still pass integration/review/CI. A rollout or production observation already
in progress does not block independent preparation of the next candidate.

Common deployment requires explicit owner adoption: name the protected candidate,
per-Issue acceptance matrix, prepared scripts, write owner and read-only peers.
The owner performs shared writes and cleanup; peers do read-only acceptance on the
same protected tuple. Do not queue ordinary per-Issue deployments for contributions
already accepted into this window. Unfinished children retain their actual state.
Do not substitute common health for each Issue's acceptance.

Use the repository's candidate capability. When `prepare --freeze` and
`dispatch --prepared` are installed and verified, bind the immutable readiness
receipt; main may advance without changing it. Baseline, workflow, companion or
CI identity drift still requires new preparation. Otherwise preserve the legacy
latest-main gate with a bounded final merge window, not a hold through observation.

Publish current decisions with Squad's decision-set CAS and consume its durable
receipts. Terminal input, including an automatic suggestion, does not block event
publication through that channel. Never submit a suggestion as user authority or
overwrite an unknown human draft. Record published, acknowledged and adopted
separately. A failed receiver needs bounded recovery through its existing runtime.

Before closure, reconcile task completion, pending operations and the reservation
with its authorized owner. Do not assume a completed task closed its reservation.
Use typed operation receipts in the checkpoint: a submitted/uncertain/completed
intent cannot be the next dispatch; reconcile it instead. The context validator
rejects that stale-next-action state. Existing untyped checkpoints need a verified
conversion; their prose alone cannot prove absence of an operation.
