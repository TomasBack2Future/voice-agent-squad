package dispatch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"
)

func qualifyReceiver(t *testing.T, s *Store, ctx context.Context, actor, native, incarnation string) {
	t.Helper()
	if _, err := s.BindController(ctx, actor, native, 0); err != nil {
		t.Fatal(err)
	}
	qualifyReceiverBound(t, s, ctx, actor, native, incarnation)
}

func qualifyReceiverBound(t *testing.T, s *Store, ctx context.Context, actor, native, incarnation string) {
	t.Helper()
	if err := s.BindReceiverReady(ctx, actor, native, incarnation, 1, os.Getpid(), "asyncRewake"); err != nil {
		t.Fatal(err)
	}
	port := startLoopbackListener(t)
	if _, err := s.db.Exec(`INSERT INTO notify_endpoints(instance, repo_id, kind, port, started_at) VALUES(?, 'repo-test', 'rewake', ?, 1)`, fmt.Sprintf("terminal:%s:%s", actor, incarnation), port); err != nil {
		t.Fatal(err)
	}
}

func startLoopbackListener(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

// Premise validation (issue #83): a standby controller that bootstraps via
// reserve/controller-bind with no bound receiver and no enabled reconciliation
// fallback must not admit asynchronous Worker launches. Today Bind succeeds
// unconditionally, so a Worker outcome can never be delivered.
func TestBindWithoutReceiverOrFallbackIsRejected(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	if _, err := s.Reserve(ctx, "DISPATCH-SQUAD-83", "github:o/r#83", "dispatcher", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, "DISPATCH-SQUAD-83", "BUG-017", "dispatcher", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindController(ctx, "dispatcher", "dispatcher-native", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bind(ctx, "DISPATCH-SQUAD-83", "dispatcher", "worker-native", 1); !errors.Is(err, ErrReceiverNotReady) {
		t.Fatalf("bind without receiver or fallback: want ErrReceiverNotReady, got %v", err)
	}
}

// A healthy exact receiver (actor/native/epoch/incarnation + live owner +
// wake support) admits the bind.
func TestBindWithHealthyExactReceiverIsAccepted(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	if _, err := s.Reserve(ctx, "DISPATCH-SQUAD-83", "github:o/r#83", "dispatcher", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, "DISPATCH-SQUAD-83", "BUG-017", "dispatcher", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindController(ctx, "dispatcher", "dispatcher-native", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.BindReceiverReady(ctx, "dispatcher", "dispatcher-native", "incarnation-1", 1, os.Getpid(), "asyncRewake"); err != nil {
		t.Fatal(err)
	}
	port := startLoopbackListener(t)
	if _, err := s.db.Exec(`INSERT INTO notify_endpoints(instance, repo_id, kind, port, started_at) VALUES('terminal:dispatcher:incarnation-1', 'repo-test', 'rewake', ?, 1)`, port); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Bind(ctx, "DISPATCH-SQUAD-83", "dispatcher", "worker-native", 1); err != nil {
		t.Fatalf("bind with healthy receiver: %v", err)
	} else if r.State != "dispatched" {
		t.Fatalf("bind=%+v", r)
	}
}

// Wrong/stale native, epoch, incarnation, or a dead owner_pid rejects the bind.
func TestBindWithWrongOrStaleReceiverIsRejected(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	if _, err := s.Reserve(ctx, "DISPATCH-SQUAD-83", "github:o/r#83", "dispatcher", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, "DISPATCH-SQUAD-83", "BUG-017", "dispatcher", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindController(ctx, "dispatcher", "dispatcher-native", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.BindReceiver(ctx, "dispatcher", "dispatcher-native", "incarnation-1", 1); err != nil {
		t.Fatal(err)
	}
	// Stale epoch: controller moved on, receiver row still at epoch 1.
	if _, err := s.db.Exec(`UPDATE dispatch_controller_bindings SET epoch=2 WHERE actor='dispatcher'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bind(ctx, "DISPATCH-SQUAD-83", "dispatcher", "worker-native", 1); !errors.Is(err, ErrReceiverNotReady) {
		t.Fatalf("bind with stale receiver epoch: want ErrReceiverNotReady, got %v", err)
	}
}

// Resume that replaces its own incarnation (release old, bind new) admits the bind.
func TestBindAfterSafeOwnIncarnationReplacementIsAccepted(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	if _, err := s.Reserve(ctx, "DISPATCH-SQUAD-83", "github:o/r#83", "dispatcher", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, "DISPATCH-SQUAD-83", "BUG-017", "dispatcher", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindController(ctx, "dispatcher", "dispatcher-native", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.BindReceiverReady(ctx, "dispatcher", "dispatcher-native", "incarnation-1", 1, os.Getpid(), "asyncRewake"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseReceiver(ctx, "dispatcher", "dispatcher-native", "incarnation-1", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.BindReceiverReady(ctx, "dispatcher", "dispatcher-native", "incarnation-2", 1, os.Getpid(), "asyncRewake"); err != nil {
		t.Fatal(err)
	}
	port := startLoopbackListener(t)
	if _, err := s.db.Exec(`INSERT INTO notify_endpoints(instance, repo_id, kind, port, started_at) VALUES('terminal:dispatcher:incarnation-2', 'repo-test', 'rewake', ?, 1)`, port); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Bind(ctx, "DISPATCH-SQUAD-83", "dispatcher", "worker-native", 1); err != nil {
		t.Fatalf("bind after own incarnation replacement: %v", err)
	} else if r.State != "dispatched" {
		t.Fatalf("bind=%+v", r)
	}
}

// Custody and readiness metadata commit atomically: replaying the same
// incarnation refreshes metadata, a foreign incarnation is rejected, and a
// failed bind leaves no partial pid-0 row behind.
func TestBindReceiverReadyIsAtomicAndIdempotent(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	if _, err := s.Reserve(ctx, "DISPATCH-SQUAD-83", "github:o/r#83", "dispatcher", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindController(ctx, "dispatcher", "dispatcher-native", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.BindReceiverReady(ctx, "dispatcher", "dispatcher-native", "incarnation-1", 1, os.Getpid(), "asyncRewake"); err != nil {
		t.Fatal(err)
	}
	if err := s.BindReceiverReady(ctx, "dispatcher", "dispatcher-native", "incarnation-1", 1, os.Getpid(), "asyncRewake"); err != nil {
		t.Fatalf("same-incarnation replay: %v", err)
	}
	if err := s.BindReceiverReady(ctx, "dispatcher", "dispatcher-native", "foreign", 1, os.Getpid(), "asyncRewake"); err == nil {
		t.Fatal("foreign incarnation replaced owned receiver")
	}
	var pid, bound int
	var wake string
	if err := s.db.QueryRow(`SELECT owner_pid, bound_at, wake_kind FROM dispatch_controller_receivers`).Scan(&pid, &bound, &wake); err != nil {
		t.Fatal(err)
	}
	if pid != os.Getpid() || bound == 0 || wake != "asyncRewake" {
		t.Fatalf("metadata not committed atomically: pid=%d bound=%d wake=%s", pid, bound, wake)
	}
	if err := s.BindReceiverReady(ctx, "dispatcher", "dispatcher-native", "bad incarnation!", 1, os.Getpid(), "asyncRewake"); err == nil {
		t.Fatal("invalid incarnation admitted")
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM dispatch_controller_receivers WHERE incarnation='bad incarnation!'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid bind left a row: count=%d err=%v", count, err)
	}
}

// Supervised/manual mode stays possible but is never reported unattended-ready.
func TestBindSupervisedModeIsAllowedButNotUnattendedReady(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	if _, err := s.Reserve(ctx, "DISPATCH-SQUAD-83", "github:o/r#83", "dispatcher", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, "DISPATCH-SQUAD-83", "BUG-017", "dispatcher", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindController(ctx, "dispatcher", "dispatcher-native", 0); err != nil {
		t.Fatal(err)
	}
	r, err := s.BindSupervised(ctx, "DISPATCH-SQUAD-83", "dispatcher", "worker-native", 1)
	if err != nil {
		t.Fatalf("supervised bind: %v", err)
	}
	if r.State != "dispatched" {
		t.Fatalf("supervised bind=%+v", r)
	}
	ready, err := s.ReceiverReady(ctx, "dispatcher")
	if err != nil {
		t.Fatal(err)
	}
	if ready.UnattendedReady {
		t.Fatalf("supervised bind reported unattended-ready: %+v", ready)
	}
}
