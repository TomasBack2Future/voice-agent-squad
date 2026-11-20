# Worker task prompt contract

Every dispatched Worker prompt is self-contained and lets one owner reach the
original user outcome without returning for routine permission. Select the role
from the implementation repository, not the tracking Issue URL:
`studio-issue-worker` for Studio, `agent-loop-worker` with the matching profile
for Squad, Importer or Interceptor. Interceptor keeps its local review and human
PR handoff boundary. The Studio-specific parts below apply only to Studio work.

## Required content

A prompt carries these parts once. Ordinary steps inside them need no later
decision; amend the assignment only for a real change of scope, authority,
semantics, shared-resource order or risk.

1. **Identity**: repository, canonical Issue reference, Squad item, reservation
   key and generation, design-section locator and READY revision (no copied
   design body), runtime-tagged callback route (App thread/host or cmux
   workspace/surface/native session, `dispatcher_agent_id`). cmux ids identify a
   session but are not a message transport; without a verified message
   API/mailbox the Worker persists events for pending reconciliation. Never pass a
   native Claude UUID to an App thread API.
2. **Outcome and end condition**: the user result and the Issue's Verification
   block by locator and revision: every criterion with its layer (`unit`,
   `local`, `staging`, `production`), method, pass and not pass. A merged PR, a
   cleared review finding or a passing substitute fixture is not the end
   condition. Closure follows the layers: only `production` rows wait for the
   production deploy.
3. **Owner scope**: owned resources and fixtures, permitted operations (source,
   PR, merge, staging, production, model calls, configuration writes, Issue
   close), target environments, explicit exclusions, and retained fixtures or
   custody that must survive.
4. **Normal path**: the expected sequence; start conditions the owner watches
   itself through a tracked `operation run` watch (for example "after PR #N
   merges", "when deploy run R finishes", "when the provider key validates"),
   with no later notification from the Dispatcher; the batch stage it waits on,
   if any, as a hold that releases itself; the acceptance method and actual
   route (local client or owned browser against
   the real target, credential route, observation, cleanup) and the evidence
   layers agreed against the Issue. Pre-authorize conditional follow-on work:
   "assess X; if condition C holds, execute Y within this scope".
5. **Failure branches**: product failure → fix and re-verify affected conditions;
   acceptance method/fixture failure → correct locally, invalidate the earlier
   attempt; external fault → stop the affected step, record recovery condition
   and responsible party, continue independent work, no blind retry and no
   substitute binding presented as the original case; unknown → one bounded
   discriminating check; real boundary crossing → one decision request with a
   concrete proposal. First valid blocking review finding → implementer model
   transition per the review protocol; recurrence → compact design reassessment.
6. **Limits**: call/cost/time budget including retries, stop conditions,
   recovery boundary and cleanup.
7. **Delivery-capability receipt**: explicit target/authority, environment access
   path and live resource identity when available, browser/login path,
   user-selected permission mode and callback route, each marked verified,
   external prerequisite or not yet checked.

The prompt also states: successful authorized closeout needs no routine
Dispatcher approval and the owning Worker closes the Issue itself; a different
Issue keeps its own tracking identity and separate reservation; do not discover,
reserve, dispatch or start another Issue; do not perform, confirm or ask the user
about another Issue's acceptance or data mutation (dependencies appear only by
identifier and resolved/unresolved state); one owned GitHub progress summary at
material phase changes; a partial delivery says "本轮交付结束，Issue 未完成" and
preserves its continuation/custody route.

A Squad-tool repair assignment is valid only with the user's current designation
of that maintenance agent; business or production authorization never substitutes
for it. Before creating a fresh assignment apply
[existing-owner routing](session-continuity.md#existing-owners-and-supported-assignment-routes):
an authorized linked obligation may stay with a suitable live owner under its
existing primary binding. Never bind a native twice or frame same-Issue follow-up
as a handoff to a completed session.

Validate the rendered prompt with `assignment_contract.py` before launch.

## Studio template

```text
Invoke $studio-issue-worker. Process exactly <ISSUE-REF> using Squad item
<ITEM-ID>; dispatch reservation <RESERVATION-KEY> generation <N>. Callback:
<RUNTIME-ROUTE>, dispatcher <DISPATCHER-AGENT-ID>. Read the Issue, workspace
AGENTS.md and repository instructions first, then the design section
<DESIGN-SECTION-LOCATOR> revision <DESIGN-REVISION> (READY) before product edits.

Outcome: <USER RESULT>. Done means every row of the Issue's Verification block
<LOCATOR> revision <vN> passes at its stated layer, owned fixtures are cleaned
up and resources released. Commit the block's named tests and see them fail
before the fix. Rows at unit, local and staging close without a production
release; only production rows wait for it, with you parked on the release stage.

Scope: you own <RESOURCES/FIXTURES>; permitted: implementation, tests, PR,
policy-compliant merge, staging claim/deployment/rollback, acceptance, Issue/PR
updates and Issue closure<, PRODUCTION OPERATIONS IF EXPLICITLY ASSIGNED>;
excluded: <EXCLUSIONS>; retain: <RETAINED FIXTURES/CUSTODY>. This is standing
authorization for this Issue; ask only for a concrete missing credential, user-only
interaction, new sensitive-data disclosure, production mutation outside this
scope or material scope expansion.

Path: <EXPECTED SEQUENCE>. Start conditions you watch yourself with a tracked
operation run watch, without waiting to be told: <CONDITIONS>. Batch stage you
wait on, released automatically when recorded: <RESERVATION:PHASE or none>.
Acceptance route prepared during implementation: <METHOD, TARGET, CREDENTIAL
ROUTE, OBSERVATION, CLEANUP>. Conditional work already authorized: <IF C THEN Y>.

Failure handling inside this scope: follow the Worker failure branches (product,
method, external, unknown, boundary). Name the changed condition for every retry
and keep still-valid evidence. Budget: <CALLS/COST/TIME incl. retries>. Stop on:
<STOP CONDITIONS>. Recovery: <BOUNDARY>.

Staging mode <legacy|staging-candidate-v1> with capability evidence <REF or
not-activated>. Select ENV by component and operation from the project profile
(ENV-001/002 legacy calls preserved; acquire cross-service sets in lexicographic
order; a deadlock is never permission to release another task's claim). Use
foreground claim --wait for contention. Production needs a separate explicit
production assignment. Merge policy: <standing|release-required>; under
release-required, request the exact head/base release once when ready.

Close the Issue yourself after acceptance, logs and cleanup; no routine
Dispatcher approval. Do not discover, reserve, dispatch or start another Issue.
```

The Worker skill and its references own the rest: claim and worktree rules, the
purpose-based Python boundary, delivery-quality preflights
(`scripts/delivery-check.mjs`), the Grok review contract, staging access and SLS
evidence, acceptance-failure classification and rollback, and closeout. Do not
restate them in the prompt unless the assignment changes them.

## Batch prompt override (explicit opt-in only)

For an authorized batch append `delivery-mode: batch-v1`, exact manifest
revision, batch ID, role (`developer` or `integration-release`), primary release
item/reservation generation, child Issue/item/reservation generation, integration
branch, permitted paths, dependency/CI evidence and final acceptance owner, and
require `references/batch-delivery.md`. These supersede only the ordinary
single-Issue ownership and independent deploy/closure steps: developers stop at an
acknowledged frozen handoff and never deploy independently; the sole integration
owner accepts the transfer before editing, holds primary plus ENV at release and
owns exact-SHA per-child acceptance/closure. Keep batch-owned holds and active
reservations until acceptance and count paused/nonterminal tasks in WIP. A missing
handoff receipt blocks a second writer. A00 READY is not closure; A14 waits on
applicable implementations/evidence, not closed children. A child assignment never
releases other holds or creates Workers.

At startup state the role and exact final acceptance owner. A developer's final
handoff says “开发已交接，Issue 未完成”, links the recipient task and child PR,
states the PR's actual target/merge status, lists pending acceptance/log/cleanup
gates and next action, and keeps Issue/item/PR in the task title. Batch review
uses one managed `squad-grok-review` with explicit high effort for batch
coordination/CI or release changes, alongside full deterministic CI, under the
actual inactive/shadow/enforced policy.

One shared product decision may serve several children; each child still has
clear scope and acceptance ownership. Batch READY never substitutes for a current
design decision. For delivery optimization put the candidate contract reference
and activation evidence in the prompt/coordination metadata, not new strict
envelope keys; a live migration states the old/new mode, owner, next allowed
action and acknowledgment location.
