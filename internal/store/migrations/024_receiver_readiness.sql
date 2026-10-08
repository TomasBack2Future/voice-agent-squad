-- Additive receiver readiness records (issue #83). A controller that
-- transitioned out of standby without activating its native receiver must
-- not admit asynchronous Worker launches. Readiness reuses the existing
-- dispatch_controller_receivers row plus the receiver's live native wake
-- endpoint (notify_endpoints kind 'rewake', instance
-- 'terminal:<actor>:<incarnation>'). No parallel ledger, no timers.
ALTER TABLE dispatch_controller_receivers ADD COLUMN owner_pid INTEGER NOT NULL DEFAULT 0;
ALTER TABLE dispatch_controller_receivers ADD COLUMN bound_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE dispatch_controller_receivers ADD COLUMN wake_kind TEXT NOT NULL DEFAULT '';
