package dispatch

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// SweepOptions are the ages after which a quiet fact becomes drift. Zero
// values take the defaults; they are observation thresholds, never deadlines
// that change an assignment.
type SweepOptions struct {
	// RequestAfter: a Worker decision request not yet handled by the Dispatcher.
	RequestAfter time.Duration
	// DecisionAfter: the current decision not yet delivered to its Worker.
	DecisionAfter time.Duration
	// QuietAfter: a proceeding assignment with no recorded progress and no
	// owned operation in flight.
	QuietAfter time.Duration
	// HoldAfter: a hold that names no structured wait, so nobody can observe
	// when it ends.
	HoldAfter time.Duration
	// ReservedBy limits the sweep to one Dispatcher's reservations; empty reads
	// every active reservation of the ledger.
	ReservedBy string
}

func (o SweepOptions) withDefaults() SweepOptions {
	if o.RequestAfter <= 0 {
		o.RequestAfter = 10 * time.Minute
	}
	if o.DecisionAfter <= 0 {
		o.DecisionAfter = 5 * time.Minute
	}
	if o.QuietAfter <= 0 {
		o.QuietAfter = 3 * time.Hour
	}
	if o.HoldAfter <= 0 {
		o.HoldAfter = 2 * time.Hour
	}
	return o
}

// SweepRow is one active assignment with the facts a stuck check needs.
type SweepRow struct {
	Reservation        string         `json:"reservation"`
	SourceRef          string         `json:"source_ref"`
	Item               string         `json:"item,omitempty"`
	State              string         `json:"state"`
	Generation         int64          `json:"generation"`
	DecisionAction     string         `json:"decision_action,omitempty"`
	DecisionRevision   int64          `json:"decision_revision,omitempty"`
	DecisionAt         int64          `json:"decision_at,omitempty"`
	RemainingCondition string         `json:"remaining_condition,omitempty"`
	Waits              []DeliveryWait `json:"waits,omitempty"`
	OnSatisfied        string         `json:"on_satisfied,omitempty"`
	NextOwner          string         `json:"next_owner"`
	OperationState     string         `json:"operation_state"`
	// LastProgressAt is the newest recorded fact of the owner: a message on its
	// canonical thread, an attestation, a phase result or an owned operation.
	LastProgressAt int64 `json:"last_progress_at"`
	QuietSeconds   int64 `json:"quiet_seconds"`
	// OpenRequests are Worker decision requests the Dispatcher has not handled.
	OpenRequests    int      `json:"open_requests"`
	OldestRequestAt int64    `json:"oldest_request_at,omitempty"`
	Drift           []string `json:"drift"`
	// Notes are facts that need no action by themselves. A plain Worker message
	// after the current decision is ordinary progress: a result that another
	// owner must consume arrives as a formal event, not as prose.
	Notes []string `json:"notes,omitempty"`
}

// SweepReport is the whole fleet in one read.
type SweepReport struct {
	VerifiedAt int64      `json:"verified_at"`
	Active     int        `json:"active"`
	Flagged    int        `json:"flagged"`
	Rows       []SweepRow `json:"rows"`
}

// Sweep reads every active reservation of this ledger and reports which ones
// need attention and why. It writes nothing and decides nothing: each drift
// code names a recorded fact for the owner of the next step.
func (s *Store) Sweep(ctx context.Context, opts SweepOptions) (*SweepReport, error) {
	opts = opts.withDefaults()
	active, err := s.List(ctx, true)
	if err != nil {
		return nil, err
	}
	now := s.now().Unix()
	if opts.ReservedBy != "" {
		own := active[:0]
		for _, r := range active {
			if r.ReservedBy == opts.ReservedBy {
				own = append(own, r)
			}
		}
		active = own
	}
	report := &SweepReport{VerifiedAt: now, Active: len(active), Rows: make([]SweepRow, 0, len(active))}
	for _, r := range active {
		d, err := s.Delivery(ctx, r.ItemID, IssueObservation{})
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			// One unreadable reservation must not hide the rest of the fleet.
			report.Rows = append(report.Rows, SweepRow{Reservation: r.ItemID, SourceRef: r.SourceRef, Item: r.CanonicalItemID, State: r.State, Generation: r.Generation,
				NextOwner: r.ReservedBy, OperationState: "unknown", Drift: []string{"sweep-read-failed"}, Notes: []string{err.Error()}})
			continue
		}
		row := SweepRow{Reservation: r.ItemID, SourceRef: r.SourceRef, Item: r.CanonicalItemID, State: r.State, Generation: r.Generation,
			DecisionAction: d.DecisionAction, RemainingCondition: d.RemainingCondition, Waits: d.Waits, NextOwner: d.NextOwner,
			OperationState: d.OperationState, Drift: []string{}}
		for _, code := range d.Drift {
			if code == "worker-report-without-event" {
				row.Notes = append(row.Notes, code)
			} else {
				row.Drift = append(row.Drift, code)
			}
		}
		// on_satisfied is shown only while the authorization is in force: the same
		// predicate as terminalevents.effectiveRelease (current revision, current
		// Dispatcher, inside the time box, Worker native not fenced).
		var outcome int64
		var structured int
		err = s.db.QueryRowContext(ctx, `SELECT revision,outcome_id,CASE WHEN on_satisfied_revision=revision AND waits_revision=revision AND waits_for!='[]' AND (wait_deadline=0 OR wait_deadline>=?) AND EXISTS(SELECT 1 FROM messages m WHERE m.repo_id=dispatch_decisions.repo_id AND m.id=dispatch_decisions.outcome_id AND m.agent_id=?) AND NOT EXISTS(SELECT 1 FROM worker_native_fences f WHERE f.repo_id=dispatch_decisions.repo_id AND f.native_session=?) THEN 'proceed' ELSE '' END,CASE WHEN waits_revision=revision AND waits_for!='[]' THEN 1 ELSE 0 END FROM dispatch_decisions WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=?`,
			now, r.ReservedBy, r.WorkerThreadID, s.repoID, r.ItemID, r.Generation, r.CanonicalItemID).Scan(&row.DecisionRevision, &outcome, &row.OnSatisfied, &structured)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if row.DecisionRevision > 0 {
			// An automatic release keeps the Dispatcher's outcome message, so the
			// history row is the time of the current revision when it exists.
			err = s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT decided_at FROM dispatch_decision_history WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=? AND revision=?),(SELECT ts FROM messages WHERE repo_id=? AND id=?),0)`,
				s.repoID, r.ItemID, r.Generation, r.CanonicalItemID, row.DecisionRevision, s.repoID, outcome).Scan(&row.DecisionAt)
			if err != nil {
				return nil, err
			}
		}
		var phaseAt int64
		if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(max(recorded_at),0) FROM dispatch_phase_results WHERE repo_id=? AND reservation_key=? AND generation=?`, s.repoID, r.ItemID, r.Generation).Scan(&phaseAt); err != nil {
			return nil, err
		}
		row.LastProgressAt = max(r.ReservedAt, d.LastOwnerActivityAt, d.LastResultAt, d.OperationRecordedAt, phaseAt)
		// An owner is quiet only since it was last free to act: a hold that just
		// ended, or a decision just published, restarts the clock.
		row.QuietSeconds = max(now-max(row.LastProgressAt, row.DecisionAt), 0)
		if r.State != "dispatched" {
			report.Rows = append(report.Rows, row)
			continue
		}
		if err = s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(min(m.ts),0) FROM terminal_event_receipts e JOIN messages m ON m.repo_id=e.repo_id AND m.id=e.source_message_id
 WHERE e.repo_id=? AND e.reservation_key=? AND e.generation=? AND e.kind='decision-request' AND e.processed_at=0`, s.repoID, r.ItemID, r.Generation).Scan(&row.OpenRequests, &row.OldestRequestAt); err != nil {
			return nil, err
		}
		if row.OpenRequests > 0 && now-row.OldestRequestAt > int64(opts.RequestAfter.Seconds()) {
			row.Drift = append(row.Drift, "decision-request-unanswered")
		}
		if row.DecisionRevision > 0 && now-row.DecisionAt > int64(opts.DecisionAfter.Seconds()) {
			// An automatic release reuses the Dispatcher's outcome under its own event
			// id; when one exists for this revision it is the wake that must arrive.
			released := fmt.Sprintf("%%/waits-released-r%d", row.DecisionRevision)
			// A decision copied by a takeover has no receipt of its own to deliver.
			var wakes, delivered int
			if err = s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(e.delivered_at>0),0) FROM terminal_event_receipts e WHERE e.repo_id=? AND e.reservation_key=? AND e.generation=? AND e.kind='decision-resolved' AND e.outcome_id=?
 AND (e.event_id LIKE ? OR NOT EXISTS(SELECT 1 FROM terminal_event_receipts x WHERE x.repo_id=e.repo_id AND x.reservation_key=e.reservation_key AND x.generation=e.generation AND x.kind='decision-resolved' AND x.event_id LIKE ?))`,
				s.repoID, r.ItemID, r.Generation, outcome, released, released).Scan(&wakes, &delivered); err != nil {
				return nil, err
			}
			if wakes > 0 && delivered == 0 {
				row.Drift = append(row.Drift, "decision-undelivered")
			}
		}
		switch {
		case row.DecisionAction == "hold":
			if structured == 0 && now-row.DecisionAt > int64(opts.HoldAfter.Seconds()) {
				row.Drift = append(row.Drift, "hold-without-checkable-wait")
			}
		case d.OperationState != "started-unresolved" && row.QuietSeconds > int64(opts.QuietAfter.Seconds()):
			row.Drift = append(row.Drift, "owner-quiet")
		}
		report.Rows = append(report.Rows, row)
	}
	sort.SliceStable(report.Rows, func(i, j int) bool {
		a, b := report.Rows[i], report.Rows[j]
		if (len(a.Drift) > 0) != (len(b.Drift) > 0) {
			return len(a.Drift) > 0
		}
		return a.QuietSeconds > b.QuietSeconds
	})
	for _, row := range report.Rows {
		if len(row.Drift) > 0 {
			report.Flagged++
		}
	}
	return report, nil
}
