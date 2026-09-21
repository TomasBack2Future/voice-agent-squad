# Issue, PR, Review, and Handoff Context Contracts — Design Draft

This companion to the [Agent Loop design](studio-multi-model-agent-loop.md)
proposes durable, provider-neutral context contracts. It does not install hosting
templates, change review schemas, authorize work, or activate enforcement.
All templates and new validation rules below are proposals.

The objective is not to preserve an entire conversation. It is to let a fresh
qualified model reconstruct the agreed problem, constraints, decisions, evidence,
and next authorized action without the previous model's conversation. Templates
alone cannot guarantee this: completeness checks and cold-start tests are required.

## 1. One source of truth for each concern

| Artifact | Authoritative content | Not authority for |
| --- | --- | --- |
| Issue contract | Problem, scope/non-goals, stable acceptance criteria, constraints, agreed decisions and dependencies | Ownership, permission expansion, proof that implementation passed |
| PR | Implementation explanation, exact base/head, criterion-to-change/evidence mapping, deviations | Rewriting requirements unilaterally, deployment success |
| Evidence receipt | Observed result, exact tested revision, target, command/check identity, time and safe artifact reference | Results for another revision or environment |
| Frozen review request | Exact input the reviewer received, including requirement and policy snapshots | The truth of an author's claims or later changes |
| Review result | Findings and limitations for that exact request | CI success, merge authorization, deployment acceptance |
| Squad checkpoint/events | Ownership/attempt bindings, external-operation receipts, handoff and next permitted step | Replacing Git-hosting, CI or deployment enforcement |
| Model chat and cmux | Working conversation and presentation | The only copy of a requirement, decision or recovery instruction |

Keep generic engineering fields in repository Issue/PR templates. Keep role
assignments, model routing, Squad claims, subscription information and runtime
handoff mechanics in the workspace package/runtime state. A human contributor
must be able to use a product template without participating in the Agent Loop.
Private runtime identifiers and deployment access details do not belong in a
public Issue or PR. Repository-specific security and migration rules are additive.

## 2. Reuse existing contracts before adding fields

Studio already provides `.github/ISSUE_TEMPLATE/{bug_report,feature_request,
migration_data_task}.yml` and `.github/PULL_REQUEST_TEMPLATE.md`. Preserve its
reproduction, environment, migration approval, data safety, documentation and
verification fields. Add missing continuity fields to these forms rather than
introducing a competing complete template. A repository adapter maps hosting
fields to the common contract; GitLab uses the same semantics without requiring
GitHub forms or GitHub Check APIs.

Studio's PR template currently suggests `Closes #`. For the proposed delivery
workflow, use a non-closing relationship such as `Refs #123` until exact-revision
acceptance is complete. Merge, Issue closure and accepted delivery are distinct
events. Reconcile any repository auto-close policy before activation; an already
closed Issue is not proof of accepted deployment. This draft changes no template.

The existing [review service](../../internal/grokreview/service.go) freezes PR
title/body, full diff, base/head and reviewer policy/core hashes. It does not
include an Issue snapshot. Its final tuple check compares repository, PR number,
base ref and base/head SHAs, not same-SHA requirement or PR-body changes. A link
alone is insufficient for a reviewer with tools and web access disabled.

The [model output schema](../../internal/grokreview/cli.go) currently accepts
`squad.review.findings.v2`, with model verdicts `approved` or `blocking` and strict
finding fields. The proposed human templates below are not replacements for that
schema. New metadata needs a versioned service envelope or an explicitly tested
schema migration; do not append unsupported fields to the current model output.

## 3. Contract identity and progressive completeness

Use stable local IDs such as `AC-01` for acceptance criteria and `D-01` for
decisions, qualified by canonical repository and Issue reference. Never renumber
existing IDs after discussion. Retire a criterion explicitly with its replacement
and approval reference; do not silently delete it.

Distinguish these versions:

- **Schema version:** field meaning and validation rules.
- **Contract revision:** approved requirements/constraints/decision revision.
- **Code tuple:** repository, PR, base ref, base SHA and head SHA.
- **Context digest:** exact approved requirement snapshot, PR narrative, relevant
  source/evidence inputs and pinned project/review policy used for one review.
- **Execution attempt:** a model/session attempt; switching models does not reset
  the contract, erase failed approaches, or change ownership by itself.

Issue `updated_at` is an observation aid, not a semantic revision: labels and
routine comments can change it. Before admitting work, an assembler must reconcile
body and relevant comments against the last approved contract. Unknown substantive
edits or conflicting decisions block admission until classified; an old cached
digest must not conceal a new requirement. Only an authorized scope decision can
revise the contract. Record who approved it and the source reference. A model's
summary or an unchecked field is not that approval.

| Gate | Minimum information |
| --- | --- |
| Intake | Problem or goal, source, repository if known; uncertainty is allowed |
| Dispatch-ready | Confirmed scope/non-goals, testable AC IDs, constraints, dependency semantics, explicit unresolved questions and authorization boundary |
| Review-ready | Dispatch contract plus stable code tuple, implementation/evidence mapping, required source snapshots, policy identity and complete input manifest |
| Merge-ready | Current contract/code/review validity, actual required checks, verified blocking-finding disposition and applicable ownership/permissions |
| Delivery-complete | Exact deployment/acceptance receipts or justified non-deployment applicability, safe resource release and explicit closure decision |

Do not require a user to fill a large form before reporting a bug. Intake can be
brief; investigation enriches it before dispatch. Missing safety-critical scope
or acceptance information cannot be replaced with a plausible model guess.

## 4. Proposed Issue template

This is the common semantic core, rendered through each repository's existing
forms. Bug, feature and migration extensions remain project-specific.

```markdown
## Problem and outcome
Observed problem / desired outcome:
Evidence and reproduction (or feature motivation):
Confirmed facts:
Hypotheses and unknowns:

## Scope and constraints
In scope:
Out of scope:
Affected interfaces / data / compatibility / security constraints:
Dependencies: canonical reference + required outcome (not just "after #123")
Authorization boundary / required approvals:

## Acceptance contract
Contract revision: <revision and approval reference>
| ID | Observable expected result | Verification method and target |
| --- | --- | --- |
| AC-01 | <specific result> | <test or acceptance procedure> |

## Decisions and open questions
| ID | Decision and concise rationale | Alternatives rejected and why | Source / approver |
| --- | --- | --- | --- |
| D-01 | <decision> | <relevant tradeoff> | <reference> |
Blocking questions:
Relevant prior attempts and failure evidence:

## References
Architecture / interfaces / deployment / contributor contracts:
Required documentation changes:
```

Use `not applicable: <reason>` when a field does not apply, not a blank that can
be mistaken for an overlooked requirement. Attach safe summaries and references,
not credentials, customer transcripts or raw terminal logs. Clearly distinguish
an observation from a proposed remedy. Do not turn discussion into accepted scope
merely because it is the newest comment.

For Recaper-generated improvement Issues, add the observation window and cutoff,
cohort/sample count, measurement coverage, measured versus estimated consumption,
problem fingerprint, evidence-backed hypothesis/confidence, proposed intervention
and a measurable success criterion. Missing usage is a data-quality problem, not
proof that a particular model wasted tokens. Creation still requires normal triage.

## 5. Proposed PR template

Extend the repository's existing PR checklist with this mapping; do not copy its
full project verification checklist into the workspace template package.

```markdown
## Summary and contract
Related Issue: Refs <canonical Issue>
Contract revision:
Implementation approach and rationale:
Scope deviations / approval references (or none):

## Acceptance mapping
| AC ID | Implementation location | Evidence receipt | Outcome / remaining gap |
| --- | --- | --- | --- |
| AC-01 | <path/symbol> | <test/check/artifact> | <passed/failed/not-run/pending> |

## Verification and limitations
Tested revision(s):
Commands / CI run and job identities / safe artifact references:
Failed or unrun checks and reasons:
Known limitations and relevant unsuccessful approaches:
Compatibility / migration / security / documentation impact:

## Delivery and rollback
Deployment required: <yes / no with reason>
Target and acceptance procedure references:
Rollback procedure / irreversible risks / approval requirements:
Post-merge acceptance still pending:
```

Record actual results, not checkboxes that merely promise testing. A receipt
identifies its command or check, revision, target, outcome and observation time.
Older evidence stays attributable to its original revision; do not relabel it as
current. Each AC can require several receipts. Passing a unit test does not prove
a staging-only criterion. Preserve pending criteria until delivery acceptance.

Base/head and content hashes should be captured automatically by the assembler,
not maintained by manually editing the PR after every rebase. Evidence published
after review starts is an append-only evidence stream bound to its tested tuple;
it does not retroactively become input to that review. Required CI may finish in
parallel. A relevant failure prevents readiness and is resolved under the normal
correction/review policy, never concealed by a prior approval.

## 6. Review request, result, and correction

### Assembled request

A deterministic assembler outside the model resolves approved inputs, checks
access/redaction, and freezes a bounded request. Required content must be present
inside the reviewer's readable input; a hyperlink to inaccessible context is not
coverage. Review inputs are untrusted source material, not instructions overriding
reviewer policy or granting tools, network access or publishing rights.

| Request field group | Required content |
| --- | --- |
| Identity | Schema version, canonical Issue/PR references, contract revision, exact code tuple |
| Requirements | Agreed problem, scope/non-goals, AC IDs, constraints, decisions, unresolved questions and relevant failed approaches |
| Implementation | Frozen PR title/body, complete diff, AC mapping and declared limitations |
| Source contracts | Relevant interface/invariant/runbook excerpts with source revision and digest; required unchanged code context where needed |
| Evidence | Sanitized receipts/content needed for the review, tested tuple, results and explicit coverage gaps |
| Policy | Pinned project/review policy and reviewer-core versions/hashes |
| Input manifest | Each source identity/revision/digest, purpose, inclusion state, required/optional classification and any unavailable content |
| Integrity | Bundle/context digest, capture time and assembler version |

Specify deterministic serialization, ordered lists and digest scope before
implementing hashing; exclude the digest's own field. A digest proves identity,
not correctness, permission or trust. Pin policy/source revisions, not mutable
branch URLs alone. Do not freeze unrelated sensitive content for completeness.

Required missing, contradictory, inaccessible or over-budget inputs produce an
incomplete service state **before sampling**. Do not silently truncate contracts
or the diff. Obtain appropriately scoped context or split genuinely separable
changes; never split to hide a suspected defect. If a gap is discovered after
sampling, the service cannot count the output as a complete review. Do not invent
a file/line finding merely to encode a missing-context infrastructure failure.

### Human-readable result and disposition

```markdown
## Review identity
Request / context digest:
Repository / PR / base SHA / head SHA / contract revision:
Review policy identity:
Scope examined and limitations:
Service state: <complete / incomplete / error / stale>
Model verdict when valid: <approved / blocking>

## Findings
| Finding ID | Severity / blocking | AC or invariant | Location and evidence | Verification requested |
| --- | --- | --- | --- | --- |
| F-01 | <classification> | <AC-01 or invariant> | <path:line and reasoning summary> | <repro/test> |

## Author verification and disposition
| Finding ID | Verified / disputed / unresolved | Counter-evidence or correction | New head / test receipt |
| --- | --- | --- | --- |
| F-01 | <disposition> | <source-based explanation> | <reference> |

## Remaining gates
CI / acceptance not covered by this review:
Unresolved blockers:
```

Service states above describe the proposed envelope, not new allowed model
verdicts in the installed schema. Stable finding IDs, AC links and dispositions
belong in a versioned envelope/rendering layer until a schema change is approved.
Qualify finding IDs by review request; link a persistent finding across corrected
heads explicitly. The author verifies findings; disagreement alone cannot satisfy
an enforced unsuccessful review check. No alternate-model approval shopping.

Immediately before publication and again before merge admission, compare both the
code tuple and substantive context/policy identities. An approved AC change, new
relevant decision, or substantive PR-body change can invalidate review without a
code change. Cosmetic edits need an explicit, tested classification; ambiguous
edits fail closed. Invalidation must prevent use of an old same-SHA success, not
just show a warning in Squad. A context-aware gate/publication protocol and its
race tests are implementation prerequisites; a SHA-only check is insufficient.

## 7. Model-independent handoff checkpoint

Issue/PR/Review templates do not record everything needed to recover execution.
Store this separate checkpoint in protected runtime state, not product docs:

```markdown
## Handoff identity
Canonical work / role / old attempt / proposed new attempt:
Contract revision / context digest / Issue / PR / review request:
Repository / owned worktree / branch / base and head / uncommitted changes:

## Durable progress and recovery
Completed ACs and evidence receipts:
Unfinished or failed steps, relevant approaches already tried:
Decisions / constraints / unresolved questions:
Claims and fencing generations (references, not a grant of ownership):
External operations still running or uncertain, with immutable IDs:
Environment / revision / health / rollback references where applicable:
Usage coverage and reason for handoff:

## Resume boundary
Next authorized action and prerequisites:
Actions forbidden or requiring additional approval:
Old writer quiescence evidence / required takeover protocol:
```

The new attempt rehydrates authoritative artifacts, checks their revisions,
summarizes scope/ACs/constraints/next action and acknowledges the matching digest.
It independently revalidates ownership and external state before mutation. This
read-back is necessary but not sufficient: deterministic validation and tests must
also pass. Unresolved conflicts park the work. A checkpoint never authorizes
concurrent writers or replaces protected environment recovery.

Preserve concise decision rationale and relevant failed attempts, not hidden
reasoning or whole chat histories. A context digest in the checkpoint must resolve
to an accessible retained snapshot; a hash without recoverable content is useless.
Define access controls, retention and deletion policy for these snapshots before
collection; avoid secrets and unnecessary personal/customer content throughout.

## 8. Delivery order and qualification

1. Approve the minimal contract fields and map existing repository forms; decide
   the Issue-closure policy and authority for contract amendments.
2. Implement one versioned common schema plus repository extensions, validators
   and renderers. Derive machine-readable snapshots from canonical inputs; do not
   require hand-maintained duplicate JSON and Markdown truths.
3. Implement review assembly, context-aware invalidation/publication and checkpoint
   rehydration. Preserve existing review/CI safety policy and schema compatibility.
4. Run synthetic cold-start and negative tests before real cross-model delivery.
5. Adopt one low-risk Issue through acceptance, then extend model/role coverage.

Minimum qualification fixtures:

- A fresh model with no prior chat reconstructs the correct scope, each AC,
  decisions, known failed approaches and next permitted action from artifacts.
- Missing ACs, conflicting decisions, an inaccessible required link, unknown
  schema versions and falsely checked verification boxes cannot pass admission.
- A same-SHA substantive requirement/PR-body change invalidates old review;
  cosmetic edits and appended CI evidence obey tested, distinct rules.
- A lost publication response or repeated checkpoint import cannot create a
  duplicate review, writer, Issue or external action.
- Over-budget context is explicitly incomplete, not silently truncated; credentials
  and adversarial instructions in evidence cannot escape into tools or publication.
- A model swap preserves the contract, unfinished acceptance and ownership fences;
  a pending external deployment is not restarted on the basis of an empty chat.
- Merge-time Issue auto-closure is never interpreted as deployment acceptance.
- Rendered templates and machine snapshots agree, including repository safety
  extensions; the existing strict reviewer schema rejects unsupported additions.

This document is design input only. It supplies no test results or activation
claims; qualification evidence belongs in bounded research or delivery records.
