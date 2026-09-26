package terminalevents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/zsiec/squad/internal/store"
)

// Decision points to an existing owner-authored message; it grants no new scope.
type Decision struct {
	Revision    int64  `json:"revision"`
	OutcomeID   int64  `json:"outcome_id"`
	Action      string `json:"action"`
	Condition   string `json:"condition"`
	WorkerAgent string `json:"worker_agent"`
}

type DecisionRequest struct {
	Reservation      string `json:"reservation"`
	Generation       int64  `json:"generation"`
	WorkerSession    string `json:"worker_session"`
	ExpectedRevision int64  `json:"expected_revision"`
	OutcomeID        int64  `json:"outcome_id"`
	Action           string `json:"action"`
	Condition        string `json:"condition"`
}

var ErrStaleDecision = errors.New("decision changed or missing: read terminal-events decision-get before proceeding")

// workerRecipient deliberately rejects ambiguous custody rather than guessing a
// session identity. Released claims can receive decisions, never write authority.
func workerRecipient(ctx context.Context, tx *sql.Tx, repo, key string, generation int64) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT c.agent_id FROM dispatch_reservations r JOIN
 (SELECT repo_id,item_id,agent_id,claimed_at FROM claims UNION ALL SELECT repo_id,item_id,agent_id,claimed_at FROM claim_history) c
 ON c.repo_id=r.repo_id AND c.item_id=r.canonical_item_id AND c.claimed_at>=r.reserved_at
 WHERE r.repo_id=? AND r.item_id=? AND r.generation=? AND r.state='dispatched'`, repo, key, generation)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var agents []string
	for rows.Next() {
		var a string
		if err = rows.Scan(&a); err != nil {
			return "", err
		}
		agents = append(agents, a)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if len(agents) != 1 {
		return "", ErrInvalidEvent
	}
	return agents[0], nil
}

// CurrentDecision excludes completed/transferred generations and canonical-item
// continuations. Revision zero means this assignment has not adopted decisions.
func (s Store) CurrentDecision(ctx context.Context, key string, generation int64, session string) (Decision, error) {
	var d Decision
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(d.revision,0),COALESCE(d.outcome_id,0),COALESCE(d.action,''),COALESCE(d.condition,''),COALESCE(d.worker_agent,'')
 FROM dispatch_reservations r LEFT JOIN dispatch_decisions d ON d.repo_id=r.repo_id AND d.reservation_key=r.item_id AND d.generation=r.generation AND d.item_id=r.canonical_item_id
 WHERE r.repo_id=? AND r.item_id=? AND r.generation=? AND r.worker_thread_id=? AND r.state='dispatched'`, s.Repo, key, generation, session).Scan(&d.Revision, &d.OutcomeID, &d.Action, &d.Condition, &d.WorkerAgent)
	return d, err
}

// Decide atomically CASes the effective decision and persists a durable wake.
// Retrying the same message and contents is idempotent; stale authors cannot
// replace a newer decision even when their message was posted later.
func (s Store) Decide(ctx context.Context, actor string, q DecisionRequest) (Decision, error) {
	var d Decision
	if q.ExpectedRevision < 0 || (q.Action != "proceed" && q.Action != "hold") || len(q.Condition) > 500 || (q.Action == "hold" && strings.TrimSpace(q.Condition) == "") {
		return d, ErrInvalidEvent
	}
	err := store.WithTxRetry(ctx, s.DB, func(tx *sql.Tx) error {
		var item string
		err := tx.QueryRowContext(ctx, `SELECT r.canonical_item_id FROM dispatch_reservations r JOIN messages m ON m.repo_id=r.repo_id AND m.id=? AND m.agent_id=r.reserved_by AND m.thread=r.canonical_item_id AND m.ts>=r.reserved_at
 WHERE r.repo_id=? AND r.item_id=? AND r.generation=? AND r.worker_thread_id=? AND r.reserved_by=? AND r.state='dispatched'`, q.OutcomeID, s.Repo, q.Reservation, q.Generation, q.WorkerSession, actor).Scan(&item)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidEvent
		}
		if err != nil {
			return err
		}
		worker, err := workerRecipient(ctx, tx, s.Repo, q.Reservation, q.Generation)
		if err != nil {
			return err
		}
		var old Decision
		err = tx.QueryRowContext(ctx, `SELECT revision,outcome_id,action,condition,worker_agent FROM dispatch_decisions WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=?`, s.Repo, q.Reservation, q.Generation, item).Scan(&old.Revision, &old.OutcomeID, &old.Action, &old.Condition, &old.WorkerAgent)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if old.Revision > 0 && old.OutcomeID == q.OutcomeID && old.Action == q.Action && old.Condition == q.Condition && old.WorkerAgent == worker && old.Revision == q.ExpectedRevision+1 {
			d = old
			return nil
		}
		if old.Revision != q.ExpectedRevision || q.OutcomeID <= old.OutcomeID {
			return ErrStaleDecision
		}
		d = Decision{old.Revision + 1, q.OutcomeID, q.Action, q.Condition, worker}
		_, err = tx.ExecContext(ctx, `INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES(?,?,?,?,?,?,?,?,?)
 ON CONFLICT(repo_id,reservation_key,generation,item_id) DO UPDATE SET revision=excluded.revision,outcome_id=excluded.outcome_id,action=excluded.action,condition=excluded.condition,worker_agent=excluded.worker_agent`, s.Repo, q.Reservation, q.Generation, item, d.Revision, d.OutcomeID, d.Action, d.Condition, worker)
		if err != nil {
			return err
		}
		publisher := s
		publisher.Recipient = ""
		_, err = publisher.recordTx(ctx, tx, actor, PublishRequest{Reservation: q.Reservation, Generation: q.Generation, WorkerSession: q.WorkerSession, Kind: "decision-resolved", OutcomeID: q.OutcomeID}, q.OutcomeID)
		return err
	})
	return d, err
}

func decisionFence(ctx context.Context, tx *sql.Tx, repo string, q PublishRequest) error {
	var revision, outcome int64
	err := tx.QueryRowContext(ctx, `SELECT d.revision,d.outcome_id FROM dispatch_decisions d JOIN dispatch_reservations r ON r.repo_id=d.repo_id AND r.item_id=d.reservation_key AND r.generation=d.generation AND r.canonical_item_id=d.item_id
 WHERE d.repo_id=? AND d.reservation_key=? AND d.generation=?`, repo, q.Reservation, q.Generation).Scan(&revision, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if q.Kind == "decision-resolved" {
		if q.OutcomeID != outcome {
			return ErrStaleDecision
		}
		return nil
	}
	if q.Kind == "blocked" || q.Kind == "decision-request" || q.Kind == "issue-closed" || q.Kind == "handoff-complete" {
		if q.ExpectedDecision != revision || q.OutcomeID <= outcome {
			return fmt.Errorf("%w (current revision %d)", ErrStaleDecision, revision)
		}
	}
	return nil
}
