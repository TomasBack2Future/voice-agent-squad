package claims

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *Store) Release(ctx context.Context, itemID, agentID, outcome string) error {
	if outcome == "" {
		outcome = "released"
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		return s.releaseInTx(ctx, tx, itemID, agentID, outcome)
	})
}

func (s *Store) releaseInTx(ctx context.Context, tx *sql.Tx, itemID, agentID, outcome string) error {
	now := s.nowUnix()

	var holder string
	var claimedAt int64
	row := tx.QueryRowContext(ctx, `SELECT agent_id, claimed_at FROM claims WHERE repo_id=? AND item_id=?`, s.repoID, itemID)
	if err := row.Scan(&holder, &claimedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotClaimed
		}
		return fmt.Errorf("lookup claim: %w", err)
	}
	if holder != agentID {
		return ErrNotYours
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO claim_history (repo_id, item_id, agent_id, claimed_at, released_at, outcome, resource_group, resource_scope)
		SELECT repo_id,item_id,agent_id,claimed_at,?,?,resource_group,resource_scope FROM claims WHERE repo_id=? AND item_id=?
	`, now, outcome, s.repoID, itemID); err != nil {
		return fmt.Errorf("history insert: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM claims WHERE repo_id=? AND item_id=?`, s.repoID, itemID); err != nil {
		return fmt.Errorf("delete claim: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE touches SET released_at = ?
		WHERE item_id = ? AND agent_id = ? AND released_at IS NULL
	`, now, itemID, agentID); err != nil {
		return fmt.Errorf("release touches: %w", err)
	}

	body := fmt.Sprintf("released %s (%s)", itemID, outcome)
	if err := postSystemMessage(ctx, tx, s.repoID, now, agentID, "global", "release", body, nil, "normal"); err != nil {
		return err
	}
	return postSystemMessage(ctx, tx, s.repoID, now, agentID, itemID, "release", body, nil, "normal")
}

// ReleaseAll releases every active claim held by agentID with the given outcome.
// No-op when the agent holds no claims. Used by `squad handoff`.
func (s *Store) ReleaseAll(ctx context.Context, agentID, outcome string) error {
	_, err := s.ReleaseAllCount(ctx, agentID, outcome)
	return err
}

// ReleaseAllCount is like ReleaseAll but returns how many claims it
// released. Used by handoff's MCP/CLI surface to report the count back.
//
// The snapshot read and per-row releases run inside a single transaction so a
// peer cannot drop one of agent's claims between snapshot and per-item delete.
// Per-row ErrNotClaimed / ErrNotYours are tolerated within the loop — handoff
// is best-effort across the snapshot, not all-or-nothing.
func (s *Store) ReleaseAllCount(ctx context.Context, agentID, outcome string) (int, error) {
	if outcome == "" {
		outcome = "released"
	}
	released := 0
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		released = 0
		rows, err := tx.QueryContext(ctx, `SELECT item_id FROM claims WHERE repo_id = ? AND agent_id = ?`, s.repoID, agentID)
		if err != nil {
			return fmt.Errorf("list claims: %w", err)
		}
		var items []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			items = append(items, id)
		}
		rows.Close()
		for _, id := range items {
			if err := s.releaseInTx(ctx, tx, id, agentID, outcome); err != nil {
				if errors.Is(err, ErrNotClaimed) || errors.Is(err, ErrNotYours) {
					continue
				}
				return err
			}
			released++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return released, nil
}
