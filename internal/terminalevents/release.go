package terminalevents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/store"
)

// releaseSatisfiedHolds turns every live hold that the Dispatcher marked
// on_satisfied=proceed, and whose waits are all satisfied, into a proceed
// decision: one new revision under the same Dispatcher outcome, a history row
// and a durable decision-resolved wake for the held Worker. Nothing else
// changes: scope, claims and merge policy stay as decided. A hold without the
// marker, with an unmet wait or with ambiguous Worker custody is left alone.
// It returns how many holds were released.
func releaseSatisfiedHolds(ctx context.Context, tx *sql.Tx, repo string) (int, error) {
	holds, err := currentHolds(ctx, tx, repo)
	if err != nil {
		return 0, err
	}
	keys := make([]string, 0, len(holds))
	for key, h := range holds {
		if h.release {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	released := 0
	for _, key := range keys {
		statuses, err := waitStatuses(ctx, tx, repo, holds[key].waits)
		if err != nil {
			return released, err
		}
		evidence := make([]string, 0, len(statuses))
		all := len(statuses) > 0
		for _, w := range statuses {
			all = all && w.Satisfied
			evidence = append(evidence, describeWait(w.Wait))
		}
		if !all {
			continue
		}
		var generation, revision, outcome int64
		var item, session, old string
		err = tx.QueryRowContext(ctx, `SELECT d.generation,d.item_id,d.revision,d.outcome_id,d.condition,r.worker_thread_id FROM dispatch_decisions d JOIN dispatch_reservations r
 ON r.repo_id=d.repo_id AND r.item_id=d.reservation_key AND r.generation=d.generation AND r.canonical_item_id=d.item_id
 WHERE d.repo_id=? AND d.reservation_key=? AND r.state='dispatched' AND d.action='hold' AND d.on_satisfied='proceed' AND d.waits_revision=d.revision`, repo, key).Scan(&generation, &item, &revision, &outcome, &old, &session)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return released, err
		}
		worker, err := workerRecipient(ctx, tx, repo, key, generation)
		if err != nil {
			// Ambiguous custody: the hold stays for the Dispatcher to resolve.
			continue
		}
		condition := "waits satisfied (" + strings.Join(evidence, "; ") + "); released hold: " + old
		if len(condition) > 500 {
			condition = condition[:500]
		}
		d := Decision{Revision: revision + 1, OutcomeID: outcome, Action: "proceed", Condition: condition, WorkerAgent: worker}
		res, err := tx.ExecContext(ctx, `UPDATE dispatch_decisions SET revision=?,action='proceed',condition=?,worker_agent=?,waits_for='[]',blocks_phase='',wait_deadline=0,waits_revision=?,on_satisfied=''
 WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=? AND revision=? AND action='hold'`, d.Revision, condition, worker, d.Revision, repo, key, generation, item, revision)
		if err != nil {
			return released, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}
		if err = recordDecisionHistory(ctx, tx, repo, key, generation, item, d, "[]", "waits-satisfied"); err != nil {
			return released, err
		}
		id := fmt.Sprintf("worker-terminal-v1/%s/%d/%s/decision-resolved/%d/waits-released-r%d", key, generation, session, outcome, d.Revision)
		if eventPattern.FindString(id) != id {
			return released, ErrInvalidEvent
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO terminal_event_receipts
 (repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id)
 VALUES(?,?,?,?,?,?,?,'decision-resolved',?,?)`, repo, worker, id, key, generation, session, item, outcome, outcome); err != nil {
			return released, err
		}
		released++
	}
	return released, nil
}

// ReleaseSatisfied applies pre-authorized hold releases now. It is idempotent
// and safe for any caller: it can only perform the transition the Dispatcher
// already decided, on facts recorded in the ledger.
func (s Store) ReleaseSatisfied(ctx context.Context) (int, error) {
	// Most calls find nothing to do; avoid a write transaction for them.
	var candidates int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM dispatch_decisions d JOIN dispatch_reservations r
 ON r.repo_id=d.repo_id AND r.item_id=d.reservation_key AND r.generation=d.generation AND r.canonical_item_id=d.item_id
 WHERE d.repo_id=? AND r.state='dispatched' AND d.action='hold' AND d.on_satisfied='proceed' AND d.waits_revision=d.revision`, s.Repo).Scan(&candidates); err != nil || candidates == 0 {
		return 0, err
	}
	released := 0
	err := store.WithTxRetry(ctx, s.DB, func(tx *sql.Tx) error {
		var e error
		released, e = releaseSatisfiedHolds(ctx, tx, s.Repo)
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
