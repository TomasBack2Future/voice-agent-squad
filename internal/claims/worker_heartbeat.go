package claims

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrWorkerFenceRejected represents a verified negative custody lookup, never a database/transport failure.
var ErrWorkerFenceRejected = errors.New("worker custody rejected")

// WorkerHeartbeat fences renewal by the exact active dispatch. It never creates
// or reacquires ownership, and never renews a claim transferred into recovery.
func (s *Store) WorkerHeartbeat(ctx context.Context, agent, reservation, session string, generation int64, check bool, requirePrimary ...bool) error {
	if agent == "" || reservation == "" || session == "" || generation < 1 {
		return fmt.Errorf("heartbeat identity and generation required")
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var since int64
		var holder, state string
		if err := tx.QueryRowContext(ctx, `SELECT r.reserved_at,COALESCE(c.agent_id,''),COALESCE(c.state,'') FROM dispatch_reservations r LEFT JOIN claims c ON c.repo_id=r.repo_id AND c.item_id=r.canonical_item_id WHERE r.repo_id=? AND r.item_id=? AND r.generation=? AND r.worker_thread_id=? AND r.state='dispatched'`, s.repoID, reservation, generation, session).Scan(&since, &holder, &state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("heartbeat dispatch fence rejected: %w", ErrWorkerFenceRejected)
			}
			return fmt.Errorf("heartbeat dispatch lookup unavailable: %w", err)
		}
		if holder != "" && holder != agent {
			return fmt.Errorf("heartbeat canonical claim belongs to another agent: %w", ErrWorkerFenceRejected)
		}
		if len(requirePrimary) > 0 && requirePrimary[0] && (holder != agent || state != "held") {
			return fmt.Errorf("heartbeat primary custody is not held: %w", ErrWorkerFenceRejected)
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
