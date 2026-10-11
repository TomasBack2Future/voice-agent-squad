-- Version25 is already eligible installed state. Harden it through an additive
-- upgrade rather than assuming its original trigger definition will rerun.
DROP TRIGGER legacy_worker_reject_old_callback;
CREATE TRIGGER legacy_worker_reject_old_callback BEFORE INSERT ON terminal_event_receipts
WHEN EXISTS(SELECT 1 FROM worker_native_fences f WHERE f.repo_id=NEW.repo_id
 AND f.native_session=NEW.worker_session)
BEGIN SELECT RAISE(ABORT,'legacy Worker native callback is fenced'); END;

-- Only the kernel-qualified synchronous hook adapter produces these records.
-- A controller admission or a configuration file alone is not hook readiness.
CREATE TABLE worker_native_hook_observations (
 repo_id TEXT NOT NULL,
 native_session TEXT NOT NULL,
 actor TEXT NOT NULL,
 observation TEXT NOT NULL,
 observed_at INTEGER NOT NULL,
 PRIMARY KEY(repo_id,native_session)
) STRICT;
