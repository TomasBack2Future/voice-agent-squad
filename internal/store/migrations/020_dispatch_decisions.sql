-- Decisions are opt-in per reservation generation and canonical item. History
-- remains in messages; this row identifies the only currently effective one.
CREATE TABLE dispatch_decisions (
 repo_id TEXT NOT NULL,
 reservation_key TEXT NOT NULL,
 generation INTEGER NOT NULL,
 item_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision > 0),
 outcome_id INTEGER NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('proceed','hold')),
 condition TEXT NOT NULL,
 worker_agent TEXT NOT NULL,
 PRIMARY KEY(repo_id,reservation_key,generation,item_id)
) STRICT;
