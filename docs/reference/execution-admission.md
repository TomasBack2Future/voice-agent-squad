# Production execution admission

Implemented in source. Installing this binary and configuring a reachable service
are separate operations; a source PR does not update an installed ledger.

`execution` adds a narrow admission service against the **same authoritative
SQLite ledger** used by claims. Do not serve a copied database on the runner.
The initial contract covers Studio `ENV-002`, with exclusive `*` or installed
`studio` scope. It does not authorize Importer, edge or external fleet changes.

## Contract and lifecycle

1. The live claim holder freezes and reviews the release manifest and production
   authorization. `squad execution authorize binding.json` records one immutable
   binding. IDs are unique and cannot be overwritten or reused. Authorization
   requires the exact current holder, generation, held state and scope.
2. The protected runner sends `POST /v1/executions/begin`. The authority independently
   reads the GitHub run using its own `gh` authentication: repository, workflow
   path/SHA, dispatch event, active state, run ID and attempt 1 must match. Admission
   then compares the complete binding and live ownership **in one transaction**,
   and registers the active operation. Exactly one run can win; a repeat from the
   same run is idempotent. The runner must not retry a lost begin automatically.
3. Before each independent write the runner sends `/v1/executions/check`. The
   exact binding, run/attempt, unexpired authorization and live claim must match.
   Receipts contain ID, state, run/attempt, step and check time, never credentials.
4. Expiry, network loss, cancellation and runner exit never remove the active pin.
   Database triggers block claim release, recovery, scope changes and replacement
   (including legacy clients and `INSERT OR REPLACE`) while it is active. Heartbeats
   still work. Releasing/replacing/transferring an unstarted claim revokes its
   authorizations, preventing release/reclaim reuse even if generation resets.
5. Use `squad execution show ID` to inspect the durable state and run identity,
   including after an ambiguous response. After the GitHub run is terminal, verify
   cluster operations and child processes have stopped and the environment has a
   known safe outcome. The holder then runs:

   ```sh
   squad execution reconcile ID --run-id RUN --run-attempt 1 \
     --confirm-external-stopped --evidence 'reference to verified terminal and environment evidence'
   ```

   This independently verifies terminal GitHub identity, checks the exact active
   run/holder and records reconciliation. It unpins but does not release the claim.
   Normal claim release/recovery follows. A lost session or `completed` Action alone
   is insufficient evidence that external work stopped. If the original holder
   needs recovery, a trusted operator must first reconcile under that holder's
   explicit recovery authority before the normal fenced claim transfer.

The runner cannot issue, renew, finish, reconcile or delete authorizations through
HTTP. There is no automatic expiry unlock. An expired active run stops new writes,
including rollback, and requires operator recovery with verified external stop
before a new authorization. Do not bypass a failed admission check.

## Binding JSON

All fields are required; unknown HTTP/CLI fields are rejected. `id` is the Studio
workflow's `dispatch_intent`. `manifest_sha256` is the reviewed frozen manifest
hash; `approval_ref` links its explicit production authorization. SHAs use full
40-character Git object IDs. `expires_at` is Unix seconds, at most 24 hours after
issuance. Scope must exactly match the claim (`""` legacy scope maps to `*`).

```json
{
  "id": "release-example",
  "item_id": "ENV-002",
  "holder": "deployer-session",
  "generation": 1,
  "scope": "*",
  "manifest_sha256": "<64 lowercase hex characters>",
  "repository": "owner/repository",
  "application_sha": "<40 lowercase hex characters>",
  "workflow_sha": "<40 lowercase hex characters>",
  "workflow_path": ".github/workflows/deploy-go-uap-production.yml",
  "operation": "candidate-upgrade",
  "target": {
    "environment": "production",
    "cluster_context": "production-context",
    "namespace": "production-namespace",
    "public_host": "studio.example.test",
    "go_release": "studio-go-production-candidate",
    "frontend_release": "studio-frontend-production-candidate"
  },
  "expires_at": 0,
  "approval_ref": "<approved frozen release reference>"
}
```

This is a shape example, deliberately not executable. Obtain every value from the
reviewed release and actual ledger. Canonical `cutover`/`rollback` bind `studio-go`
and `studio-frontend`; other supported operations bind candidate releases.
`preflight` has no admission because it cannot mutate the cluster.

HTTP bodies contain `binding` (the object above), positive integer `run_id`,
`run_attempt: 1` and bounded identifier `step`. Content type is `application/json`,
maximum body 16 KiB, with `Authorization: Bearer TOKEN`. Rejections never return
submitted data. The service performs no automatic network retries or mutation.

## Installation and transport

Run from the repository whose ledger owns `ENV-002`, with the existing authoritative
Squad home. The authority host needs read access via `gh` to the workflow repository.
Use a random token of at least 32 characters in a regular owner-only file.

```sh
squad execution serve --listen 127.0.0.1:7788 --token-file /protected/admission.token
```

Loopback HTTP is suitable for a separately managed authenticated tunnel. A
non-loopback listener requires `--tls-cert` and `--tls-key`; the runner validates
TLS normally. Bind privately and manage reachability, startup and token rotation
as deployment configuration. Do not expose the general dashboard or MCP to the
runner. The source does not install a service, open a firewall or copy secrets.

Trusted local MCP tools `squad_execution_authorize`, `squad_execution_show` and
`squad_execution_reconcile` share the same store and GitHub checks as the CLI.
Serving a network listener remains a CLI process operation. Their arguments are
`binding`; `id`; and `id/run_id/run_attempt/evidence/confirm_external_stopped`,
respectively. No tool grants broader production authorization.

## Limits

This fences cooperative workflow writes and ledger ownership. It is not a sandbox
against a compromised runner or direct database administrator, and it cannot stop
an already submitted Helm operation or Couchbase request. Pinning prevents a new
owner from starting while those operations might still run. External systems not
using this gate need their own authority and execution protocol.
