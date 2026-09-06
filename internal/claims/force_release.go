package claims

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) ForceRelease(ctx context.Context, itemID, byAgent, reason string) (priorHolder string, err error) {
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(itemID)), "ENV-") {
		return "", ErrProtectedResource
	}
	if reason == "" {
		return "", ErrReasonRequired
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var claimedAt int64
		row := tx.QueryRowContext(ctx, `SELECT agent_id, claimed_at FROM claims WHERE repo_id=? AND item_id=?`, s.repoID, itemID)
		if scanErr := row.Scan(&priorHolder, &claimedAt); scanErr != nil {
			if errors.Is(scanErr, sql.ErrNoRows) {
				return ErrNotClaimed
			}
			return fmt.Errorf("lookup claim: %w", scanErr)
		}
		now := s.nowUnix()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO claim_history (repo_id, item_id, agent_id, claimed_at, released_at, outcome)
			VALUES (?, ?, ?, ?, ?, 'force_released')
		`, s.repoID, itemID, priorHolder, claimedAt, now); err != nil {
			return fmt.Errorf("history: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM claims WHERE repo_id=? AND item_id=?`, s.repoID, itemID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE touches SET released_at = ?
			WHERE item_id = ? AND agent_id = ? AND released_at IS NULL
		`, now, itemID, priorHolder); err != nil {
			return err
		}

		body := fmt.Sprintf("force-released %s from %s by %s: %s", itemID, priorHolder, byAgent, reason)
		mentions := []string{priorHolder}
		if err := postSystemMessage(ctx, tx, s.repoID, now, byAgent, "global", "system", body, mentions, "high"); err != nil {
			return err
		}
		return postSystemMessage(ctx, tx, s.repoID, now, byAgent, itemID, "system", body, mentions, "high")
	})
	if err != nil {
		return "", err
	}
	return priorHolder, nil
}
