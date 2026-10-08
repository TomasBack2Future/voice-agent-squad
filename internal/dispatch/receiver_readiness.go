package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
	"time"

	"github.com/zsiec/squad/internal/store"
)

// ErrReceiverNotReady rejects unattended asynchronous admission when the
// controller has neither a healthy exact receiver nor an enabled,
// correctly targeted reconciliation fallback.
var ErrReceiverNotReady = errors.New("dispatch: receiver not ready for unattended admission")

// ReceiverReadiness reports how a controller can receive Worker outcomes.
// Only UnattendedReady admits asynchronous launch/bind.
type ReceiverReadiness struct {
	RepoID          string `json:"repo_id"`
	Actor           string `json:"actor"`
	Native          string `json:"native_session"`
	Epoch           int64  `json:"epoch"`
	Incarnation     string `json:"incarnation,omitempty"`
	OwnerPID        int    `json:"owner_pid,omitempty"`
	WakeKind        string `json:"wake_kind,omitempty"`
	UnattendedReady bool   `json:"unattended_ready"`
	Route           string `json:"route"`
	Repair          string `json:"repair,omitempty"`
}

// ReceiverReady qualifies exact ledger/actor/native/epoch, receiver
// incarnation/owner health and native wake support, or a genuinely enabled
// reconciliation fallback. It never enables anything; a paused fallback is
// reported unavailable, not unattended-ready.
func (s *Store) ReceiverReady(ctx context.Context, actor string) (*ReceiverReadiness, error) {
	ready := &ReceiverReadiness{RepoID: s.repoID, Actor: actor, Route: "none"}
	b, err := s.Controller(ctx, actor)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			ready.Repair = "bootstrap the controller first: reserve the canonical source, then controller-bind the verified native (provisional); activate the receiver before Worker launch"
			return ready, nil
		}
		return nil, err
	}
	ready.Native, ready.Epoch = b.Native, b.Epoch
	var incarnation string
	var ownerPID int
	var boundAt int64
	var wakeKind string
	err = s.db.QueryRowContext(ctx, `SELECT r.incarnation, r.owner_pid, r.bound_at, r.wake_kind
		FROM dispatch_controller_receivers r
		WHERE r.repo_id=? AND r.actor=? AND r.native_session=? AND r.epoch=?`, s.repoID, actor, b.Native, b.Epoch).Scan(&incarnation, &ownerPID, &boundAt, &wakeKind)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		ready.Incarnation, ready.OwnerPID, ready.WakeKind = incarnation, ownerPID, wakeKind
		if !processAlive(ownerPID) {
			ready.Repair = fmt.Sprintf("receiver incarnation %s owner_pid %d is dead; resume the same native %s to replace its own incarnation, then re-run preflight", incarnation, ownerPID, b.Native)
			return s.withFallback(ctx, ready)
		}
		if err := s.wakeReachable(ctx, actor, incarnation); err != nil {
			ready.Repair = fmt.Sprintf("receiver incarnation %s has no live native wake endpoint: %v; re-arm the receiver via its session launcher (never inject terminal input)", incarnation, err)
			return s.withFallback(ctx, ready)
		}
		ready.UnattendedReady = true
		ready.Route = "receiver"
		return ready, nil
	}
	ready.Repair = fmt.Sprintf("no receiver bound for %s/%s/epoch %d; activate the exact-native receiver via its session launcher before asynchronous launch (reserve/controller-bind stay provisional)", actor, b.Native, b.Epoch)
	return s.withFallback(ctx, ready)
}

// withFallback also qualifies a genuinely enabled, correctly targeted
// reconciliation fallback. A paused or wrong-target fallback never qualifies
// and is never enabled here.
func (s *Store) withFallback(ctx context.Context, ready *ReceiverReadiness) (*ReceiverReadiness, error) {
	var port int
	var startedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT port, started_at FROM notify_endpoints
		WHERE repo_id=? AND kind='reconcile' AND instance=?`, s.repoID, "reconcile:"+ready.Actor+":"+ready.Native).Scan(&port, &startedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ready, nil
	}
	if err != nil {
		return nil, err
	}
	if !endpointAlive(port) {
		ready.Repair += "; reconciliation fallback endpoint is registered but unreachable (stale row, not fallback)"
		return ready, nil
	}
	_ = startedAt
	ready.UnattendedReady = true
	ready.Route = "reconciliation-fallback"
	ready.Repair = ""
	return ready, nil
}

func (s *Store) wakeReachable(ctx context.Context, actor, incarnation string) error {
	var port int
	err := s.db.QueryRowContext(ctx, `SELECT port FROM notify_endpoints
		WHERE repo_id=? AND kind='rewake' AND instance=?`, s.repoID, "terminal:"+actor+":"+incarnation).Scan(&port)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("no rewake endpoint for this incarnation")
	}
	if err != nil {
		return err
	}
	if !endpointAlive(port) {
		return fmt.Errorf("rewake endpoint 127.0.0.1:%d unreachable", port)
	}
	return nil
}

func endpointAlive(port int) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// BindReceiverReady records session-owned receiver custody with owner health and
// wake-kind metadata. ownerPID must be the calling receiver owner's live pid;
// wakeKind names the native wake path (e.g. "asyncRewake"). Custody and
// metadata commit atomically: a partial row is never left behind.
func (s *Store) BindReceiverReady(ctx context.Context, actor, native, incarnation string, epoch int64, ownerPID int, wakeKind string) error {
	if !controllerToken.MatchString(incarnation) || epoch < 1 {
		return fmt.Errorf("exact receiver incarnation and epoch required")
	}
	if ownerPID <= 0 || !processAlive(ownerPID) {
		return fmt.Errorf("receiver owner_pid must be a live process")
	}
	if wakeKind == "" {
		return fmt.Errorf("receiver wake kind required")
	}
	return store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `INSERT INTO dispatch_controller_receivers(repo_id,actor,native_session,epoch,incarnation,owner_pid,bound_at,wake_kind) SELECT repo_id,actor,native_session,epoch,?,?,?,? FROM dispatch_controller_bindings b WHERE repo_id=? AND actor=? AND native_session=? AND epoch=? AND NOT EXISTS(SELECT 1 FROM dispatch_retired_controllers f WHERE f.repo_id=b.repo_id AND f.actor=b.actor) ON CONFLICT(repo_id,actor) DO NOTHING`, incarnation, ownerPID, s.now().Unix(), wakeKind, s.repoID, actor, native, epoch)
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
		// Idempotent replay of the same incarnation refreshes owner metadata;
		// a foreign incarnation still rejects it.
		result, err = tx.ExecContext(ctx, `UPDATE dispatch_controller_receivers SET owner_pid=?, bound_at=?, wake_kind=?
			WHERE repo_id=? AND actor=? AND native_session=? AND epoch=? AND incarnation=?
			AND NOT EXISTS(SELECT 1 FROM dispatch_retired_controllers WHERE repo_id=? AND actor=?)`,
			ownerPID, s.now().Unix(), wakeKind, s.repoID, actor, native, epoch, incarnation, s.repoID, actor)
		if err != nil {
			return err
		}
		n, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("another receiver owns this controller")
		}
		return nil
	})
}
