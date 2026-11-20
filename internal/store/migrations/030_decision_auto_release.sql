-- on_satisfied lets the Dispatcher pre-authorize a structured hold's release:
-- 'proceed' turns the hold into a proceed decision, and wakes the held Worker,
-- as soon as every wait is satisfied. '' keeps the earlier behavior (a
-- satisfied hold waits for a new Dispatcher decision). It is only honored when
-- waits_revision=revision, so an older binary's rewrite never inherits it.
ALTER TABLE dispatch_decisions ADD COLUMN on_satisfied TEXT NOT NULL DEFAULT '';

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
