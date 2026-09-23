-- The authoritative ledger pins ownership until an externally verified terminal
-- execution is reconciled. Expiry stops new writes; it never releases a pin.
CREATE TABLE execution_authorizations (
 repo_id TEXT NOT NULL,
 id TEXT NOT NULL,
 item_id TEXT NOT NULL,
 holder TEXT NOT NULL,
 generation INTEGER NOT NULL,
 binding TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('authorized','active','reconciled','revoked')),
 run_id INTEGER NOT NULL DEFAULT 0,
 run_attempt INTEGER NOT NULL DEFAULT 0,
 last_step TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 reconciliation TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(repo_id,id)
);
CREATE UNIQUE INDEX execution_one_active ON execution_authorizations(repo_id,item_id) WHERE state='active';
CREATE TRIGGER execution_claim_delete BEFORE DELETE ON claims
BEGIN
 SELECT RAISE(ABORT,'active execution must be reconciled before releasing ownership') WHERE EXISTS (
  SELECT 1 FROM execution_authorizations e WHERE e.repo_id=OLD.repo_id AND e.item_id=OLD.item_id AND e.state='active'
 );
 UPDATE execution_authorizations SET state='revoked' WHERE repo_id=OLD.repo_id AND item_id=OLD.item_id AND state='authorized';
END;
CREATE TRIGGER execution_claim_update BEFORE UPDATE ON claims
WHEN NEW.agent_id!=OLD.agent_id OR NEW.generation!=OLD.generation OR NEW.state!=OLD.state
 OR NEW.resource_group!=OLD.resource_group OR NEW.resource_scope!=OLD.resource_scope
 OR NEW.repo_id!=OLD.repo_id OR NEW.item_id!=OLD.item_id OR NEW.claimed_at!=OLD.claimed_at
BEGIN
 SELECT RAISE(ABORT,'active execution must be reconciled before transferring ownership') WHERE EXISTS (
  SELECT 1 FROM execution_authorizations e WHERE e.repo_id=OLD.repo_id AND e.item_id=OLD.item_id AND e.state='active'
 );
 UPDATE execution_authorizations SET state='revoked' WHERE repo_id=OLD.repo_id AND item_id=OLD.item_id AND state='authorized';
END;
-- INSERT OR REPLACE may bypass delete triggers with recursive_triggers disabled.
CREATE TRIGGER execution_claim_replace BEFORE INSERT ON claims
WHEN EXISTS (SELECT 1 FROM execution_authorizations e WHERE e.repo_id=NEW.repo_id AND e.item_id=NEW.item_id AND e.state='active')
BEGIN
 SELECT RAISE(ABORT,'active execution must be reconciled before replacing ownership');
END;
CREATE TRIGGER execution_claim_reauthorize BEFORE INSERT ON claims
BEGIN
 UPDATE execution_authorizations SET state='revoked' WHERE repo_id=NEW.repo_id AND item_id=NEW.item_id AND state='authorized';
END;
