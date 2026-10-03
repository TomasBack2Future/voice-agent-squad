-- A source Worker pin protects its complete dispatch tuple as well as the claim.
CREATE TRIGGER worker_execution_reservation_update BEFORE UPDATE ON dispatch_reservations
WHEN NEW.repo_id!=OLD.repo_id OR NEW.item_id!=OLD.item_id OR NEW.reserved_by!=OLD.reserved_by
 OR NEW.generation!=OLD.generation OR NEW.worker_thread_id!=OLD.worker_thread_id
 OR NEW.state!=OLD.state OR NEW.canonical_item_id!=OLD.canonical_item_id OR NEW.source_ref!=OLD.source_ref
BEGIN
 SELECT RAISE(ABORT,'active Worker execution must join before dispatch transfer') WHERE EXISTS(
 SELECT 1 FROM execution_authorizations e WHERE e.repo_id=OLD.repo_id AND e.state='active'
 AND CASE WHEN json_valid(e.binding) THEN json_extract(e.binding,'$.kind') END='worker'
 AND json_extract(e.binding,'$.reservation')=OLD.item_id);
END;
CREATE TRIGGER worker_execution_reservation_delete BEFORE DELETE ON dispatch_reservations
BEGIN
 SELECT RAISE(ABORT,'active Worker execution must join before dispatch deletion') WHERE EXISTS(
 SELECT 1 FROM execution_authorizations e WHERE e.repo_id=OLD.repo_id AND e.state='active'
 AND CASE WHEN json_valid(e.binding) THEN json_extract(e.binding,'$.kind') END='worker'
 AND json_extract(e.binding,'$.reservation')=OLD.item_id);
END;
CREATE TRIGGER worker_execution_reservation_replace BEFORE INSERT ON dispatch_reservations
BEGIN
 SELECT RAISE(ABORT,'active Worker execution must join before dispatch replacement') WHERE EXISTS(
 SELECT 1 FROM execution_authorizations e WHERE e.repo_id=NEW.repo_id AND e.state='active'
 AND CASE WHEN json_valid(e.binding) THEN json_extract(e.binding,'$.kind') END='worker'
 AND json_extract(e.binding,'$.reservation')=NEW.item_id);
END;
CREATE TRIGGER worker_execution_controller_update BEFORE UPDATE ON dispatch_controller_bindings
WHEN NEW.repo_id!=OLD.repo_id OR NEW.actor!=OLD.actor OR NEW.native_session!=OLD.native_session OR NEW.epoch!=OLD.epoch
BEGIN
 SELECT RAISE(ABORT,'active Worker execution must join before controller transfer') WHERE EXISTS(
 SELECT 1 FROM execution_authorizations e WHERE e.repo_id=OLD.repo_id AND e.state='active'
 AND CASE WHEN json_valid(e.binding) THEN json_extract(e.binding,'$.kind') END='worker'
 AND json_extract(e.binding,'$.controller')=OLD.actor);
END;
CREATE TRIGGER worker_execution_controller_delete BEFORE DELETE ON dispatch_controller_bindings
BEGIN
 SELECT RAISE(ABORT,'active Worker execution must join before controller deletion') WHERE EXISTS(
 SELECT 1 FROM execution_authorizations e WHERE e.repo_id=OLD.repo_id AND e.state='active'
 AND CASE WHEN json_valid(e.binding) THEN json_extract(e.binding,'$.kind') END='worker'
 AND json_extract(e.binding,'$.controller')=OLD.actor);
END;
CREATE TRIGGER worker_execution_controller_replace BEFORE INSERT ON dispatch_controller_bindings
BEGIN
 SELECT RAISE(ABORT,'active Worker execution must join before controller replacement') WHERE EXISTS(
 SELECT 1 FROM execution_authorizations e WHERE e.repo_id=NEW.repo_id AND e.state='active'
 AND CASE WHEN json_valid(e.binding) THEN json_extract(e.binding,'$.kind') END='worker'
 AND json_extract(e.binding,'$.controller')=NEW.actor);
END;
CREATE TRIGGER worker_execution_controller_retire BEFORE INSERT ON dispatch_retired_controllers
BEGIN
 SELECT RAISE(ABORT,'active Worker execution must join before controller retirement') WHERE EXISTS(
 SELECT 1 FROM execution_authorizations e WHERE e.repo_id=NEW.repo_id AND e.state='active'
 AND CASE WHEN json_valid(e.binding) THEN json_extract(e.binding,'$.kind') END='worker'
 AND json_extract(e.binding,'$.controller')=NEW.actor);
END;

-- Native ownership is independent of item and caller-selected local state path.
CREATE UNIQUE INDEX worker_execution_one_native ON execution_authorizations(
 json_extract(binding,'$.native'))
WHERE state='active' AND CASE WHEN json_valid(binding) THEN json_extract(binding,'$.kind') END='worker';
