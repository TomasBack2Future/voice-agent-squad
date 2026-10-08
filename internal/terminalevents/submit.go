package terminalevents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/store"
)

// Rejection names the exact failed publish condition plus a concrete repair
// action. It replaces the generic ErrInvalidEvent for Worker outcome
// submissions so a repo mismatch can never again be misdiagnosed as a
// missing decision.
type Rejection struct {
	Condition string `json:"condition"`
	Repair    string `json:"repair"`
}

func (r *Rejection) Error() string {
	return fmt.Sprintf("event rejected (%s): %s", r.Condition, r.Repair)
}

// SubmitRequest carries the assignment identity plus the outcome body. The
// caller never extracts a message ID: Submit stores the canonical message
// and the durable event in one transaction and returns both IDs.
type SubmitRequest struct {
	Reservation      string `json:"reservation"`
	Generation       int64  `json:"generation"`
	WorkerSession    string `json:"worker_session"`
	Kind             string `json:"kind"`
	Body             string `json:"body"`
	ExpectedDecision int64  `json:"expected_decision"`
}

// SubmitResult returns the stored identities.
type SubmitResult struct {
	MessageID int64  `json:"message_id"`
	EventID   string `json:"event_id"`
}

// MaxSubmitBodyBytes caps outcome bodies, mirroring the chat post cap.
const MaxSubmitBodyBytes = 64 * 1024

// Submit validates the explicit ledger/repository plus reservation,
// generation, native session and actor, then atomically stores the canonical
// outcome message and the durable terminal event. It is idempotent: the same
// request returns the same IDs without duplicates. Failed validation leaves
// no half-committed message or event.
func (s Store) Submit(ctx context.Context, actor string, q SubmitRequest) (SubmitResult, error) {
	var out SubmitResult
	actor = strings.TrimSpace(actor)
	q.Reservation = strings.TrimSpace(q.Reservation)
	q.WorkerSession = strings.TrimSpace(q.WorkerSession)
	q.Kind = strings.TrimSpace(q.Kind)
	if s.Repo == "" || actor == "" || q.Reservation == "" || q.WorkerSession == "" {
		return out, &Rejection{Condition: "ambiguous-identity", Repair: "pass explicit ledger/repository, reservation, worker session and actor; nothing is guessed across repositories"}
	}
	if q.Generation < 1 || (q.Kind != "issue-closed" && q.Kind != "handoff-complete" && q.Kind != "blocked" && q.Kind != "decision-request") {
		return out, &Rejection{Condition: "malformed-request", Repair: "generation must be >= 1 and kind one of issue-closed, handoff-complete, blocked, decision-request"}
	}
	if strings.TrimSpace(q.Body) == "" || len(q.Body) > MaxSubmitBodyBytes {
		return out, &Rejection{Condition: "malformed-request", Repair: "outcome body must be non-empty and at most 65536 bytes"}
	}
	err := store.WithTxRetry(ctx, s.DB, func(tx *sql.Tx) error {
		var e error
		out, e = s.submitTx(ctx, tx, actor, q)
		return e
	})
	return out, err
}

func (s Store) submitTx(ctx context.Context, tx *sql.Tx, actor string, q SubmitRequest) (SubmitResult, error) {
	var out SubmitResult
	var item, owner, state string
	var reservedAt int64
	err := tx.QueryRowContext(ctx, `SELECT canonical_item_id, reserved_by, state, reserved_at FROM dispatch_reservations
		WHERE repo_id=? AND item_id=? AND generation=? AND worker_thread_id=?`,
		s.Repo, q.Reservation, q.Generation, q.WorkerSession).Scan(&item, &owner, &state, &reservedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, s.diagnoseReservation(ctx, tx, q)
	}
	if err != nil {
		return out, err
	}
	terminal := q.Kind == "issue-closed" || q.Kind == "handoff-complete" || q.Kind == "blocked"
	if state != "dispatched" && (state != "completed" || !terminal) {
		return out, &Rejection{Condition: "reservation-state", Repair: fmt.Sprintf("reservation %s generation %d is %s; only dispatched reservations accept Worker outcomes (completed accepts terminal kinds)", q.Reservation, q.Generation, state)}
	}
	// Idempotent retry: same assignment identity plus identical body returns
	// the stored IDs. A different body for the same event slot is a replay
	// conflict, never a silent second message.
	var messageID, sourceID int64
	var eventID, oldBody string
	err = tx.QueryRowContext(ctx, `SELECT e.event_id, e.outcome_id, e.source_message_id, m.body FROM terminal_event_receipts e
		JOIN messages m ON m.id=e.outcome_id AND m.repo_id=e.repo_id
		WHERE e.repo_id=? AND e.reservation_key=? AND e.generation=? AND e.worker_session=? AND e.kind=? AND e.item_id=?`,
		s.Repo, q.Reservation, q.Generation, q.WorkerSession, q.Kind, item).Scan(&eventID, &messageID, &sourceID, &oldBody)
	if err == nil {
		if oldBody != q.Body || sourceID != messageID {
			return out, &Rejection{Condition: "replay-conflict", Repair: "a different outcome body was already submitted for this reservation/generation/kind; submit a new outcome under a new assignment step instead"}
		}
		return SubmitResult{MessageID: messageID, EventID: eventID}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err := submitDecisionFence(ctx, tx, s.Repo, q, item); err != nil {
		return out, err
	}
	var held int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT agent_id,claimed_at FROM claims WHERE repo_id=? AND item_id=?
		UNION ALL SELECT agent_id,claimed_at FROM claim_history WHERE repo_id=? AND item_id=?)
		WHERE agent_id=? AND claimed_at>=?`, s.Repo, item, s.Repo, item, actor, reservedAt).Scan(&held)
	if err != nil {
		return out, err
	}
	if held == 0 {
		return out, &Rejection{Condition: "actor-custody", Repair: fmt.Sprintf("actor %s holds no claim on %s since reservation start; the outcome must come from the Worker that owns the assignment", actor, item)}
	}
	now := time.Now().Unix()
	kind, mentions := "milestone", "[]"
	switch q.Kind {
	case "blocked":
		kind = "stuck"
	case "decision-request":
		kind = "ask"
		mentions = fmt.Sprintf("[%q]", owner)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority)
		VALUES(?,?,?,?,?,?,?,'normal')`, s.Repo, now, actor, item, kind, q.Body, mentions)
	if err != nil {
		return out, err
	}
	messageID, err = result.LastInsertId()
	if err != nil {
		return out, err
	}
	id, err := s.recordTx(ctx, tx, actor, PublishRequest{q.Reservation, q.Generation, q.WorkerSession, q.Kind, messageID, q.ExpectedDecision}, messageID)
	if err != nil {
		return out, err
	}
	return SubmitResult{MessageID: messageID, EventID: id}, nil
}

// diagnoseReservation pinpoints which assignment-identity condition failed
// without guessing across repositories.
func (s Store) diagnoseReservation(ctx context.Context, tx *sql.Tx, q SubmitRequest) error {
	var n int
	_ = tx.QueryRowContext(ctx, `SELECT count(*) FROM dispatch_reservations WHERE repo_id=? AND item_id=?`, s.Repo, q.Reservation).Scan(&n)
	if n == 0 {
		_ = tx.QueryRowContext(ctx, `SELECT count(*) FROM dispatch_reservations WHERE item_id=?`, q.Reservation).Scan(&n)
		if n > 0 {
			return &Rejection{Condition: "reservation-repo-mismatch", Repair: fmt.Sprintf("reservation %s exists but not in ledger %s; run submit from the assignment ledger directory (squad --help shows repo discovery), never copy messages across repositories", q.Reservation, s.Repo)}
		}
		return &Rejection{Condition: "absent-reservation", Repair: fmt.Sprintf("no reservation %s in ledger %s; reserve and bind through the Dispatcher before submitting an outcome", q.Reservation, s.Repo)}
	}
	_ = tx.QueryRowContext(ctx, `SELECT count(*) FROM dispatch_reservations WHERE repo_id=? AND item_id=? AND generation=?`, s.Repo, q.Reservation, q.Generation).Scan(&n)
	if n == 0 {
		return &Rejection{Condition: "wrong-generation", Repair: fmt.Sprintf("reservation %s has no generation %d in this ledger; read the current generation from dispatch list before submitting", q.Reservation, q.Generation)}
	}
	return &Rejection{Condition: "wrong-worker", Repair: fmt.Sprintf("worker session %s is not bound to %s generation %d; submit from the bound native session only", q.WorkerSession, q.Reservation, q.Generation)}
}

// submitDecisionFence keeps the opt-in decision contract: unadopted
// assignments publish exactly as before; adopted assignments require the
// current expected revision. Holds are preserved: a held assignment cannot
// report terminal completion. Nothing auto-proceeds.
func submitDecisionFence(ctx context.Context, tx *sql.Tx, repo string, q SubmitRequest, item string) error {
	var revision int64
	var action string
	err := tx.QueryRowContext(ctx, `SELECT revision, action FROM dispatch_decisions
		WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=?`, repo, q.Reservation, q.Generation, item).Scan(&revision, &action)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if q.ExpectedDecision != revision {
		return &Rejection{Condition: "stale-decision", Repair: fmt.Sprintf("assignment adopted decision revision %d; read terminal-events decision-get and resubmit with --expected-decision %d", revision, revision)}
	}
	if action == "hold" && (q.Kind == "issue-closed" || q.Kind == "handoff-complete") {
		return &Rejection{Condition: "decision-hold", Repair: "assignment is on hold; resolve the hold through the Dispatcher before reporting terminal completion"}
	}
	return nil
}
