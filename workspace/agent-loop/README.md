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
