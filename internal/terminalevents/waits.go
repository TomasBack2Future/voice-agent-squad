package terminalevents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/store"
)

// Wait is one structured dependency of a hold: the hold stays justified until
// the named reservation records the phase result Until requires, or reaches a
// terminal reservation state when Until is "terminal".
type Wait struct {
	Reservation string `json:"reservation"`
	Phase       string `json:"phase,omitempty"`
	// Until is "executed" (all checks of the phase ran, any verdict), "passed"
	// (all ran and passed) or "terminal" (the reservation completed, failed or
	// was cancelled; Phase must be empty).
	Until string `json:"until"`
}

// WaitStatus is a wait as read back with its current satisfaction.
type WaitStatus struct {
	Wait
	Satisfied bool   `json:"satisfied"`
	Evidence  string `json:"evidence,omitempty"`
}

// PhaseResult is an owner's durable phase outcome: execution and verdict are
// separate, so a failed check can block acceptance without freezing a
// successor that only needed the checks to have run.
type PhaseResult struct {
	Reservation   string `json:"reservation"`
	Generation    int64  `json:"generation"`
	WorkerSession string `json:"worker_session"`
	Phase         string `json:"phase"`
	Checks        string `json:"checks"`
	Verdict       string `json:"verdict"`
	Deferred      string `json:"deferred,omitempty"`
	OutcomeID     int64  `json:"outcome_id"`
	RecordedBy    string `json:"recorded_by,omitempty"`
	RecordedAt    int64  `json:"recorded_at,omitempty"`
	EventID       string `json:"event_id,omitempty"`
}

var phaseName = regexp.MustCompile(`^[a-z0-9][a-z0-9:._-]{0,63}$`)

const maxWaits = 8

func validateWaits(q DecisionRequest, self string, now time.Time) error {
	if len(q.WaitsFor) == 0 {
		if q.WaitDeadline != 0 || q.BlocksPhase != "" {
			return &Rejection{Condition: "invalid-wait", Repair: "--wait-deadline and --blocks-phase require at least one --wait-for"}
		}
		return nil
	}
	if q.Action != "hold" {
		return &Rejection{Condition: "invalid-wait", Repair: "structured waits belong to a hold; a proceed decision carries none"}
	}
	if len(q.WaitsFor) > maxWaits {
		return &Rejection{Condition: "invalid-wait", Repair: fmt.Sprintf("at most %d waits per decision", maxWaits)}
	}
	if q.BlocksPhase != "" && !phaseName.MatchString(q.BlocksPhase) {
		return &Rejection{Condition: "invalid-wait", Repair: "--blocks-phase must match " + phaseName.String()}
	}
	if q.WaitDeadline != 0 && q.WaitDeadline <= now.Unix() {
		return &Rejection{Condition: "expired-wait-deadline", Repair: "a time box must end in the future; publish the decision the expired time box called for instead"}
	}
	seen := map[Wait]bool{}
	for _, w := range q.WaitsFor {
		switch {
		case w.Reservation == "":
			return &Rejection{Condition: "invalid-wait", Repair: "each wait names a reservation"}
		case w.Reservation == self:
			return &Rejection{Condition: "wait-cycle", Repair: "an assignment cannot wait on itself"}
		case w.Until == "terminal" && w.Phase != "":
			return &Rejection{Condition: "invalid-wait", Repair: "a terminal wait names no phase"}
		case (w.Until == "executed" || w.Until == "passed") && !phaseName.MatchString(w.Phase):
			return &Rejection{Condition: "invalid-wait", Repair: "an executed/passed wait names a phase matching " + phaseName.String()}
		case w.Until != "terminal" && w.Until != "executed" && w.Until != "passed":
			return &Rejection{Condition: "invalid-wait", Repair: "until must be executed, passed or terminal"}
		case seen[w]:
			return &Rejection{Condition: "invalid-wait", Repair: "duplicate wait"}
		}
		seen[w] = true
	}
	return nil
}

type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// waitSatisfied reads the current fact behind one wait; it never infers a
// result from prose, a receipt acknowledgement or a process exit.
func waitSatisfied(ctx context.Context, q querier, repo string, w Wait) (bool, string, error) {
	if w.Until == "terminal" {
		var state string
		err := q.QueryRowContext(ctx, `SELECT state FROM dispatch_reservations WHERE repo_id=? AND item_id=?`, repo, w.Reservation).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return false, "", nil
		}
		if err != nil {
			return false, "", err
		}
		done := state == "completed" || state == "failed" || state == "cancelled"
		if done {
			return true, "reservation " + state, nil
		}
		return false, "", nil
	}
	var checks, verdict string
	var outcome, generation int64
	err := q.QueryRowContext(ctx, `SELECT checks,verdict,outcome_id,generation FROM dispatch_phase_results WHERE repo_id=? AND reservation_key=? AND phase=?`, repo, w.Reservation, w.Phase).Scan(&checks, &verdict, &outcome, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	ok := checks == "all" && (w.Until == "executed" || verdict == "passed")
	if !ok {
		return false, "", nil
	}
	// A result is evidence of the named checks, whoever ran them; the generation
	// is provenance, not identity. The phase name identifies what was checked.
	return true, fmt.Sprintf("phase %s checks=%s verdict=%s outcome %d, generation %d", w.Phase, checks, verdict, outcome, generation), nil
}

func waitStatuses(ctx context.Context, q querier, repo string, waits []Wait) ([]WaitStatus, error) {
	out := make([]WaitStatus, 0, len(waits))
	for _, w := range waits {
		ok, ev, err := waitSatisfied(ctx, q, repo, w)
		if err != nil {
			return nil, err
		}
		out = append(out, WaitStatus{Wait: w, Satisfied: ok, Evidence: ev})
	}
	return out, nil
}

func decodeWaits(raw string) ([]Wait, error) {
	var w []Wait
	if raw == "" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		return nil, err
	}
	return w, nil
}

func encodeWaits(w []Wait) string {
	if len(w) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(w)
	return string(b)
}

type activeHold struct {
	blocks string
	waits  []Wait
}

// currentHolds reads every effective structured hold of live assignments.
func currentHolds(ctx context.Context, tx *sql.Tx, repo string) (map[string]activeHold, error) {
	rows, err := tx.QueryContext(ctx, `SELECT d.reservation_key,d.blocks_phase,d.waits_for FROM dispatch_decisions d JOIN dispatch_reservations r
 ON r.repo_id=d.repo_id AND r.item_id=d.reservation_key AND r.generation=d.generation AND r.canonical_item_id=d.item_id
 WHERE d.repo_id=? AND r.state='dispatched' AND d.action='hold' AND d.waits_for!='[]' AND d.waits_revision=d.revision`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]activeHold{}
	for rows.Next() {
		var key, blocks, raw string
		if err = rows.Scan(&key, &blocks, &raw); err != nil {
			return nil, err
		}
		w, err := decodeWaits(raw)
		if err != nil {
			return nil, fmt.Errorf("decision %s waits: %w", key, err)
		}
		out[key] = activeHold{blocks: blocks, waits: w}
	}
	return out, rows.Err()
}

// blocksNode reports whether a hold stops progress toward the waited phase.
// A terminal wait is blocked by any hold; "" blocks every phase.
func (h activeHold) blocksNode(w Wait) bool {
	return h.blocks == "" || w.Until == "terminal" || h.blocks == w.Phase
}

// checkWaitGraph rejects a hold that waits on something already satisfied
// (a stale wait condition) or that closes a current-phase cycle: following
// unsatisfied waits from the new targets must never return to a phase this
// hold blocks. Called inside the decision transaction, after earlier steps of
// a reorder batch are visible.
func checkWaitGraph(ctx context.Context, tx *sql.Tx, repo, self string, q DecisionRequest) error {
	if len(q.WaitsFor) == 0 {
		return nil
	}
	for _, w := range q.WaitsFor {
		var state string
		err := tx.QueryRowContext(ctx, `SELECT state FROM dispatch_reservations WHERE repo_id=? AND item_id=?`, repo, w.Reservation).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return &Rejection{Condition: "invalid-wait", Repair: fmt.Sprintf("no reservation %q in this ledger", w.Reservation)}
		}
		if err != nil {
			return err
		}
		ok, ev, err := waitSatisfied(ctx, tx, repo, w)
		if err != nil {
			return err
		}
		if ok {
			return &Rejection{Condition: "wait-already-satisfied", Repair: fmt.Sprintf("%s is already satisfied (%s); consume that result and publish the next decision instead of waiting on it; to wait on a later candidate or attempt, use a candidate-specific phase name (for example acceptance:s7 or merge:pr1972)", describeWait(w), ev)}
		}
	}
	holds, err := currentHolds(ctx, tx, repo)
	if err != nil {
		return err
	}
	holds[self] = activeHold{blocks: q.BlocksPhase, waits: q.WaitsFor}
	type frame struct {
		w    Wait
		path []string
	}
	stack := make([]frame, 0, len(q.WaitsFor))
	for _, w := range q.WaitsFor {
		stack = append(stack, frame{w, []string{self}})
	}
	visited := map[Wait]bool{}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		h, held := holds[f.w.Reservation]
		if !held || !h.blocksNode(f.w) {
			continue
		}
		path := append(append([]string{}, f.path...), describeWait(f.w))
		if f.w.Reservation == self {
			return &Rejection{Condition: "wait-cycle", Repair: "current-phase wait cycle " + strings.Join(path, " -> ") + "; publish the predecessor's proceed first (decision-reorder applies both atomically)"}
		}
		if visited[f.w] {
			continue
		}
		visited[f.w] = true
		for _, next := range h.waits {
			ok, _, err := waitSatisfied(ctx, tx, repo, next)
			if err != nil {
				return err
			}
			if !ok {
				stack = append(stack, frame{next, path})
			}
		}
	}
	return nil
}

func describeWait(w Wait) string {
	if w.Until == "terminal" {
		return w.Reservation + " terminal"
	}
	return w.Reservation + " " + w.Phase + " " + w.Until
}

// RecordPhaseResult stores the owner's phase outcome. A Worker recording its
// own result also publishes a phase-result event to the responsible
// Dispatcher in the same transaction, so consumption uses the existing
// receipt route. Retrying the same outcome is idempotent; an older outcome
// cannot replace a newer one.
func (s Store) RecordPhaseResult(ctx context.Context, actor string, p PhaseResult) (PhaseResult, error) {
	switch {
	case !phaseName.MatchString(p.Phase):
		return p, &Rejection{Condition: "invalid-phase-result", Repair: "phase must match " + phaseName.String()}
	case p.Checks != "all" && p.Checks != "partial":
		return p, &Rejection{Condition: "invalid-phase-result", Repair: "checks must be all or partial"}
	case p.Verdict != "passed" && p.Verdict != "failed" && p.Verdict != "partial":
		return p, &Rejection{Condition: "invalid-phase-result", Repair: "verdict must be passed, failed or partial"}
	case p.Checks == "partial" && p.Verdict == "passed":
		return p, &Rejection{Condition: "invalid-phase-result", Repair: "a phase whose checks did not all run cannot have passed"}
	case len(p.Deferred) > 300:
		return p, &Rejection{Condition: "invalid-phase-result", Repair: "deferred is at most 300 characters"}
	case p.Reservation == "" || p.Generation < 1 || p.WorkerSession == "" || p.OutcomeID < 1:
		return p, ErrInvalidEvent
	}
	err := store.WithTxRetry(ctx, s.DB, func(tx *sql.Tx) error {
		var item, owner string
		var reservedAt int64
		err := tx.QueryRowContext(ctx, `SELECT canonical_item_id,reserved_by,reserved_at FROM dispatch_reservations WHERE repo_id=? AND item_id=? AND generation=? AND worker_thread_id=? AND state IN ('dispatched','completed')`, s.Repo, p.Reservation, p.Generation, p.WorkerSession).Scan(&item, &owner, &reservedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidEvent
		}
		if err != nil {
			return err
		}
		var own int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE repo_id=? AND id=? AND agent_id=? AND thread=? AND ts>=?`, s.Repo, p.OutcomeID, actor, item, reservedAt).Scan(&own); err != nil {
			return err
		}
		if own != 1 {
			return &Rejection{Condition: "invalid-phase-result", Repair: "outcome must be your own message on the canonical item thread posted after this assignment started"}
		}
		worker := actor != owner
		if worker {
			recipient, err := workerRecipientIn(ctx, tx, s.Repo, p.Reservation, p.Generation, "dispatched", "completed")
			if err != nil || recipient != actor {
				return &Rejection{Condition: "invalid-phase-result", Repair: "only the assignment's claimant Worker or its Dispatcher records a phase result"}
			}
		}
		var old PhaseResult
		err = tx.QueryRowContext(ctx, `SELECT checks,verdict,deferred,outcome_id,recorded_by,recorded_at FROM dispatch_phase_results WHERE repo_id=? AND reservation_key=? AND phase=?`, s.Repo, p.Reservation, p.Phase).Scan(&old.Checks, &old.Verdict, &old.Deferred, &old.OutcomeID, &old.RecordedBy, &old.RecordedAt)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		case old.OutcomeID == p.OutcomeID && old.Checks == p.Checks && old.Verdict == p.Verdict && old.Deferred == p.Deferred && old.RecordedBy == actor:
			p.RecordedBy, p.RecordedAt = old.RecordedBy, old.RecordedAt
			if worker {
				p.EventID, err = s.phaseEvent(ctx, tx, actor, p)
			}
			return err
		case p.OutcomeID <= old.OutcomeID:
			return &Rejection{Condition: "stale-phase-result", Repair: fmt.Sprintf("phase %s already records outcome %d; a replacement must cite a newer message", p.Phase, old.OutcomeID)}
		}
		p.RecordedBy, p.RecordedAt = actor, time.Now().Unix()
		if _, err = tx.ExecContext(ctx, `INSERT INTO dispatch_phase_results(repo_id,reservation_key,phase,generation,item_id,checks,verdict,deferred,outcome_id,recorded_by,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(repo_id,reservation_key,phase) DO UPDATE SET generation=excluded.generation,item_id=excluded.item_id,checks=excluded.checks,verdict=excluded.verdict,deferred=excluded.deferred,outcome_id=excluded.outcome_id,recorded_by=excluded.recorded_by,recorded_at=excluded.recorded_at`,
			s.Repo, p.Reservation, p.Phase, p.Generation, item, p.Checks, p.Verdict, p.Deferred, p.OutcomeID, actor, p.RecordedAt); err != nil {
			return err
		}
		if worker {
			p.EventID, err = s.phaseEvent(ctx, tx, actor, p)
		}
		return err
	})
	if err == nil && p.EventID != "" {
		s.wakeReceivers(ctx)
	}
	return p, err
}

func (s Store) phaseEvent(ctx context.Context, tx *sql.Tx, actor string, p PhaseResult) (string, error) {
	publisher := s
	publisher.Recipient = ""
	return publisher.recordTx(ctx, tx, actor, PublishRequest{Reservation: p.Reservation, Generation: p.Generation, WorkerSession: p.WorkerSession, Kind: "phase-result", OutcomeID: p.OutcomeID}, p.OutcomeID)
}

// unconsumedDependents lists live holds that wait on this phase result and
// whose every wait is now satisfied: the result has not been consumed until
// each of them has a new effective decision.
func unconsumedDependents(ctx context.Context, tx *sql.Tx, repo, reservation string, outcome int64) ([]string, error) {
	var phase string
	err := tx.QueryRowContext(ctx, `SELECT phase FROM dispatch_phase_results WHERE repo_id=? AND reservation_key=? AND outcome_id=?`, repo, reservation, outcome).Scan(&phase)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	holds, err := currentHolds(ctx, tx, repo)
	if err != nil {
		return nil, err
	}
	var out []string
	for key, h := range holds {
		cites := false
		for _, w := range h.waits {
			if w.Reservation == reservation && (w.Phase == phase || w.Until == "terminal") {
				cites = true
			}
		}
		if !cites {
			continue
		}
		all := true
		for _, w := range h.waits {
			ok, _, err := waitSatisfied(ctx, tx, repo, w)
			if err != nil {
				return nil, err
			}
			all = all && ok
		}
		if all {
			out = append(out, key)
		}
	}
	return out, nil
}

// DecideBatch applies several decision CAS steps in one transaction, in order.
// A reorder publishes the predecessor's proceed before the successor's wait,
// so a cycle can never be observed and a failure leaves every decision as it
// was. Retrying the identical batch is idempotent.
func (s Store) DecideBatch(ctx context.Context, actor string, qs []DecisionRequest) ([]Decision, error) {
	if len(qs) < 1 || len(qs) > 8 {
		return nil, &Rejection{Condition: "invalid-reorder", Repair: "a reorder carries 1 to 8 decisions"}
	}
	seen := map[string]bool{}
	for _, q := range qs {
		if seen[q.Reservation] {
			return nil, &Rejection{Condition: "invalid-reorder", Repair: "each reservation appears once per reorder"}
		}
		seen[q.Reservation] = true
		if err := s.precheck(ctx, q); err != nil {
			return nil, err
		}
	}
	out := make([]Decision, len(qs))
	err := store.WithTxRetry(ctx, s.DB, func(tx *sql.Tx) error {
		for i, q := range qs {
			d, err := s.decideTx(ctx, tx, actor, q)
			if err != nil {
				return &BatchError{Index: i, Reservation: q.Reservation, Err: err}
			}
			out[i] = d
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.wakeReceivers(ctx)
	return out, nil
}

// BatchError names the step of a reorder that failed; nothing was applied.
type BatchError struct {
	Index       int
	Reservation string
	Err         error
}

func (e *BatchError) Error() string {
	return fmt.Sprintf("reorder step %d (%s) rejected, no decision changed: %v", e.Index, e.Reservation, e.Err)
}

func (e *BatchError) Unwrap() error { return e.Err }
