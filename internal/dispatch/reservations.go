// Package dispatch stores durable Dispatcher reservations. A reservation is
// not work ownership; the Worker must still atomically claim the Squad item.
// Its sole purpose is to prevent two scheduler cycles from creating duplicate
// Worker tasks in the discovery-to-creation gap.
package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/store"
)

var (
	ErrAlreadyReserved  = errors.New("dispatch: source already has an active reservation")
	ErrNotFound         = errors.New("dispatch: reservation not found")
	ErrNotOwner         = errors.New("dispatch: reservation is owned by another dispatcher")
	ErrGeneration       = errors.New("dispatch: reservation generation changed")
	ErrExpired          = errors.New("dispatch: reservation expired before worker binding")
	ErrIdentityConflict = errors.New("dispatch: item and source refer to different reservations")
	ErrCanonicalItem    = errors.New("dispatch: canonical item is not attached")
)

type Reservation struct {
	RepoID          string `json:"repo_id"`
	ItemID          string `json:"reservation_key"`
	CanonicalItemID string `json:"canonical_item_id,omitempty"`
	SourceRef       string `json:"source_ref"`
	ReservedBy      string `json:"reserved_by"`
	ReservedAt      int64  `json:"reserved_at"`
	UpdatedAt       int64  `json:"updated_at"`
	ExpiresAt       int64  `json:"expires_at"`
	State           string `json:"state"`
	Generation      int64  `json:"generation"`
	WorkerThreadID  string `json:"worker_thread_id,omitempty"`
	Note            string `json:"note,omitempty"`
}

type Store struct {
	db     *sql.DB
	repoID string
	now    func() time.Time
}

func New(db *sql.DB, repoID string, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, repoID: repoID, now: now}
}

func (s *Store) Reserve(ctx context.Context, itemID, sourceRef, actor, note string, ttl time.Duration) (*Reservation, error) {
	itemID = strings.TrimSpace(itemID)
	sourceRef = strings.TrimSpace(sourceRef)
	actor = strings.TrimPrefix(strings.TrimSpace(actor), "@")
	note = strings.TrimSpace(note)
	if itemID == "" || sourceRef == "" || actor == "" {
		return nil, fmt.Errorf("dispatch: item, source, and actor are required")
	}
	if ttl <= 0 || ttl > 24*time.Hour {
		return nil, fmt.Errorf("dispatch: ttl must be greater than zero and at most 24h")
	}
	now := s.now().Unix()
	expires := now + int64(ttl/time.Second)
	var out Reservation
	err := store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		current, err := findReservationTx(ctx, tx, s.repoID, itemID, sourceRef)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if current.SourceRef != sourceRef {
				return fmt.Errorf("%w: requested item=%s source=%s; existing item=%s source=%s",
					ErrIdentityConflict, itemID, sourceRef, current.ItemID, current.SourceRef)
			}
			if current.State == "completed" {
				return fmt.Errorf("%w: %s is already completed", ErrAlreadyReserved, itemID)
			}
			active := current.State == "dispatched" ||
				(current.State == "reserved" && current.ExpiresAt > now)
			if active {
				out = *current
				return ErrAlreadyReserved
			}
			existingKey := current.ItemID
			res, err := tx.ExecContext(ctx, `
				UPDATE dispatch_reservations
				SET item_id=?, reserved_by=?, reserved_at=?, updated_at=?, expires_at=?,
				    state='reserved', generation=generation+1,
				    worker_thread_id='', note=?
				WHERE repo_id=? AND item_id=? AND generation=?
			`, itemID, actor, now, now, expires, note, s.repoID, existingKey, current.Generation)
			if err != nil {
				return err
			}
			changed, _ := res.RowsAffected()
			if changed != 1 {
				return ErrGeneration
			}
			out = Reservation{RepoID: s.repoID, ItemID: itemID, CanonicalItemID: current.CanonicalItemID, SourceRef: sourceRef,
				ReservedBy: actor, ReservedAt: now, UpdatedAt: now, ExpiresAt: expires,
				State: "reserved", Generation: current.Generation + 1, Note: note}
			return nil
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO dispatch_reservations
			  (repo_id, item_id, source_ref, reserved_by, reserved_at, updated_at,
			   expires_at, state, generation, worker_thread_id, note)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'reserved', 1, '', ?)
		`, s.repoID, itemID, sourceRef, actor, now, now, expires, note)
		if err != nil {
			return fmt.Errorf("insert dispatch reservation: %w", err)
		}
		out = Reservation{RepoID: s.repoID, ItemID: itemID, SourceRef: sourceRef,
			ReservedBy: actor, ReservedAt: now, UpdatedAt: now, ExpiresAt: expires,
			State: "reserved", Generation: 1, Note: note}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrAlreadyReserved) {
			return &out, err
		}
		return nil, err
	}
	return &out, nil
}

// Attach records the canonical Squad item after the caller has won the source
// reservation. No Worker may be bound until this step succeeds.
func (s *Store) Attach(ctx context.Context, reservationKey, canonicalItemID, actor string, generation int64) (*Reservation, error) {
	reservationKey = strings.TrimSpace(reservationKey)
	canonicalItemID = strings.TrimSpace(canonicalItemID)
	actor = strings.TrimPrefix(strings.TrimSpace(actor), "@")
	if reservationKey == "" || canonicalItemID == "" || actor == "" || generation <= 0 {
		return nil, fmt.Errorf("dispatch: reservation key, canonical item, actor, and positive generation are required")
	}
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `
		UPDATE dispatch_reservations
		SET canonical_item_id=?, updated_at=?
		WHERE repo_id=? AND item_id=? AND reserved_by=? AND generation=?
		  AND state='reserved' AND expires_at>?
	`, canonicalItemID, now, s.repoID, reservationKey, actor, generation, now)
	if err != nil {
		return nil, fmt.Errorf("attach canonical item: %w", err)
	}
	changed, _ := res.RowsAffected()
	if changed != 1 {
		return nil, s.explainBindFailure(ctx, reservationKey, actor, generation, now)
	}
	return s.Get(ctx, reservationKey)
}

func (s *Store) Bind(ctx context.Context, itemID, actor, workerThreadID string, generation int64) (*Reservation, error) {
	itemID = strings.TrimSpace(itemID)
	actor = strings.TrimPrefix(strings.TrimSpace(actor), "@")
	workerThreadID = strings.TrimSpace(workerThreadID)
	if itemID == "" || actor == "" || workerThreadID == "" || generation <= 0 {
		return nil, fmt.Errorf("dispatch: item, actor, worker thread, and positive generation are required")
	}
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `
		UPDATE dispatch_reservations
		SET state='dispatched', worker_thread_id=?, updated_at=?, expires_at=0
		WHERE repo_id=? AND item_id=? AND reserved_by=? AND generation=?
		  AND state='reserved' AND expires_at>? AND canonical_item_id<>''
	`, workerThreadID, now, s.repoID, itemID, actor, generation, now)
	if err != nil {
		return nil, err
	}
	changed, _ := res.RowsAffected()
	if changed != 1 {
		return nil, s.explainBindFailure(ctx, itemID, actor, generation, now)
	}
	return s.Get(ctx, itemID)
}

func (s *Store) Close(ctx context.Context, itemID, actor, state, note string, generation int64) (*Reservation, error) {
	itemID = strings.TrimSpace(itemID)
	actor = strings.TrimPrefix(strings.TrimSpace(actor), "@")
	state = strings.TrimSpace(state)
	if state != "completed" && state != "failed" && state != "cancelled" {
		return nil, fmt.Errorf("dispatch: close state must be completed, failed, or cancelled")
	}
	if itemID == "" || actor == "" || generation <= 0 {
		return nil, fmt.Errorf("dispatch: item, actor, and positive generation are required")
	}
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `
		UPDATE dispatch_reservations
		SET state=?, note=?, updated_at=?, expires_at=0
		WHERE repo_id=? AND item_id=? AND reserved_by=? AND generation=?
	`, state, strings.TrimSpace(note), now, s.repoID, itemID, actor, generation)
	if err != nil {
		return nil, err
	}
	changed, _ := res.RowsAffected()
	if changed != 1 {
		return nil, s.explainOwnershipFailure(ctx, itemID, actor, generation)
	}
	return s.Get(ctx, itemID)
}

func (s *Store) Get(ctx context.Context, itemID string) (*Reservation, error) {
	row := s.db.QueryRowContext(ctx, reservationSelect+` WHERE repo_id=? AND item_id=?`, s.repoID, itemID)
	r, err := scanReservation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func (s *Store) List(ctx context.Context, activeOnly bool) ([]Reservation, error) {
	q := reservationSelect + ` WHERE repo_id=?`
	if activeOnly {
		q += ` AND state IN ('reserved','dispatched')`
	}
	q += ` ORDER BY updated_at DESC, item_id`
	rows, err := s.db.QueryContext(ctx, q, s.repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reservation{}
	for rows.Next() {
		r, err := scanReservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

const reservationSelect = `SELECT repo_id, item_id, source_ref, reserved_by,
	canonical_item_id, reserved_at, updated_at, expires_at, state, generation, worker_thread_id, note
	FROM dispatch_reservations`

type scanner interface{ Scan(...any) error }

func scanReservation(s scanner) (*Reservation, error) {
	var r Reservation
	err := s.Scan(&r.RepoID, &r.ItemID, &r.SourceRef, &r.ReservedBy, &r.CanonicalItemID,
		&r.ReservedAt, &r.UpdatedAt, &r.ExpiresAt, &r.State, &r.Generation,
		&r.WorkerThreadID, &r.Note)
	return &r, err
}

func findReservationTx(ctx context.Context, tx *sql.Tx, repoID, itemID, sourceRef string) (*Reservation, error) {
	rows, err := tx.QueryContext(ctx, reservationSelect+`
		WHERE repo_id=? AND (item_id=? OR source_ref=?)`, repoID, itemID, sourceRef)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []*Reservation
	for rows.Next() {
		r, err := scanReservation(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, r)
	}
	if len(found) == 0 {
		return nil, sql.ErrNoRows
	}
	if len(found) > 1 {
		return nil, ErrIdentityConflict
	}
	return found[0], nil
}

func (s *Store) explainBindFailure(ctx context.Context, itemID, actor string, generation, now int64) error {
	r, err := s.Get(ctx, itemID)
	if err != nil {
		return err
	}
	if r.ReservedBy != actor {
		return ErrNotOwner
	}
	if r.Generation != generation {
		return ErrGeneration
	}
	if r.State == "reserved" && r.ExpiresAt <= now {
		return ErrExpired
	}
	if r.State == "reserved" && r.CanonicalItemID == "" {
		return ErrCanonicalItem
	}
	return fmt.Errorf("dispatch: reservation is in state %s", r.State)
}

func (s *Store) explainOwnershipFailure(ctx context.Context, itemID, actor string, generation int64) error {
	r, err := s.Get(ctx, itemID)
	if err != nil {
		return err
	}
	if r.ReservedBy != actor {
		return ErrNotOwner
	}
	if r.Generation != generation {
		return ErrGeneration
	}
	return fmt.Errorf("dispatch: reservation update rejected")
}
