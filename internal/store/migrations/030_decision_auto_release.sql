-- on_satisfied_revision lets the Dispatcher pre-authorize a structured hold's
-- release: when it equals the row's revision, the hold becomes a proceed
-- decision, and its Worker is woken, as soon as every wait is satisfied. It
-- stores the revision that carried the authorization rather than a flag, so a
-- later revision written by any binary that does not set it (including one that
-- already knows the wait columns) never inherits it. 0 means not authorized.
ALTER TABLE dispatch_decisions ADD COLUMN on_satisfied_revision INTEGER NOT NULL DEFAULT 0;

-- dispatch_decisions keeps only the current row per assignment. The history
-- records every revision so the number and kind of decisions an assignment
-- needed can be measured. decided_by is the Dispatcher, or 'waits-satisfied'
-- for a pre-authorized automatic release.
CREATE TABLE dispatch_decision_history (
 repo_id TEXT NOT NULL,
 reservation_key TEXT NOT NULL,
 generation INTEGER NOT NULL,
 item_id TEXT NOT NULL,
 revision INTEGER NOT NULL,
 outcome_id INTEGER NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('proceed','hold')),
 condition TEXT NOT NULL,
 waits_for TEXT NOT NULL DEFAULT '[]',
 on_satisfied TEXT NOT NULL DEFAULT '',
 decided_by TEXT NOT NULL,
 decided_at INTEGER NOT NULL,
 PRIMARY KEY(repo_id,reservation_key,generation,item_id,revision)
) STRICT;
