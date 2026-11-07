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

// Decision points to an existing owner-authored message; it grants no new scope.
type Decision struct {
	Revision    int64  `json:"revision"`
	OutcomeID   int64  `json:"outcome_id"`
	Action      string `json:"action"`
	Condition   string `json:"condition"`
	WorkerAgent string `json:"worker_agent"`
	// MergePolicy is "standing" (default) or "release-required": the latter makes
	// the native tool boundary refuse merge-class commands without MergeRelease.
	MergePolicy  string        `json:"merge_policy,omitempty"`
	MergeRelease *MergeRelease `json:"merge_release,omitempty"`
	// WaitsFor is the hold's structured dependencies with their current
	// satisfaction; BlocksPhase is the phase the hold stops ("" = every phase).
	WaitsFor     []WaitStatus `json:"waits_for,omitempty"`
	BlocksPhase  string       `json:"blocks_phase,omitempty"`
	WaitDeadline int64        `json:"wait_deadline,omitempty"`
	// WaitsSatisfied: every wait is met, so this hold needs a new decision.
	// WaitOverdue: the time box ended with waits unmet; it never auto-proceeds.
	WaitsSatisfied bool `json:"waits_satisfied,omitempty"`
	WaitOverdue    bool `json:"wait_overdue,omitempty"`
}

type DecisionRequest struct {
	Reservation      string `json:"reservation"`
	Generation       int64  `json:"generation"`
	WorkerSession    string `json:"worker_session"`
	ExpectedRevision int64  `json:"expected_revision"`
	OutcomeID        int64  `json:"outcome_id"`
	Action           string `json:"action"`
	Condition        string `json:"condition"`
	// MergePolicy is optional; empty keeps the assignment's current policy.
	MergePolicy  string        `json:"merge_policy,omitempty"`
	MergeRelease *MergeRelease `json:"merge_release,omitempty"`
	// WaitsFor makes a hold's dependencies checkable (hold only). A wait on an
	// already satisfied result or one closing a current-phase cycle is refused.
	WaitsFor     []Wait `json:"waits_for,omitempty"`
	BlocksPhase  string `json:"blocks_phase,omitempty"`
	WaitDeadline int64  `json:"wait_deadline,omitempty"`
}

var ErrStaleDecision = errors.New("decision changed or missing: read terminal-events decision-get before proceeding")

// workerRecipient deliberately rejects ambiguous custody rather than guessing a
// session identity. Released claims can receive decisions, never write authority.
func workerRecipient(ctx context.Context, tx *sql.Tx, repo, key string, generation int64) (string, error) {
	return workerRecipientIn(ctx, tx, repo, key, generation, "dispatched")
}

// workerRecipientIn resolves the sole claimant of an assignment in one of the
// named reservation states; a late operation exit may still reach its original
// Worker after the reservation completed.
func workerRecipientIn(ctx context.Context, tx *sql.Tx, repo, key string, generation int64, states ...string) (string, error) {
	marks := make([]string, len(states))
	args := []any{repo, key, generation}
	for i, st := range states {
		marks[i] = "?"
		args = append(args, st)
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT c.agent_id FROM dispatch_reservations r JOIN
 (SELECT repo_id,item_id,agent_id,claimed_at FROM claims UNION ALL SELECT repo_id,item_id,agent_id,claimed_at FROM claim_history) c
 ON c.repo_id=r.repo_id AND c.item_id=r.canonical_item_id AND c.claimed_at>=r.reserved_at
 WHERE r.repo_id=? AND r.item_id=? AND r.generation=? AND r.state IN (`+strings.Join(marks, ",")+`)`, args...)
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

// DecisionLookupError names why a decision lookup could not locate its
// assignment, so "no decision recorded" (revision zero) is never confused with
// a request that identified no live assignment.
type DecisionLookupError struct {
	Condition string
	Detail    string
}

func (e *DecisionLookupError) Error() string {
	return fmt.Sprintf("decision lookup %s: %s", e.Condition, e.Detail)
}

func (s Store) diagnoseLookup(ctx context.Context, key string, generation int64) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT generation,state FROM dispatch_reservations WHERE repo_id=? AND item_id=?`, s.Repo, key)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var g int64
		var state string
		if err = rows.Scan(&g, &state); err != nil {
			return err
		}
		found = true
		if g != generation {
			continue
		}
		if state != "dispatched" {
			return &DecisionLookupError{"not-dispatched", fmt.Sprintf("reservation %s generation %d is %s, not dispatched", key, generation, state)}
		}
		return &DecisionLookupError{"worker-session-mismatch", fmt.Sprintf("--worker-session is not the native session bound to reservation %s generation %d", key, generation)}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if !found {
		return &DecisionLookupError{"reservation-not-found", fmt.Sprintf("no reservation %q in this ledger", key)}
	}
	return &DecisionLookupError{"generation-mismatch", fmt.Sprintf("reservation %s has no generation %d", key, generation)}
}

// CurrentDecision excludes completed/transferred generations and canonical-item
// continuations. Revision zero means this assignment has not adopted decisions.
func (s Store) CurrentDecision(ctx context.Context, key string, generation int64, session string) (Decision, error) {
	var d Decision
	var item string
	switch {
	case key == "":
		return d, &DecisionLookupError{"missing-reservation", "--reservation is required"}
	case generation < 1:
		return d, &DecisionLookupError{"invalid-generation", "--generation is required and must be at least 1"}
	case session == "":
		return d, &DecisionLookupError{"missing-worker-session", "--worker-session is required: decision-get never resolves a session implicitly"}
	}
	var rawWaits string
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(d.revision,0),COALESCE(d.outcome_id,0),COALESCE(d.action,''),COALESCE(d.condition,''),COALESCE(d.worker_agent,''),COALESCE(d.merge_policy,'standing'),COALESCE(r.canonical_item_id,''),COALESCE(CASE WHEN d.waits_revision=d.revision THEN d.waits_for ELSE '[]' END,'[]'),CASE WHEN d.waits_revision=d.revision THEN d.blocks_phase ELSE '' END,CASE WHEN d.waits_revision=d.revision THEN d.wait_deadline ELSE 0 END
 FROM dispatch_reservations r LEFT JOIN dispatch_decisions d ON d.repo_id=r.repo_id AND d.reservation_key=r.item_id AND d.generation=r.generation AND d.item_id=r.canonical_item_id
 WHERE r.repo_id=? AND r.item_id=? AND r.generation=? AND r.worker_thread_id=? AND r.state='dispatched'`, s.Repo, key, generation, session).Scan(&d.Revision, &d.OutcomeID, &d.Action, &d.Condition, &d.WorkerAgent, &d.MergePolicy, &item, &rawWaits, &d.BlocksPhase, &d.WaitDeadline)
	if errors.Is(err, sql.ErrNoRows) {
		return d, s.diagnoseLookup(ctx, key, generation)
	}
	if err == nil {
		err = d.loadWaits(ctx, s.DB, s.Repo, rawWaits, time.Now())
	}
	if err == nil && d.Revision == 0 {
		// An unadopted assignment keeps the original zero-value decision shape.
		d.MergePolicy = ""
	}
	if err == nil && d.Revision > 0 && d.Action == "proceed" {
		d.MergeRelease, err = currentRelease(ctx, s.DB, s.Repo, key, generation, item, d.Revision, time.Now().Unix())
	}
	return d, err
}

// Decide atomically CASes the effective decision and persists a durable wake.
// Retrying the same message and contents is idempotent; stale authors cannot
// replace a newer decision even when their message was posted later.
func (s Store) Decide(ctx context.Context, actor string, q DecisionRequest) (Decision, error) {
	var d Decision
	if err := s.precheck(ctx, q); err != nil {
		return d, err
	}
	err := store.WithTxRetry(ctx, s.DB, func(tx *sql.Tx) error {
		var e error
		d, e = s.decideTx(ctx, tx, actor, q)
		return e
	})
	if err == nil {
		s.wakeReceivers(ctx)
	}
	return d, err
}

// precheck validates a request and verifies live PR facts outside any
// transaction; decideTx re-checks everything that depends on the ledger.
func (s Store) precheck(ctx context.Context, q DecisionRequest) error {
	if q.MergePolicy != "" && q.MergePolicy != "standing" && q.MergePolicy != "release-required" {
		return ErrInvalidEvent
	}
	if q.MergeRelease != nil {
		if q.Action != "proceed" {
			return &Rejection{Condition: "invalid-merge-release", Repair: "a merge release can only accompany a proceed decision"}
		}
		if err := q.MergeRelease.validate(time.Now()); err != nil {
			return err
		}
		if err := q.MergeRelease.verify(ctx, s.PRFacts); err != nil {
			return err
		}
	}
	if q.ExpectedRevision < 0 || (q.Action != "proceed" && q.Action != "hold") || len(q.Condition) > 500 || (q.Action == "hold" && strings.TrimSpace(q.Condition) == "") {
		return ErrInvalidEvent
	}
	return validateWaits(q, q.Reservation, time.Now())
}

func (s Store) decideTx(ctx context.Context, tx *sql.Tx, actor string, q DecisionRequest) (Decision, error) {
	var d Decision
	err := func() error {
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
		oldWaits := "[]"
		err = tx.QueryRowContext(ctx, `SELECT revision,outcome_id,action,condition,worker_agent,merge_policy,CASE WHEN waits_revision=revision THEN waits_for ELSE '[]' END,CASE WHEN waits_revision=revision THEN blocks_phase ELSE '' END,CASE WHEN waits_revision=revision THEN wait_deadline ELSE 0 END FROM dispatch_decisions WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=?`, s.Repo, q.Reservation, q.Generation, item).Scan(&old.Revision, &old.OutcomeID, &old.Action, &old.Condition, &old.WorkerAgent, &old.MergePolicy, &oldWaits, &old.BlocksPhase, &old.WaitDeadline)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		policy := q.MergePolicy
		if policy == "" {
			policy = old.MergePolicy
		}
		if policy == "" {
			// The first decision of a new generation keeps the assignment's policy.
			if e := tx.QueryRowContext(ctx, `SELECT merge_policy FROM dispatch_decisions WHERE repo_id=? AND reservation_key=? AND item_id=? ORDER BY generation DESC LIMIT 1`, s.Repo, q.Reservation, item).Scan(&policy); e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		if policy == "" {
			policy = "standing"
		}
		waits := encodeWaits(q.WaitsFor)
		if old.Revision > 0 && old.OutcomeID == q.OutcomeID && old.Action == q.Action && old.Condition == q.Condition && old.WorkerAgent == worker && old.Revision == q.ExpectedRevision+1 && old.MergePolicy == policy && oldWaits == waits && old.BlocksPhase == q.BlocksPhase && old.WaitDeadline == q.WaitDeadline {
			d = old
			if err = d.loadWaits(ctx, tx, s.Repo, oldWaits, time.Now()); err != nil {
				return err
			}
			if q.MergeRelease != nil {
				rel, e := currentRelease(ctx, tx, s.Repo, q.Reservation, q.Generation, item, old.Revision, time.Now().Unix())
				if e != nil {
					return e
				}
				if rel == nil || rel.PRRepo != q.MergeRelease.PRRepo || rel.PRNumber != q.MergeRelease.PRNumber || rel.HeadSHA != q.MergeRelease.HeadSHA || rel.BaseSHA != q.MergeRelease.BaseSHA {
					return ErrStaleDecision
				}
			}
			return nil
		}
		if old.Revision != q.ExpectedRevision || q.OutcomeID <= old.OutcomeID {
			return ErrStaleDecision
		}
		if err = checkWaitGraph(ctx, tx, s.Repo, q.Reservation, q); err != nil {
			return err
		}
		d = Decision{Revision: old.Revision + 1, OutcomeID: q.OutcomeID, Action: q.Action, Condition: q.Condition, WorkerAgent: worker, MergePolicy: policy, BlocksPhase: q.BlocksPhase, WaitDeadline: q.WaitDeadline}
		_, err = tx.ExecContext(ctx, `INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent,merge_policy,waits_for,blocks_phase,wait_deadline,waits_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(repo_id,reservation_key,generation,item_id) DO UPDATE SET revision=excluded.revision,outcome_id=excluded.outcome_id,action=excluded.action,condition=excluded.condition,worker_agent=excluded.worker_agent,merge_policy=excluded.merge_policy,waits_for=excluded.waits_for,blocks_phase=excluded.blocks_phase,wait_deadline=excluded.wait_deadline,waits_revision=excluded.waits_revision`, s.Repo, q.Reservation, q.Generation, item, d.Revision, d.OutcomeID, d.Action, d.Condition, worker, policy, waits, q.BlocksPhase, q.WaitDeadline, d.Revision)
		if err != nil {
			return err
		}
		if err = d.loadWaits(ctx, tx, s.Repo, waits, time.Now()); err != nil {
			return err
		}
		// Any new revision revokes earlier releases; only this request's release survives.
		if _, err = tx.ExecContext(ctx, `DELETE FROM dispatch_merge_releases WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=?`, s.Repo, q.Reservation, q.Generation, item); err != nil {
			return err
		}
		if q.MergeRelease != nil {
			var evidence int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM messages m WHERE m.repo_id=? AND m.id=? AND m.thread=? AND m.agent_id=? AND m.ts>=(SELECT reserved_at FROM dispatch_reservations WHERE repo_id=? AND item_id=? AND generation=?)`, s.Repo, q.MergeRelease.EvidenceOutcome, item, actor, s.Repo, q.Reservation, q.Generation).Scan(&evidence); err != nil {
				return err
			}
			if evidence != 1 {
				return &Rejection{Condition: "invalid-merge-release", Repair: "evidence_outcome must be your own message on the canonical item thread, posted after this assignment started"}
			}
			r := q.MergeRelease
			if _, err = tx.ExecContext(ctx, `INSERT INTO dispatch_merge_releases(repo_id,reservation_key,generation,item_id,decision_revision,pr_repo,pr_number,head_sha,base_sha,evidence_outcome,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, s.Repo, q.Reservation, q.Generation, item, d.Revision, r.PRRepo, r.PRNumber, r.HeadSHA, r.BaseSHA, r.EvidenceOutcome, r.ExpiresAt); err != nil {
				return err
			}
			d.MergeRelease = &MergeRelease{PRRepo: r.PRRepo, PRNumber: r.PRNumber, HeadSHA: r.HeadSHA, BaseSHA: r.BaseSHA, EvidenceOutcome: r.EvidenceOutcome, ExpiresAt: r.ExpiresAt, DecisionRevision: d.Revision}
		}
		publisher := s
		publisher.Recipient = ""
		_, err = publisher.recordTx(ctx, tx, actor, PublishRequest{Reservation: q.Reservation, Generation: q.Generation, WorkerSession: q.WorkerSession, Kind: "decision-resolved", OutcomeID: q.OutcomeID}, q.OutcomeID)
		return err
	}()
	return d, err
}

// loadWaits attaches the hold's waits with their current satisfaction.
func (d *Decision) loadWaits(ctx context.Context, q querier, repo, raw string, now time.Time) error {
	waits, err := decodeWaits(raw)
	if err != nil || len(waits) == 0 {
		return err
	}
	if d.WaitsFor, err = waitStatuses(ctx, q, repo, waits); err != nil {
		return err
	}
	d.WaitsSatisfied = true
	for _, w := range d.WaitsFor {
		d.WaitsSatisfied = d.WaitsSatisfied && w.Satisfied
	}
	d.WaitOverdue = !d.WaitsSatisfied && d.WaitDeadline > 0 && now.Unix() > d.WaitDeadline
	return nil
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
	if q.Kind == "blocked" || q.Kind == "decision-request" || q.Kind == "issue-closed" || q.Kind == "handoff-complete" || q.Kind == "runtime-failure" {
		if q.ExpectedDecision != revision {
			return &Rejection{Condition: "stale-decision", Repair: fmt.Sprintf("assignment adopted decision revision %d; read terminal-events decision-get and resubmit with --expected-decision %d", revision, revision)}
		}
		if q.OutcomeID <= outcome {
			return &Rejection{Condition: "pre-decision-outcome", Repair: fmt.Sprintf("outcome message %d predates adopted decision revision %d (decision message %d); post the outcome after reading the current decision", q.OutcomeID, revision, outcome)}
		}
	}
	return nil
}
