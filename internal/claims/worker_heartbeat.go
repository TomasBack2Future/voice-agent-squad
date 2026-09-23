package claims

import (
	"context"
	"database/sql"
	"fmt"
)

// WorkerHeartbeat fences renewal by the exact active dispatch. It never creates
// or reacquires ownership, and never renews a claim transferred into recovery.
func (s *Store) WorkerHeartbeat(ctx context.Context, agent, reservation, session string, generation int64, check bool) error {
	if agent == "" || reservation == "" || session == "" || generation < 1 {
		return fmt.Errorf("heartbeat identity and generation required")
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var since int64
		var holder string
		if err := tx.QueryRowContext(ctx, `SELECT r.reserved_at,COALESCE(c.agent_id,'') FROM dispatch_reservations r LEFT JOIN claims c ON c.repo_id=r.repo_id AND c.item_id=r.canonical_item_id WHERE r.repo_id=? AND r.item_id=? AND r.generation=? AND r.worker_thread_id=? AND r.state='dispatched'`, s.repoID, reservation, generation, session).Scan(&since, &holder); err != nil {
			return fmt.Errorf("heartbeat dispatch fence rejected: %w", err)
		}
		if holder != "" && holder != agent {
			return fmt.Errorf("heartbeat canonical claim belongs to another agent")
		}
		if check {
			return nil
		}
		now := s.nowUnix()
		if _, err := tx.ExecContext(ctx, `UPDATE claims SET last_touch=? WHERE repo_id=? AND agent_id=? AND state='held' AND claimed_at>=?`, now, s.repoID, agent, since); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE agents SET last_tick_at=?,status='active' WHERE repo_id=? AND id=?`, now, s.repoID, agent)
		return err
	})
}
