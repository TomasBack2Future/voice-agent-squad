-- Recovery is a fenced ownership transfer, not a release-and-reclaim gap.
-- generation lets downstream logs and deployment evidence identify which
-- owner epoch authorized an operation. state remains "held" for ordinary
-- claims and becomes "recovering" after an administrative takeover.
ALTER TABLE claims ADD COLUMN state TEXT NOT NULL DEFAULT 'held';
ALTER TABLE claims ADD COLUMN generation INTEGER NOT NULL DEFAULT 1;
ALTER TABLE claims ADD COLUMN previous_agent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE claims ADD COLUMN recovery_reason TEXT NOT NULL DEFAULT '';

CREATE TABLE claim_recoveries (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  repo_id            TEXT NOT NULL,
  item_id            TEXT NOT NULL,
  from_agent_id      TEXT NOT NULL,
  to_agent_id        TEXT NOT NULL,
  previous_generation INTEGER NOT NULL,
  generation         INTEGER NOT NULL,
  recovered_at       INTEGER NOT NULL,
  holder_session     TEXT NOT NULL,
  reason             TEXT NOT NULL,
  evidence           TEXT NOT NULL
) STRICT;
CREATE INDEX idx_claim_recoveries_item
  ON claim_recoveries(repo_id, item_id, recovered_at);
