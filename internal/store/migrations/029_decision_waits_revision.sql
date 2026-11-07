-- waits_revision records the decision revision whose writer set the structured
-- waits. An older binary that rewrites the decision (its upsert does not know
-- the wait columns) leaves waits_revision behind, so readers treat that newer
-- revision as a plain hold instead of inheriting the earlier waits.
ALTER TABLE dispatch_decisions ADD COLUMN waits_revision INTEGER NOT NULL DEFAULT 0;
UPDATE dispatch_decisions SET waits_revision=revision WHERE waits_for!='[]';
