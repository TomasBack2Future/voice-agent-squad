# Dispatcher-owned design admission

The Dispatcher owns the product decision and cross-Issue coherence before
implementation starts. Use its existing global view; do not add a mandatory
Designer role, another review committee or an extra human approval round.
Investigator supplies missing facts. Worker implements the admitted decision.

## Decide with bounded context

For a new feature or changed behavior, inspect the relevant existing user flow,
terminology, entry points/routes and owning contracts. Compare related active
Issues, not just touched files: separate implementations may still duplicate a
product concept or disagree about shared behavior. Cite concrete evidence and
distinguish observations from proposed decisions. A detailed Issue or passing
technical tests does not by itself prove the product design is coherent.

For a small defect with established correct behavior, a short statement of that
behavior, evidence and acceptance is enough. Reuse an adequate existing spec by
reference. Do not force every Issue through an investigation or generate a new
design document to satisfy a template.

When evidence is missing, use one bounded Investigator assignment containing
the specific question, relevant Issue/repository, evidence expected and stopping
condition. Reconcile existing ownership before dispatch; do not create a duplicate
investigation or implementation Worker. Use the installed reservation/session
mechanism and count nonterminal investigations in the same WIP budget. If that
runtime cannot represent the role, record the gap rather than disguising an
Investigator as an implementation Worker. Investigation returns evidence to the
same Issue; the Dispatcher makes or revises the product decision.

Ask the user only for a material product tradeoff that cannot be resolved from
existing intent/authority. Ordinary naming consistency, existing behavior and
routine implementation decisions do not create new approval requirements.

## Canonical Issue decision

Maintain a clearly delimited `## Design / admission` section in the existing
Issue body. Preserve the original report, requested outcomes and unrelated
sections. Read the latest body before an update; if it changed, reconcile rather
than overwrite another author's changes. Do not treat GitHub body editing as an
atomic compare-and-swap. Verify the result after writing. Use `gh` with a body
file, and avoid rewriting the Issue on every unchanged dispatch cycle.

Record proportionate detail:

- Revision and disposition: `READY`, `needs-investigation` or `needs-decision`.
- User outcome and non-goals; existing behavior for a small repair.
- Product consistency: chosen terms, entry point and user flow; evidence of
  overlap/conflict checks against existing functionality.
- Issue consistency: related work, explicit dependencies, shared decisions,
  implementation boundaries and compatibility/migration requirements.
- Acceptance: observable user scenarios, including relevant error/recovery
  behavior; required environment capabilities and unresolved prerequisites.
- Evidence references and remaining questions. Blocking questions preclude READY.

These dispositions belong to this section; they are not assumed to exist as
Squad enum values or GitHub labels. Do not confuse product/design READY with
release readiness. A verified downstream environment dependency can remain
pending while independent implementation proceeds; an unresolved product
boundary or contradiction cannot.

The Issue section is the decision authority. A referenced spec can supply detail
but must not become a conflicting second status/decision source. Use an explicit
revision such as `d1`; increment it when product decisions or acceptance change.
Assignments carry the Issue/section locator and revision, not the full text.
For Agent Loop envelopes use `design_admission: {"section": "Design / admission",
"revision": "d1", "status": "READY"}`. This field describes the referenced Issue
decision; it does not replace live verification or confer execution authority.
Preserve a brief supersession reason/evidence when replacing a decision.

Example: an existing `/reports` area already means evaluation comparisons.
A proposed issue-submission page must explicitly resolve terminology and entry
point overlap before READY. Similar-looking URLs are evidence of possible user
confusion, not proof of an HTTP routing collision. The final naming choice comes
from the product context; do not hard-code a universal replacement route.

## Dispatch and change handling

Before reserving a new implementation Worker, verify the READY revision against
the latest relevant Issue requirements, active dependencies and product evidence.
Refresh affected decisions when those inputs change; do not reread the whole
product on every heartbeat. For several children sharing a design, reference the
same decision and give each child bounded scope and acceptance ownership.

Keep conceptual conflicts separate from scheduling edges: fix inconsistent
semantics in the design; add a dependency only when evidence establishes an
actual prerequisite or shared-resource constraint. Mere similar names or nearby
Issue numbers do not justify serialization.

The Worker reads the assigned revision before product mutation. Missing/stale
admission or contradictory requirements trigger one concrete Dispatcher decision
request. New product semantics, entry points, scope or cross-Issue API behavior
need a design revision; ordinary algorithms, code layout and fixes within the
accepted behavior remain Worker decisions. Continue independent authorized work
while waiting. Design correction does not renew or expand execution authority.

For active Workers, do not retroactively mass-restart, revoke reservations or
send routine "reload skill" messages. Apply this contract to new assignments.
If an actual design conflict affects active work, record the decision first,
then send one verified issue-local scope correction at a safe boundary, retaining
valid implementation and existing claim/ENV safety rules.

On completion, verify acceptance against the admitted revision as well as the
technical evidence. Track post-dispatch product-decision changes and acceptance
rework when available; the goal is less rework, not more documents or roles.
