# Optional workspace bootstrap

This opt-in package implements a new-session Codex entrypoint plus the minimum
portable context layer needed for a bounded Worker pilot. It does not install
skills, switch an App, migrate state, collect usage, dispatch work, approve
releases or activate the proposed loop. The broader
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

The workspace `AGENTS.md` owns skill routing. Product workflow skills remain in
their owning repositories; cross-repository or machine capabilities have one
versioned source and are installed rather than copied. In particular, Studio
Simulation/Evaluation is not routed from the word `eval` to ConvoAI task logs,
and `studio-sls-logs` is the single target identity for staging and production
SLS reads.

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
