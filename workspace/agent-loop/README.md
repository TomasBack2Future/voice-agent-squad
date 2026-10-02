# Optional workspace bootstrap

This opt-in package implements a new-session Codex entrypoint plus the minimum
portable context layer needed for a bounded Worker pilot, with opt-in local
skill synchronization. It does not switch an App, migrate live task state,
collect usage, dispatch work, approve releases or activate the proposed loop. The broader
[design](../../docs/proposals/studio-multi-model-agent-loop.md) and
[context contracts](../../docs/proposals/agent-loop-context-contracts.md) remain
proposals beyond the implemented surfaces listed here.

## Portable Worker context package

The package separates four concerns that the first Studio pilot carried in one
large prompt:

| Concern | Canonical source |
| --- | --- |
| Generic Worker lifecycle | `roles/worker/SKILL.md` |
| Review admission, single-flight and freeze | `roles/worker/references/review-readiness.md` |
| Studio repository and delivery capabilities | `projects/studio/profile.json` |
| Importer repository and Compose delivery capabilities | `projects/importer/profile.json` |
| Squad source and review capabilities | `projects/squad/profile.json` |
| Interceptor source and human PR handoff | `projects/interceptor/profile.json` |
| Bounded dispatch input | `schemas/assignment-envelope.schema.json` |
| Compact/resume continuity | `schemas/checkpoint.schema.json` |
| Live cmux session transport | `tools/cmux-sessions/SKILL.md` |

`examples/assignment.studio-worker.json` is 1–2 KB when compactly serialized and
contains identities and authorization, not a copy of the Issue. A Worker starts
from that envelope, the workspace and repository `AGENTS.md`, and the canonical
Issue. It reads project references progressively by phase. The checkpoint keeps
durable progress and the next authorized action outside model conversation.

Validate the schemas, examples, cross-file identity and envelope budget without
network or provider access:

```bash
python3 workspace/agent-loop/validate_context_package.py
python3 -m unittest discover -s workspace/agent-loop/tests -v
```

The Studio profile deliberately names logical environment resources and requires
explicit kubeconfig/context selection. It contains no cluster nickname,
credential, customer data, local home path, or live deployment state. The
assignment must say whether staging and production are authorized; production
defaults are never inferred from merge or staging success.

For `TomasBack2Future/convoai-studio-importer`, select `importer@1` and the
absolute installed `projects/importer/profile.json` path in the assignment.
Use the generic `agent-loop-worker` role with the Importer repository's own
instructions and Python/Docker gates. The Studio profile is not a fallback:
preflight continues to reject any assignment/profile/checkout repository mismatch.
The package synchronization includes this profile with the Worker scripts and
skills; never add it by editing an installed snapshot.

Select the Worker role from the implementation repository, independently of
the GitHub tracking Issue: Studio uses `studio-issue-worker`; Squad, Importer
and Interceptor use `agent-loop-worker` with their matching project profile.
Keep the source `repository`, explicit `repository_host` and `clone_layout`
consistent with the checkout. A tracking Issue grants no source-host permissions.
The Squad profile supplies source/review gates only; it does not authorize binary
installation, live database changes or session restarts. Interceptor retains its
local review and human PR handoff contract.

Importer owns ENV-003/004 and uses its repository's Compose delivery adapter.
Independent Studio and Importer work may proceed concurrently after resource
policy adoption, subject to actual claim scopes: an omitted Studio scope still
has legacy broad coverage. Serialize writes to the same shared Studio test
Project even when deployment locks differ. Keep ConvoAI source environment,
Importer deployment target and Studio destination explicit and separate.
This profile grants no production, backfill or credential-change authority.

The workspace `AGENTS.md` owns skill routing. Product workflow skills remain in
their owning repositories; cross-repository or machine capabilities have one
versioned source and are installed rather than copied. In particular, Studio
Simulation/Evaluation is not routed from the word `eval` to ConvoAI task logs,
and `studio-sls-logs` is the single target identity for staging and production
SLS reads.

## Verified Claude Worker launch

The launcher remains a supervisor of the native client. Every 30 seconds while
that client runs (including tool/CI waits), it renews claims with the exact dispatch
key, generation, native session and child agent identity. Client exit stops renewal;
there is no detached timer or model heartbeat prompt. The event receiver belongs
to the supervisor lifetime. Heartbeat does not consume the mailbox. A heartbeat
runtime/fence failure stops renewal and prints a diagnostic without killing a
client that may have an external operation in flight. Reconcile that assignment;
never blindly reacquire claims. Preflight rejects binaries lacking this command.

Updating this source or installing skills alone does not update an existing
launcher or CLI. Roll out the new runtime to all cleanup callers and launch new
Workers through this supervisor; existing sessions require an explicitly
coordinated migration. An older CLI can still apply its old stale-claim policy.

Use `claude_worker_launcher.py` instead of copying per-task Python launchers.
Keep the schema-valid assignment separate from
`examples/claude-launch.json`: session ids, executable paths and runtime mode
belong to the launch config, not arbitrary new assignment authorization keys.
Keep delivery receipts and callback routing in coordination metadata. The
assignment branch must be a real checked-out branch, not a branch-creation plan.

Reserve and attach the canonical item before checking. Supply the actual
Dispatcher owner, child native UUID, fresh agent id, executable paths, ledger
working directory and already-authorized permission mode. In `native` mode,
point at the Squad executable directly; it uses `SQUAD_SESSION_ID` and
`SQUAD_AGENT` without requiring Codex identity variables. Explicit ledger cwd
replaces the machine-specific coordination wrapper's directory switch.

Some existing installations use `squad-coordination -> squad-codex`, whose
implicit contract requires `CODEX_THREAD_ID` or `CODEX_SESSION_ID` and exits 2
without one. When that exact installed wrapper must be retained, select
`codex-wrapper-compat`; both aliases are derived from the child UUID, never the
Dispatcher's environment. This adapter does not install/change a wrapper or
migrate an active session's identity. `codex_session.py` configures a Codex
provider/model launch; it is not this Claude/Squad identity adapter.

```bash
python3 workspace/agent-loop/worker_preflight.py \
  --assignment /absolute/assignment.json --profile /absolute/profile.json \
  --runtime claude --skill /absolute/.claude/skills/agent-loop-worker/SKILL.md \
  --launch-config /absolute/claude-launch.json
# Independent reproduction uses the same canonical path, with parent ids removed:
env -u CODEX_THREAD_ID -u CODEX_SESSION_ID -u SQUAD_SESSION_ID -u SQUAD_AGENT \
  python3 workspace/agent-loop/claude_worker_launcher.py \
  --assignment /absolute/assignment.json --config /absolute/claude-launch.json --check
```

`--check` performs one read-only coordination query with the same child identity
used at launch. A valid reserved/unbound entry passes with `binding: pending`;
that is preparation, not ownership or permission to execute work. It never
starts a model, claims, binds or mutates an environment. Preflight without
`--launch-config` explicitly reports `launcher.status: not_checked`.

Use the same command without `--check` as the new cmux workspace command, then
bind the returned native id to the reservation. Only a successful, well-formed
read of that exact still-unbound reservation may wait for binding. Nonzero exits
(including unknown errors), command timeouts, malformed JSON, wrong ownership,
source/item/generation or another bound session fail immediately. Diagnostics
include exit status and bounded, credential-filtered stderr; an empty or unknown
error is not evidence of transient contention. The wait is bounded; do not
resubmit an ambiguous creation attempt. Revalidate actual binding before any
manual retry. Launch rechecks the assigned Git identity and clean worktree.

The receipt proves coordination access in an identity-sanitized environment;
it does not prove model authentication, effective runtime approval, browser
login or deployment access. Keep those as separate checks. `--check` deliberately
retains normal PATH/HOME/auth configuration: it strips inherited identity, not
all credentials. This is a cold-start launcher, not a resume or owner-transfer
tool; preserve existing session identities during resume. No existing local
wrapper or live Worker is changed by adding this source capability.

## Local repository skill synchronization

`skill_sync.py` installs local Git `post-merge`, `post-checkout` and
`post-rewrite` hooks. A pull/merge, checkout or completed rebase in the selected
checkout and branch refreshes committed skills into **one versioned snapshot**.
`.codex/skills`, `.claude/skills` and Codex's `.agents/skills` discovery entries
all link to that snapshot. It needs Python 3 on macOS/Linux and does not use
client-specific hook support, the Squad daemon, an MCP connection or network.

Install deliberately, while the source checkout is on the selected branch:

```bash
python3 workspace/agent-loop/skill_sync.py install \
  --repo /absolute/path/to/voice-agent-squad \
  --source workspace/agent-loop \
  --target /absolute/path/to/workspace \
  --branch main
```

`--target` is explicit: use a workspace by default; choosing your home directory
installs user-level entries. One source package/target is allowed per repository
hook registration. `--source` can also select another repository's `skills` or
`.agents/skills` directory. Skills require an unquoted lowercase `name:` in
SKILL.md frontmatter; names must be unique. The entire **committed** package is
preserved, including sibling scripts, profiles and reference documents. Symlinks
and submodules inside it are rejected. Untracked and uncommitted files are never
published. Source edits made without these Git events require a manual run:

```bash
python3 workspace/agent-loop/skill_sync.py run \
  --config /absolute/source/repository/.git/hooks/squad-skill-sync.json --check
# Remove --check to synchronize immediately.
```

For a linked source worktree, use the hooks path reported by
`git rev-parse --path-format=absolute --git-path hooks`. The config pins the
source checkout and branch: other worktrees, feature branches and detached HEADs
skip automatic publication. Existing executable hooks run first with their
original arguments; their failure prevents synchronization. A custom
`core.hooksPath` is rejected rather than replacing the user's hook manager.

Existing skill directories, unowned links, locally edited managed links and
modified installed snapshots fail with an explicit conflict before client
updates. This includes legacy manually installed links: inventory and preserve
those installations before deliberately migrating their ownership. There is no
force-overwrite flag. Different repositories cannot silently take over the same
skill name. A target lock serializes synchronization; receipts record the commit,
file hashes and exact managed links. An atomic `current` pointer changes the
package revision; unchanged skill names retain stable discovery links. Renamed
or removed skills remove only still-owned links. Filesystem errors may leave
partial new-name entries; the ownership journal permits a subsequent retry.
`--check` validates/plans without switching skills (it may create lock/state
directories). Old snapshots are retained for inspection and pinned consumers;
there is no automatic garbage collection.

Disable the hooks and restore their predecessors without removing installed
skills or historical snapshots:

```bash
python3 workspace/agent-loop/skill_sync.py uninstall \
  --repo /absolute/path/to/voice-agent-squad
```

Hook installation copies the reviewed runner into Git's local hooks directory;
repository updates do not silently replace executable hook code. Re-run install
to update that runner. Hook failures are visible but cannot undo the Git operation
that already completed. There is no watcher, fetch, session restart or live
assignment/profile rewrite. New Workers must still pass startup validation
against a matching selected package/profile; running sessions are not assumed to
reload skill instructions. These are local setup operations, not a ledger API or
new MCP coordination surface.

## Worker startup probe

From an installed or reviewed package, before creating the session:

```bash
python3 worker_preflight.py --assignment /absolute/assignment.json \
  --profile /absolute/selected/profile.json --runtime claude \
  --skill /absolute/workspace/.claude/skills/agent-loop-worker/SKILL.md \
  --tool squad --tool squad-grok-review
```

Supply each required client-visible skill with another `--skill`. Relative
profile paths in the assignment resolve from its worktree; an absolute installed
profile path is preferable. The receipt fingerprints inputs, checks a clean
cold-start Git root/branch/base/origin, resolves skill links and executables, and
explicitly leaves ownership, API access, approval and environment `not_checked`.
It does not certify model behavior or inspect effective client configuration.
An isolated deterministic pilot is included in the existing Python test suite.

The Worker's [operational readiness](roles/worker/references/operational-readiness.md)
defines shared-blocker ownership, lock-free preparation, explicit attestation cwd
and durable terminal events independent of a provider's callback tools.

## Single-session provider selection

Use Python 3.11+ and a qualified Codex CLI (the configuration interface was
inspected on 0.142.5). Keep credentials and provider definitions in user-level
configuration. `sub2api` must already have an HTTPS `base_url`,
`wire_api = "responses"`, and `env_key = "SUB2API_API_KEY"`. The launcher reads
those settings but never writes them or prints the endpoint/key. It does not
source shell files, import credentials or create an account.

Plan a new session without starting Codex or making a model request:

```bash
python3 workspace/agent-loop/codex_session.py \
  --route sub2api --model <explicit-model-id> --effort high \
  --cwd /absolute/path/to/owned/worktree
```

Inspect the safe JSON plan, then add `--launch` to start the interactive CLI in
the current terminal. Use `--route openai` for the existing built-in OpenAI
authentication path. The default sandbox is `read-only`; `--sandbox workspace-write`
is an explicit choice for authorized implementation. Approval stays `on-request`.
Model/effort identifiers must be qualified on the selected route before use;
the launcher does not assert model availability or remaining quota.

The helper passes provider, model and effort as process-local CLI overrides.
It does **not** invoke `codex-app-use`, edit `config.toml`/profile files, relabel
rollouts, update the App database, change workspace metadata, or restart the App.
Other sessions retain their own configuration. Codex itself may create its normal
new session state when launched; this is not a full filesystem-isolation layer.
It inherits ordinary user/project rules, hooks and configured tools, so a
read-only sandbox is not a promise of zero tools or zero network activity.

Scope is a **new CLI session**, not changing the provider of the current desktop
task. The helper accepts no arbitrary pass-through flags, resume selector,
automatic retry or fallback. Missing sub2api credentials fail before launch.
Detected user-level or environment endpoint overrides on the OpenAI route are
rejected until separately qualified. This preflight does not audit every effective
configuration layer; qualify managed/system configuration on the execution host. The
helper does not install itself on PATH or replace the user's existing commands.

### Resume and concurrency boundary

- Two terminal processes may choose different providers without a global switch.
  Repository ownership and environment locks still apply independently.
- Same-route resume must preserve the original provider/model/account binding and
  qualified session identity. The launcher intentionally does not implement it.
- Cross-route handoff starts a new session from durable context. Never rewrite
  old provider metadata or assume encrypted provider history is portable.
- A provider label on a historical session is not trusted usage provenance if a
  history synchronization utility has relabeled it. Capture immutable route and
  account-bucket attribution at attempt start in the eventual runtime adapter.
- This helper is not a credentials sandbox or quota admission engine. Do not use
  it as an unattended Dispatcher before the remaining qualification gates pass.

The local CLI contract and [official configuration documentation](https://learn.chatgpt.com/docs/config-file/config-advanced)
support per-process `--config` overrides (including `model`). Named user profiles are another
supported option, but this helper deliberately pins the selected fields rather
than inheriting an independently drifting profile's model selection.

## Qualification scope

| Route | Required independent qualification |
| --- | --- |
| Codex / OpenAI | Actual auth mode, model catalog, structured output, same-route resume, permission denial and applicable account quota buckets |
| Codex / sub2api | Actual upstream model, Responses compatibility, output/resume/tool behavior, gateway quota/limits and account-pool semantics |
| Claude / existing approved gateway | Resolved model, permission boundaries, structured output/resume, distinction between launches, turns and API requests |
| Grok / existing subscription | Read-only review contract, session identity, usage semantics and required publisher policy |
| Muse | Deferred; excluded from initial selection/fallback and launch prerequisites |

Codex's two entries are one runtime with distinct service routes, not necessarily
two independent quota pools. Do not add their balances or apply the OpenAI account
quota to the gateway. Treat unsupported/stale quota as unknown. Runtime-reported
cost estimates, token counters, subscription balances and actual charges are
different measurements. No billing fallback or account rotation is authorized.

Run deterministic tests without provider access:

```bash
python3 -m unittest discover -s workspace/agent-loop/tests -v
```

These cover plans, independent overrides, launch argument construction, parent
environment preservation, no configuration/history writes, missing credentials,
invalid configuration, refusal of arbitrary overrides, context schemas,
assignment size and cross-file identity. They do not establish live provider
health, cross-model semantic correctness or staging readiness. The existing CI
test matrix runs these tests; no separate model-calling Actions workflow is added.

Use [workspace migration](workspace-migration.md) before relocating anything,
and the [baseline receipt template](baseline-receipt.md) for environment evidence.

### Installed resource admission and continuation

Deployment-authorized Claude launches now probe `squad resources check ENV-ID`
in the configured live ledger with the child identity, both at preflight and
launch. Policy-based profiles require installed policy. Contention is included
in the receipt and does not fail startup. A missing item, policy or CLI capability
fails before model launch. Explicit `resource_overrides` in the launch config
map staging/production to a verified legacy item with an `admission_reference`;
they do not change the strict assignment schema or authorize extra environments.

`dispatch continue KEY --from-item OLD --item NEW --worker-session UUID
--generation N` updates a dispatched reservation for a same-actor continuation
only after OLD is done/released and NEW is claimed. CLI and
`squad_dispatch_continue` preserve source/session/generation and record the
transition in the reservation note; old-item events are no longer accepted.
Use this operation instead of a prose-only continuation mapping. Resource checks
are also exposed as `squad_resources_check` and never claim or release resources.

## Rolling delivery planning

`rolling_delivery.py SNAPSHOT.json` evaluates a fresh, read-only ledger/PR snapshot
for the next independent merges, a dependency-closed cutoff set, effective WIP and
completion/reservation reconciliation. It neither assigns nor merges work. See
[the Dispatcher contract](../coordination-skills/squad-dispatcher/references/rolling-delivery.md).
Typed checkpoint operation receipts reject dispatch of an already submitted intent;
legacy checkpoints remain readable and require verified adoption before migration.

## Separate tracking and implementation hosts

Assignments/profile pairs may explicitly set `repository_host`; omission retains
GitHub compatibility. Bitbucket HTTPS `scm/` paths require an explicit matching
`clone_layout: bitbucket-server` in both assignment and profile; plain hosts
retain their original two-segment path (including a real owner named `scm`). Preflight and the launcher validate host + namespace/name,
profile identity, branch/base and a clean checkout. The tracking `issue` and
reservation `github:<issue>` remain independent of the code host. Interceptor's
[profile](projects/interceptor/profile.json) and [delivery contract](projects/interceptor/references/bitbucket-delivery.md)
cover Bitbucket Server source/test/local-review and human-operated PR handoff.
No hosting API credentials or automatic PR publication are required.

The rolling planner now returns `review_next` independently of merge holds and
CI/ENV waits. Only verified complete-diff admissions can enter it; in-flight or
already-attempted tuples reconcile instead of sampling again. See the
[review lane contract](../coordination-skills/squad-dispatcher/references/rolling-delivery.md#independent-review-lane).

## Qualified Codex control plane

The maintained Codex Worker adapter is `codex_worker_launcher.py`, not the
standalone provider-selection helper. Current Worker adoption remains unavailable
at the claim-loss execution-fence boundary below.
`schemas/codex-launch.schema.json` is a separate
launch config: exact native UUID, actor and Dispatcher, absolute executables,
selected model/provider/effort, approval policy **and** approval reviewer,
sandbox, ledger, prompt, private state directory, and an explicit owning local
Unix endpoint/PID. The config accepts no bypass option or arbitrary credentials.
The source adapter currently qualifies the installed **0.159.2 CLI** contract on
the OpenAI route. Other versions/providers and App targets are unavailable until
separately implemented and qualified. An App thread ID is never inferred from a
CLI reservation, and source preparation leaves existing schedules unchanged. Preserve explicit current user overrides, including an already ACTIVE fallback, and unrelated pauses. A schedule targeting a retired controller does not prove new-controller wake or authorize parallel dispatch.

The endpoint must already belong to the intended native server and have the
exact thread loaded. The helper verifies its process and socket incarnation,
reads live thread identity/model/effort, and rejoins the loaded thread without
settings overrides to inspect effective approval and sandbox. It never resumes
an unloaded thread to manufacture delivery capability. Native servers are
client infrastructure supplied by the selected client, not new background
controllers installed by this package. A lost route does not kill that client,
its server or external operations, or reacquire its claims.

Before adoption, explicitly qualify the selected binary/model and CLI idle
queue on a **new isolated thread**. Point the qualification config at a private
probe server, never a live Worker/Dispatcher endpoint. The probe uses an empty
isolated directory, read-only sandbox and tools-disabled instructions; it
performs two small inference turns and exercises the installed `codex queue
--remote … --thread … --message …` command. It writes only a safe local receipt,
not credentials, model reasoning or private output. `model/list`, executable
presence, queue help and saved target metadata are insufficient. Qualification
expires after 24 hours or any binary/selection change. It proves CLI queue
semantics and selected-model access for that probe, not App delivery or quota.

```sh
python3 codex_worker_launcher.py --assignment /absolute/assignment.json \
  --config /absolute/probe-launch.json --qualify /absolute/empty-probe-directory
python3 worker_preflight.py --assignment /absolute/assignment.json \
  --profile /absolute/selected/profile.json --runtime codex \
  --skill /absolute/workspace/.agents/skills/agent-loop-worker/SKILL.md \
  --launch-config /absolute/worker-launch.json
python3 codex_worker_launcher.py --assignment /absolute/assignment.json \
  --config /absolute/worker-launch.json
```

The launch config's `qualification_file` selects that receipt. Its Worker target
is verified independently against the reservation and live endpoint before
resume. Cold start retains the clean assigned base check. Later same-native
resume requires `checkpoint_file` matching the actual branch/base/head/dirty
state and ownership generation; its head must descend from the original base.
No old client or competing writer may remain active: the canonical native
launcher lock supplies exclusion for this lane, and ordinary takeover policy
still applies to any writer outside it. Checkpoint validation never grants
new operations or permission to overwrite unrelated edits.

The launcher supervises only its selected CLI and bounded receiver/heartbeat.
A transient heartbeat timeout/transport failure retries on the existing 30-second
client interval, each command bounded to ten seconds. Plain CLI nonzero/ValueError
failures also remain retryable; stderr cannot
classify custody loss. Only a matching `squad.worker-heartbeat.v1` atomic negative
receipt confirms rejection, including released/recovering primary claims.

**Worker adoption is currently blocked before client or receiver creation:**
Codex 0.159.2 has no qualified persistent claim-loss execution fence in this
adapter. A warning, stopping renewal or interrupting one turn cannot prevent
future protected writes after confirmed or long-lived unknown custody.
`thread/unsubscribe` detaches a subscriber; `increment_elicitation` pauses timeout
accounting and leaves direct input enabled, not execution. A new empty read-only
isolated probe verified that counter behavior without a model/tool call. No
permission downgrade, process kill or timer fallback establishes this capability.
The Codex runtime/adapter owner must qualify a supported execution suspension
that preserves external operations and exact policy, blocks future writes until
normal custody revalidation, and has an explicit fenced recovery. Until then
preflight, direct supervisor start and Worker receiver attach all fail closed.
Runnable negative checks, positive native proof requirements and installer rollback inputs are in
[Worker fence qualification](worker-fence-qualification.md).
This boundary does not stop existing installed clients or change their policy;
source capability, reviewed installation and safe adoption remain distinct.
`codex_receiver.py` also supports a Dispatcher-role config owned by that selected
native client's PID, with the same qualified binary/selection/endpoint, live
worktree identity, server incarnation, fresh receiver incarnation and bounded
lifetime. Dispatcher callbacks derive their recipients from Squad; Worker
decision events additionally match exact reservation/generation/native/item.
Receiver configs are launch artifacts, separate from assignment envelopes.

Structured native submission is preceded by an fsynced local intent. **0.159.2
does not dedupe repeated `clientUserMessageId` values**. Accepted intents are
not resubmitted after restart. A lost reply gets one bounded inspection of
native queue/history; if acceptance cannot be proved, delivery remains uncertain
and stops without a duplicate. Operator recovery must reconcile that intent,
not delete the journal to force another submission. Native queue acceptance is
recorded with `terminal-events delivered EVENT --delivery-session INCARNATION`
(or MCP `squad_terminal_events_delivered`). Handling remains the recipient's
explicit `terminal-events ack` after reading current decisions/reconciling the
outcome. A receipt is data, never new authority. Retry is bounded; faults remain
pending reconciliation. No terminal input or user drafts are touched.

`terminal-events listen --defer-delivery` leaves pipe output pending until native
acceptance. Default listen/hook behavior remains compatible with Claude.
Delivered/ack updates recheck current reservation, recipient, native, generation,
state and decision custody atomically, including valid released-claim history.

An optional `human_authorization` object carries an existing human reference,
assignment/repository, authorized operations and the explicitly selected managed
review provider/destination/content scope. Claude and Codex both pass it as
compact machine-readable prompt context. Missing receipts are explicitly absent.
It cannot expand assignment operations, change runtime permissions, infer new
sensitive disclosure authorization, or enable bypass after a denial. Never put
credentials, customer logs or private output in this object.

## Phase-specific delivery readiness

`delivery_readiness.py` evaluates an independently verified trigger-chain
snapshot; it is advisory and cannot merge, deploy or close an Issue. Optional
`delivery_readiness_file` on the launch config binds that snapshot to the exact
assignment/reservation/generation/native/actor and current Dispatcher admission.
The launcher re-reads the adopted bound decision before using it. A snapshot
records each actual workflow event/effect, immutable revision/evidence reference,
and downstream trigger links. Main-push reachability to a deploy node retains
the auto-deploy environment gate; CI-only main push, tag publication and an
independent workflow-dispatch deployment form a manual-deploy lane. Missing or
unverified trigger evidence is not permission to reclassify a legacy lane.
For environment-enabled projects or assignments, `workflow_inventory` must
cover every tracked `.github/workflows/*.yml`/`.yaml` file with its actual content
SHA-256 at the current source head. Trigger nodes bind `workflow_path` to that
inventory and revision; omissions, empty chains and changed files block readiness.
An environment-enabled project cannot infer a source-only lane from CI evidence.
Claude configs without this opt-in retain their existing resource policy.

Readiness is computed separately for source, merge, deploy, acceptance and
closure. A Dispatcher-admitted manual-deploy source merge requires exact
review/CI, but does not require staging fixture access, a deployment candidate
or an unrelated environment claim. Actual auto-deploy merges retain current
protected ENV ownership checks. Deploy/acceptance require their own verified
candidate/ownership and acceptance access. The source launcher admits source
or merge phases only; environment operations still use the project's supported
adapter and atomic resource admission. The generic Squad source-only profile
has no staging requirement.

Snapshots and checkpoints keep `pr-created`, `source-merged`,
`staging-accepted` and `issue-closed` distinct, with exact-revision outcome
receipts. An unaccepted product Issue cannot be closed from a PR or merge
observation. A source-only Issue can close on its verified source outcome.
Checkpoint `delivery_readiness` records selected phase/lane/outcome, admission
owner/revision, same native ID and receipt pointer. A waiting decision reaches
the existing Dispatcher through the fenced event route; adoption resumes that
same native Worker through its remaining authorized gates.

### Dispatcher continuity and the shared-App boundary

`codex_dispatcher_launcher.py` supplies a separate Dispatcher-role CLI path;
it does not take a Worker assignment, claim work or run a Dispatcher controller.
Its bounded receiver routes to the existing Dispatcher actor, and native event
handling invokes the existing Dispatcher skill for one reconciliation cycle.
A transition uses `schemas/codex-dispatcher-launch.schema.json` and an immutable
`schemas/codex-dispatcher-transition.schema.json` receipt. The latter binds the
exact native UUID/actor/cwd, existing checkpoint hash, prior App selection,
current reservation generations/native Workers, authorization reference and
joined old writer/receiver/external operations. The callback actor must match
those current reservations. Launch resumes that UUID without permission/model
settings overrides and checks the effective response before any prompt.

Unlike a Worker launch, this transition may **preserve** an existing `never` /
`danger-full-access` selection. It cannot infer or enable that policy from a
new Worker assignment: both the prior receipt and effective native resume must
match exactly. A mismatch blocks, including an attempted silent downgrade.
Qualification still uses read-only, tools-disabled isolated turns.

```sh
python3 codex_dispatcher_launcher.py --config /absolute/dispatcher-probe.json \
  --qualify /absolute/empty-probe-directory
python3 codex_dispatcher_launcher.py --config /absolute/dispatcher-launch.json --check
# Only the installation/adoption owner runs launch, from the selected terminal:
python3 codex_dispatcher_launcher.py --config /absolute/dispatcher-launch.json
```

The currently implemented ownership fence supports an already joined
**dedicated** old process, independently verified absent by its recorded PID, followed by a distinct
selected native server. Any live recorded PID is rejected even if its start text differs; PID reuse is conservatively blocked. It does not stop that process itself. A **shared App
backend** fails closed before any native resume or process-cutover check: process
exit would affect unrelated sessions, and `thread/unsubscribe` only detaches a
subscriber, not all other writers. Neither idle status, saved metadata, closing
an App window nor a local helper lock establishes native exclusive ownership.
Codex 0.159.2 has no qualified per-thread writer-epoch transfer in this adapter.
Therefore a shared-App Dispatcher cannot currently adopt this CLI path safely.
Do not terminate its backend, issue business-session probes or change its policy
as a workaround. This is a concrete remaining adapter gap, not live-flow success.

The activation owner owns that boundary: obtain/implement and isolate-qualify a
supported per-thread transfer that fences the old native writer without stopping
shared infrastructure, retains checkpoint/policy/callback custody, rejects old
writer input, and permits a fenced reverse transfer. Qualify one candidate;
unsupported/uncertain ownership remains blocked rather than retried. No timer,
second controller or permanent supervisor is a permitted replacement.

For a supported cutover, retain the installed package/binary and old client
configuration. Receiver health follows the selected CLI process/start identity;
its config and delivery journal persist the receiver incarnation and native
intent/acceptance. Confirm a legitimate event's native acceptance separately
from explicit handled acknowledgement. To return to App, first join the new
CLI and its receiver at a safe operation boundary, retain journals and claims,
then resume the same UUID with its original effective selection. A shared-App
return also needs the supported per-thread reverse fence; do not invent one.


### Fresh Dispatcher adoption after controller handoff

`codex_dispatcher_adoption.py` is the separate fresh-native route. It requires an
installed successful `dispatch handoff` audit receipt and exact current new
actor/native/controller epoch. It never impersonates the old actor, starts a new
thread or stops an App backend. Only the new inactive dedicated CLI must be
joined before resume; the actual selected Unix server and unchanged effective
model/effort/sandbox/approval reviewer are independently qualified. The schema is
`schemas/codex-dispatcher-adoption.schema.json`.

Controller handoff is a ledger protocol fence. The complete exact old-owner
reservation inventory is CAS checked in one transaction; ownership and
unhandled owner-directed recipients move together. Worker identity/generation,
claims, decisions, external operations and handled history remain unchanged.
The retired actor cannot reserve/reacquire or perform transferred owner actions.
This does not assert an operating-system fence over arbitrary unrelated tools.
Keep the old controller inactive under its authorized role; never use direct SQL
or actor impersonation to defeat the installed protocol.

Every Codex Dispatcher, including continuity at epoch 1, requires its verified
controller binding. Its supervisor claims the exact native/epoch/incarnation in
the ledger before starting the client, in addition to the local lock. Another
incarnation is rejected even when it uses a different state directory. Helper
exit or transport failure retains the lease while the client remains alive.
The supervisor releases only after both client and helper have actually joined;
a crash/unknown release leaves it occupied until the owning installer verifies
those joins and uses the exact release route. No lease expiry, timer or background controller is
introduced. Controller listen/delivered/ack uses `--native-session`; the native
binding is distinct from delivery incarnation. Acceptance still requires a real
persisted event delivered, handled and followed by authorized downstream work.

Rollback leaves retired actors retired and preserves receiver intent journals,
claims, decisions and receipt history. Stop only the new owned receiver/client
at a safe boundary. Retain a fence-capable runtime and ledger migration; an older
runtime lacking controller/native fencing is not an eligible active rollback.
A further owner-initiated handoff to a registered fresh actor/native is the
supported custody reversal; never revive an unfenced shared App writer.

Environment workflow evidence uses the complete commit tree and committed blobs,
not index files or dirty content. Install the pinned parser with
`python3 -m pip install -r workspace/agent-loop/workflow-requirements.txt`.
Actual YAML `on` events must be completely covered and match graph labels;
workflow_run parents must match named committed workflows. Duplicate keys,
aliases/tags, unknown action/command effects or unsupported triggers fail closed
for independent qualification rather than creating a weaker environment lane.
Manual source merge remains independent of fixture access once actual routing
and Dispatcher admission are verified.

Native client supervision persists a private per-native writer journal before
OS spawn and records the actual client/helper PIDs. The session lock alone is
insufficient after launcher death. Every launcher checks the retained journal
before RPC or native resume; an unjoined intent blocks replacement even if the
configured original writer PID has exited. Normal supervision records `joined`
only after both owned processes have returned. A failed exec records
`not-started` without inventing PIDs. Dispatcher receiver custody is acquired in
the ledger before starting the new client, so another incarnation cannot start a
second writer by choosing a different local state directory.

Retain the same journal/state directory across adoption and rollback. Unknown
spawn/PID-record windows and interrupted client/helper joins remain blocked on
actual original native tool/process completion and external-operation custody.
The native/runtime adapter maintainer and installer own that bounded qualification;
there is no force-clear, lease expiry or automatic client/server kill. Source
preparation never clears an existing live writer record to force adoption.
