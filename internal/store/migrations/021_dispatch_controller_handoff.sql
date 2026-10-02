-- Additive protocol fence: retired controllers cannot reserve new work or regain
-- their old cohort by racing an owner-initiated handoff. Other actors/ENV untouched.
CREATE TABLE dispatch_controller_bindings (
 repo_id TEXT NOT NULL,
 actor TEXT NOT NULL,
 native_session TEXT NOT NULL,
 epoch INTEGER NOT NULL CHECK(epoch > 0),
 PRIMARY KEY(repo_id,actor),
 UNIQUE(repo_id,native_session)
) STRICT;
CREATE TABLE dispatch_retired_controllers (
 repo_id TEXT NOT NULL,
 actor TEXT NOT NULL,
 retired_at INTEGER NOT NULL,
 successor TEXT NOT NULL,
 PRIMARY KEY(repo_id,actor)
) STRICT;
CREATE TABLE dispatch_handoffs (
 repo_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 request_sha256 TEXT NOT NULL,
 receipt TEXT NOT NULL,
 PRIMARY KEY(repo_id,request_id)
) STRICT;
CREATE TRIGGER dispatch_reject_retired_insert BEFORE INSERT ON dispatch_reservations
 WHEN EXISTS(SELECT 1 FROM dispatch_retired_controllers f WHERE f.repo_id=NEW.repo_id AND f.actor=NEW.reserved_by)
 BEGIN SELECT RAISE(ABORT,'retired Dispatcher cannot reserve'); END;
CREATE TRIGGER dispatch_reject_retired_update BEFORE UPDATE ON dispatch_reservations
 WHEN EXISTS(SELECT 1 FROM dispatch_retired_controllers f WHERE f.repo_id=NEW.repo_id AND f.actor=NEW.reserved_by)
 BEGIN SELECT RAISE(ABORT,'retired Dispatcher cannot own reservations'); END;
CREATE TABLE dispatch_controller_receivers (
 repo_id TEXT NOT NULL,
 actor TEXT NOT NULL,
 native_session TEXT NOT NULL,
 epoch INTEGER NOT NULL,
 incarnation TEXT NOT NULL,
 PRIMARY KEY(repo_id,actor)
) STRICT;
