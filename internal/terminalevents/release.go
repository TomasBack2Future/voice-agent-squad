package terminalevents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zsiec/squad/internal/store"
)

// markedHold is a live hold whose current revision carries the Dispatcher's
// release authorization.
type markedHold struct {
	key, item, session, condition string
	generation, revision, outcome int64
	waits                         []Wait
}

// markedHolds reads only holds that may release themselves: dispatched, a hold,
// authorized at exactly the current revision, by the Dispatcher that owns the
// reservation now, and inside their time box. An authorization does not
// survive a newer revision, a controller change or an expired time box: each of
// those returns the hold to the responsible Dispatcher. A hold whose waits
// cannot be decoded is skipped, never an error for the caller's own work.
func markedHolds(ctx context.Context, q querier, repo string, now time.Time) ([]markedHold, error) {
	rows, err := q.QueryContext(ctx, `SELECT d.reservation_key,d.generation,d.item_id,d.revision,d.outcome_id,d.condition,r.worker_thread_id,d.waits_for
 FROM dispatch_decisions d
 JOIN dispatch_reservations r ON r.repo_id=d.repo_id AND r.item_id=d.reservation_key AND r.generation=d.generation AND r.canonical_item_id=d.item_id
 JOIN messages m ON m.repo_id=d.repo_id AND m.id=d.outcome_id AND m.agent_id=r.reserved_by
 WHERE d.repo_id=? AND r.state='dispatched' AND d.action='hold' AND d.on_satisfied_revision=d.revision AND d.waits_revision=d.revision
 AND d.waits_for!='[]' AND (d.wait_deadline=0 OR d.wait_deadline>=?) ORDER BY d.reservation_key`, repo, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []markedHold
	for rows.Next() {
		var h markedHold
		var raw string
		if err = rows.Scan(&h.key, &h.generation, &h.item, &h.revision, &h.outcome, &h.condition, &h.session, &raw); err != nil {
			return nil, err
		}
		waits, derr := decodeWaits(raw)
		if derr != nil || len(waits) == 0 {
			continue
		}
		h.waits = waits
		out = append(out, h)
	}
	return out, rows.Err()
}

// satisfied reports whether every wait of the hold is met, with the evidence.
func (h markedHold) satisfied(ctx context.Context, q querier, repo string) (bool, []string, error) {
	evidence := make([]string, 0, len(h.waits))
	for _, w := range h.waits {
		if w.Until == "terminal" {
			// Never authorized (validateWaits); refuse a row written another way.
			return false, nil, nil
		}
		ok, _, err := waitSatisfied(ctx, q, repo, w)
		if err != nil || !ok {
			return false, nil, err
		}
		evidence = append(evidence, describeWait(w))
	}
	return true, evidence, nil
}

// releaseSatisfiedHolds turns every marked hold whose waits are all satisfied
// into a proceed decision: one new revision under the same Dispatcher outcome,
// a history row and a durable decision-resolved wake for the held Worker.
// Nothing else changes: scope, claims and merge policy stay as decided. A hold
// with an unmet wait or with ambiguous Worker custody is left alone. It returns
// how many holds were released.
func releaseSatisfiedHolds(ctx context.Context, tx *sql.Tx, repo string, now time.Time) (int, error) {
	holds, err := markedHolds(ctx, tx, repo, now)
	if err != nil {
		return 0, err
	}
	released := 0
	for _, h := range holds {
		ok, evidence, err := h.satisfied(ctx, tx, repo)
		if err != nil {
			return released, err
		}
		if !ok {
			continue
		}
		worker, err := workerRecipient(ctx, tx, repo, h.key, h.generation)
		if errors.Is(err, ErrInvalidEvent) {
			// Ambiguous custody: the hold stays for the Dispatcher to resolve.
			continue
		}
		if err != nil {
			return released, err
		}
		d := Decision{Revision: h.revision + 1, OutcomeID: h.outcome, Action: "proceed", WorkerAgent: worker,
			Condition: truncateRunes("waits satisfied ("+strings.Join(evidence, "; ")+"); released hold: "+h.condition, 500)}
		res, err := tx.ExecContext(ctx, `UPDATE dispatch_decisions SET revision=?,action='proceed',condition=?,worker_agent=?,waits_for='[]',blocks_phase='',wait_deadline=0,waits_revision=?,on_satisfied_revision=0
 WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=? AND revision=? AND action='hold' AND on_satisfied_revision=revision`, d.Revision, d.Condition, worker, d.Revision, repo, h.key, h.generation, h.item, h.revision)
		if err != nil {
			return released, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}
		if err = recordDecisionHistory(ctx, tx, repo, h.key, h.generation, h.item, d, "[]", "waits-satisfied"); err != nil {
			return released, err
		}
		id := fmt.Sprintf("worker-terminal-v1/%s/%d/%s/decision-resolved/%d/waits-released-r%d", h.key, h.generation, h.session, h.outcome, d.Revision)
		if eventPattern.FindString(id) != id {
			return released, ErrInvalidEvent
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO terminal_event_receipts
 (repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id)
 VALUES(?,?,?,?,?,?,?,'decision-resolved',?,?)`, repo, worker, id, h.key, h.generation, h.session, h.item, h.outcome, h.outcome); err != nil {
			return released, err
		}
		released++
	}
	return released, nil
}

// truncateRunes cuts s to at most max bytes without splitting a rune.
func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// ReleaseSatisfied applies pre-authorized hold releases now. It is idempotent
// and safe for any caller: it can only perform the transition the responsible
// Dispatcher already decided, on facts recorded in the ledger.
func (s Store) ReleaseSatisfied(ctx context.Context) (int, error) {
	now := time.Now()
	// Decide on the read connection first: a write transaction is opened only
	// when some marked hold is actually satisfied, never once per listener poll.
	holds, err := markedHolds(ctx, s.DB, s.Repo, now)
	if err != nil {
		return 0, err
	}
	ready := false
	for _, h := range holds {
		ok, _, err := h.satisfied(ctx, s.DB, s.Repo)
		if err != nil {
			return 0, err
		}
		ready = ready || ok
	}
	if !ready {
		return 0, nil
	}
	released := 0
	err = store.WithTxRetry(ctx, s.DB, func(tx *sql.Tx) error {
		var e error
		released, e = releaseSatisfiedHolds(ctx, tx, s.Repo, now)
		return e
	})
	if err == nil && released > 0 {
		s.wakeReceivers(ctx)
	}
	return released, err
}

// releaseBestEffort runs before a recipient reads its pending events. A failure
// leaves the hold in place, where it stays visible as hold-waits-satisfied
// drift; it never fails the read.
func (s Store) releaseBestEffort(ctx context.Context) {
	releaseCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _ = s.ReleaseSatisfied(releaseCtx)
}
