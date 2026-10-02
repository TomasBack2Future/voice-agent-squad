# Executable acceptance and interrupted ownership

Use before environment admission and when an acceptance operation stalls. Reuse
one concise section of the existing assignment/checkpoint, not another approval
form, daemon, role or independent ledger.

## Prepare before holding ENV

Map each required outcome to its command/script or necessary UI interaction,
immutable fixture, observable assertion and evidence location. Verify available
access and a cheap read-only baseline through the intended path. Prepare scripts
and negative-path fixtures before claiming ENV; any fixture creation still needs
its applicable write authorization and lock. For retry/cancel/recovery acceptance,
identify a supported eligible state and how to obtain it; do not discover mid-test
that only failed/cancelled records can be retried. Reuse valid retained evidence.
Separate correctness, environment health, performance objectives and review state.
Existing approved hard gates remain; do not invent latency gates from one sample.

Bind the plan to the configured API origin/Project and candidate/fixture provenance.
Check execution and ephemeral-cleanup scope separately, including the actual supported
cleanup method; credential presence is not sufficient. A retained fixture needs a
verified reference/state. When creation requires ENV, prepare its supported command,
definition and eligible expected state before the lock, then verify the created
fixture under ownership before the assertion. Do not require an unauthorized write
to pass preparation or claim that a prepared fixture already exists. For retained
writes, record custody instead of inventing a deletion API. Identify login/MFA or
other human-only prerequisites early and only from verified capability evidence.

Use the existing read-only delivery preflight's `acceptance-readiness` action where
that helper is available; its compact fields are defined by the
[executable fixtures](../../../../coordination-skills/studio-issue-worker/scripts/delivery-check.test.mjs).
This checks evidence completeness, not actual access or permission. Retain the
checked plan in the current checkpoint, and recheck only
changed/stale inputs. Incomplete readiness blocks ENV admission, not independent code
or script preparation; assign one preparation owner and an explicit resume condition.

Use the UI for the user interaction being accepted. Use supported authenticated
API scripts for hashes, frozen metadata, pagination and final-state assertions.
Keep UI authentication in its supported browser context when required; never
export cookies or credentials to simplify a script. For browser-only access,
use bounded asynchronous requests and sanitized results rather than a long
synchronous eval. Choose explicit timeouts and bounded polling appropriate to
the operation; do not treat the browser bridge's timeout as server duration.

## Diagnose a timeout once, with attribution

Record the operation, UTC window, component revision, expected status/semantic
result, and request/trace ID when available. Distinguish tool cancellation,
client/eval timeout, network/edge wait, server error and unfinished external work.
Correlate the same request with structured server evidence before assigning the
cause to the server, browser or change. Successful unrelated requests do not
exonerate a failed request; empty or truncated logs do not prove absence.

After two no-progress observations, or the existing admission's diagnostic budget,
perform one bounded diagnosis before another attempt. Change the failed condition
or execution method before retrying. Do not repeatedly reopen panes or launch
untracked requests. Reuse completed portions of acceptance and report remaining
assertions explicitly. A healthy revision is not functional acceptance; an
acceptance timeout is not proof that rollout failed. Classify failing control
fixtures before attributing them to the new feature.

For a shared failure, report its run/trace, exact component identity and failed
assertion once, then follow the
[shared-blocker contract](operational-readiness.md#one-shared-blocker-one-repair-owner).
A transient error permits only the existing policy's safe bounded retry
(default at most one when no budget
is specified); verify prior operations are terminal or safely idempotent first.
An unchanged deterministic error needs a verified changed condition before rerun.
Do not discard other accepted assertions or group unrelated error codes as one
cause. An unresolved external mutation never becomes permission for a new attempt.

A review timeout/error has no verdict. Follow actual enforcement policy, retain
the unresolved review status, and repair the operational cause before resampling;
never relabel an empty findings list as approval. Static review does not replace
runtime correctness or transport diagnosis.

## Interrupted while owning an environment

Honor explicit stop and user-rejected tool results. Do not bypass a denial,
automatically resume the rejected operation, or infer user intent from a generic
client cancellation label. If the stop permits a handoff, record the primary/ENV
claims, last verified revision, external operations, remaining assertions and
reason. If execution was stopped immediately, Dispatcher reconstructs this
read-only in its next existing reconciliation cycle; do not require another
Worker tool call to make the pause visible.

Paused with ENV is an actionable coordination state, not ordinary lock contention
or proof of session death. Dispatcher identifies it in the existing cycle and
arranges an authorized resume, handoff or safe owner release after verification.
It never force-releases or resumes on elapsed time alone. Preserve recovery
ownership while an external operation remains unresolved. When writes and
version-sensitive checks finish, the owner releases ENV before final bookkeeping.
Distinguish active work time from interrupted time in the outcome.
