# HTTP API

Reference for the JSON endpoints exposed by `squad serve` (and consumed by the SPA, the TUI, and any custom integrations). All routes are mounted under `/api/`. Live updates ride a Server-Sent Events stream at `/api/events`.

> **Need a typed client?** The Go HTTP+SSE client lives at `internal/tui/client/`. CLI users should reach for `squad <verb>` (see [commands.md](commands.md)) — the API is the integration surface, not the day-to-day UX.

## Conventions

- **Transport:** plain HTTP/1.1, JSON request/response bodies.
- **Auth:** loopback only by default. Bind address is `127.0.0.1` unless explicitly overridden.
- **Status codes:** `200` on success, `404` when a named subject is missing, `500` for internal errors. The error body is always `{"error":"<message>"}`.
- **Empty lists:** list endpoints always return `[]`, never `null`, even when nothing matches.
- **Walkers swallow malformed-file errors:** parser errors on individual on-disk files are skipped silently — a typo in one spec file does not 500 the whole list. Outright IO failure on the parent directory still 500s.

## Specs

### `GET /api/specs`

List all specs parsed from `.squad/specs/`.

**Query parameters:** none.

**Response:** array of summary rows.

```json
[
  {
    "name": "sample-spec",
    "title": "Sample spec for server tests",
    "path": "/abs/path/to/.squad/specs/sample-spec.md"
  }
]
```

### `GET /api/specs/{name}`

Full detail for a single spec, keyed by slug (`name`).

**Path parameters:**
- `name` — spec slug (the filename minus `.md`).

**Response:**

```json
{
  "name": "sample-spec",
  "title": "Sample spec for server tests",
  "motivation": "Server tests need a real spec on disk to walk",
  "acceptance": ["GET /api/specs returns this spec", "GET /api/specs/sample-spec returns full body"],
  "non_goals": ["Production scenarios"],
  "integration": ["Used by internal/server tests only"],
  "body_markdown": "# Sample spec\n\nThis is a sample spec body...",
  "path": "/abs/path/to/.squad/specs/sample-spec.md"
}
```

`acceptance`, `non_goals`, and `integration` are always arrays (empty when the frontmatter list is missing).

**Errors:** `404` when no spec matches `{name}`.

## Epics

### `GET /api/epics`

List all epics parsed from `.squad/epics/`.

**Query parameters:**
- `spec` (optional) — restrict to epics whose `spec:` frontmatter equals this slug.

**Response:** array of summary rows.

```json
[
  {
    "name": "sample-epic",
    "spec": "sample-spec",
    "status": "open",
    "parallelism": "low",
    "path": "/abs/path/to/.squad/epics/sample-epic.md"
  }
]
```

### `GET /api/epics/{name}`

Full detail for a single epic.

**Path parameters:**
- `name` — epic slug.

**Response:**

```json
{
  "name": "sample-epic",
  "spec": "sample-spec",
  "status": "open",
  "parallelism": "low",
  "body_markdown": "# Sample epic\n\nSample epic body for tests.\n",
  "path": "/abs/path/to/.squad/epics/sample-epic.md"
}
```

**Errors:** `404` when no epic matches `{name}`.

## Attestations

### `GET /api/items/{id}/attestations`

The evidence ledger for an item: every recorded `squad attest` row, oldest first. Both successful and failed attestations are returned (`exit_code` distinguishes them).

**Path parameters:**
- `id` — item id (e.g. `BUG-100`, `FEAT-007`).

**Response:** array of ledger rows.

```json
[
  {
    "id": 42,
    "kind": "test",
    "command": "go test ./...",
    "exit_code": 0,
    "output_hash": "sha256-hex",
    "output_path": ".squad/attestations/sha256-hex.txt",
    "created_at": 1714134000,
    "agent_id": "agent-blue"
  }
]
```

`kind` is one of `test`, `lint`, `typecheck`, `build`, `review`, `manual`. `created_at` is Unix seconds. The endpoint returns `[]` for an item with no attestations (rather than 404) — non-existence of the item is indistinguishable from "no evidence yet" by design.

## Learnings

### `GET /api/learnings`

List learning artifacts walked from the configured learnings root (`.squad/learnings/`).

**Query parameters (all optional, applied as conjunctive filters):**
- `state` — one of `proposed`, `approved`, `rejected`.
- `kind` — one of `gotcha`, `pattern`, `dead-end`.
- `area` — exact-match filter against the learning's `area:` frontmatter.

**Response:** array of summary rows.

```json
[
  {
    "id": "GOTCHA-001",
    "kind": "gotcha",
    "slug": "sample-gotcha",
    "title": "Sample gotcha for server tests",
    "area": "server",
    "state": "approved",
    "created": "2026-04-26",
    "created_by": "agent-test",
    "paths": ["internal/server/**"],
    "related_items": ["BUG-100"]
  }
]
```

### `GET /api/learnings/{slug}`

Full detail for a single learning, including body and evidence.

**Path parameters:**
- `slug` — the learning's directory name under its kind/state pair.

**Response:**

```json
{
  "id": "GOTCHA-001",
  "kind": "gotcha",
  "slug": "sample-gotcha",
  "title": "Sample gotcha for server tests",
  "area": "server",
  "state": "approved",
  "created": "2026-04-26",
  "created_by": "agent-test",
  "session": "test-session",
  "paths": ["internal/server/**"],
  "evidence": ["tests pass"],
  "related_items": ["BUG-100"],
  "body_markdown": "# Sample gotcha\n\n## Looks like\n...",
  "path": "/abs/path/to/.squad/learnings/gotchas/approved/sample-gotcha.md"
}
```

`paths`, `evidence`, and `related_items` are always arrays. `session` may be empty when the proposal didn't capture one.

**Errors:** `404` when no learning matches `{slug}` in any kind/state combination.

## SSE events

`GET /api/events` opens a Server-Sent Events stream. Each frame has the shape:

```text
event: <kind>
data: {"kind":"<kind>","payload":{...}}

```

Subscribers should match on the `event:` line. Comment-only `: ping` heartbeats arrive at the configured ping interval to keep proxies from idle-closing the connection. A `lag` event is emitted when the server-side bus has dropped events for this subscriber; its `payload.dropped` is a best-effort drop count.

The subscription is registered before the initial response is flushed, so events
published immediately after connection acknowledgement enter that subscription.
The stream does not replay events published before subscription. Slow consumers
can still overflow the bounded queue: treat `lag` as a signal to refetch current
state, not as a lossless-delivery guarantee or an exact accounting total. Pending
drops are also flushed independently of heartbeats and subsequent publications.

The two events landed in the round that introduced specs/epics/learnings/attestations endpoints:

### `attestation_recorded`

Fires immediately after a successful `Insert` on the attestation ledger when the ledger is wired to the server's bus (i.e. publishes from server-mediated paths; CLI inserts via `squad attest` skip the publish because they don't share the bus). Re-inserts that hit the ledger's unique constraint and return the existing row do **not** re-publish.

**Payload:**

| Field | Type | Meaning |
|---|---|---|
| `item_id` | string | The item the attestation is scoped to. |
| `kind` | string | One of `test`, `lint`, `typecheck`, `build`, `review`, `manual`. |
| `id` | int64 | The new ledger row id. |
| `created_at` | int64 | Unix seconds when the row was written. |

```json
{
  "kind": "attestation_recorded",
  "payload": {"item_id": "BUG-100", "kind": "test", "id": 42, "created_at": 1714134000}
}
```

### `learning_state_changed`

Fires after `learning.Promote` successfully renames a learning between state directories (`proposed` ↔ `approved` ↔ `rejected`). Server-side promotions (the SPA / dashboard pathway) publish; CLI promotions via `squad learning approve` / `reject` pass `nil` for the bus and skip the publish.

**Payload:**

| Field | Type | Meaning |
|---|---|---|
| `slug` | string | The learning's slug. |
| `kind` | string | One of `gotcha`, `pattern`, `dead-end`. |
| `from_state` | string | The state the learning was in before the move. |
| `to_state` | string | The state directory it now lives in. |
| `path` | string | Absolute path of the destination file. |

```json
{
  "kind": "learning_state_changed",
  "payload": {
    "slug": "sample-gotcha",
    "kind": "gotcha",
    "from_state": "proposed",
    "to_state": "approved",
    "path": "/abs/path/to/.squad/learnings/gotchas/approved/sample-gotcha.md"
  }
}
```

## See also

- [commands.md](commands.md) — every `squad <verb>` and what it shells through.
- [db-schema.md](db-schema.md) — the underlying tables the read-side endpoints aggregate from.
- [hooks.md](hooks.md) — Claude Code hooks that ride the same SSE stream for in-session updates.
