-- Reserve an external source before creating its canonical Squad item.  This
-- closes the discovery-to-item-creation race between overlapping Dispatcher
-- cycles while preserving existing reservations created with item ids.
ALTER TABLE dispatch_reservations
  ADD COLUMN canonical_item_id TEXT NOT NULL DEFAULT '';

UPDATE dispatch_reservations
SET canonical_item_id = item_id
WHERE canonical_item_id = '';

CREATE UNIQUE INDEX idx_dispatch_reservations_canonical_item
  ON dispatch_reservations(repo_id, canonical_item_id)
  WHERE canonical_item_id <> '';
