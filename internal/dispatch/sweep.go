package dispatch

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
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
	// OperationAfter: an owned operation recorded as started with no exit for
	// this long. A started operation is normally a watch still running; only an
	// unusually old one is worth checking for a lost process.
	OperationAfter time.Duration
	// UnactedAfter: a proceed decision (set by the Dispatcher or by an automatic
	// release) after which the owner recorded nothing. A handoff is complete when
	// the released owner starts its next step, not when the result was recorded.
	UnactedAfter time.Duration
	// ReservedBy limits the sweep to one Dispatcher's reservations; empty reads
	// every active reservation of the ledger.
	ReservedBy string
	// Issues are the caller's timestamped observations of the Issues it is
	// responsible for (its batch). With them the sweep reports each row's Issue
	// state and finds what no live assignment covers: an Issue still open whose
	// reservation has ended, and one that never had a reservation. They are
	// evidence with an age, never closure authority.
	Issues []IssueObservation
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
	if o.OperationAfter <= 0 {
		o.OperationAfter = 12 * time.Hour
	}
	if o.UnactedAfter <= 0 {
		o.UnactedAfter = 15 * time.Minute
	}
	return o
}

// SweepRow is one active assignment with the facts a stuck check needs.
type SweepRow struct {
	Reservation string `json:"reservation"`
	SourceRef   string `json:"source_ref"`
	Item        string `json:"item,omitempty"`
	State       string `json:"state"`
	Generation  int64  `json:"generation"`
	// IssueState is "open", "closed" or "unknown" (no fresh observation given);
	// Continuation says who carries an open Issue (see dispatch delivery).
	IssueState         string         `json:"issue_state,omitempty"`
	Continuation       string         `json:"continuation,omitempty"`
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
	// DecisionAckedAt is when the Worker showed it read the current decision: it
	// acknowledged the wake or said something on its thread afterwards.
	// FirstActionAt is when it started: the earliest owned operation it began
	// for this assignment, milestone, attestation, phase result or claim of its
	// own after the decision. A message is reading, not starting; another
	// agent's record and an operation begun before the decision are neither.
	// FirstActionSeconds is how long starting took. Zero: not yet.
	DecisionAckedAt    int64 `json:"decision_acked_at,omitempty"`
	FirstActionAt      int64 `json:"first_action_at,omitempty"`
	FirstActionSeconds int64 `json:"first_action_seconds,omitempty"`
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
	VerifiedAt int64 `json:"verified_at"`
	Active     int   `json:"active"`
	Flagged    int   `json:"flagged"`
	// OpenWithoutOwner counts observed open Issues that no active reservation
	// covers; each has a row. StaleObservations counts observations too old to
	// say anything (over five minutes): those Issues were not judged, so a zero
	// OpenWithoutOwner means "none" only when this is zero too.
	OpenWithoutOwner  int        `json:"open_without_owner"`
	StaleObservations int        `json:"stale_observations"`
	Rows              []SweepRow `json:"rows"`
}

const maxSweepObservations = 500

// expiredUnbound: a reservation that never got a Worker and is past its expiry.
func expiredUnbound(state string, expiresAt, now int64) bool {
	return state == "reserved" && expiresAt > 0 && expiresAt < now
}

// sweepObservations validates the caller's observations once, before any row
// is read, so a malformed list fails the call instead of replacing a row's
// ledger facts. It trims and upper-cases, keeps the last observation of a
// source, and sets stale ones aside to be counted rather than judged.
func sweepObservations(issues []IssueObservation, now int64) (map[string]IssueObservation, int, error) {
	if len(issues) > maxSweepObservations {
		return nil, 0, fmt.Errorf("at most %d issue observations per sweep", maxSweepObservations)
	}
	fresh := make(map[string]IssueObservation, len(issues))
	staleSources := map[string]bool{}
	for i, o := range issues {
		o.Source, o.State = strings.TrimSpace(o.Source), strings.ToUpper(strings.TrimSpace(o.State))
		switch {
		case o.Source == "":
			return nil, 0, fmt.Errorf("issue observation %d: source_ref is required", i)
		case o.State != "OPEN" && o.State != "CLOSED":
			return nil, 0, fmt.Errorf("issue observation %d (%s): state must be OPEN or CLOSED", i, o.Source)
		case o.VerifiedAt <= 0 || o.VerifiedAt > now:
			return nil, 0, fmt.Errorf("issue observation %d (%s): verified_at must be a past unix time", i, o.Source)
		}
		if now-o.VerifiedAt > int64((5 * time.Minute).Seconds()) {
			staleSources[o.Source] = true
			delete(fresh, o.Source)
			continue
		}
		delete(staleSources, o.Source)
		fresh[o.Source] = o
	}
	return fresh, len(staleSources), nil
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
	observed, stale, err := sweepObservations(opts.Issues, now)
	if err != nil {
		return nil, err
	}
	covered := map[string]bool{}
	rowAt := map[string]int{}
	report := &SweepReport{VerifiedAt: now, Active: len(active), StaleObservations: stale, Rows: make([]SweepRow, 0, len(active))}
	add := func(row SweepRow) {
		rowAt[row.Reservation] = len(report.Rows)
		report.Rows = append(report.Rows, row)
	}
	for _, r := range active {
		// A reservation never bound to a Worker and past its expiry holds the key
		// but covers nothing; Reserve treats it as free.
		if !expiredUnbound(r.State, r.ExpiresAt, now) {
			covered[r.SourceRef] = true
		}
		d, err := s.Delivery(ctx, r.ItemID, observed[r.SourceRef])
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			// One unreadable reservation must not hide the rest of the fleet.
			add(SweepRow{Reservation: r.ItemID, SourceRef: r.SourceRef, Item: r.CanonicalItemID, State: r.State, Generation: r.Generation,
				NextOwner: r.ReservedBy, OperationState: "unknown", Drift: []string{"sweep-read-failed"}, Notes: []string{err.Error()}})
			continue
		}
		row := SweepRow{Reservation: r.ItemID, SourceRef: r.SourceRef, Item: r.CanonicalItemID, State: r.State, Generation: r.Generation,
			DecisionAction: d.DecisionAction, RemainingCondition: d.RemainingCondition, Waits: d.Waits, NextOwner: d.NextOwner,
			OperationState: d.OperationState, Drift: []string{}}
		if _, ok := observed[r.SourceRef]; ok {
			row.IssueState, row.Continuation = d.IssueState, d.Continuation
		}
		for _, code := range d.Drift {
			switch {
			case code == "worker-report-without-event":
				row.Notes = append(row.Notes, code)
			case code == "owned-operation-unresolved" && now-d.OperationRecordedAt <= int64(opts.OperationAfter.Seconds()):
				// Started, no exit yet: a watch doing its job, not a lost one.
				row.Notes = append(row.Notes, "operation-in-flight")
			default:
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
			add(row)
			continue
		}
		if err = s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(min(m.ts),0) FROM terminal_event_receipts e JOIN messages m ON m.repo_id=e.repo_id AND m.id=e.source_message_id
 WHERE e.repo_id=? AND e.reservation_key=? AND e.generation=? AND e.kind='decision-request' AND e.processed_at=0`, s.repoID, r.ItemID, r.Generation).Scan(&row.OpenRequests, &row.OldestRequestAt); err != nil {
			return nil, err
		}
		if row.OpenRequests > 0 && now-row.OldestRequestAt > int64(opts.RequestAfter.Seconds()) {
			row.Drift = append(row.Drift, "decision-request-unanswered")
		}
		// handedOver: the current decision set the owner going, as opposed to
		// amending an owner that was already working. It is the first decision of
		// the assignment or it follows a hold (a Dispatcher proceed or an
		// automatic release).
		handedOver := row.DecisionRevision == 1
		if row.DecisionRevision > 0 {
			// An automatic release reuses the Dispatcher's outcome under its own event
			// id; when one exists for this revision it is the wake that counts. A
			// decision copied by a takeover has no receipt of its own.
			release := fmt.Sprintf("%%/waits-released-r%d", row.DecisionRevision)
			var wakes, delivered int
			if err = s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(e.delivered_at>0),0),COALESCE(max(CASE WHEN e.processed_note NOT LIKE 'superseded:%' THEN e.processed_at ELSE 0 END),0) FROM terminal_event_receipts e WHERE e.repo_id=? AND e.reservation_key=? AND e.generation=? AND e.kind='decision-resolved' AND e.outcome_id=?
 AND (e.event_id LIKE ? OR NOT EXISTS(SELECT 1 FROM terminal_event_receipts x WHERE x.repo_id=e.repo_id AND x.reservation_key=e.reservation_key AND x.generation=e.generation AND x.kind='decision-resolved' AND x.event_id LIKE ?))`,
				s.repoID, r.ItemID, r.Generation, outcome, release, release).Scan(&wakes, &delivered, &row.DecisionAckedAt); err != nil {
				return nil, err
			}
			if wakes > 0 && delivered == 0 && now-row.DecisionAt > int64(opts.DecisionAfter.Seconds()) {
				row.Drift = append(row.Drift, "decision-undelivered")
			}
			// What follows the current decision must be attributable to it: recorded
			// by this assignment's Worker, after the decision, and for an automatic
			// release (which keeps the old outcome message) strictly after it.
			var decidedBy string
			err = s.db.QueryRowContext(ctx, `SELECT decided_by FROM dispatch_decision_history WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=? AND revision=?`,
				s.repoID, r.ItemID, r.Generation, r.CanonicalItemID, row.DecisionRevision).Scan(&decidedBy)
			if err != nil && err != sql.ErrNoRows {
				return nil, err
			}
			from := row.DecisionAt
			if decidedBy == "waits-satisfied" {
				from++
			}
			// The Worker: whoever claimed the canonical item under this reservation.
			const worker = `(SELECT agent_id FROM claims WHERE repo_id=? AND item_id=? AND claimed_at>=? UNION SELECT agent_id FROM claim_history WHERE repo_id=? AND item_id=? AND claimed_at>=?)`
			who := []any{s.repoID, r.CanonicalItemID, r.ReservedAt, s.repoID, r.CanonicalItemID, r.ReservedAt}
			// Read: the Worker acknowledged the wake, or said anything on its thread
			// after the decision. Saying "received, still waiting" is reading, not
			// starting.
			var said int64
			if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(min(m.ts),0) FROM messages m WHERE m.repo_id=? AND m.thread=? AND m.id>? AND m.ts>=? AND m.agent_id IN `+worker,
				append([]any{s.repoID, r.CanonicalItemID, outcome, from}, who...)...).Scan(&said); err != nil {
				return nil, err
			}
			if said > 0 && (row.DecisionAckedAt == 0 || said < row.DecisionAckedAt) {
				row.DecisionAckedAt = said
			}
			// Started: the earliest thing the Worker did, not said. An owned
			// operation it started for this assignment, a milestone it recorded, an
			// attestation or phase result of its own, or a claim it took (the
			// environment lock before a deploy). An operation started before the
			// decision belongs to the work before it.
			if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(min(t),0) FROM (
 SELECT min(m.ts) t FROM messages m WHERE m.repo_id=? AND m.thread=? AND m.id>? AND m.ts>=? AND m.agent_id IN `+worker+`
  AND (m.kind='milestone' OR (m.kind='owned-operation' AND json_valid(m.body) AND json_extract(m.body,'$.state')='started'
   AND json_extract(m.body,'$.request.reservation')=? AND json_extract(m.body,'$.request.generation')=? AND json_extract(m.body,'$.request.worker_session')=?))
 UNION ALL SELECT min(created_at) FROM attestations WHERE repo_id=? AND item_id=? AND kind!='release' AND created_at>=? AND agent_id IN `+worker+`
 UNION ALL SELECT min(recorded_at) FROM dispatch_phase_results WHERE repo_id=? AND reservation_key=? AND generation=? AND recorded_at>=? AND recorded_by IN `+worker+`
 UNION ALL SELECT min(claimed_at) FROM (SELECT agent_id,claimed_at FROM claims WHERE repo_id=? UNION ALL SELECT agent_id,claimed_at FROM claim_history WHERE repo_id=?) WHERE claimed_at>=? AND agent_id IN `+worker+`)`,
				append(append(append(append(append(append(append(
					[]any{s.repoID, r.CanonicalItemID, outcome, from}, who...),
					r.ItemID, r.Generation, r.WorkerThreadID,
					s.repoID, r.CanonicalItemID, from), who...),
					s.repoID, r.ItemID, r.Generation, from), who...),
					s.repoID, s.repoID, from), who...)...).Scan(&row.FirstActionAt); err != nil {
				return nil, err
			}
			if row.FirstActionAt > 0 {
				row.FirstActionSeconds = row.FirstActionAt - row.DecisionAt
			}
			if row.DecisionRevision > 1 {
				var previous string
				err = s.db.QueryRowContext(ctx, `SELECT action FROM dispatch_decision_history WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=? AND revision=?`,
					s.repoID, r.ItemID, r.Generation, r.CanonicalItemID, row.DecisionRevision-1).Scan(&previous)
				if err != nil && err != sql.ErrNoRows {
					return nil, err
				}
				handedOver = previous == "hold"
			}
		}
		switch {
		case row.DecisionAction == "hold":
			if structured == 0 && now-row.DecisionAt > int64(opts.HoldAfter.Seconds()) {
				row.Drift = append(row.Drift, "hold-without-checkable-wait")
			}
		case handedOver && row.FirstActionAt == 0 && now-row.DecisionAt > int64(opts.UnactedAfter.Seconds()):
			// Set going (first decision, or released from a hold) and nothing
			// started since: the handoff has not reached the owner's first step. An
			// operation still running from before the decision does not answer it.
			// An amended proceed for an owner already working is left to owner-quiet.
			row.Drift = append(row.Drift, "decision-unacted")
		case d.OperationState == "started-unresolved":
			// A tracked operation in flight explains a quiet ledger.
		case row.QuietSeconds > int64(opts.QuietAfter.Seconds()):
			row.Drift = append(row.Drift, "owner-quiet")
		}
		add(row)
	}
	// Observed open Issues that no active reservation covers: the reservation
	// ended (partial retirement, a completed execution), expired unbound, or
	// never existed. Without this the fleet read is only as wide as the live
	// sessions.
	sources := make([]string, 0, len(observed))
	for source := range observed {
		if !covered[source] {
			sources = append(sources, source)
		}
	}
	sort.Strings(sources)
	for _, source := range sources {
		o := observed[source]
		if o.State != "OPEN" {
			continue
		}
		var key, owner, state string
		var expires int64
		err := s.db.QueryRowContext(ctx, `SELECT item_id,reserved_by,state,expires_at FROM dispatch_reservations WHERE repo_id=? AND source_ref=?`, s.repoID, source).Scan(&key, &owner, &state, &expires)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if err == sql.ErrNoRows {
			report.OpenWithoutOwner++
			report.Rows = append(report.Rows, SweepRow{SourceRef: source, State: "unreserved", IssueState: "open", Continuation: "unassigned",
				NextOwner: opts.ReservedBy, OperationState: "unknown", Drift: []string{"open-issue-unreserved"}})
			continue
		}
		if opts.ReservedBy != "" && owner != opts.ReservedBy {
			continue
		}
		if at, ok := rowAt[key]; ok {
			// Already listed: an expired, never-bound reservation.
			report.OpenWithoutOwner++
			report.Rows[at].Drift = append(report.Rows[at].Drift, "open-issue-no-active-owner")
			continue
		}
		if state == "dispatched" || (state == "reserved" && !expiredUnbound(state, expires, now)) {
			// Became active after the list above was read: it has an owner.
			continue
		}
		d, err := s.Delivery(ctx, key, o)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			report.Rows = append(report.Rows, SweepRow{Reservation: key, SourceRef: source, State: state, NextOwner: owner, OperationState: "unknown",
				Drift: []string{"sweep-read-failed"}, Notes: []string{err.Error()}})
			continue
		}
		report.OpenWithoutOwner++
		row := SweepRow{Reservation: key, SourceRef: source, Item: d.Reservation.CanonicalItemID, State: d.Reservation.State, Generation: d.Reservation.Generation,
			IssueState: d.IssueState, Continuation: d.Continuation, RemainingCondition: d.RemainingCondition, NextOwner: d.NextOwner,
			OperationState: d.OperationState, LastProgressAt: max(d.LastOwnerActivityAt, d.LastResultAt, d.Reservation.UpdatedAt),
			Drift: append([]string{"open-issue-no-active-owner"}, d.Drift...)}
		row.QuietSeconds = max(now-row.LastProgressAt, 0)
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
