# Grok review command reference

This page describes the optional `squad-grok-review` product command, not a
consumer's development-agent roles, scheduling loop or repository workflow.
Whether and when a workspace invokes it is external policy. Installing or
building Squad does not enable a review gate in another repository.

## Command behavior

The command:

1. Reads PR metadata and diff through the installed GitHub CLI.
2. Freezes repository, PR, base SHA and head SHA.
3. Runs the installed Grok CLI headlessly with its bundled review policy,
   all tool executions denied, web/subagents disabled, at most three agent
   turns, and strict structured output. The extra turns let the model recover
   from a denied tool request and use the supplied patch.
4. Validates the returned verdict and findings.
5. Re-reads the PR and rejects publication if its base or head changed.
6. Publishes a sanitized comment and a SHA-bound Check through the configured
   GitHub App installation.
7. Updates a safe observation record under `~/.squad/grok-reviews/`.

There is no webhook service or external review scheduler. The command does not
merge, deploy, acquire environment ownership, assign tasks or modify product code.
Its local trust model protects against accidental stale-input publication; it
does not protect against a malicious operator controlling the host.

The Grok child receives neither the GitHub token nor the App private key.
Publication and observation records exclude hidden reasoning, raw output,
prompts, command arguments, environment values and credentials.

## Installation and configuration

Build the optional binary explicitly:

```bash
go install ./cmd/squad-grok-review
```

The per-user configuration is `squad/grok-review.json` beneath the platform
user-config directory: `~/Library/Application Support/squad/` on macOS or
`$XDG_CONFIG_HOME/squad/` on Linux. A synthetic example:

```json
{
  "app_id": 123456,
  "installation_id": 12345678,
  "app_private_key": "/secure/path/review-app.pem",
  "model": "grok-4.6",
  "reasoning_effort": "medium"
}
```

`SQUAD_GROK_REVIEW_CONFIG` or `--config` can select another absolute JSON path.
App identity flags support explicit overrides; all IDs and key locations must
come from the actual installation, not these example values. Do not commit the
configuration or private key, or include them in model input or evidence.

Run the non-sampling diagnostic before using the command:

```bash
squad-grok-review doctor --reasoning-effort medium
squad-grok-review doctor --repo owner/repository --reasoning-effort medium
```

It validates the binary, bundled policy, App authentication, Grok CLI flags and
login, selected model and writable Grok session storage without a model call,
comment or Check publication. Filesystem denial is a local-permissions failure,
not proof of invalid authentication. The host execution environment must permit
the configured session storage and required network access.

`--repo` additionally reads one bounded pull-request listing with the reviewer's
installation token. With `--pr`, it also reads that PR's identity. Neither probe
samples a model or publishes. The JSON `repository_access` is `readable` only
after that check; without `--repo` it is `not_checked`. Read access does not prove
Check/comment write permission. Use the repository probe during Worker startup
instead of discovering missing App coverage at final review.

## Invocation contract

```bash
squad-grok-review \
  --repo owner/repository \
  --pr 123 --mode shadow --reasoning-effort medium --timeout 20m
```

`--mode` accepts `shadow` or `required`; it selects publication names, not
GitHub branch protection. `--model` defaults to `grok-4.6`.
Reasoning effort precedence is explicit flag, per-user configuration, then
built-in `medium`; supported values are `low`, `medium`, `high` and `xhigh`.
The CLI's default timeout is twenty minutes. Pass `--timeout 20m` explicitly
when invoking an older installed binary that may still default to ten minutes.
This does not authorize resampling a timed-out base/head tuple.
Use `--help` and [the CLI source](../../cmd/squad-grok-review/main.go) for all
options and output limits.

A review is evidence for one frozen base/head tuple. A new base or head makes
the old tuple stale even when the textual patch is unchanged. Timeout, transport
failure and invalid output are incomplete results, never approval. Do not
reinterpret an error as a passing Check or repeatedly sample unchanged input
to seek a different verdict. Correct verified defects before requesting a new
review of changed input.

## Publication and authorization

The configured App needs metadata/read, pull-requests/read-write,
contents/read and checks/write on the selected repository. It does not need
Actions, deployment, environment, administration or contents-write permission.

The comment includes the exact head, verdict, summary and sanitized findings.
The Check is `grok-review-shadow` or `grok-review`. A comment alone is not a
merge gate. Enforcement exists only when the repository actually requires the
correct App-pinned Check on the current head; the CLI cannot install that policy.
A model approval followed by publication failure is not a successfully published
review. Model findings require source verification and do not replace tests.

## Observation schema

Safe observations include mode, stage, PR/base/head identity, sanitized
verdict/summary/finding titles, model usage and cost, durations and publication
links. Stages are `freezing -> sampling -> validating -> publishing`, followed
by `approved`, `blocking`, `error` or `stale`.

`reasoning_effort`, `reviewer_duration_ms`, `duration_ms` and safe token counts
describe the invocation. Missing usage is unknown, not zero.
`failure_kind` distinguishes timeout, cancellation, argument/authentication,
transport, output-limit and invalid-output failures; `failure_stage` identifies
freezing, sampling, validation, identity checks or publication.

These records are read-only observations, not merge authority. Monitor
availability does not change the command result.

## Regression boundaries

Tests must preserve frozen input identity, stale-result rejection, strict
output validation, credential isolation, sanitized publication, timeout/error
classification and the distinction between model approval and publication.
No test should use live credentials or a real environment as its fixture.

## Local Git review for human-operated PR lanes

`--provider local-git --repository-host HOST --repo namespace/name --worktree
/absolute/checkout --base-sha SHA --description-file /absolute/contract.md`
reviews a clean local worktree with the existing bounded Grok runner. Use these
same flags with `doctor`. No hosting API, GitHub App key, PR, comment or check is
created. `--pr` and `--mode required` are rejected in this mode. Results are local
revision-bound evidence for human handoff, never native approval or merge authority.
The frozen input includes the complete binary-aware diff and contract text. Local
origin (without encoded aliases), base/head, clean status and the full contract/diff are rechecked after
sampling; changed input invalidates the result. Status is namespaced by local
host/repository. See the [Interceptor handoff](../../workspace/agent-loop/projects/interceptor/references/bitbucket-delivery.md).

Local Git inspection disables replace refs and legacy grafts, and clears inherited
`GIT_*` repository/index/config overrides. It preserves normal HOME/PATH and does
not delete or rewrite the user's replacement refs or Git configuration.

## Bounded recovery of a joined sampling timeout

The GitHub wrapper now reserves a durable repository/PR single-flight before
sampling, independently of the status directory. Its private SQLite admission
store defaults to `~/.squad/grok-review-admission`; configure `admission_dir` once
for the reviewer installation. Every invocation for that installation must use
that same directory. Changing it to evade custody is unsupported. It is separate
from the Squad ledger. Receipts store input hashes and selected settings, never
source diffs, prompts, credentials or raw model output. Normal invocation also rejects an existing managed Check on that head; it cannot sidestep recovery by omitting `recover`.

A normal invocation prints `attempt_id`. Only a joined sampling timeout with no
valid verdict and its failed published original-mode Check is eligible for one
explicit recovery:

```sh
squad-grok-review recover --from ORIGINAL_ATTEMPT_ID \
  --repo owner/repo --pr 9 --mode required \
  --model grok-4.7 --reasoning-effort medium --timeout 20m
```

Use the original configuration and output limits. The operation rejects changes
to repository/PR/base/head, title/body, complete diff, reviewer core/policy,
model/effort/timeout, mode, App identity or output bounds. It verifies all
current-head shadow and required Checks from the configured App, rejects valid verdicts and
unresolved Checks, and requires the original failed Check. It reserves the
one-use slot atomically before invoking the existing bounded managed reviewer.
The synchronous child invocation and publication must return before the flight
is joined. Status observations are never join authority. The result still uses
the real current-head Check publication; review and CI gates remain required.

Publication-only failures, cancellations, unknown/unjoined custody and valid
approved/blocking findings cannot recover. A second timeout cannot chain another
recovery. Interruption after reservation leaves the flight and slot occupied;
restarting the command or changing head cannot sample again. No force-clear or
automatic retry is provided. Preserve both attempts; absent usage/cost remains
unknown, never zero-cost evidence. An admission-store failure blocks sampling;
a join-write failure blocks completion even if publication succeeded.

Older invocations have no authoritative admission receipt. The owning Worker
may provide an absolute `--from` legacy custody JSON of schema
`squad.review-recovery.legacy.v1` (see `LegacyRecoveryReceipt` in
`internal/grokreview/legacy_recovery.go`). It supplies the exact original frozen
bundle SHA-256 and settings, original safe terminal status path/hash, and a
pre-sampling `squad.review-input.v1` input receipt (`LegacyInputReceipt`) with its
path/hash and recorded timestamp. A hash of only diff/body or a later reconstructed
bundle cannot stand in for missing original complete-input evidence. It also needs a
bounded `squad.review-join.v1` exit receipt path/hash for both wrapper and owned
reviewer PIDs. Alternatively, `native_join` references the owning Codex native
history's original launch, join and terminal tool records, with SHA-256 for each
raw call and output line. The qualified host-retained exec/write_stdin/wait chain
must link the original returned tool session, eventual completed cell and exit-1
structured timeout report to the exact tuple/settings/failed Check. No asserted
"joined" prose or invented PID is accepted. Unfamiliar native history formats
remain unavailable. Append-only history may grow without changing those records.
The adapter independently verifies the selected join provenance, matches the
original terminal status and inventories its sibling status files for live or
valid attempts on the relevant exact tuple; valid different-head history is retained without blocking that recovery. Missing original inputs/settings or joined process evidence
blocks import; a failure comment is insufficient. Existing receipt/slot custody
cannot be overwritten by reimporting legacy JSON. Preserve old receipts read-only.


### Prospective re-admission when original full input is unavailable

`readmit` is explicitly a **new-input** operation, not legacy replay. The original
failed receipt keeps its complete-input hash empty and unknown usage/cost intact.
The original native delivery owner supplies a bounded absolute
`squad.review-readmission.prospective.v1` receipt (`ProspectiveReadmission` in
`internal/grokreview/prospective.go`). It contains:

- `original`: the legacy terminal status/settings/failed Check and genuine
  `native_join` above, with no original input receipt/hash. PID assertions cannot
  substitute for the owning native's launch/join/terminal chain in this lane.
- `diff_sha256`, `body_sha256`, and an absolute retained `content_evidence_path`
  with its raw-file `content_evidence_sha256`. That JSON must contain the original
  `diff_sha256` and `pr_body_sha256`. The maintained unversioned historical
  admission layout may instead use `complete_diff_sha256` and `body_sha256`;
  its repository, PR, base/head and original mode must match the original attempt.
  Unknown historical fields/layouts, duplicate fields, malformed or empty hashes
  and conflicting canonical/alias values reject. Coexisting values must agree.
  The raw-file SHA remains mandatory: retain the genuine original artifact,
  never rewrite, invent or backdate it. These hashes prove content, not old full
  input; current disclosure, native custody, fresh complete-input freeze and
  atomic one-use gates remain independent requirements.
- `owner`: exact `actor` and `native`; the caller's `SQUAD_AGENT` and
  `CODEX_THREAD_ID` / `SQUAD_SESSION_ID=codex:<native>` must agree. These are trusted
  local operator bindings, not authority to impersonate another Worker.
- `disclosure`: the real scope receipt below, with operation
  `prospective_legacy_readmission`, exact original tuple and mode, and an empty
  original `bundle_sha256`. A prior ordinary-review permission alone is not this
  new-input authorization.

```sh
squad-grok-review readmit --from /absolute/prospective-receipt.json \
  --repo owner/repo --pr 9 --mode shadow \
  --model grok-4.7 --reasoning-effort high --timeout 20m
```

When authentic complete retained diff bytes and the current gateway diff differ
only in descriptive hunk context, the prospective receipt may add
`renderer_equivalence` (`PatchRendererEvidence` in `internal/grokreview/renderer.go`).
It contains schema `squad.review-renderer-equivalence.v1`, the exact original
`identity` with unknown bundle hash empty, and absolute `original_patch_path` /
`current_patch_path` with their respective raw `*_patch_sha256` hashes. The
original raw SHA must equal the retained `diff_sha256`; never replace that hash
or regenerate missing historical bytes. Both nonempty regular streams are
bounded to 8 MiB. Qualification parses complete Git patches and valid hunk
ranges/counts, with isolated Git parse-only validation of metadata/binary data.
Only the descriptive suffix after a valid closing `@@` may differ. All file
headers, metadata, ranges, source whitespace, newline markers and order stay
byte-identical; malformed/truncated/ambiguous input rejects. The streams are
re-read before reservation and the current bytes must equal the frozen gateway
patch. The child receipt and CLI output add `renderer_provenance` with both raw hashes, rule
and evidence hash; authorization retains the complete proof paths/tuple. No
normalized digest authorizes sampling. Body, disclosure, native custody,
settings, failed Check and atomic one-use gates are unchanged. Missing authentic
original bytes remains an explicit qualification failure.

The embedded reviewer core also requires positive supplied scope or an actual
supplied failing check for compile/undefined-symbol findings. An unchanged
declaration omitted from a diff hunk cannot establish absence. Supported defects
still block; this contract never changes a prior verdict or approves an attempt.

Both `recover` and `readmit` preserve the **original** shadow or required mode,
model, effort, timeout, App and output limits. A shadow result never fulfills the
required gate. `readmit` verifies current base/head/diff/body against the retained
original identity, then freezes the complete new current bundle (including
current core/policy/description), its SHA-256, timestamp, owner and authorization
hash in the private store **before** model invocation. The child receipt and CLI
output label it `prospective-legacy-new-input` and retain `parent_attempt`.
Never describe that child as equivalent to the missing old prompt.

All import/recover/readmit lanes share an atomic one-use root keyed by the
original failed attempt **and** repository/PR/base-ref/base/head, across modes and
complete-input hashes. Existing consumed recovery history is backfilled on store
open. A used or interrupted root cannot be refunded or bypassed by ordinary
sampling, mode/core/policy changes, alternate stores/exporters or a new PR. A
valid current-tuple verdict in either mode or any live managed Check blocks
recovery; different-head terminal history remains valid and does not block it.
A second terminal failure ends this path; the original owner records the actual
failure and uses the normal decision route without further sampling.

### Exact disclosure scope readback

`authorization-readback` reads existing local operator-authored receipts without
initializing Grok, provider credentials, GitHub or the admission store:

```sh
squad-grok-review authorization-readback \
  --request /absolute/current-scope.json \
  --receipt /absolute/existing-disclosure.json
```

Both files use `squad.review-disclosure.v1`, with `owner`, exact `identity`
(`repository`, `pr`, `base_ref`, `base_sha`, `head_sha`, `bundle_sha256`), `mode`,
`operation` (`managed_review` or `prospective_legacy_readmission`),
`provider: grok`, and `content: source_diff_and_review_contract`. The real receipt
also has `reference` pointing to retained human authorization and `disposition`
(`granted`, `pending`, `denied`). A request describes scope; it grants nothing.
Use an empty complete-input hash only for explicitly unknown original prospective
identity. The operation reports `unavailable` for no receipt, preserves exact
pending/denied status, or returns `scope-mismatch` plus the retained reference and
receipt disposition. It never propagates a grant to another PR/head/native/mode
or operation and never samples/publishes. An operator must derive the receipt
from actual human evidence: this local trust contract does not authenticate or
manufacture that evidence. Keep one existing pending question when authority is
absent; Full Access and source implementation permission are not disclosure grants.

This is source capability, not installation or authorization to invoke another
Worker's review. The installation owner controls adoption. The existing Worker
owns the failed invocation and any eligible recovery after reviewed installation;
source preparation never invokes a foreign Worker’s recovery.


### Joining an interrupted or failed durable join

`reconcile --from ATTEMPT_ID` is a local custody operation, not review sampling.
Use the original selected `--repo`, `--pr`, original mode, model/effort/timeout,
App identity and canonical admission directory. The runner records actual wrapper
and reviewer PIDs; terminal completion journals a private safe receipt before the
SQLite join. Reconcile requires verified original process absence and restores a
terminal journal after a join-write failure, preserving verdict/publication and
unknown usage accurately. If the original recorded processes both exited before
terminal journaling, it joins an interrupted/error attempt without inventing a
verdict, publication, cost or usage. Exact input history and one-use recovery slots
stay consumed. A corrected different head may then enter normal admission.

No lease timeout or force-clear proves child join. Missing historical process
provenance requires the existing qualified original native/process join receipt;
`reconcile --from ABSOLUTE_LEGACY_RECEIPT` verifies it through the same original
input/status/join contract before clearing only the matching flight. Missing proof
remains explicitly blocked. Reconcile never calls the model or publishes/replaces
a Check; actual current-head review/CI gates remain required. Repeated identical
terminal joins are idempotent, while changed settings/input/repo/PR or a live
original process are rejected.

The private receipt commits a launch stage before OS spawn. A verified dead
wrapper still in `admitted` proves no sampler was launched and can be reconciled
without inventing a child PID. A crash after `launching` but before recording the
actual child PID remains blocked on original child/native join provenance; wrapper
absence alone cannot prove an orphan sampler ended. The runtime/adapter maintainer
and installation owner own that bounded provenance qualification. Joined replay
is idempotent and never refunds an exact-input or one-use recovery slot.

Shadow and required publication are separate lanes: a shadow sample never
satisfies or consumes the required Check. Exact-input dedupe includes mode,
while single-flight remains repository/PR-wide across both modes. Changing
model/effort within a mode does not grant another normal sample.

Before any publication, admission durably checkpoints the synchronously joined
sampling result. Failure to record that custody prevents publishing. Publication
checkpoints its actual response independently. If the response or terminal
journal is lost, `reconcile` reads the configured App's actual Check, matching
mode, head, attempt ID, complete frozen-input hash and expected verdict. This is
read-only GitHub access and does not initialize Grok. A uniquely matched failed
no-verdict timeout restores the one-use recovery prerequisite; ambiguous, live,
wrong-input or wrong-verdict evidence fails closed. Original terminal artifacts
and every actual review/CI gate remain intact; no approval is synthesized.

### Safe terminal diagnostics

New sampling receipts, local status files and command JSON include an optional
`terminal_diagnostics` object. It records the exact input SHA-256, process start
and finish times, whether the direct child started and was waited, its exit code
when known, and the deadline producer (`reviewer_timeout`, `caller_deadline` or
`caller_cancel`). `joined_at` is present only after waiting that direct child.
Stdout/stderr byte counts and SHA-256 cover all received bytes, even when the
in-memory output cap truncates them; `output_truncated` makes that distinction
explicit. No raw input, output, stderr, prompts or hidden reasoning is retained
in this object. Direct-child wait is not remote provider cancellation proof or
proof that every descendant exited.

After a joined timeout/cancellation, the adapter can read the Grok CLI's retained
session metadata from only this invocation's physical private working-directory
index under the configured home. It does not search other sessions or make a
model/API call. Attribution requires the retained prompt to match the complete
frozen input byte for byte, one unambiguous UUID session, matching configured
model/effort, and bounded regular files without symlinks. Reads are capped at
32 MiB for prompt history, 64 KiB for summary, 4 MiB for events and 20,000 event
records. Duplicate keys, malformed/truncated records, unsupported event shapes,
wrong session/model, and timestamps outside the invocation window reject the
session evidence. Missing or incompatible retention is reported as unavailable
or unqualified and does not change the sampling result.

`session_evidence: qualified_local` adds only the local CLI session UUID,
`local_request_id`, an event-stream hash, and allowlisted first-token/reasoning
phase timestamps and counts. These are CLI-reported observations: notification
counts are not token usage, and a local request ID is not independently verified
provider wire correlation. Other Grok versions or phases may remain unqualified.
The adapter does not infer a finish reason, transport health, remote cancellation,
cost, usage, approval or retry authority from these observations. In particular,
active reasoning notifications followed by a deadline remain **no verdict**.
Historical receipts and failed Checks are unchanged; diagnostics neither reset
nor refund consumed recovery roots, admit another attempt, change settings, nor
substitute for independent review or business acceptance.
