# Candidate preparation and shared staging acceptance

Use this contract for an explicitly assigned `staging-candidate-v1` delivery.
It changes environment acquisition, not product scope, review policy or claim
ownership. Batch contribution transfer still follows [batch delivery](batch-delivery.md).

## Activate only against an implemented release path

The Dispatcher records one verified capability reference for the repository:
the installed workflow revision, tested candidate preparation command, explicit
deployment command, automatic rollout suppression, immutable component/CI binding
and duplicate-dispatch reconciliation. Read that repository contract rather than
inventing command flags. A skill install or a feature branch is not activation.
If CI/prefetch can still mutate staging automatically, retain legacy acquire-before-
merge delivery. Ship the release-path change using that old protected path first.

Once activated, prepare outside ENV: review and integration checks, policy-compliant
main merge, terminal main CI, immutable images, completed registry prefetch,
executable acceptance scripts and access. Only the assigned release owner claims
ENV, immediately before the first shared environment mutation. Revalidate the
exact candidate tuple, admission and deployed baseline after acquisition. If
superseded, prepare the newly selected candidate outside ENV; never deploy an
unverified newer main or occupy ENV while waiting for a build.

Repository policy determines which prepared SHA is admissible (including any
latest-main requirement). This contract does not waive it. Freeze all component
SHAs/digests; a later main push must neither auto-deploy nor change the running
acceptance tuple. Record the workflow run IDs. An uncertain dispatch response
requires lookup by the original candidate/attempt, not blind redispatch.

## Integrate and accept one batch

Use one existing owner and a bounded set of compatible contributions. Check route,
API, schema, configuration and fixture conflicts as well as file overlap. Prefer
one integration branch and one final main PR; verify the combined diff and current
base. Child green checks alone do not prove the combined candidate. Never retarget
or merge a frozen child PR without its explicit ownership handoff. A single ready
Issue can take the same candidate path; do not wait for an unfinished batch mate.

The environment owner deploys once and records the protected version and acceptance
window. Assigned peers can run their issue-local read-only API/UI acceptance in
parallel without claiming ENV. The owner performs shared writes unless a supported
delegation mechanism explicitly admits them; disjoint fixture names alone do not
grant write authority. Serialize conflicting operations. Each Issue retains its
own AC results; health, common smoke or another Issue's pass cannot close it.

Finish required version-sensitive checks and cleanup before releasing ENV. Release
before final comments, attest bookkeeping or Issue closure. Retain ownership during
unsafe or unknown operations and use the existing bounded recovery policy. No
rollback rehearsal. A failing child remains open even when the common rollout is
healthy; record whether its failure needs the protected environment before deciding
to release. Never release solely to improve the timing metric.

## Existing Workers and measurements

Loading new files does not change a running assignment. The Dispatcher sends one
authorized migration decision stating the mode, capability reference, exact batch
and candidate owner, scope, next allowed action and handoff boundary. The Worker
records acceptance before switching. Preserve its native identity and reservation.
A waiting Worker cancels only its own wait at a safe boundary if needed; it checks
whether acquisition won the race and reports any held ENV before transition.
Never kill/restart a deployment, force-release a holder, or silently overwrite a
Worker's instructions. An in-flight rollout completes under its existing contract.

Use existing batch/item evidence for timestamps: ready, wait start, ENV acquired,
deploy started, rollout ready, common checks done, per-Issue acceptance done and
ENV released. Include CI/prefetch runs, unique candidate/deploy counts and retries.
Report wall time separately from summed parallel job time. Distinguish queue wait,
environment occupation, actual rollout and validation. Compare real batches to the
same baseline boundaries; simulated tests and estimated savings are not measured
delivery improvements. Unknown timestamps remain unknown.
