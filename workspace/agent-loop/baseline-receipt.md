# Environment baseline receipt template

Copy this into restricted operational evidence storage and complete it for one
observation window. Do not keep filled live status in repository architecture or
contributor documentation. A source/doc PR can improve the contract without
claiming the environment already passes it.

```markdown
## Identity and provenance
Observation UTC window / observer:
Environment role:
Platform / explicit kubeconfig and context references / namespace / release:
Or explicit host and Compose project:
Selected source contract SHA / runbook version:
Routed API / frontend release and exact revisions:
Workflow run ID, workflow/tooling SHA, requested deploy/release SHA:
Image index/platform digest mapping and actual workload image IDs:

## Component evidence
| Component | Expected identity | Observed identity | Readiness / restarts | Evidence / disposition |
| --- | --- | --- | --- | --- |
| API and its business Interceptor | | | | pending |
| Control Plane | | | | pending |
| Internal Import Worker | | | | pending |
| Frontend and edge route | | | | pending |
| Execution fleet and its companion Interceptors | | | | pending |
| External Importer / state owner / exporter | | | | pending |

## Functional and recovery evidence
Applicable exact-release CI and acceptance receipts:
Functional checks and target revision (read-only versus mutating):
SLS/observability window and coverage:
Schema/queue compatibility and explicit read limits:
Backup/restore rehearsal and rollback references:
Outstanding deployment or migration operations:
Missing, denied, skipped or contradictory evidence:
Decision: verified / incomplete / unhealthy, with rationale:
```

Use `not-applicable` with a reason, never a blank implicitly counted as passed.
Record independent Helm releases separately; API/worker/frontend can legitimately
have different source revisions, but each must match its accepted release cohort.
The fleet and Importer are not automatically rolled forward by an API Helm action.
Importer source environment, deployment target and Studio destination are distinct.

Read-only discovery does not authorize acceptance that creates Sessions/Runs,
changes queues, deploys, recovers locks or queries unrestricted customer data.
Reuse retained exact-release evidence where it is applicable and fresh enough;
otherwise leave that row pending for separately authorized validation. Do not
dispatch a workflow merely to produce a green baseline.

A workflow's head SHA may describe tooling rather than the deployed image. A
successful prior Action does not establish current health; a failed recent Action
may have restored a healthy prior revision. Reconcile its rollback/preservation
outcome and current routing before classifying the live environment.
