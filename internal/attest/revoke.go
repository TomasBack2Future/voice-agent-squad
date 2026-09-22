package attest

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Revocation corrects evidence without changing the historical process result.
// A replacement is a pointer, never permission to reactivate the original.
type Revocation struct {
	Reason        string `json:"reason"`
	AgentID       string `json:"agent_id"`
	CreatedAt     int64  `json:"created_at"`
	ReplacementID int64  `json:"replacement_id,omitempty"`
}

// Revoke is repo-scoped and idempotent for the exact same correction. Any
// resolved actor can invalidate evidence, but cannot promote evidence through
// this operation. The actor and reason remain attached to the original record.
func (l *Ledger) Revoke(ctx context.Context, id int64, reason, agent string, replacement int64) (*Revocation, error) {
	reason, agent = strings.TrimSpace(reason), strings.TrimSpace(agent)
	if l.repoID == "" || id <= 0 || reason == "" || agent == "" || replacement < 0 {
		return nil, fmt.Errorf("repo, positive attestation id, reason and actor required; replacement must be nonnegative")
	}
	// One conditional write serializes correction against competing revocations.
	// The replacement must be a newer, successful, still-active row for the same
	// repo/item/kind. A later revocation never revives its predecessor.
	_, err := l.db.ExecContext(ctx, `INSERT INTO attestation_revocations
 (attestation_id, reason, agent_id, created_at, replacement_id)
 SELECT a.id, ?, ?, ?, NULLIF(?,0) FROM attestations a
 WHERE a.id=? AND a.repo_id=? AND (?=0 OR EXISTS (
 SELECT 1 FROM attestations b WHERE b.id=? AND b.id>a.id
 AND b.repo_id=a.repo_id AND b.item_id=a.item_id AND b.kind=a.kind
 AND b.exit_code=0 AND NOT EXISTS (
 SELECT 1 FROM attestation_revocations v WHERE v.attestation_id=b.id)))
 ON CONFLICT(attestation_id) DO NOTHING`, reason, agent, l.nowUnix(), replacement, id, l.repoID, replacement, replacement)
	if err != nil {
		return nil, fmt.Errorf("revoke attestation: %w", err)
	}
	var v Revocation
	err = l.db.QueryRowContext(ctx, `SELECT v.reason,v.agent_id,v.created_at,COALESCE(v.replacement_id,0)
 FROM attestation_revocations v JOIN attestations a ON a.id=v.attestation_id
 WHERE a.id=? AND a.repo_id=?`, id, l.repoID).Scan(&v.Reason, &v.AgentID, &v.CreatedAt, &v.ReplacementID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("attestation not found in repo or replacement is not newer active successful evidence of the same item/kind")
	}
	if err != nil {
		return nil, err
	}
	if v.Reason != reason || v.AgentID != agent || v.ReplacementID != replacement {
		return nil, fmt.Errorf("attestation already revoked with a different correction")
	}
	return &v, nil
}
