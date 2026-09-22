-- Definitions are opt-in per ledger; existing callers retain the default scope.
CREATE TABLE resource_definitions (
  repo_id TEXT NOT NULL,
  item_id TEXT NOT NULL,
  resource_group TEXT NOT NULL,
  default_scope TEXT NOT NULL,
  service_scope TEXT NOT NULL,
  PRIMARY KEY (repo_id, item_id)
);
ALTER TABLE claims ADD COLUMN resource_group TEXT NOT NULL DEFAULT '';
ALTER TABLE claims ADD COLUMN resource_scope TEXT NOT NULL DEFAULT '';
ALTER TABLE claim_history ADD COLUMN resource_group TEXT NOT NULL DEFAULT '';
ALTER TABLE claim_history ADD COLUMN resource_scope TEXT NOT NULL DEFAULT '';
CREATE TABLE claim_waits (
  repo_id TEXT NOT NULL,
  wait_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  item_id TEXT NOT NULL,
  resource_group TEXT NOT NULL,
  resource_scope TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, wait_id)
);
-- Enforce conflicts for legacy binaries too: they omit both new columns.
CREATE TRIGGER resource_claim_guard BEFORE INSERT ON claims
BEGIN
  SELECT RAISE(ABORT, 'resource scope invalid') WHERE
    (NEW.resource_scope != '' AND NOT EXISTS (
      SELECT 1 FROM resource_definitions d WHERE d.repo_id=NEW.repo_id AND d.item_id=NEW.item_id
      AND NEW.resource_scope IN ('*', d.service_scope)))
    OR (NEW.resource_group != '' AND NOT EXISTS (
      SELECT 1 FROM resource_definitions d WHERE d.repo_id=NEW.repo_id AND d.item_id=NEW.item_id
      AND d.resource_group=NEW.resource_group));
  SELECT RAISE(ABORT, 'resource claim conflict') WHERE EXISTS (
    SELECT 1 FROM resource_definitions d JOIN claims c ON c.repo_id=d.repo_id
    WHERE d.repo_id=NEW.repo_id AND d.item_id=NEW.item_id
      AND c.resource_group=d.resource_group
      AND (c.resource_scope='*' OR
           COALESCE(NULLIF(NEW.resource_scope,''),d.default_scope)='*' OR
           c.resource_scope=COALESCE(NULLIF(NEW.resource_scope,''),d.default_scope))
  );
END;
CREATE TRIGGER resource_claim_snapshot AFTER INSERT ON claims
WHEN EXISTS (SELECT 1 FROM resource_definitions d WHERE d.repo_id=NEW.repo_id AND d.item_id=NEW.item_id)
BEGIN
  UPDATE claims SET
    resource_group=(SELECT resource_group FROM resource_definitions WHERE repo_id=NEW.repo_id AND item_id=NEW.item_id),
    resource_scope=COALESCE(NULLIF(NEW.resource_scope,''),(SELECT default_scope FROM resource_definitions WHERE repo_id=NEW.repo_id AND item_id=NEW.item_id))
  WHERE repo_id=NEW.repo_id AND item_id=NEW.item_id;
END;
-- Legacy release paths omit scope columns; capture the held snapshot before
-- those paths delete ownership. History remains meaningful across upgrades.
CREATE TRIGGER resource_history_snapshot AFTER INSERT ON claim_history
WHEN NEW.resource_group='' AND EXISTS (
 SELECT 1 FROM claims c WHERE c.repo_id=NEW.repo_id AND c.item_id=NEW.item_id AND c.agent_id=NEW.agent_id
)
BEGIN
 UPDATE claim_history SET
 resource_group=(SELECT resource_group FROM claims WHERE repo_id=NEW.repo_id AND item_id=NEW.item_id AND agent_id=NEW.agent_id),
 resource_scope=(SELECT resource_scope FROM claims WHERE repo_id=NEW.repo_id AND item_id=NEW.item_id AND agent_id=NEW.agent_id)
 WHERE rowid=NEW.rowid;
END;
