-- Structured waits make a hold's dependencies checkable instead of prose: each
-- wait names another reservation and the phase result (or terminal state) that
-- satisfies it. blocks_phase names the phase of this assignment the hold stops
-- ('' means every phase). wait_deadline is an optional time box (unix seconds);
-- expiry is surfaced for reconciliation and never turns a hold into proceed.
ALTER TABLE dispatch_decisions ADD COLUMN waits_for TEXT NOT NULL DEFAULT '[]';
ALTER TABLE dispatch_decisions ADD COLUMN blocks_phase TEXT NOT NULL DEFAULT '';
ALTER TABLE dispatch_decisions ADD COLUMN wait_deadline INTEGER NOT NULL DEFAULT 0;

-- A phase result is the owner's durable statement that a phase's checks were
-- executed (all or partially) and with which verdict. It is evidence for
-- dependent waits, never Issue closure or acceptance authority.
CREATE TABLE dispatch_phase_results (
 repo_id TEXT NOT NULL,
 reservation_key TEXT NOT NULL,
 phase TEXT NOT NULL,
 generation INTEGER NOT NULL,
 item_id TEXT NOT NULL,
 checks TEXT NOT NULL CHECK(checks IN ('all','partial')),
 verdict TEXT NOT NULL CHECK(verdict IN ('passed','failed','partial')),
 deferred TEXT NOT NULL DEFAULT '',
 outcome_id INTEGER NOT NULL,
 recorded_by TEXT NOT NULL,
 recorded_at INTEGER NOT NULL,
 PRIMARY KEY(repo_id,reservation_key,phase)
) STRICT;
