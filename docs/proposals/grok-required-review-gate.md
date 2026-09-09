# Local Grok pull-request review

Status: accepted for implementation

## Decision

Use a cooperative local review flow. The owning Codex Worker invokes one
reviewed command after its pull request is stable and quick local checks pass.
The review runs alongside full deterministic CI; both outcomes are joined
before merge readiness:

```text
Worker -> squad-grok-review -> grok CLI -> validate result
       -> GitHub App comment + SHA-bound Check
```

There is no webhook service, external scheduler, reviewer Agent, xAI HTTP
adapter, or reviewer database. The Dispatcher does not participate.

This decision trusts the local Worker to follow `AGENTS.md`. It is designed to
prevent accidental omission and stale-SHA merging, not to resist a malicious
Worker with control of the host. That narrower trust model is intentional.

## Local command

`squad-grok-review`:

1. Reads the current PR metadata and diff with the installed GitHub CLI.
2. Records the exact repository, PR, base SHA, and head SHA being reviewed.
3. Runs the installed `grok` CLI once in headless mode with the immutable core
   and repository policy, no tools, no web search, no subagents, and strict JSON
   output.
4. Validates the verdict and findings.
5. Re-reads the PR and refuses publication if the base or head changed during review.
6. Uses a short-lived GitHub App installation token to publish a sanitized PR
   comment and a Check Run on the reviewed head.
7. Atomically updates a safe observation record under
   `~/.squad/grok-reviews/` for the local read-only Squad monitor.

The command never gives the GitHub token or App private key to the Grok child.
It never publishes Grok's hidden thought, raw stdout/stderr, prompt, command
arguments, environment, or credentials.

The observation record contains only the review mode and stage, PR/base/head
identity, sanitized verdict/summary/finding titles, model usage and cost,
timings, and GitHub comment/Check links. It never contains the frozen diff,
prompt, private key, installation token, Grok hidden thought, stdout, or stderr.
Its lifecycle is `freezing -> sampling -> validating -> publishing`, followed
by `approved`, `blocking`, `error`, or `stale`. Monitor availability does not
change the review or merge result.

Build and run it locally:

```bash
go install ./cmd/squad-grok-review
```

Create the per-user configuration at the platform user-config location under
`squad/grok-review.json` (`~/Library/Application Support/squad/` on macOS or
`$XDG_CONFIG_HOME/squad/` on Linux):

```json
{
  "app_id": 4862345,
  "installation_id": 12323344,
  "app_private_key": "/secure/path/voice-agent-grok-reviewer.pem",
  "reasoning_effort": "medium"
}
```

The optional `SQUAD_GROK_REVIEW_CONFIG` environment variable or `--config`
flag may point to another absolute JSON path. CLI identity flags remain
available for controlled overrides, but Workers should use the shared local
configuration instead of assembling publisher identity arguments themselves.
The installation ID and key path are local setup values. Do not commit the
configuration, private key, or private-key contents, or include them in Agent
prompts, logs, comments, or Squad messages.

Validate the installed binary, reviewer bundle, private key, GitHub App
authentication, Grok CLI version and required flags, Grok login, configured
model, and writable Grok session storage without making a model call or
publishing a comment or Check:

```bash
squad-grok-review doctor --reasoning-effort medium
```

The Grok CLI persists its own session metadata under the configured Grok home.
Codex Workers must therefore run both `doctor` and the real review command as a
narrowly approved command outside the Codex filesystem sandbox. This is local
reviewer access only: it does not acquire a Squad environment claim and does
not authorize any deployment, merge, or unrelated filesystem operation. A
filesystem denial is reported as `local_permissions`, not authentication.

After doctor succeeds, a Worker passes repository, PR, derived mode and effort:

```bash
squad-grok-review \
  --repo TomasBack2Future/voice-agent-studio \
  --pr 705 --mode shadow --reasoning-effort medium
```

The wrapper pins effort instead of inheriting the user's global Grok settings.
Precedence is explicit flag, local configuration, then built-in `medium`.
Use `high` for security/authentication, migration, concurrency/locking, or
deployment/rollback changes. Select effort before sampling, not after seeing
a verdict. The 10-minute hard timeout is unchanged.

Start the local command as a managed asynchronous process with a retained
session handle, while full CI/integration checks and already-required isolated
builds run for the same frozen revision. Workers can prepare acceptance and
rollback notes outside the frozen input. Do not edit code/PR description during
sampling, duplicate workflows, occupy a hosted runner waiting for Grok, or hold
ENV. Collect both results before the merge gate. Required CI always applies;
only enforced mode requires App-pinned success. Timeout is incomplete in every
mode, never approval. Base/head changes invalidate the old tuple even for a
patch-identical rebase. Cancel/join obsolete owned work before a corrected head.

Run the command once for a substantive head. A pre-sampling failure may be
retried only after its operational cause is materially repaired; a sampled
timeout does not authorize blind retries. A valid blocking result requires verifying the finding and
pushing a real correction; do not repeatedly sample the same head or add a
no-op commit to obtain a different verdict.

Safe status records expose `reasoning_effort`, `reviewer_duration_ms` (CLI
invocation), `duration_ms` (whole review workflow), and token counts including
`reasoning_tokens` when reported. Missing usage is unknown, not zero cost.
`failure_kind` distinguishes timeout, cancellation, argument/authentication,
transport, output-limit and invalid-output errors; `failure_stage` distinguishes
freezing, sampling, validation, identity checks and publication. An approved
model result followed by a publishing error is not a successfully published
review. These fields never include raw output or hidden reasoning.

## GitHub publication

The App publishes two representations of the same result:

- A PR comment containing the exact head SHA, verdict, summary, and sanitized
  findings for human review.
- `grok-review-shadow` or `grok-review` on the exact head SHA for machine use.

A comment alone is not a merge gate. When enforcement is desired, repository
rules must require `grok-review` from the dedicated App and require the branch
to be up to date. The Worker revalidates the current head and Check after
acquiring the environment lock.

The App needs only metadata/read, pull-requests/read-write, contents/read, and
checks/write for the selected repository. It does not need a webhook, Actions,
deployment, environment, administration, or contents-write permission.

## Rollout

1. **Inactive:** the wrapper/App is unavailable; existing deterministic gates
   remain authoritative.
2. **Shadow:** Workers run the wrapper and publish `grok-review-shadow`, but its
   result does not block merging.
3. **Enforced:** branch policy requires the App-pinned `grok-review` Check on
   the current, up-to-date head.

Start with shadow on real Studio PRs. Enable enforcement only after the command
is reliable enough that provider availability and false blocks are acceptable.

## Acceptance

- A local command reviews one current PR with `grok` CLI and no model tools.
- The App posts a sanitized comment and Check bound to the reviewed head SHA.
- A head change during review produces no publication.
- Approved maps to Check success; blocking or error maps to failure.
- The Grok child environment contains no GitHub credential.
- Workers invoke the wrapper outside the environment lock and verify findings
  before changing code.
- Dispatcher and monitor remain read-only with respect to review execution.
