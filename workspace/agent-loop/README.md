# Optional workspace bootstrap

This opt-in package implements a new-session Codex entrypoint plus the minimum
portable context layer needed for a bounded Worker pilot, with opt-in local
skill synchronization. It does not switch an App, migrate live task state,
collect usage, dispatch work, approve releases or activate the proposed loop. The broader
[design](../../docs/proposals/studio-multi-model-agent-loop.md) and
[context contracts](../../docs/proposals/agent-loop-context-contracts.md) remain
proposals beyond the implemented surfaces listed here.

## Role and runtime contract

[Runtime-independent roles](runtime-compatibility.md) defines equal operational
requirements for Muse, Claude and Codex across Dispatcher, Worker, Deployer,
Reviewer and Investigator. It is a normative contract, not a claim that every
adapter is implemented or qualified. The Worker schema below stays Worker-only.
Qualify executable/version, client surface, effective policy and each required
operation separately; use existing repair owners for missing capabilities.

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
starts a model, claims, binds or mutates an environment. Execution preflight
requires a checked launch config for Claude, Codex and Muse. Use `--context-only`
explicitly for portable input checks without a launcher; the receipt reports
`status: context-checked` and `launcher.status: not_checked`, never `launch-checked`.
Do not use that receipt to admit a session. Muse source/test Workers use
`muse_worker_launcher.py` with the checked Muse launch configuration and the
shared `.agents/skills` discovery path. Environment execution remains a separate
qualification; this launcher rejects staging/production assignment authority.
Unknown runtimes are rejected by both the CLI and the importable Python API.

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

### Exact-worktree Claude trust

The launch check includes `workspace_trust` with the canonical assigned path,
configuration file, and `established` or `will-establish` status. It remains
read-only. After binding, worktree validation and fenced heartbeat admission,
launch records Claude's documented `projects[absolute-worktree].hasTrustDialogAccepted`
entry before starting the client. It never trusts the parent, main checkout or
home directory, and does not change permission mode or add bypass flags. Other
project/configuration fields are preserved; malformed or symlinked configuration
blocks startup. Writes are atomic and use the native `<config>.lock` directory protocol
(qualified with Claude 2.1.288), including mtime renewal, ownership checks and
concurrent configuration-change detection before replacement. A busy native
lock blocks after a bounded wait; it is never stolen.

The normal trust store is `~/.claude.json`; `CLAUDE_CONFIG_DIR` selects its
`.claude.json` instead. Optional `client_config_directory` pins that same directory
for both trust setup and the child. It is not a credential-copy or onboarding
helper. The native readback probe uses a fresh temporary home, fake API key,
linked Git worktree and no task prompt:

```sh
SQUAD_CLAUDE_TRUST_BINARY=/absolute/claude \
  python3 -m unittest discover -s workspace/agent-loop/tests -p test_claude_workspace_trust.py
```

This reproduces the dialog without trust and verifies interactive startup with
only the exact worktree trusted, while main-checkout trust stays false. Qualify
the selected native version before installation: Claude's trust-path resolution
can change between versions. See [Claude's trust contract](https://code.claude.com/docs/en/permissions#project-allow-rules-and-workspace-trust).

### Claude Dispatcher receiver custody

`terminal_receiver.py` reads the current controller actor/native/epoch and binds
its delivery incarnation before listening. An optional `controller_epoch` pins
an expected epoch; a mismatch fails closed. Native identity never comes from the
incarnation. Worker receivers do not acquire controller custody.

One local lock covers binding, listening and release. On replacement, owner exit,
delivery or listener failure, the receiver releases only its exact native/epoch/
incarnation. Its write-ahead local journal permits reconciliation of a prior owned binding
intent or bound incarnation after the process exits, including a lost command
response; a foreign receiver is never replaced. Failed
release retains the journal for reconciliation. Delivery still produces a model
reminder, not a handling ACK. Run `test_terminal_receiver_ledger.py` to exercise
handoff, binding/replacement, foreign/stale rejection and actual delivery against
an isolated built Squad CLI and SQLite ledger.

## Local repository skill synchronization

`skill_sync.py` installs local Git `post-merge`, `post-checkout` and
`post-rewrite` hooks. A pull/merge, checkout or completed rebase in the selected
checkout and branch refreshes committed skills into **one versioned snapshot**.
`.codex/skills`, `.claude/skills` and Codex's `.agents/skills` discovery entries
all link to that snapshot. Muse may discover the shared `.agents/skills` entries;
verify discovery and actual loading in the selected installed client before use.
Do not create an independent Muse skill copy merely for its runtime name.
It needs Python 3 on macOS/Linux and does not use
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

### Role-skill canonical sources

`canonical-sources.json` declares exactly one canonical source per routed
skill (Issue #14). In-repo skills resolve to their versioned directory;
external skills (Importer/Studio-owned, or not yet versioned here) record
their owning repository and current status instead of a copy. The old
`studio-staging-sls-logs` name is a time-bounded compatibility alias of
`studio-sls-logs` only.

Audit deterministically without touching live installs:

```bash
python3 workspace/agent-loop/audit_canonical_sources.py
python3 workspace/agent-loop/audit_canonical_sources.py --installed <skills-dir>
```

The audit fails on duplicate skill names with different content across the
declared in-repo roots, on a canonical entry whose source is missing or
mismatched, and on an installed entry that is neither a symlink into a
declared canonical source nor byte-identical to it. It never relinks or
reinstalls skills; live installation remains a separately authorized
operator step.

## Worker startup probe

From an installed or reviewed package, prepare portable inputs before execution
preflight (this command does not authorize creating the session):

```bash
python3 worker_preflight.py --assignment /absolute/assignment.json \
  --profile /absolute/selected/profile.json --runtime claude \
  --skill /absolute/workspace/.claude/skills/agent-loop-worker/SKILL.md \
  --tool squad --tool squad-grok-review --context-only
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
| Muse | `muse_worker_launcher.py` admits bounded source/test/handoff Workers with the exact reviewed native build, fixed native write/shell denial, an owned Linux container tool runtime, complete custody pin and decision/outcome route. Environment and other role adapters remain separately qualified |

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

### Muse lifecycle qualification and execution boundary

`muse_session_host.py` is a bounded MSP lifecycle probe, **not an admitted
unattended task executor**. The previous renewal-only host has been disabled:
stopping heartbeats does not prevent a native tool or descendant from continuing
to write after losing ownership. Neither `turn/interrupt` nor
`session/setApprovalMode` is a persistent Squad-generation execution fence.
Managed starts through this lifecycle probe fail before spawning a server or receiver;
Deployer, Reviewer and Investigator execution has no adapter here. This does not
exclude Muse from interactive roles under their ordinary operation authority.

One user-approved exception (#84, option B) reuses this host:
`muse_recovery_executor.py` may continue an existing interactive `--yolo` Worker
on its own native after a confirmed runtime failure. It runs only on a Dispatcher
`continue` decision, visibly in that Worker's cmux workspace, and keeps the
Worker's original posture: model, max effort, allowAll and hooks. It is parity
with the interactive TUI, not a new fence. A claim lost mid-turn is still unfenced,
exactly as for the TUI it replaces. No other Muse task execution is admitted
through MSP.

Probe configuration requires absolute client/coordination/ledger/workspace/state
and prompt paths, a native UUID, `agent_id`, `role: "probe"`,
`model: "muse-spark-1.3-contributor"`, `provider: "meta"`,
`permission_mode: "yolo"`, and an explicit catalog-supported `reasoning_effort`.
No defaults, alternative models, legacy approval/sandbox config or task prompt
are accepted for execution. `environment` permits only PATH, GH_CONFIG_DIR and
SQUAD_HOME routing paths. Do not put secrets in config or evidence.

The pinned adapter checks Muse `1.4.2-R4684.1` and its exported stable schema
fingerprint. MSP `serve` explicitly receives `--provider meta --model
muse-spark-1.3-contributor --disable-sandbox --trust-workspace`; it has no
`--yolo` option. `session/start` explicitly selects model/provider/`allowAll`.
The host reads back exact session/workspace/model/provider/approval and catalog
effort. Sandbox posture is fixed by the host's process arguments, not a fabricated
wire field. Resume first reads the retained session and rejects a mismatched or
non-idle selection before loading, then verifies the resumed reply. It never
silently repairs permissions or switches models. Unknown versions, schemas,
models and permission states fail closed.

`--prepare` creates one no-turn probe session; `--check` verifies its no-turn
resume. Normal invocation rejects managed execution; there is no arbitrary
`/rpc`, user prompt or event-turn escape. Evidence says `lifecycle-verified` and
`task_execution: blocked`, never unattended-ready. Native duplicate-session
admission is separate from, and does not prove, claim-loss write exclusion.

Opt-in native tests use temporary workspaces with no Squad claim/controller:

```sh
MUSE_NATIVE_EXECUTABLE=/absolute/path/to/muse \
MUSE_NATIVE_EVIDENCE=/absolute/private/evidence \
python3 -m unittest discover -s workspace/agent-loop/tests -p test_muse_native.py
```

They verify explicit 1.3/YOLO readback, a bounded text-only provider response,
same-native resume, wrong permission/model rejection and duplicate native
loading rejection. Ordinary CI skips these account-dependent tests. Recorded
permissions and model catalog alone are not successful provider execution.

The remaining native adapter requirement is a persistent per-tool execution gate
bound atomically to current controller actor/native/epoch and Worker/claim
generation, including descendant and in-flight operation custody. Until that
capability is implemented and independently qualified, keep managed execution
blocked and preserve the original owners, external operations and receipts.
Source tests do not authorize installing the adapter or migrating live sessions.

## Qualified Codex control plane

The Worker launch schema accepts the selected `read-only`, `workspace-write`
or `danger-full-access` sandbox and `on-request`, `untrusted` or `never`
approval policy. Schema acceptance does not qualify execution. Admission checks
the already-loaded native's actual `thread/resume` reply against the exact
selected model, effort, provider, sandbox, approval policy and reviewer, without
settings overrides. A mismatch fails closed; recorded metadata and launch argv
alone cannot establish effective Full Access. App delivery and persistent
all-writer execution fencing retain their independent qualification gates.


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
Native input acceptance and handling acknowledgement are separate. The ledger may
return accepted-but-unacknowledged events again while a Dispatcher handles a long
operation. The receiver uses its durable accepted-input journal to avoid another
native submission, records transport acceptance again, and keeps listening for
new events within the existing client/lifetime bound. Accepted replays never
consume a transport retry allowance or terminate the receiver merely because the
handling ack is delayed. An uncertain submission, identity mismatch or rejected
custody still stops without resubmitting or acknowledging handling.

An absent old helper does not prove its terminal join or permit a replacement
controller. The installation owner retains original launcher/helper exit
and external-operation custody evidence. Directly attaching a new helper to an
old supervisor is unsupported: that supervisor cannot join the replacement
helper before releasing its global receiver lease. Keep the current client and
lease intact until the existing owner establishes the normal safe joined
boundary for its owned CLI and helper, with original operations retained.
Then use the reviewed `codex_dispatcher_adoption.py --config
/absolute/retained.resume.json` for the same controller native/actor/epoch,
with actual inactive-writer join evidence and the same state directory.
The fresh supervised client/receiver incarnation is admitted only after the old
writer journal records actual joins; the global receiver bind, local locks,
selected server incarnation and unchanged runtime policy still apply. Never
fabricate dead PIDs, rewrite locale-sensitive historical process-start evidence,
clear an unresolved lease, stop a shared App backend or retarget a timer.
Source changes do not perform this controlled installation/resume. Worker-role
attach remains closed under the unqualified persistent execution fence.

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

## Portable runtime entry

Invoke the same installed-package entry for Claude, Codex or Muse:

```sh
python3 runtime_entry.py --runtime muse capabilities
python3 runtime_entry.py --runtime muse --native-session NATIVE --agent ACTOR \
  --ledger /absolute/coordination --squad /absolute/native/squad \
  exec -- dispatch list --json
python3 runtime_entry.py --runtime claude worker \
  --assignment /absolute/assignment.json --config /absolute/launch.json --check
```

The `worker` entry delegates Muse to the custody-bound source/test/handoff
launcher described below; its config, claim, reservation and container checks
remain mandatory. Native adoption is not inferred from this routing capability.

Use the actual native Squad binary, not a wrapper which derives Codex identity
or switches ledger directories. No global wrapper, installation or live client
is changed. `exec` forwards argument boundaries and exit codes unchanged, with
explicit per-native/per-ledger identity and no inherited foreign actor.

For portable in-turn or explicitly awaited event consumption, the same global
identity arguments support `listen --delivery-session INCARNATION --max 30s`.
This emits the existing `worker-terminal-delivery-v1` JSON without marking it
delivered. It does not provide idle wake to an unsupported client. Native
receivers still own their existing lifetime, controller bindings and journals.

After the actual recipient reads current decisions and handles an event, save
this bounded result with that delivery receipt:

```json
{"schema_version":"squad.handled-events.v1","runtime":"muse",
 "native_session":"NATIVE","agent":"ACTOR","delivery_session":"INCARNATION",
 "handled":[{"event_id":"EXACT_DELIVERED_EVENT_ID","note":"existing-checkpoint/reconciliation-reference"}]}
```

Then invoke `handled --delivery /absolute/delivery.json --receipt /absolute/handled.json`
with the same global arguments. It validates the complete batch before writes,
records native acceptance implied by that explicit recipient result, then ACKs
only those events. ACK is also attempted when delivery reports an error, because
delivery rejects an already processed event while ACK can confirm the same
handling note idempotently. If ACK cannot confirm processing, the helper stops
with a nonzero result. Partial success is safe to replay with the same notes. Never synthesize a handling result merely because a
listener returned, a native turn ended or an operation remains unresolved. The
Squad store independently rechecks recipient, generation and current decision.

## Muse source Worker execution

Use `muse_worker_launcher.py`, not the lifecycle probe, for a new bounded
source/test/handoff assignment. The assignment must already have a primary
claim and a dispatched reservation for its exact native UUIDv7. Its launch
configuration follows [muse-launch.schema.json](schemas/muse-launch.schema.json):
absolute native/Squad/settings/prompt/ledger/state paths, explicit coordination
home, Worker/controller identities, primary claim generation and controller epoch, Meta 1.3 Contributor,
YOLO, and a resolved supported reasoning effort. Keep configuration and state
private; credentials stay in the native client's existing environment. The
launcher copies only endpoint/catalog settings into its isolated configuration.
Project MCP servers and hooks require their own qualification and are rejected.

The default `progress_view: live` is a passive first-class progress display in
the launcher's cmux surface. It renders startup/native/effort identity, agent text,
tool names/results, turn completion and periodic claim-renewal status while the
same qualified MSP host and supervisor run. Output is bounded per item and
terminal control sequences are removed; private reasoning is not rendered.
`progress_view: quiet` is available for machine-only callers. Closing the display
does not transfer custody or stop cleanup. This is a live viewer, not an interactive
Muse TUI: do not interrupt it and resume via the naked CLI to obtain visibility,
because that loses the mediated tool/pin/report contract.

`reasoning_effort` in the task's launch config is an explicit assignment override.
When absent, read `reasoning_effort` from the persisted native `settings_file`;
when neither sets it, use `max`, matching the standing Muse launch preference.
Invalid values block rather than silently downgrading. Do not guess `high` or
write it into launch configs merely to satisfy an old schema. The check receipt
and startup evidence record the effective value and its source. New runs and
`--resume` use the same resolver and supply the result to every initial/decision
turn; resume preserves task/native/custody identity, not a stale arbitrary effort.

The native host receives `--disable-write --disable-shell` for its full lifetime,
in addition to explicit model/provider, disabled sandbox and trusted workspace.
MSP selects and reads back `allowAll`; `serve` does not accept a `--yolo` flag.
Mutation tools come only from the exact-session MCP bridge. `write_file` requires
the previous SHA-256 and an assigned-workspace path. `run_command` runs in an
unprivileged, dedicated Linux container with only that workspace mounted. It
mounts no ledger, host credential home, Docker socket or other worktree. Thus
kernel containment covers reparenting, `setsid` and cleared child environments.
The current lane does not promise host GitHub credentials, a shared Git worktree
metadata mount, environment operations or detached services. Choose an explicit
source/test/handoff assignment rather than claiming unsupported delivery phases.

Build [the source tool image](containers/muse-source.Dockerfile) explicitly and
select its immutable `sha256:...` image ID, Docker executable, context and daemon
ID in `tool_runtime`. The daemon must report Linux and cgroup v2. The checked
image must contain the toolchain required by that assignment; shell output and
command lifetime are bounded. Docker or daemon identity failure does not fall
back to a host shell. This is execution mediation; the user's YOLO approval mode
and model selection remain unchanged.

```sh
python3 workspace/agent-loop/muse_worker_launcher.py \
  --assignment /absolute/assignment.json --config /absolute/muse-launch.json --check
python3 workspace/agent-loop/muse_worker_launcher.py \
  --assignment /absolute/assignment.json --config /absolute/muse-launch.json
# Original task/native/claim/reservation only; preserve edits and commits.
python3 workspace/agent-loop/muse_worker_launcher.py \
  --assignment /absolute/assignment.json --config /absolute/muse-launch.json --resume
```

The supervisor acquires an execution pin for the immutable native, actor,
primary claim generation, reservation generation/source, and controller native/
epoch. The ledger rejects competing native admission and custody changes while
that pin is active. Expiry, failed renewal and closed admission retain the pin;
they never release task custody. A hold blocks source mutations while allowing
assignment decision reads and handled acknowledgements. Mutation admission also requires observed successful native reads of the
canonical Worker skill and selected profile at startup; retain its actual reads
and reported identities in startup evidence.

The session-owned receiver requests deferred delivery, submits a durable wake
to the original native session, and records transport acceptance separately from
handling. Only the model's typed `decision_get` and `acknowledge` tools handle and confirm a decision. The
`report` tool records actual results; after native/MCP/container joins the
supervisor records one durable outcome and publishes `handoff-complete` or
`blocked` with the current decision revision. It does not release the primary
claim, close the Issue or start another controller. After a completed native turn
and queued decisions drain, the bounded invocation exits; later continuation
uses `--resume` on this same assignment. It is not a permanently idle TUI daemon.

State retains the original writer intent, binding, tool journal, native items,
delivery command journal, report and join receipt. A lost startup/result or
unjoined process blocks replacement; reconcile that original evidence and
actual container handles. Supervisor-only `worker-execution outcome/close`
verify private bounded receipts against the original journals, exact binding,
actual native/bridge/tool process absence and owned container state. They are
not exposed to the model. Completion reporting does not prove that a Dispatcher
received or handled its pending outcome event.

Deterministic regression tests run in the ordinary Python and Go suites.
Opt-in container tests require `MUSE_CONTAINER_QUALIFICATION` pointing to a
private launch configuration and create/remove only their own containers. Retain
real native new/resume, source mutation, command, decision handling, outcome and
join evidence before installing this lane for new Workers. Source changes alone
do not migrate existing sessions or establish native adoption.

Suspension closes mutation admission while retaining the exact execution pin.
A suspended bridge may read decisions, acknowledge handled events, inspect its
claim/mailbox and record a blocked report under a separate exact-custody check.
It cannot reopen writes, request review or record completion. In-flight hold
joins the owned container before the model handles the decision and returns the
blocked result; the original primary claim remains with its owner.

### Muse runtime-failure hook and handling contract

`muse_failure_hook.py` is a PostLLMCall observation-only adapter. It admits
only the ledger-bound Worker's native session, rejecting
unrelated/reminder/subagent sessions and unqualified StopFailure; StopFailure
stays supplemental until exact-version proof exists. Status `failed` or
`timed_out` is a failure. Muse ends a model call whose stream produced no
first event as `timed_out`, and this case is classified `connection`. Only
`success` is verified healthy progress. `cancelled` and any other status of the
bound native is neither: the hook appends its closed status token and turn id
to the bounded `ignored.json` audit and changes no episode. It classifies the error
into a closed enum (`exhausted`, `connection`, `auth`, `quota`, `config`,
`unknown`), dedupes by reservation/generation/native plus continuous episode
under a lock, and publishes one sanitized observation through #88's atomic
`terminal-events submit` with bounded retries: one call stores the message
and the durable event and returns both IDs. Each failure episode maps to
Submit's stable request key (`--request-key ep-N`): retries within one
outage deduplicate to the same IDs, and the next independent outage after
healthy progress uses a new key and records again. Only native health
signals allocate episodes: the first failure with no open outage allocates
the next `ep-N` and freezes that first observation; verified healthy
progress of the same native (`note_progress`) ends the outage even when
delivery is unavailable. Delivery is a separate in-order outbox: each
frozen episode replays with its own key and immutable body until Submit
confirms it, so a lost reply or a crash after commit replays idempotently.
Transport recovery never opens or closes an episode. The hook returns
quickly and never waits for its own turn to end (D84-2 ordering).

`muse_recovery_executor.py` performs that one continuation for an interactive
`--yolo` Worker. `--check` runs the same admission, goal matrix and custody
reads and changes nothing. Before changing
anything it requires:

- the delivered keyed `runtime-failure` event of this exact
  reservation/generation/native;
- the native session log's last run record showing `terminal: failed`;
- `decide` returning `continue`;
- a dispatched reservation bound to that native, the Worker's exact claim
  (`claim-inspect`: item, holder, `held`, configured `claim_generation`; agent
  registration is not custody). The decision must be absent (`decision-get` reports
  revision 0 with an empty action) or an adopted revision whose action is exactly
  `proceed`.

It then compare-and-sets the episode's single attempt in its state directory.
Immediately before the signal it re-reads the session log. The last run must
still be the failed terminal, and the route-facts pid must still be the admitted
client; otherwise it stops without signalling. It sends SIGTERM only to that pid,
and only while the pid still runs the configured client in the workspace; there
is no SIGKILL. Any stop after the attempt is spent, including a coordination read
timeout, is still recorded in `recovery-ep-N.json`. The episode is then left to
the Dispatcher rather than retried. It starts `muse serve` with the Worker's hooks in a private settings layer
(`muse serve -c hooks=` admits no handler; the user settings layer does). That layer
is a per-recovery XDG config home whose `muse/settings.json` is a private copy plus
`hooks`; every other entry, including Muse credentials, is a symlink, and nothing
live or in the Worker workspace is edited. It then runs
`session/resume` on the same native. The resumed session must be idle, have its
original model and have a failed last turn. It sets max effort and allowAll,
rechecks custody, and only then continues. The native's durable goal state
(`goals.db`) selects the only admitted continuation:

- `active` or no goal: one bounded `turn/start` with the configured
  continuation prompt. No goal is created or touched.
- `blocked` by the failure: `goal/resume`.
- user `paused` or unknown: stop, so user pauses stay paused.
- `complete` (the native finished-goal status): stop. `recovery-ep-N.json`
  records it as an unsupported gap, and no budget is spent. Any other status is
  unknown and stops. The executor
follows the chained turns until none runs and none starts within
`quiet_seconds` (bounded by `max_seconds`). It then writes `recovery-ep-N.json`
and closes the host.
The Worker is left idle on its native for the Dispatcher's next decision. It
never types into a composer and writes nothing to the ledger. The required
`expected_server_version`/`expected_schema_fingerprint` pin the qualified MSP
build.

`muse_failure_handling.py` is the pure Dispatcher-side contract over that
observation: `decide` returns `continue` (one bounded continuation per
episode), `awaiting-terminal` (early event: never acked, consumed or dropped)
or `stop` (auth/quota/config, paused, completed, pending decision, spent
budget). The budget key includes the episode identity. No
hook->reply->failure loop: no duplicate Worker, relay restart or external
replay is admitted. A stopped MSP session routes through the same durable
`runtime-failure` event as a handoff candidate for the owning Dispatcher cycle;
MSP still lacks a qualified per-thread writer-epoch transfer (see the shared-App
boundary above), so ownership continuity after a stop remains an adapter gap,
not proven delivery. The recovery executor above is the only admitted return
path, and only for the same native under its unchanged custody.
