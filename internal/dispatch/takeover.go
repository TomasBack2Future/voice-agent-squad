package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/store"
)

// TakeoverRequest is an operator recovery contract, not normal dispatch. The
// stopped evidence must identify native clients, supervisors and external work.
// Empty NewWorkerSession transfers dispatcher custody only, preserving the live
// Worker's fence. Replacing a Worker also transfers its ordinary canonical claim.
type TakeoverRequest struct {
	Reservation              string `json:"reservation"`
	ExpectedDispatcher       string `json:"expected_dispatcher"`
	DispatcherSession        string `json:"dispatcher_session"`
	ExpectedWorkerSession    string `json:"expected_worker_session"`
	ExpectedGeneration       int64  `json:"expected_generation"`
	NewDispatcher            string `json:"new_dispatcher"`
	NewWorkerSession         string `json:"new_worker_session,omitempty"`
	ExpectedHolder           string `json:"expected_holder,omitempty"`
	ExpectedClaimGeneration  int64  `json:"expected_claim_generation,omitempty"`
	NewHolder                string `json:"new_holder,omitempty"`
	ConfirmDispatcherStopped bool   `json:"confirm_dispatcher_stopped"`
	ConfirmWorkerStopped     bool   `json:"confirm_worker_stopped"`
	Reason                   string `json:"reason"`
	Evidence                 string `json:"evidence"`
}

type TakeoverResult struct {
	Reservation     *Reservation `json:"reservation"`
	AuditMessageID  int64        `json:"audit_message_id"`
	ClaimGeneration int64        `json:"claim_generation,omitempty"`
	PendingEvents   []string     `json:"pending_events_requiring_reconciliation"`
}

// Takeover CASes every changed binding in one transaction. ENV and pinned
// executions are excluded. It never fabricates a terminal Worker outcome.
func (s *Store) Takeover(ctx context.Context, actor string, q TakeoverRequest) (*TakeoverResult, error) {
	if actor == "" || q.Reservation == "" || q.ExpectedDispatcher == "" || q.DispatcherSession == "" || q.ExpectedWorkerSession == "" || q.ExpectedGeneration < 1 || q.NewDispatcher == "" || strings.TrimSpace(q.Reason) == "" || strings.TrimSpace(q.Evidence) == "" {
		return nil, fmt.Errorf("takeover requires exact identities, generation, reason and stopped/external-operation evidence")
	}
	replace := q.NewWorkerSession != ""
	if !q.ConfirmDispatcherStopped || (replace && !q.ConfirmWorkerStopped) {
		return nil, fmt.Errorf("takeover requires independent stopped-client confirmation")
	}
	if replace && (q.ExpectedHolder == "" || q.NewHolder == "" || q.ExpectedClaimGeneration < 1 || q.NewHolder == q.ExpectedHolder || q.NewWorkerSession == q.ExpectedWorkerSession) {
		return nil, fmt.Errorf("replacement requires distinct Worker identity and exact ordinary claim fence")
	}
	if !replace && (q.ExpectedHolder != "" || q.NewHolder != "" || q.ExpectedClaimGeneration != 0) {
		return nil, fmt.Errorf("custody-only takeover cannot change a claim")
	}
	if !replace && q.NewDispatcher == q.ExpectedDispatcher {
		return nil, fmt.Errorf("custody-only takeover requires a new dispatcher")
	}
	now := s.now().Unix()
	out := &TakeoverResult{PendingEvents: []string{}}
	err := store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		// Retry output must describe only the transaction that commits.
		*out = TakeoverResult{PendingEvents: []string{}}
		// This legacy per-reservation migration cannot transfer or recreate the
		// controller/native epoch. Require the complete-cohort handoff protocol
		// once a ledger has entered that protocol, including retired actors.
		var controllers int
		if err := tx.QueryRowContext(ctx, `SELECT
            (SELECT count(*) FROM dispatch_controller_bindings WHERE repo_id=?) +
            (SELECT count(*) FROM dispatch_retired_controllers WHERE repo_id=?)`, s.repoID, s.repoID).Scan(&controllers); err != nil {
			return err
		}
		if controllers != 0 {
			return fmt.Errorf("legacy takeover unavailable for controller-bound ledger; use controller handoff, preserve Worker custody")
		}
		r, e := scanReservation(tx.QueryRowContext(ctx, reservationSelect+` WHERE repo_id=? AND item_id=?`, s.repoID, q.Reservation))
		if e != nil {
			return e
		}
		if r.ReservedBy != q.ExpectedDispatcher || r.WorkerThreadID != q.ExpectedWorkerSession || r.Generation != q.ExpectedGeneration || r.State != "dispatched" {
			return fmt.Errorf("takeover reservation fence changed")
		}
		for _, id := range []string{q.ExpectedDispatcher, q.ExpectedHolder} {
			if id == "" {
				continue
			}
			var fresh int
			if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE repo_id=? AND id=? AND status='active' AND last_tick_at>=?`, s.repoID, id, now-int64((5*time.Minute)/time.Second)).Scan(&fresh); e != nil {
				return e
			}
			if fresh > 0 {
				return fmt.Errorf("takeover holder %s recently active", id)
			}
		}
		rows, e := tx.QueryContext(ctx, `SELECT event_id FROM terminal_event_receipts WHERE repo_id=? AND reservation_key=? AND generation=? AND processed_at=0`, s.repoID, q.Reservation, r.Generation)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			out.PendingEvents = append(out.PendingEvents, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if replace {
			if strings.HasPrefix(r.CanonicalItemID, "ENV-") {
				return fmt.Errorf("protected environment requires recover")
			}
			var holder, state, group string
			var gen, claimed, last int64
			e = tx.QueryRowContext(ctx, `SELECT agent_id,state,resource_group,generation,claimed_at,last_touch FROM claims WHERE repo_id=? AND item_id=?`, s.repoID, r.CanonicalItemID).Scan(&holder, &state, &group, &gen, &claimed, &last)
			if e != nil {
				return e
			}
			if holder != q.ExpectedHolder || gen != q.ExpectedClaimGeneration || state != "held" || group != "" {
				return fmt.Errorf("takeover ordinary claim fence changed or protected")
			}
			if last >= now-300 {
				return fmt.Errorf("takeover claim recently renewed; stop supervisor and wait for stale threshold")
			}
			var protected int
			e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM claims WHERE repo_id=? AND agent_id=? AND (item_id LIKE 'ENV-%' OR resource_group<>'')`, s.repoID, holder).Scan(&protected)
			if e != nil {
				return e
			}
			if protected != 0 {
				return fmt.Errorf("recover protected environment custody before Worker replacement")
			}
			// Prevent ambiguous workerRecipient history at one-second timestamp precision.
			if claimed >= now || r.ReservedAt >= now {
				return fmt.Errorf("takeover needs a later custody epoch")
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES(?,?,?,?,?,'session-takeover')`, s.repoID, r.CanonicalItemID, holder, claimed, now)
			if e != nil {
				return e
			}
			res, e := tx.ExecContext(ctx, `UPDATE claims SET agent_id=?,generation=generation+1,claimed_at=?,last_touch=?,previous_agent_id=?,recovery_reason=? WHERE repo_id=? AND item_id=? AND agent_id=? AND generation=? AND state='held'`, q.NewHolder, now, now, holder, q.Reason, s.repoID, r.CanonicalItemID, holder, gen)
			if e != nil {
				return e
			}
			n, _ := res.RowsAffected()
			if n != 1 {
				return fmt.Errorf("takeover claim CAS failed")
			}
			_, e = tx.ExecContext(ctx, `UPDATE touches SET released_at=? WHERE repo_id=? AND item_id=? AND agent_id=? AND released_at IS NULL`, now, s.repoID, r.CanonicalItemID, holder)
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) SELECT repo_id,reservation_key,?,item_id,revision,outcome_id,action,condition,? FROM dispatch_decisions WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=?`, r.Generation+1, q.NewHolder, s.repoID, q.Reservation, r.Generation, r.CanonicalItemID)
			if e != nil {
				return e
			}
			out.ClaimGeneration = gen + 1
			r.Generation++
			r.WorkerThreadID = q.NewWorkerSession
			r.ReservedAt = now
		} else {
			// Same Worker epoch: pending receipts follow the new reconciliation owner.
			_, e = tx.ExecContext(ctx, `UPDATE terminal_event_receipts SET recipient=?,delivered_session='',delivered_at=0 WHERE repo_id=? AND reservation_key=? AND generation=? AND recipient=? AND processed_at=0`, q.NewDispatcher, s.repoID, q.Reservation, r.Generation, q.ExpectedDispatcher)
			if e != nil {
				return e
			}
		}
		res, e := tx.ExecContext(ctx, `UPDATE dispatch_reservations SET reserved_by=?,worker_thread_id=?,generation=?,reserved_at=?,updated_at=? WHERE repo_id=? AND item_id=? AND reserved_by=? AND worker_thread_id=? AND generation=? AND state='dispatched'`, q.NewDispatcher, r.WorkerThreadID, r.Generation, r.ReservedAt, now, s.repoID, q.Reservation, q.ExpectedDispatcher, q.ExpectedWorkerSession, q.ExpectedGeneration)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return fmt.Errorf("takeover reservation CAS failed")
		}
		audit, _ := json.Marshal(struct {
			Request TakeoverRequest `json:"request"`
			Pending []string        `json:"pending_events"`
		}{q, out.PendingEvents})
		res, e = tx.ExecContext(ctx, `INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?, 'session-takeover',?,'[]','high')`, s.repoID, now, actor, r.CanonicalItemID, string(audit))
		if e != nil {
			return e
		}
		out.AuditMessageID, e = res.LastInsertId()
		if e != nil {
			return e
		}
		r.ReservedBy = q.NewDispatcher
		r.UpdatedAt = now
		out.Reservation = r
		return nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("takeover expected reservation or claim missing")
		}
		return nil, err
	}
	return out, nil
}
