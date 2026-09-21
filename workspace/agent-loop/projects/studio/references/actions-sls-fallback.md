# Studio Worker fallback for an existing synthetic SLS run

Use this only for re-checking an existing synthetic Studio logging run when the
task has GitHub access but no local SLS read credential. It is not a general
product-log search.

The canonical direct-query skill is `studio-sls-logs`. Keep this fallback in the
Studio project profile rather than broadening the direct log-query skill with a
GitHub Actions execution path.

The maintained `Verify Studio Logging UAP Dev` workflow accepts
`existing_synthetic_run_id`. In that mode, its verification job queries SLS
using protected `uap-dev` Environment Secrets and skips kubeconfig setup, route
probe, Secret discovery, rollout inspection, Helm, and every Kubernetes API
operation.

```bash
gh workflow run verify-uap-logging.yml \
  --repo TomasBack2Future/voice-agent-studio \
  --ref main \
  -f expected_runtime_revision=DEPLOYED_40_CHAR_SHA \
  -f log_lookback=10m \
  -f existing_synthetic_run_id=GITHUB_RUN_ID-RUN_ATTEMPT
```

Watch the dispatched run with `gh`, then download only its non-secret logging
evidence artifact:

```bash
gh run watch RUN_ID --repo TomasBack2Future/voice-agent-studio --exit-status
gh run download RUN_ID \
  --repo TomasBack2Future/voice-agent-studio \
  --name studio-logging-uap-evidence-DEPLOYED_40_CHAR_SHA \
  --dir /private/tmp/studio-sls-evidence-RUN_ID
```

The safe evidence includes index/shape status, heartbeat and correlation
counts, response size/hash, and a classified failure kind; it never contains
raw SLS bodies.

The current workflow still prepares a pinned `kubectl` artifact as shared job
setup, even though the SLS-only branch does not contact Kubernetes. Therefore:

- from the investigator's perspective, no kubeconfig or cluster access is
  required;
- for a literal zero-`kubectl` execution path, use the direct helper instead;
- do not claim this fallback supports arbitrary event, request, trace, run, or
  time-range queries.

If the synthetic run ID is unknown or the investigation concerns ordinary
application events, report that the fallback does not apply. Do not invent an
ID and do not rerun a mutation-capable route probe merely to obtain logs.
