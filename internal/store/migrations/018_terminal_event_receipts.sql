CREATE TABLE terminal_event_receipts (
 repo_id TEXT NOT NULL,
 recipient TEXT NOT NULL,
 event_id TEXT NOT NULL,
 reservation_key TEXT NOT NULL,
 generation INTEGER NOT NULL,
 worker_session TEXT NOT NULL,
 item_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 outcome_id INTEGER NOT NULL,
 source_message_id INTEGER NOT NULL,
 delivered_session TEXT NOT NULL DEFAULT '',
 delivered_at INTEGER NOT NULL DEFAULT 0,
 processed_at INTEGER NOT NULL DEFAULT 0,
 processed_note TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(repo_id, recipient, event_id)
);
