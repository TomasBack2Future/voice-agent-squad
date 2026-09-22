-- Corrections are append-only. Original command, exit status and output survive.
CREATE TABLE attestation_revocations (
    attestation_id INTEGER PRIMARY KEY REFERENCES attestations(id),
    reason TEXT NOT NULL CHECK(length(trim(reason)) > 0),
    agent_id TEXT NOT NULL CHECK(length(trim(agent_id)) > 0),
    created_at INTEGER NOT NULL,
    replacement_id INTEGER REFERENCES attestations(id),
    CHECK(replacement_id IS NULL OR replacement_id > attestation_id)
);
