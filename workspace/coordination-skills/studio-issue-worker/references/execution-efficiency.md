# Efficient real-database validation

Read before preparing isolated database evidence.

Reuse an existing verified engine and cached immutable image where compatible.
Use task-isolated containers, networks and volumes; a new Colima profile is not the
default. Inspect existing environment descriptors and helpers before inventing a setup.
Confirm target database version, edition and required Query profile capabilities.
Do not stop/reconfigure another task's resources or remove shared engine/image caches.

Before loading scale fixtures or applying a large index set, run a small readiness
probe through the same SDK and network placement as the real test: authentication,
KV write/read/delete in an owned synthetic namespace, Query, index service/storage mode,
required index creation/online state, and EXPLAIN/profile capability.
A successful management HTTP request does not establish SDK connectivity: Couchbase
advertises node addresses that must be reachable from the test process. Use verified
alternate-address mapping or place the test runner on the database network.
Record topology and bootstrap settings for reuse without credentials.
Use [delivery quality](delivery-quality.md) for fixture custody, retained-resource
deletion checks, successful-operation sampling and stage-duration accounting.
Use maintained minimum DDL when it accurately covers the test's dependencies; retain
full-schema gates when required. Do not repeatedly reinitialize a healthy test environment.

Check real database syntax and covering plans early, before expensive review or release.
Do not infer zero Fetch from mock SQL string checks. Classify failures as product,
test harness or environment and fix the evidenced cause before retrying.
Run independent checks together, freeze the review input once, and overlap review
with full CI as allowed by review-readiness.md. Re-run only affected checks after changes,
plus required final gates. Preserve the 20-minute review cap and acceptance requirements.
The user's 1.5x request means the app's Fast-mode lightning button (service tier),
independent of reasoning effort and Worker concurrency. Preserve enabled Fast mode;
do not reinterpret it as an efficiency target or claim that shell/tests run faster.

At startup compare actual task ID with reservation binding before mutation.
If another task owns the binding, report the duplicate and yield without adopting its
agent identity or forcing registration. If setup binding is pending, report once to
the Dispatcher and do useful read-only preparation.
