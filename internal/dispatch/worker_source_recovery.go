package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

// SourceWorkerRecovery binds explicit human authority to one current-controller
// transition when the joined original native cannot return a consent message.
// Its old decision receipts remain undelivered; the successor receives a new HOLD.
type SourceWorkerRecovery struct {
	HumanAuthoritySHA256 string   `json:"human_authority_sha256"`
	ExpiresAt            int64    `json:"expires_at"`
	PendingEventIDs      []string `json:"pending_event_ids"`
	AttestationOutcomeID int64    `json:"attestation_outcome_id"`
}

func (s *Store) validateSourceWorkerRecovery(ctx context.Context, tx *sql.Tx, q WorkerHandoffRequest, digest string) error {
	recovery := q.SourceRecovery
	now := s.now().Unix()
	if !validLegacyHash(recovery.HumanAuthoritySHA256) || recovery.AttestationOutcomeID < 1 || recovery.ExpiresAt <= now || recovery.ExpiresAt > now+86400 || len(recovery.PendingEventIDs) > 32 || !sort.StringsAreSorted(recovery.PendingEventIDs) {
		return errors.New("bounded exact human-authorized source recovery attestation required")
	}
	for index, event := range recovery.PendingEventIDs {
		if event == "" || (index > 0 && recovery.PendingEventIDs[index-1] == event) {
			return errors.New("source recovery decision snapshot contains an invalid or duplicate receipt")
		}
	}
	var body string
	if err := tx.QueryRowContext(ctx, `SELECT body FROM messages WHERE repo_id=? AND id=? AND agent_id=? AND thread=? AND kind IN ('say','supervisory-attestation') AND ts>=?`, s.repoID, recovery.AttestationOutcomeID, q.Controller.Actor, q.Claim.Item, q.Expected.ReservedAt).Scan(&body); err != nil {
		return errors.New("current controller source recovery attestation unavailable")
	}
	var proof struct {
		Schema               string `json:"schema_version"`
		RequestSHA256        string `json:"request_sha256"`
		HumanAuthoritySHA256 string `json:"human_authority_sha256"`
		ExpiresAt            int64  `json:"expires_at"`
	}
	if len(body) > 16384 || json.Unmarshal([]byte(body), &proof) != nil || proof.Schema != "squad.source-worker-recovery-attestation.v1" || proof.RequestSHA256 != digest || proof.HumanAuthoritySHA256 != recovery.HumanAuthoritySHA256 || proof.ExpiresAt != recovery.ExpiresAt {
		return errors.New("exact controller source recovery digest and human authority required")
	}
	rows, err := tx.QueryContext(ctx, `SELECT event_id,recipient,worker_session,kind FROM terminal_event_receipts WHERE repo_id=? AND reservation_key=? AND generation=? AND processed_at=0 ORDER BY event_id`, s.repoID, q.Expected.ItemID, q.Expected.Generation)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var event, recipient, native, kind string
		if err := rows.Scan(&event, &recipient, &native, &kind); err != nil {
			return err
		}
		if index >= len(recovery.PendingEventIDs) || recovery.PendingEventIDs[index] != event || recipient != q.Claim.Actor || native != q.Expected.WorkerThreadID || kind != "decision-resolved" {
			return errors.New("source recovery pending decisions changed or other owner handling remains required")
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if index != len(recovery.PendingEventIDs) {
		return errors.New("source recovery pending decision snapshot changed")
	}
	return nil
}
