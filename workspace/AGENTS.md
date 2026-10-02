# Studio workspace routing

This is a multi-repository workspace, not a monorepo. Enter the target repository
and read its root AGENTS.md before work. Each repository's instructions and docs
must remain usable when cloned alone or opened in an isolated worktree.

| Directory | Responsibility | Default branch |
| --- | --- | --- |
| `voice-agent-studio/` | Go/React product, contracts, Helm and release workflows | main |
| `voice-agent-squad/` | Local coordination CLI/MCP/plugin  | main |
| `convoai-studio-importer/` | Independently maintained Python source-to-Studio importer | main |
| `interceptor/` | Go request workflow/forwarding service and companion tools | master |

Do not treat sibling repositories as one Git history, copy credentials between
them, or assume a change to one has updated the others. Use independent branches,
tests and delivery evidence. Preserve unrelated work and active worktrees.

Each repository's AGENTS.md owns portable development rules; topic documents own
architecture and runbooks. Workspace-specific working policy belongs in this
outer file and the explicitly selected versioned package under
`voice-agent-squad/workspace/agent-loop`, not in product repository documents.
Keep live task state out of maintained documentation. Source work does not
authorize package installation, shared-environment changes or active-task
migration.

For an Agent Loop Worker, require one schema-valid assignment envelope and select
one role skill plus one project profile by ID/version. The canonical Issue owns
requirements; do not copy the Dispatcher's conversation into the Worker prompt.
Load phase-specific references only when that phase begins, and resume from a
schema-valid checkpoint rather than transcript replay. Data Analyze is historical
migration evidence, not a runtime dependency of this workspace.

## Studio database routing

Before Studio database access or deployment configuration changes, read the
current installed `.agents/project-profiles/studio/profile.json` and its
`references/couchbase-environments.md`. Their canonical source is
`voice-agent-squad/workspace/agent-loop/projects/studio/`; update and install that
source rather than editing a historical package snapshot.

Studio staging and production use `couchbase-cn-2` and bucket
`voice-agent-studio`, with separate scopes and runtime users: staging uses
`staging` / `voice-agent-studio-staging`; production uses `_default` /
`voice-agent-studio-production`. Production retains scope-limited sequential
query permission for its existing SQL. DDL uses separate operations credentials.
Keep the existing one-way old-to-new XDCR unchanged; writes on the new cluster
must be reconciled before any database rollback. Connection addresses, aliases
and network rules belong in the canonical reference, not inferred from old
reports or another environment.

## Runtime-independent role compatibility

Muse, Claude and Codex are eligible for every authorized role, including
Dispatcher, Worker, Deployer, Reviewer and Investigator. Apply the same capability
requirements to the same role/operation/custody mode; client names do not grant
or remove authority. Verify the actual execution surface and installed adapter,
not just skill visibility or executable presence. Use the maintained
`voice-agent-squad/workspace/agent-loop/runtime-compatibility.md` contract.
Missing support is a repairable capability gap with an owner; do not silently
substitute a different model, weaker permission/custody rule, or another role.
Existing review requirements, runtime guards, pauses and installation boundaries
remain in force. Generic auto-pick loops and client-specific hooks cannot override
a dedicated role's assignment or prove unattended progress.

## Session control plane

Squad is durable authority for assignment, ownership, dependency and environment
state. cmux is only the live-session transport and visibility layer.

- Use `tools/cmux-sessions/SKILL.md` and the cmux CLI for session launch,
  observation and narrowly authorized intervention. Prefer the bundled absolute
  CLI path when `cmux` is not on `PATH`; Computer Use is not the control plane.
- Create one new cmux workspace and native agent session per Worker. Persist the
  workspace ID, surface ID and native session ID with the reservation. Titles
  begin `[#<issue>][<item>]` and add the PR when known.
- Observation is bounded and read-only. Address exact IDs and never interpret a
  quiet session as terminal while durable ownership or an external operation is
  live.
- Input to a Worker is limited to its initial assignment, explicit user
  override, immediate safety/scope correction, or one verified dependency
  transition. Never send routine status requests or acknowledgements.

## Skill authority

Choose by object and operation, never an ambiguous keyword. Repository-owned
product/API skills at the checked-out revision are authoritative; global skills
are for machine-level or cross-repository capabilities and must be installed
from one versioned source. Do not use Data Analyze skills for new work.

| Object | Route | Exclusion |
| --- | --- | --- |
| Assigned Studio Issue | `studio-issue-worker` + Studio profile | Exact Issue/item only; no queue scan |
| Dispatch and Worker launch | `squad-dispatcher` + `cmux-sessions` | No implementation or environment claim |
| Stopped `ENV-*` holder | `squad-env-recovery` | Not ordinary lock contention |
| Studio Simulation → Evaluation | Studio repository `voice-agent-studio-simulation-evaluation` | Not a ConvoAI task investigation |
| Simulation materialization | `studio-simulation-materializer` | `dispatcher_runs` / `ai_ai` materialization only |
| ConvoAI call runtime task | `convoai-task-investigation` | Requires task ID/appid/bundle/transcript/stop reason; generic `eval` excluded |
| ConvoAI Session import | Importer repository `convoai-call-studio-import` | Import workflow only |
| Studio SLS evidence | Studio repository `.agents/skills/studio-sls-logs` | Explicit environment, UTC window and selector; read-only |

The old `studio-staging-sls-logs` name is a migration alias only. New references
use `studio-sls-logs` for both staging and production.

## Scoped environment resources

Preserve ENV-001/002 and existing claims. After explicit adoption of the versioned
`resources/studio.json` policy, Studio-only claims use `--scope studio` and
independent Importer/Feedback operations use their own environment items. Omitted
scope keeps legacy broad coverage. Ordinary Studio API traffic does not require a
Studio deployment lock. Cross-service acceptance acquires all required resources
in lexicographic item-ID order. Never bypass a detected cycle or automatically
release protected ownership. Installing policy or migrating active tasks is a
separate operation from editing source.

## Flow audits and instruction adoption

A workflow retrospective directly repairs authorized skills and working
agreements; its code findings are routed through existing Issues/PRs and owners.
Search open and closed work before filing, distinguish merged repairs from
unconfirmed causes, and do not infer Worker/dispatch authority from Issue creation.
Read the Dispatcher package's flow-retrospective reference for the role boundary.

Worker startup verifies and reads its role skill and project profile at the
selected source/package identities, recording that binding in existing evidence.
A catalog entry or installed link alone is not proof of instruction loading.
Resume reconciles decisions, current phase references, custody and external work;
link refresh does not migrate a running session. User pause stops new work safely
while preserving owned changes and operation evidence. Report source, commit,
merge, installation, session adoption and actual effect separately.

For an evidenced source-changing release blocker, merge the tested/reviewed fix
into main, select one exact new main head and review its complete production
delta, including extra merges/configuration/migrations/compatibility/downstream.
Prepare fresh CI/images/provenance, staging and production admission with a new
immutable tag/manifest; retain old evidence and never reuse old acceptance as new.
Pin the selected SHA. Transient failures requiring no source change retain the
original candidate. Preserve in-flight release identities, locks and ownership;
only the release owner performs an authorized transition.
