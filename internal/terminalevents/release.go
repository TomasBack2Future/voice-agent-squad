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

// effectiveRelease is the one definition of "this hold's release authorization
// is in force", over dispatch_decisions d joined to its reservation r, with the
// current unix time as its single parameter: authorized at exactly the current
// revision, by the Dispatcher that owns the reservation now, inside the time
// box, for a Worker native that is not fenced (a fenced native can receive no
// wake). It does not survive a newer revision, a controller change, an expired
// time box or a fence; each returns the hold to the responsible Dispatcher. The release,
// the owner's read of its decision and the sweep all use this predicate, so
// none of them can believe in a release another would refuse. The dispatch
// package repeats it for the sweep (it cannot import this one).
const effectiveRelease = `(d.on_satisfied_revision=d.revision AND d.waits_revision=d.revision AND d.waits_for!='[]'
 AND (d.wait_deadline=0 OR d.wait_deadline>=?)
 AND EXISTS(SELECT 1 FROM messages m WHERE m.repo_id=d.repo_id AND m.id=d.outcome_id AND m.agent_id=r.reserved_by)
 AND NOT EXISTS(SELECT 1 FROM worker_native_fences f WHERE f.repo_id=d.repo_id AND f.native_session=r.worker_thread_id))`

// markedHolds reads the live holds whose release authorization is in force. A
// hold whose waits cannot be decoded is skipped, never an error for the
// caller's own work.
func markedHolds(ctx context.Context, q querier, repo string, now time.Time) ([]markedHold, error) {
	rows, err := q.QueryContext(ctx, `SELECT d.reservation_key,d.generation,d.item_id,d.revision,d.outcome_id,d.condition,r.worker_thread_id,d.waits_for
 FROM dispatch_decisions d
 JOIN dispatch_reservations r ON r.repo_id=d.repo_id AND r.item_id=d.reservation_key AND r.generation=d.generation AND r.canonical_item_id=d.item_id
 WHERE `+effectiveRelease+` AND d.repo_id=? AND r.state='dispatched' AND d.action='hold' ORDER BY d.reservation_key`, now.Unix(), repo)
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
// Nothing else changes: scope, claims and merge policy stay as decided.
//
// It runs only in its own transaction (ReleaseSatisfied), never inside another
// owner's write, and each hold is released under its own savepoint: a hold with
// an unmet wait, ambiguous custody or writes the schema refuses is left exactly
// as it was for the Dispatcher's sweep, and the remaining holds still release.
// It returns how many holds were released.
func releaseSatisfiedHolds(ctx context.Context, tx *sql.Tx, repo string) (int, error) {
	// The clock is read here, with the write lock already held: a time box that
	// expired while this transaction waited for the lock is expired.
	now := time.Now()
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
		if _, err = tx.ExecContext(ctx, `SAVEPOINT hold_release`); err != nil {
			return released, err
		}
		done, err := h.release(ctx, tx, repo, worker, evidence, now)
		if err != nil || !done {
			// Undo this hold only. A write the schema refuses (for example a wake
			// for a native fenced after the read above) must not undo the others.
			if _, rerr := tx.ExecContext(ctx, `ROLLBACK TO hold_release`); rerr != nil {
				return released, rerr
			}
		}
		if _, rerr := tx.ExecContext(ctx, `RELEASE hold_release`); rerr != nil {
			return released, rerr
		}
		if err == nil && done {
			released++
		}
	}
	return released, nil
}

// release performs one hold's transition. It reports false, with no error,
// when the hold changed under it (the CAS matched no row).
func (h markedHold) release(ctx context.Context, tx *sql.Tx, repo, worker string, evidence []string, now time.Time) (bool, error) {
	id := fmt.Sprintf("worker-terminal-v1/%s/%d/%s/decision-resolved/%d/waits-released-r%d", h.key, h.generation, h.session, h.outcome, h.revision+1)
	if eventPattern.FindString(id) != id {
		return false, nil
	}
	d := Decision{Revision: h.revision + 1, OutcomeID: h.outcome, Action: "proceed", WorkerAgent: worker,
		Condition: truncateRunes("waits satisfied ("+strings.Join(evidence, "; ")+"); released hold: "+h.condition, 500)}
	res, err := tx.ExecContext(ctx, `UPDATE dispatch_decisions SET revision=?,action='proceed',condition=?,worker_agent=?,waits_for='[]',blocks_phase='',wait_deadline=0,waits_revision=?,on_satisfied_revision=0
 WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=? AND revision=? AND action='hold' AND on_satisfied_revision=revision
 AND (wait_deadline=0 OR wait_deadline>=?)`, d.Revision, d.Condition, worker, d.Revision, repo, h.key, h.generation, h.item, h.revision, now.Unix())
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	if err = recordDecisionHistory(ctx, tx, repo, h.key, h.generation, h.item, d, "[]", "waits-satisfied"); err != nil {
		return false, err
	}
	// The release keeps the hold's outcome id, which is what delivery uses to tell
	// a current decision from a superseded one. Close the hold's own wake here so
	// it can be neither delivered nor acknowledged once revision+1 is effective,
	// exactly as an ordinary newer decision retires the previous one.
	if _, err = tx.ExecContext(ctx, `UPDATE terminal_event_receipts SET processed_at=?,processed_note=?
 WHERE repo_id=? AND reservation_key=? AND generation=? AND kind='decision-resolved' AND outcome_id=? AND processed_at=0 AND event_id!=?`,
		now.Unix(), fmt.Sprintf("superseded: hold released at revision %d", d.Revision), repo, h.key, h.generation, h.outcome, id); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO terminal_event_receipts
 (repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id)
 VALUES(?,?,?,?,?,?,?,'decision-resolved',?,?)`, repo, worker, id, h.key, h.generation, h.session, h.item, h.outcome, h.outcome)
	if err != nil {
		return false, err
	}
	return true, nil
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

// ReleaseSatisfied applies pre-authorized hold releases now, waiting for the
// write lock like any other write. It is idempotent and safe for any caller: it
// can only perform the transition the responsible Dispatcher already decided,
// on facts recorded in the ledger.
func (s Store) ReleaseSatisfied(ctx context.Context) (int, error) {
	return s.releaseSatisfied(ctx, true)
}

// releaseReady decides on the read connection whether any marked hold can be
// released, so a write transaction is opened only then and never once per
// listener poll: not for an unsatisfied hold, and not for one whose custody is
// ambiguous and cannot be released anyway.
func (s Store) releaseReady(ctx context.Context) (bool, error) {
	holds, err := markedHolds(ctx, s.DB, s.Repo, time.Now())
	if err != nil {
		return false, err
	}
	for _, h := range holds {
		ok, _, err := h.satisfied(ctx, s.DB, s.Repo)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		if _, err = workerRecipient(ctx, s.DB, s.Repo, h.key, h.generation); errors.Is(err, ErrInvalidEvent) {
			continue
		} else if err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (s Store) releaseSatisfied(ctx context.Context, wait bool) (int, error) {
	ready, err := s.releaseReady(ctx)
	if err != nil || !ready {
		return 0, err
	}
	released := 0
	apply := func(tx *sql.Tx) error {
		var e error
		released, e = releaseSatisfiedHolds(ctx, tx, s.Repo)
		return e
	}
	if wait {
		err = store.WithTxRetry(ctx, s.DB, apply)
	} else {
		err = s.tryTx(ctx, apply)
	}
	if err != nil {
		// The transaction rolled back, or the lock was busy: nothing was released.
		return 0, err
	}
	if released > 0 {
		s.wakeReceivers(ctx)
	}
	return released, nil
}

// tryTx runs fn in one write transaction only if the write lock is free right
// now. The store's connections wait up to their busy timeout for the lock, and
// that wait does not end with the context, so the attempt uses one connection
// with the timeout switched off and restores it before the connection returns
// to the pool. A busy lock is an error to the caller, who retries on a later
// read.
func (s Store) tryTx(ctx context.Context, fn func(*sql.Tx) error) error {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var busy int64
	if err = conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busy); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `PRAGMA busy_timeout=0`); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf(`PRAGMA busy_timeout=%d`, busy))
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// releaseBestEffort applies pending releases around another operation. After a
// phase result commits and before an acknowledgement it waits for the write
// lock like the write it accompanies. After a recipient has read its pending
// events it only takes the lock if it is free (wait=false): a poll never waits
// on a release it does not own. A failure leaves the hold in place, where it
// stays visible as hold-waits-satisfied drift and is retried by the next read;
// it never fails the operation it accompanies.
func (s Store) releaseBestEffort(ctx context.Context, wait bool) int {
	n, _ := s.releaseSatisfied(context.WithoutCancel(ctx), wait)
	return n
}
