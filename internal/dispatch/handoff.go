package dispatch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/zsiec/squad/internal/store"
)

type ControllerBinding struct {
	Actor  string `json:"actor"`
	Native string `json:"native_session"`
	Epoch  int64  `json:"epoch"`
}
type HandoffRequest struct {
	RequestID     string        `json:"request_id"`
	ExpectedEpoch int64         `json:"expected_epoch"`
	OldNative     string        `json:"old_native"`
	NewActor      string        `json:"new_actor"`
	NewNative     string        `json:"new_native"`
	Reservations  []Reservation `json:"reservations"`
}
type HandoffReceipt struct {
	OldActor         string `json:"old_actor"`
	OldNative        string `json:"old_native"`
	NewActor         string `json:"new_actor"`
	NewNative        string `json:"new_native"`
	Epoch            int64  `json:"epoch"`
	RequestID        string `json:"request_id"`
	ReservationCount int    `json:"reservation_count"`
	PendingRerouted  int64  `json:"pending_rerouted"`
	At               int64  `json:"at"`
}

var controllerToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (s *Store) Controller(ctx context.Context, actor string) (ControllerBinding, error) {
	var b ControllerBinding
	err := s.db.QueryRowContext(ctx, `SELECT actor,native_session,epoch FROM dispatch_controller_bindings WHERE repo_id=? AND actor=? AND NOT EXISTS(SELECT 1 FROM dispatch_retired_controllers f WHERE f.repo_id=dispatch_controller_bindings.repo_id AND f.actor=dispatch_controller_bindings.actor)`, s.repoID, actor).Scan(&b.Actor, &b.Native, &b.Epoch)
	return b, err
}

// BindController bootstraps a legacy owner only while its complete cohort remains
// owned by that actor. Caller/installer independently verifies native identity.
// Once bound, the native cannot be changed; handoff creates a distinct actor.
func (s *Store) BindController(ctx context.Context, actor, native string, expected int64) (ControllerBinding, error) {
	b := ControllerBinding{actor, native, 1}
	if !controllerToken.MatchString(actor) || !controllerToken.MatchString(native) || expected != 0 {
		return b, fmt.Errorf("controller bootstrap requires exact actor/native and epoch zero")
	}
	err := store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		var retired, count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM dispatch_retired_controllers WHERE repo_id=? AND actor=?`, s.repoID, actor).Scan(&retired); err != nil {
			return err
		}
		if retired != 0 {
			return ErrNotOwner
		}
		var old ControllerBinding
		err := tx.QueryRowContext(ctx, `SELECT actor,native_session,epoch FROM dispatch_controller_bindings WHERE repo_id=? AND actor=?`, s.repoID, actor).Scan(&old.Actor, &old.Native, &old.Epoch)
		if err == nil {
			if old == b {
				return nil
			}
			return ErrGeneration
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM dispatch_reservations WHERE repo_id=? AND reserved_by=?`, s.repoID, actor).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return ErrNotOwner
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO dispatch_controller_bindings(repo_id,actor,native_session,epoch) VALUES(?,?,?,1)`, s.repoID, actor, native)
		return err
	})
	return b, err
}

// Handoff transfers the entire exact owner cohort and unhandled callback routing
// atomically. Generations, native Workers, claims, decisions and external operations
// are untouched. Old controller exclusion is durable protocol custody, not TUI silence.
func (s *Store) Handoff(ctx context.Context, actor string, q HandoffRequest) (HandoffReceipt, error) {
	var out HandoffReceipt
	if !controllerToken.MatchString(q.RequestID) || !controllerToken.MatchString(actor) || !controllerToken.MatchString(q.NewActor) || !controllerToken.MatchString(q.OldNative) || !controllerToken.MatchString(q.NewNative) || actor == q.NewActor || q.OldNative == q.NewNative || q.ExpectedEpoch < 1 || len(q.Reservations) < 1 || len(q.Reservations) > 1000 {
		return out, fmt.Errorf("exact distinct controller identities and bounded complete inventory required")
	}
	raw, err := json.Marshal(struct {
		Actor   string
		Request HandoffRequest
	}{actor, q})
	if err != nil {
		return out, err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	err = store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		var previousHash, previousReceipt string
		err := tx.QueryRowContext(ctx, `SELECT request_sha256,receipt FROM dispatch_handoffs WHERE repo_id=? AND request_id=?`, s.repoID, q.RequestID).Scan(&previousHash, &previousReceipt)
		if err == nil {
			if previousHash != hash {
				return fmt.Errorf("handoff replay changed input")
			}
			return json.Unmarshal([]byte(previousReceipt), &out)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var native string
		var epoch int64
		if err = tx.QueryRowContext(ctx, `SELECT native_session,epoch FROM dispatch_controller_bindings WHERE repo_id=? AND actor=? AND NOT EXISTS(SELECT 1 FROM dispatch_retired_controllers WHERE repo_id=? AND actor=?)`, s.repoID, actor, s.repoID, actor).Scan(&native, &epoch); err != nil {
			return ErrNotOwner
		}
		if native != q.OldNative || epoch != q.ExpectedEpoch {
			return ErrGeneration
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM agents WHERE repo_id=? AND id=?`, s.repoID, q.NewActor).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("new actor must register legitimately before handoff")
		}
		if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM dispatch_controller_bindings WHERE repo_id=? AND (actor=? OR native_session=?))+(SELECT count(*) FROM dispatch_retired_controllers WHERE repo_id=? AND actor=?)+(SELECT count(*) FROM dispatch_reservations WHERE repo_id=? AND reserved_by=?)`, s.repoID, q.NewActor, q.NewNative, s.repoID, q.NewActor, s.repoID, q.NewActor).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("target controller already has custody or was retired")
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM dispatch_reservations WHERE repo_id=? AND reserved_by=?`, s.repoID, actor).Scan(&count); err != nil {
			return err
		}
		if count != len(q.Reservations) {
			return fmt.Errorf("complete owner inventory changed")
		}
		seen := map[string]bool{}
		for _, expected := range q.Reservations {
			if seen[expected.ItemID] || expected.RepoID != s.repoID || expected.ReservedBy != actor {
				return fmt.Errorf("duplicate or wrong-owner inventory")
			}
			seen[expected.ItemID] = true
			row, err := scanReservation(tx.QueryRowContext(ctx, reservationSelect+` WHERE repo_id=? AND item_id=?`, s.repoID, expected.ItemID))
			if err != nil {
				return err
			}
			if *row != expected {
				return fmt.Errorf("reservation inventory changed: %s", expected.ItemID)
			}
		}
		out = HandoffReceipt{actor, q.OldNative, q.NewActor, q.NewNative, epoch + 1, q.RequestID, count, 0, s.now().Unix()}
		if _, err = tx.ExecContext(ctx, `INSERT INTO dispatch_controller_bindings(repo_id,actor,native_session,epoch) VALUES(?,?,?,?)`, s.repoID, q.NewActor, q.NewNative, out.Epoch); err != nil {
			return err
		}
		// Only unhandled owner-directed events move. Worker decision wakes and
		// historical handled evidence retain their original recipients.
		result, err := tx.ExecContext(ctx, `UPDATE terminal_event_receipts SET recipient=?,delivered_session='',delivered_at=0 WHERE repo_id=? AND recipient=? AND processed_at=0 AND kind!='decision-resolved' AND EXISTS(SELECT 1 FROM dispatch_reservations r WHERE r.repo_id=terminal_event_receipts.repo_id AND r.item_id=terminal_event_receipts.reservation_key AND r.reserved_by=? AND r.generation=terminal_event_receipts.generation AND r.worker_thread_id=terminal_event_receipts.worker_session)`, q.NewActor, s.repoID, actor, actor)
		if err != nil {
			return err
		}
		out.PendingRerouted, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE dispatch_reservations SET reserved_by=? WHERE repo_id=? AND reserved_by=?`, q.NewActor, s.repoID, actor); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO dispatch_retired_controllers(repo_id,actor,retired_at,successor) VALUES(?,?,?,?)`, s.repoID, actor, out.At, q.NewActor); err != nil {
			return err
		}
		receipt, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO dispatch_handoffs(repo_id,request_id,request_sha256,receipt) VALUES(?,?,?,?)`, s.repoID, q.RequestID, hash, string(receipt))
		return err
	})
	return out, err
}

func (s *Store) HandoffReceipt(ctx context.Context, id string) (HandoffReceipt, error) {
	var receipt HandoffReceipt
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT receipt FROM dispatch_handoffs WHERE repo_id=? AND request_id=?`, s.repoID, id).Scan(&raw)
	if err != nil {
		return receipt, err
	}
	err = json.Unmarshal([]byte(raw), &receipt)
	return receipt, err
}

func (s *Store) BindReceiver(ctx context.Context, actor, native, incarnation string, epoch int64) error {
	if !controllerToken.MatchString(incarnation) || epoch < 1 {
		return fmt.Errorf("exact receiver incarnation and epoch required")
	}
	return store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `INSERT INTO dispatch_controller_receivers(repo_id,actor,native_session,epoch,incarnation) SELECT repo_id,actor,native_session,epoch,? FROM dispatch_controller_bindings b WHERE repo_id=? AND actor=? AND native_session=? AND epoch=? AND NOT EXISTS(SELECT 1 FROM dispatch_retired_controllers f WHERE f.repo_id=b.repo_id AND f.actor=b.actor) ON CONFLICT(repo_id,actor) DO NOTHING`, incarnation, s.repoID, actor, native, epoch)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
		var actual string
		err = tx.QueryRowContext(ctx, `SELECT incarnation FROM dispatch_controller_receivers WHERE repo_id=? AND actor=? AND native_session=? AND epoch=? AND NOT EXISTS(SELECT 1 FROM dispatch_retired_controllers WHERE repo_id=? AND actor=?)`, s.repoID, actor, native, epoch, s.repoID, actor).Scan(&actual)
		if err != nil {
			return err
		}
		if actual != incarnation {
			return fmt.Errorf("another receiver owns this controller")
		}
		return nil
	})
}
func (s *Store) ReleaseReceiver(ctx context.Context, actor, native, incarnation string, epoch int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM dispatch_controller_receivers WHERE repo_id=? AND actor=? AND native_session=? AND incarnation=? AND epoch=?`, s.repoID, actor, native, incarnation, epoch)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("receiver release fence rejected")
	}
	return nil
}
