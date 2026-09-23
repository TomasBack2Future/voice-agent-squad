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
