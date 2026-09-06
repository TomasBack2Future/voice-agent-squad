-- A durable reservation closes the discovery -> task-creation race. Only one
-- active Dispatcher generation may create a Worker for a canonical source.
CREATE TABLE dispatch_reservations (
  repo_id          TEXT NOT NULL,
  item_id          TEXT NOT NULL,
  source_ref       TEXT NOT NULL,
  reserved_by      TEXT NOT NULL,
  reserved_at      INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  expires_at       INTEGER NOT NULL,
  state            TEXT NOT NULL,
  generation       INTEGER NOT NULL DEFAULT 1,
  worker_thread_id TEXT NOT NULL DEFAULT '',
  note             TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (repo_id, item_id),
  UNIQUE (repo_id, source_ref),
  CHECK (state IN ('reserved', 'dispatched', 'completed', 'failed', 'cancelled'))
) STRICT;
CREATE INDEX idx_dispatch_reservations_state
  ON dispatch_reservations(repo_id, state, updated_at);
