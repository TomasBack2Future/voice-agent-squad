# Staging gate, access and acceptance evidence

Applies to the ordinary Issue owner and the batch integration/release owner.
A batch developer transfers these obligations; it does not independently deploy.

## Existing access before asking the user

For deployment/recovery (not application-log retrieval), first resolve the
existing project runbook, configured workflow and user-provided cluster entry.
Check explicit KUBECONFIG paths, context metadata and known shell aliases or
functions such as `kuap`. A non-interactive shell may not load the user's
interactive zsh configuration; absence in that shell does not prove missing
credentials. Inspect only the named entry and sanitized path/context metadata,
not the entire shell configuration, environment, kubeconfig or Secret contents.

Do not blindly execute an alias/function or eval its expansion. Resolve its
target and options, then use explicit kubeconfig/context/namespace arguments for
read-only identity/readiness checks. Confirm the intended staging cluster,
namespace and releases; never change the default context, substitute production,
extract workflow Secrets or bypass a permissions denial. Existing access is not
ownership: all shared mutations still require the Issue/primary and ENV claims.
If documented local/workflow access is unavailable or mismatched, report the
specific failed check and ask only for a missing path/context or access action,
never secret values. Do not keep rediscovering access on every operation.

## Capacity probes and EKlet scheduling

Before a capacity probe, compare its scheduling requirements with the target
workload's reviewed Pod template and the live admitted Pod spec: routing labels
and annotations, node selectors/affinity, tolerations, resource requests and the
required concurrent Pod count. Use the intended workload's supported scheduling
contract; do not invent a `fargate` label or infer the execution path from its
absence. For a workload intended for EKlet, explicitly carry its required EKlet
selection and tolerations into the probe rather than relying on later automatic
injection. Keep probe labels distinct from application Service selectors. A
smaller or differently scheduled probe does not prove rollout capacity.

When the Studio assignment requires explicit EKlet node affinity, include this
under the Pod `spec` (a Deployment uses `spec.template.spec`):

```yaml
affinity:
  nodeAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
        - matchExpressions:
            - key: node.kubernetes.io/instance-type
              operator: In
              values:
                - eklet
```

A matching `nodeSelector`, a toleration, preferred affinity or an eventual EKlet
node assignment does not prove this explicit required-affinity field exists.
Preserve compatible existing constraints; do not append an OR alternative that
allows non-EKlet nodes or overwrite unrelated affinity rules. Compare the exact
candidate's rendered workload templates, server-side dry-run results and actual
created Pod specs separately. Dry-run success is not evidence of a running Pod.
Check every affected component, including execution-worker a/b and Stateful
Interceptor; API/frontend affinity does not establish their configuration.
Record any gap between the probe and the release templates. A probe-only change
does not fix the chart or prove the next deployment will carry the field; route
required template changes through the source/review/release contract without
silently mutating a frozen candidate or the live workloads.

Retain the submitted and admitted scheduling fields and the timestamped events
for each owned probe Pod UID before cleanup. Follow the full sequence: admission,
FailedScheduling, assignment to a node, sandbox/container creation, image pull,
and Running/Ready. Distinguish an initial missing toleration from a later failure:
if the cluster injects the toleration and assigns the Pod to an EKlet node, then
`QuotaMaxClusterPodsLimit` is evidence of a subsequent cluster Pod quota blocker.
Do not assume a scheduling correction resolves that quota error, or that a past
quota error predetermines the outcome of an explicitly authorized corrected
probe. Report the actual retry result; a later pass establishes capacity for that
attempt, not that affinity was the sole cause (available capacity may also change).
Conversely, Pending or a timeout alone does not establish exhausted
capacity; record the actual failed condition and remaining uncertainty. Repeated
event counts are not counts of distinct failed Pods or a measured quota deficit.
Keep the raw error code/message and its scope, for example
`FailedCreatePodSandBox` with `UnsupportedOperation.QuotaMaxClusterPodsLimit`;
do not derive a quota increase from apparently conflicting limit fields alone.
An explicit one-time retry authorizes only that attempt: record the changed
configuration, actual Pod readback, runtime result and cleanup, not just dry-run
success, and do not turn it into automatic repeated retries.

On failure, clean up only owned probe resources and verify their removal. Release
ENV only when no protected operation remains; report whether deployment actually
started and the verified running revision. Preserve applicable image prefetch,
frozen-candidate and preflight evidence, rechecking time-sensitive capacity before
retrying. Send the result through the existing formal Dispatcher event route with
the scheduling defect and quota blocker separated, the quota/environment owner,
next action and recovery condition. If that owner has not accepted the handoff,
report it as pending, not resolved. Do not repeat the unchanged probe, increase
quota, alter shared scheduling or delete other workloads without the applicable
authority and evidence of a changed condition.

## Studio database environment

Before direct database access or a deployment configuration change, read the
Studio profile's [Couchbase environment contract](../../../agent-loop/projects/studio/references/couchbase-environments.md).
Select the environment explicitly; staging and production no longer share the
same Studio cluster. Runtime application credentials do not authorize index DDL.

## Merge and deployment gate

Select resources by the deployed component and operation. Preserve `ENV-001`
and `ENV-002`: after the scoped resource policy and runtime are installed, use
`--scope studio` for Studio-only work. Omitted scope retains legacy whole-environment
coverage. Independent Importer/Feedback changes use their dedicated resource IDs
from the project profile; ordinary Studio API calls do not add a Studio lock.
A frontend feedback change is still Studio work. Cross-service exact-revision
acceptance acquires all required resources in lexicographic item-ID order. Never
upgrade a held scope, wait on your own overlapping claim, or release an unsafe
operation to resolve a deadlock. On a detected cycle, stop the new wait and report
the chain. Stopped-holder recovery remains fenced.

Apply [candidate delivery](staging-candidates.md) when the explicit assignment and
verified repository capability activate it: prepare main CI/images/prefetch before
claiming, then revalidate the immutable candidate and current staging. In legacy
mode, require the exact PR head to be based on latest `main`, mergeable and green
before claiming, and re-read those facts after acquisition. Never mix the late
claim timing with a release chain that can still deploy automatically.

Record:

- PR head and merge SHA;
- Actions build/deploy run ids and terminal conclusions;
- immutable image digests plus OCI revision labels for every release component;
- running release/revision and required workload readiness;
- feature-level acceptance inputs, expected/actual counts and timings;
- staging log evidence disposition described below;
- cleanup identifiers and independent zero-residual result;
- rollback revision and health evidence when rollback occurs.

Do not accept a mutable tag, a successful workflow alone, health alone, or an
old acceptance result for a different exact revision.

## Failure classification and safe recovery

Classify a failed acceptance as product/deployment, harness/configuration,
transient infrastructure, or unknown using the failing step, exact running
revision, mixed-revision check, readiness, data-integrity and availability
signals. Record the evidence; timeout or verifier exit code alone is not proof
of a product defect or harmless infrastructure failure.

Ordinary staging delivery must not perform rollback rehearsals or redeploy only
to reproduce valid evidence. Preserve a usable recovery path and last accepted
revision. Reuse batch evidence only when its component identity, selectors and
acceptance window cover this Issue; unrelated components need not share one SHA.
Read-only peers may validate the protected candidate concurrently; shared writes
still require the installed ownership contract and must not borrow another
Worker's ENV claim.

Brief staging unavailability is acceptable within a bounded forward-repair
window. For a diagnosed, repairable failure with data integrity and revision
identity established, record the repair, start, deadline and attempt; default to
one substantive repair/retry or 15 minutes, whichever comes first. A stricter
assignment limit wins. Do not reset the window by restarting a workflow. Run
required checks for corrections and revalidate the deployed tuple. This policy
does not disable an existing workflow's automatic failure recovery.

Recover immediately for possible data corruption, uncontrolled mixed revisions
or unknown operation identity; also recover when the bounded repair fails or
expires. An uncertain verifier alone calls for bounded diagnosis, not an automatic
rehearsal or a request for the user to run commands. Use the last
accepted exact revision, not merely the previous Helm revision. Verify each
release's current revision/status, deployment attempt, rollout token and
captured recovery provenance against the live operation before invoking recovery.
A workflow rejecting an unrelated revision is a fencing/safety failure, not a
missing-credential diagnosis. Never replay an older attempt or use local access
to bypass that guard. Reconcile the tuple and supported recovery path first.

Verify the restored workloads and safe exact revision, then release ENV and
notify waiters. Failed or ambiguous recovery keeps ENV protected; record the
remaining operation/access blocker and notify the Dispatcher, not safe completion.
A healthy rollback is not Issue acceptance: continue the same Issue's fix/PR.

## Required staging log disposition

Before accepting a runtime-affecting Issue, use the maintained
the repository-owned `studio-sls-logs` skill selected by workspace routing for a bounded
post-deploy query covering its synthetic acceptance window and affected services.
Reuse the authorized shared helper credentials; never request AK/SK in chat or
use kubectl/Pod logs/Secrets as an SLS substitute.

Record UTC bounds (SLS native `__time__`), selectors, returned/truncated counts,
request/trace correlation when available, relevant sanitized event/error counts
and conclusion. Bind these to separate exact-SHA/digest evidence: do not invent
a revision field in SLS or claim a shared time window proves revision identity.
An empty result is inconclusive, not a clean pass; check selector/ingestion
coverage with the SLS skill's bounded widening rule. Missing/denied access or
insufficient coverage leaves required evidence pending, never implicitly passed.

Use one of `verified`, `pending`, or `not-applicable` with an evidence pointer
or specific reason in the acceptance matrix. A docs-only/UI-only change with no
affected structured-log path may use not-applicable after explaining why; no
generic exemption for runtime changes. Log errors require classification, not
automatic attribution to this Issue; green logs never replace functional tests.

For a batch, reuse one exact-release query set across children where its window
and selectors actually cover them, with a per-child mapping and conclusion.
The integration owner collects it before final closure; developers neither run
extra deployments nor claim pre-deploy log observations as final acceptance.
