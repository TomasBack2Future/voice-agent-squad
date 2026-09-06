package claims

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const recentHolderHeartbeat = 5 * time.Minute

// RecoveryRequest is deliberately verbose because recovery transfers a live
// lock without ever making it free. The caller must first verify the owning
// Codex task is stopped and record the external-system evidence used to make
// that decision.
type RecoveryRequest struct {
	ItemID               string
	ExpectedHolder       string
	RecoveringAgent      string
	HolderSession        string
	Reason               string
	Evidence             string
	ConfirmHolderStopped bool
}

type RecoveryResult struct {
	ItemID             string
	FromAgent          string
	ToAgent            string
	PreviousGeneration int64
	Generation         int64
	RecoveredAt        int64
}

// Recover atomically replaces the expected holder with the recovering agent.
// There is no release window. A compare-and-swap on holder + generation fences
// concurrent recovery attempts, and the displaced epoch is retained in both
// claim_history and claim_recoveries.
func (s *Store) Recover(ctx context.Context, req RecoveryRequest) (*RecoveryResult, error) {
	req.ItemID = strings.TrimSpace(req.ItemID)
	req.ExpectedHolder = strings.TrimPrefix(strings.TrimSpace(req.ExpectedHolder), "@")
	req.RecoveringAgent = strings.TrimPrefix(strings.TrimSpace(req.RecoveringAgent), "@")
	req.HolderSession = strings.TrimSpace(req.HolderSession)
	req.Reason = strings.TrimSpace(req.Reason)
	req.Evidence = strings.TrimSpace(req.Evidence)
	if req.ExpectedHolder == "" {
		return nil, ErrExpectedHolderRequired
	}
	if req.Reason == "" {
		return nil, ErrReasonRequired
	}
	if !req.ConfirmHolderStopped {
		return nil, ErrRecoveryConfirmationRequired
	}
	if req.HolderSession == "" || req.Evidence == "" {
		return nil, ErrRecoveryEvidenceRequired
	}
	if req.ItemID == "" || req.RecoveringAgent == "" {
		return nil, fmt.Errorf("claims: item and recovering agent are required")
	}

	var result RecoveryResult
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var holder, intent string
		var claimedAt, generation int64
		row := tx.QueryRowContext(ctx, `
			SELECT agent_id, claimed_at, generation, COALESCE(intent, '')
			FROM claims WHERE repo_id=? AND item_id=?
		`, s.repoID, req.ItemID)
		if err := row.Scan(&holder, &claimedAt, &generation, &intent); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotClaimed
			}
			return fmt.Errorf("lookup claim for recovery: %w", err)
		}
		if holder != req.ExpectedHolder {
			return fmt.Errorf("%w: expected %s, found %s", ErrHolderChanged, req.ExpectedHolder, holder)
		}

		// Agent registration is not required by the Codex wrapper, so absence
		// is not liveness evidence. Presence of a fresh active row is useful
		// negative evidence, however, and blocks an accidental takeover.
		var status string
		var lastTick int64
		err := tx.QueryRowContext(ctx, `
			SELECT status, last_tick_at FROM agents WHERE id=? AND repo_id=?
		`, holder, s.repoID).Scan(&status, &lastTick)
		if err == nil && status == "active" && lastTick >= s.now().Add(-recentHolderHeartbeat).Unix() {
			return fmt.Errorf("%w: %s ticked at %d", ErrHolderRecentlyActive, holder, lastTick)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("lookup holder heartbeat: %w", err)
		}

		now := s.nowUnix()
		res, err := tx.ExecContext(ctx, `
			UPDATE claims
			SET agent_id=?, claimed_at=?, last_touch=?,
			    intent=?, state='recovering', generation=generation+1,
			    previous_agent_id=?, recovery_reason=?
			WHERE repo_id=? AND item_id=? AND agent_id=? AND generation=?
		`, req.RecoveringAgent, now, now, "recovery: "+req.Reason,
			holder, req.Reason, s.repoID, req.ItemID, holder, generation)
		if err != nil {
			return fmt.Errorf("recover claim: %w", err)
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrHolderChanged
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO claim_history
			  (repo_id, item_id, agent_id, claimed_at, released_at, outcome)
			VALUES (?, ?, ?, ?, ?, 'recovered')
		`, s.repoID, req.ItemID, holder, claimedAt, now); err != nil {
			return fmt.Errorf("recovery history: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO claim_recoveries
			  (repo_id, item_id, from_agent_id, to_agent_id,
			   previous_generation, generation, recovered_at,
			   holder_session, reason, evidence)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, s.repoID, req.ItemID, holder, req.RecoveringAgent,
			generation, generation+1, now, req.HolderSession, req.Reason, req.Evidence); err != nil {
			return fmt.Errorf("recovery audit: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE touches SET released_at=?
			WHERE repo_id=? AND item_id=? AND agent_id=? AND released_at IS NULL
		`, now, s.repoID, req.ItemID, holder); err != nil {
			return fmt.Errorf("release displaced touches: %w", err)
		}

		body := fmt.Sprintf("recovered %s generation %d from %s to %s: %s",
			req.ItemID, generation+1, holder, req.RecoveringAgent, req.Reason)
		mentions := []string{holder}
		if err := postSystemMessage(ctx, tx, s.repoID, now, req.RecoveringAgent,
			"global", "recovery", body, mentions, "high"); err != nil {
			return err
		}
		if err := postSystemMessage(ctx, tx, s.repoID, now, req.RecoveringAgent,
			req.ItemID, "recovery", body, mentions, "high"); err != nil {
			return err
		}
		result = RecoveryResult{
			ItemID: req.ItemID, FromAgent: holder, ToAgent: req.RecoveringAgent,
			PreviousGeneration: generation, Generation: generation + 1, RecoveredAt: now,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
