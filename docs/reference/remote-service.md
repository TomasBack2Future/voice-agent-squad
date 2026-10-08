# Shared coordination service

`squad service` serves a dedicated ledger from one Linux host over authenticated
HTTP. Remote CLI and MCP clients share that host's SQLite database and `.squad`
files; workspaces and source checkouts stay on the clients. Local CLI behavior is
unchanged when `SQUAD_REMOTE_URL` is absent.

## Service and client configuration

Initialize a **dedicated** Git ledger with `squad init --yes`, using a dedicated
`HOME` and `SQUAD_HOME`. Do not initialize or copy a live agent's store. Configure:

```json
{
  "workspace": "/var/lib/squad-service/ledger",
  "home": "/var/lib/squad-service/home",
  "receipts": "/var/lib/squad-service/receipts",
  "clients": [{
    "id": "alice-session-1",
    "token_sha256": "<SHA-256 hex of a cryptographically random token>",
    "agent": "alice-session-1",
    "session": "native-session-1",
    "role": "worker"
  }]
}
```

Create a random token of at least 32 characters per client/native session. Store
only its hash in service configuration; deliver the token through the client's
secret mechanism, never Git, chat or shared logs. Agent, session and token hashes
must be unique. Roles are `observer`, `worker`, and `controller`. Restart after
rotation. Agent/native identity comes from authenticated configuration, not from
request arguments. Clients sharing a token share an identity and must not do so.

Start `squad service --config /etc/squad-service/service.json`. Its default is
`127.0.0.1:7788`. Use an HTTPS reverse proxy or SSH tunnel; direct nonloopback
listeners require `--tls-cert` and `--tls-key`. The service has no cookies/CORS
administration surface, and rejects browser Origin headers. `/healthz` reports
process liveness and exact build version; use authenticated `status` to verify
ledger access. Never deploy multiple service instances against the same files.

Set `SQUAD_REMOTE_URL` to the HTTPS base URL (which may include a proxy prefix),
and `SQUAD_REMOTE_TOKEN` through the client's private environment. Run ordinary
commands such as `squad register`, `squad new task "Title" --ready`, `squad claim
TASK-001`, `squad say "Progress"`, `squad release TASK-001`, and `squad status`.
Requests never fall back to a client's local ledger. HTTP is allowed only on
loopback for tunnels. Redirects are rejected without forwarding credentials.

For Codex, Claude or Muse MCP configuration, use `squad mcp` with those environment
variables. This stdio bridge forwards JSON-RPC to authenticated `POST /mcp`.
Stateless HTTP MCP clients can also use that endpoint directly; no server-side
SSE stream, subscriptions or session persistence are required. `tools/list`
advertises only the allowed tools for that credential. Notifications return 202;
initialize, ping, tool discovery and calls return JSON.

## Supported boundary

The command/tool allowlists live in `internal/remote/policy.go`. Workers can
register, read, capture/admit items, claim/release, post chat/progress, heartbeat,
request review, hand off, and finish through existing ledger checks. Controllers
add ordinary fenced dispatch transitions and decisions. Observer credentials
cannot invoke work mutations (existing read handlers may refresh internal caches).
CLI-only dispatch subcommands remain CLI-only; HTTP does not invent MCP tools.

No remote arbitrary command attestation, filesystem upload, installation,
recovery/takeover, resource-policy editing, worktree creation or verification
override is exposed. `--repo`, `--worktree`, `--force`, `--skip-verify`, blocking
wait/tail and stdin commands are denied. Claims do not provision a server-side
worktree even if the ledger's local default normally would. Administrators own
configuration, including any fixed `done` verification gates. Execution/checkpoint
files remain owned by each worker; progress/evidence references can be posted to
the durable ledger. Running remote workers and migrating active sessions need
their own workflow integration and are not accomplished by starting this service.

Operations use the existing CLI/MCP handlers under isolated, server-selected
identity and environment. One bounded execution queue serializes file mutations
and SQLite transactions; no client-supplied shell, cwd or environment is run.
At most 32 requests queue, each execution has a two-minute limit. Long polling
and host event receivers must run separately. The service does not own cmux or
wake remote native sessions by itself.

## Retry and durability

CLI requests carry `Idempotency-Key`. Set `SQUAD_REQUEST_ID` explicitly when a
caller must retain a key across its own crash. HTTP MCP clients can supply the
same optional header; the stdio bridge always supplies one. A completed request
replays its stored response for the same principal/path/body, including across
service restart or binary update. Reusing a key for different input is rejected.
Without the optional HTTP MCP key, only the underlying ledger's operation fences
apply; generic creates/messages must not be blindly retried.

Admission is persisted before executing, and the response before returning.
Disconnects do not cancel already admitted work. A crash between mutation and
response persistence leaves an uncertain receipt: it returns 409 and requires
ledger reconciliation instead of repeating the mutation. This is intentionally
not a claim of transactional exactly-once execution across filesystem/SQLite.
CLI transport errors show the request key and never automatically retry.
Receipts contain potentially private output; directory mode is 0700 and files
0600. Back up ledger, database and receipts together while the service is stopped.
Do not prune receipts before their callers' retry/reconciliation horizon.

## Linux deployment and automatic main adoption

`deploy/` contains optional systemd templates and a deliberately installed updater.
Use a dedicated `squad-service` user; put data in `/var/lib/squad-service`, hashed
configuration in `/etc/squad-service/service.json`, and immutable binaries plus
`manifest.json` in `/opt/squad-service/releases/<SHA>`. The manifest contains `sha`.
An atomic `/opt/squad-service/current` symlink selects a release. Configuration
should be root-owned, group-readable by the service, mode 0640. The service can
write only its state directory and temporary files. Allow TCP 443 at the proxy;
keep the upstream listener on loopback. Proxy timeout should exceed 135 seconds,
body limit 1 MiB, and Authorization must be forwarded. Reload the proxy only after
its configuration check passes.

The optional updater expects a clean clone in `/opt/squad-service/source`, writable
by the service user, Go with automatic toolchain downloads, Python 3, and operator
`gh` access to this repository's CI metadata. It fetches current main, requires a
successful exact-SHA push `ci.yml` run, ancestry from the deployed commit, and no
store implementation changes. It builds as the service user and runs isolated
real-binary remote acceptance before stopping the service. It backs up ledger,
database and receipts, atomically selects the binary, and checks its reported SHA.
The timer checks every five minutes. Main ahead of CI remains pending; store
changes require explicit compatibility admission and never auto-downgrade schema.
The installed updater itself is root-owned and does not self-update from Git.

On startup failure the updater restores the previous binary only, never rewinding
state that might already contain accepted writes. Preserve the failed manifest
and logs; another attempt at that SHA requires operator reconciliation. Backups
and receipts need operator-managed retention according to the recovery horizon.

An empty central ledger is an available service, not migration of existing work.
Before cutover, stop new local admission, wait for safe ownership boundaries,
back up both stores, establish one authority, transfer identities/claims using
supported fenced operations, configure every client, and verify a real durable
event reaches its native owner and completes downstream reconciliation. Preserve
all user pauses. Until then existing local work stays on its current ledger.

## Verification

`go test -race ./internal/remote` covers authentication, roles, identity spoofing,
durable/uncertain replay and token leakage through redirects. Build `squad`, then
run `python3 scripts/test_remote_service.py /absolute/path/to/squad` for isolated
real CLI/MCP identities, writes, competing claims, owner checks, reservation
generations and restart persistence. Never run this against a live ledger.
