# Recovery smoke and native qualification

Run this from the source repository. It uses temporary Git repositories, a
newly built Squad executable, isolated SQLite ledgers and owned fixture
processes. It neither reads the live Squad ledger nor invokes models in
`contract` mode. Go and Python 3 are required.

```sh
python3 -m pip install -r workspace/agent-loop/workflow-requirements.txt
PYTHONDONTWRITEBYTECODE=1 python3 workspace/agent-loop/recovery_smoke.py contract \
  --output /tmp/squad-recovery-smoke.json
```

A nonempty suite must run without failures, errors, skips or expected failures.
A missing test module, zero collected tests or missing native prerequisites
cannot produce a passing report. The report records the source revision and
whether the checkout is dirty, each suite's counts and the native coverage gaps.
It is test evidence, **not an execution admission or ownership receipt**.

## What runs without model access

| Coverage | Actual assertion |
| --- | --- |
| All nine directed Claude/Codex/Muse controller identity combinations, including same-client controller replacement | Real portable identity adapter, Squad CLI and SQLite; controller handoff retains the legacy Worker, claim, pause and uncommitted fixture; replay is idempotent; retired controller cannot rebind |
| Post-transfer event handling and progress | Pending result reaches the new controller; polling does not mean handled; explicit delivery and handling persist; next decision reaches the original Worker; a new phase publishes once |
| Dispatcher, Worker, Deployer, Reviewer, Investigator role boundary | Worker envelope accepts Worker and rejects the other four roles; deployment is never relabeled as source work |
| Interrupted tools, stale ownership, lost response and result replay | Existing owned-writer/stdio, receiver, result-publication and readiness regressions |
| Timeout continuation | Completed steps are retained; pause, outstanding decisions, active external operations and exhausted budgets prevent unsafe retry |

The runtime names above select real **coordination identity adapters**. They do
not claim that nine native model/client transfers occurred. The test intentionally
uses a legacy Worker without an execution pin; controller handoff must preserve
it, and must not be mistaken for permission to replace it. Worker replacement,
terminal reservations with remaining acceptance, external custody and native
stop/fencing are separate acceptance obligations (see #119 / PR #120).

The dedicated `recovery-smoke` CI job runs for runtime/workspace, Go/store and
CI changes, and on main/manual full runs. Its JSON report is uploaded even on
failure. The aggregate `gate` rejects a missing, failed, cancelled or skipped
selected job. `release-smoke` remains the independent packaging/version check.
An administrator must enable the documented required check in hosting settings
if branch protection is desired; adding a workflow alone does not enable it.

## Native runs and missing coverage

```sh
python3 workspace/agent-loop/recovery_smoke.py coverage --output /tmp/coverage.json
python3 workspace/agent-loop/recovery_smoke.py native --runtime muse --role worker \
  --output /tmp/native-worker.json
```

The second command deliberately invokes real model-backed tests and requires
`MUSE_NATIVE_EXECUTABLE` and `MUSE_WORKER_QUALIFICATION`, as described in the
[Agent Loop native qualification instructions](../workspace/agent-loop/README.md).
Use owned isolated fixtures, the exact selected executable/model/effort/permissions
and the existing installer-qualified configuration. Preserve the tests' native
receipts and client hashes alongside the report. Missing prerequisites cause
skips which this runner converts to failure; it never falls back to simulation.

The existing Muse suites qualify only **source Worker new/resume/hold** on their
actual configuration. Even a passing run explicitly leaves cross-runtime
migration unqualified. They do not qualify Deployer/ENV operations, Reviewer,
Investigator, Dispatcher wake, other clients, or another executable version.
Other runtime/role selections return `blocked` without launching anything until
a complete native suite is implemented. The coverage report enumerates all 15
runtime/role cells, so missing implementations cannot disappear from a green
contract run. Existing Codex native receiver/fencing work remains tracked in #79.

For a new native operation, add its real positive and negative suite, register
its **exact scope** in `NATIVE_SUITES`/`NATIVE_SCOPE`, and test that missing inputs
and skipped cases fail. A full cross-runtime migration suite must start from the
actual donor state (including legacy donors), retain code, holds, retry budgets
and external operations, join/fence the old writer, adopt the replacement, execute
the next task step, deliver/handle its result and clean up. Declaring a runtime,
a fake protocol host or a passing unit test does not satisfy that contract.
