package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zsiec/squad/internal/store"
)

type PartialRetirement struct {
	Expected       Reservation      `json:"expected"`
	Issue          IssueObservation `json:"issue"`
	BlockedOutcome int64            `json:"blocked_outcome"`
}

// RetirePartial preserves an OPEN Issue and its blocked item while retiring
// only the reconciled execution. The existing failed state permits a new
// generation; the audit message distinguishes partial delivery from a failure.
func (s *Store) RetirePartial(ctx context.Context, actor string, q PartialRetirement) (*Reservation, error) {
	if actor == "" || q.Expected.ReservedBy != actor || q.Expected.RepoID != s.repoID || q.Expected.Generation < 1 || q.Expected.WorkerThreadID == "" || q.BlockedOutcome < 1 {
		return nil, fmt.Errorf("exact owner, bound reservation and blocked outcome required")
	}
	now := s.now().Unix()
	if q.Issue.Source != q.Expected.SourceRef || q.Issue.State != "OPEN" || q.Issue.VerifiedAt <= 0 || q.Issue.VerifiedAt > now {
		return nil, fmt.Errorf("fresh matching OPEN Issue observation required")
	}
	body, err := json.Marshal(struct {
		Schema  string            `json:"schema"`
		Request PartialRetirement `json:"request"`
	}{"squad.partial-retirement.v1", q})
	if err != nil {
		return nil, err
	}
	err = store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		current, e := scanReservation(tx.QueryRowContext(ctx, reservationSelect+` WHERE repo_id=? AND item_id=?`, s.repoID, q.Expected.ItemID))
		if e != nil {
			return e
		}
		var prior int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE repo_id=? AND agent_id=? AND thread=? AND kind='partial-retirement' AND body=?`, s.repoID, actor, q.Expected.CanonicalItemID, string(body)).Scan(&prior); e != nil {
			return e
		}
		if prior > 0 && current.State == "failed" && current.Generation == q.Expected.Generation && current.WorkerThreadID == q.Expected.WorkerThreadID {
			return nil
		}
		if now-q.Issue.VerifiedAt > int64((5 * time.Minute).Seconds()) {
			return fmt.Errorf("fresh matching OPEN Issue observation required")
		}
		if *current != q.Expected || (current.State != "dispatched" && current.State != "completed") {
			return fmt.Errorf("reservation changed; read current identity before retry")
		}
		var status, worker, action string
		if e = tx.QueryRowContext(ctx, `SELECT status FROM items WHERE repo_id=? AND item_id=?`, s.repoID, current.CanonicalItemID).Scan(&status); e != nil || status != "blocked" {
			return fmt.Errorf("canonical item must remain blocked")
		}
		if e = tx.QueryRowContext(ctx, `SELECT worker_agent,action FROM dispatch_decisions WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=?`, s.repoID, current.ItemID, current.Generation, current.CanonicalItemID).Scan(&worker, &action); e != nil || worker == "" || action != "hold" {
			return fmt.Errorf("original Worker must be fenced by its current hold decision")
		}
		var n int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM terminal_event_receipts e JOIN messages m ON m.id=e.outcome_id AND m.repo_id=e.repo_id WHERE e.repo_id=? AND e.reservation_key=? AND e.generation=? AND e.worker_session=? AND e.recipient=? AND e.item_id=? AND e.kind='blocked' AND e.outcome_id=? AND e.processed_at>0 AND m.agent_id=?`, s.repoID, current.ItemID, current.Generation, current.WorkerThreadID, actor, current.CanonicalItemID, q.BlockedOutcome, worker).Scan(&n); e != nil || n != 1 {
			return fmt.Errorf("original Worker's blocked outcome must be handled by its owner")
		}
		if e = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM claims WHERE repo_id=? AND agent_id=?)+(SELECT count(*) FROM execution_authorizations WHERE repo_id=? AND holder=? AND state='active')`, s.repoID, worker, s.repoID, worker).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			return fmt.Errorf("worker retains claims or active execution custody")
		}
		unresolved, e := store.UnresolvedOperation(ctx, tx, s.repoID, worker, current.ItemID, current.WorkerThreadID, current.Generation)
		if e != nil {
			return e
		}
		if unresolved {
			return fmt.Errorf("owned operation or exit handling remains unresolved")
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?,'partial-retirement',?,'[]','normal')`, s.repoID, now, actor, current.CanonicalItemID, string(body)); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE dispatch_reservations SET state='failed',note=?,updated_at=?,expires_at=0 WHERE repo_id=? AND item_id=? AND reserved_by=? AND generation=?`, "Partial delivery; Issue OPEN; see partial-retirement audit outcome", now, s.repoID, current.ItemID, actor, current.Generation)
		return e
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, q.Expected.ItemID)
}
