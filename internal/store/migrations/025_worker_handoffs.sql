-- Successful exact-tuple source Worker transfers are immutable retry receipts.
-- This does not revoke a live writer, transfer ENV, or replace a controller.
CREATE TABLE worker_handoffs (
 repo_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 request_sha256 TEXT NOT NULL,
 receipt TEXT NOT NULL,
 PRIMARY KEY(repo_id,request_id)
) STRICT;

-- A legacy observation is produced by the native-owner/host adapter, not an
-- imported JSON assertion or a manufactured execution authorization.
CREATE TABLE legacy_worker_stops (
 repo_id TEXT NOT NULL,
 id TEXT NOT NULL,
 request_sha256 TEXT NOT NULL,
 preparation TEXT NOT NULL,
 processes TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('prepared','observed','transferred')),
 observation_sha256 TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(repo_id,id)
) STRICT;
CREATE TABLE worker_native_fences (
 repo_id TEXT NOT NULL,
 native_session TEXT NOT NULL,
 actor TEXT NOT NULL,
 reservation_key TEXT NOT NULL,
 generation INTEGER NOT NULL,
 stop_id TEXT NOT NULL,
 PRIMARY KEY(repo_id,native_session)
) STRICT;

CREATE TRIGGER legacy_worker_reject_execution_pin BEFORE INSERT ON execution_authorizations
WHEN NEW.state='active' AND EXISTS(
 SELECT 1 FROM worker_native_fences f WHERE f.repo_id=NEW.repo_id
 AND f.native_session=json_extract(NEW.binding,'$.native'))
BEGIN SELECT RAISE(ABORT,'legacy Worker native is fenced'); END;

CREATE TRIGGER legacy_worker_reject_execution_reopen BEFORE UPDATE ON execution_authorizations
WHEN NEW.state='active' AND EXISTS(
 SELECT 1 FROM worker_native_fences f WHERE f.repo_id=NEW.repo_id
 AND f.native_session=json_extract(NEW.binding,'$.native'))
BEGIN SELECT RAISE(ABORT,'legacy Worker native is fenced'); END;

CREATE TRIGGER legacy_worker_reject_native_insert BEFORE INSERT ON dispatch_reservations
WHEN EXISTS(SELECT 1 FROM worker_native_fences f WHERE f.repo_id=NEW.repo_id
 AND f.native_session=NEW.worker_thread_id)
BEGIN SELECT RAISE(ABORT,'legacy Worker native cannot resume automatically'); END;

CREATE TRIGGER legacy_worker_reject_native_rebind BEFORE UPDATE OF worker_thread_id ON dispatch_reservations
WHEN NEW.worker_thread_id!=OLD.worker_thread_id AND EXISTS(
 SELECT 1 FROM worker_native_fences f WHERE f.repo_id=NEW.repo_id
 AND f.native_session=NEW.worker_thread_id)
BEGIN SELECT RAISE(ABORT,'legacy Worker native cannot resume automatically'); END;

CREATE TRIGGER legacy_worker_reject_old_callback BEFORE INSERT ON terminal_event_receipts
WHEN NEW.kind!='decision-resolved' AND EXISTS(
 SELECT 1 FROM worker_native_fences f WHERE f.repo_id=NEW.repo_id
 AND f.native_session=NEW.worker_session)
BEGIN SELECT RAISE(ABORT,'legacy Worker native callback is fenced'); END;
