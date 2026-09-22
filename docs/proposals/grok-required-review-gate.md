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
The CLI's default timeout is ten minutes; the example explicitly selects twenty.
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
