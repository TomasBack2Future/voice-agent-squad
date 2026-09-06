package dispatch

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/store"
)

func newDispatchStore(t *testing.T, now *time.Time) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "global.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db, "repo-test", func() time.Time { return *now })
}

func TestReserveBindCloseLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	r, err := s.Reserve(ctx, "DISPATCH-STUDIO-501", "github:o/r#501", "dispatcher-a", "ready", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if r.Generation != 1 || r.State != "reserved" {
		t.Fatalf("reserve=%+v", r)
	}
	if _, err := s.Reserve(ctx, "DISPATCH-STUDIO-501", "github:o/r#501", "dispatcher-b", "duplicate", 15*time.Minute); !errors.Is(err, ErrAlreadyReserved) {
		t.Fatalf("want ErrAlreadyReserved, got %v", err)
	}
	if _, err := s.Bind(ctx, "DISPATCH-STUDIO-501", "dispatcher-a", "thread-too-early", 1); !errors.Is(err, ErrCanonicalItem) {
		t.Fatalf("bind without canonical item: want ErrCanonicalItem, got %v", err)
	}
	r, err = s.Attach(ctx, "DISPATCH-STUDIO-501", "STUDIO-501", "dispatcher-a", 1)
	if err != nil || r.CanonicalItemID != "STUDIO-501" {
		t.Fatalf("attach=%+v err=%v", r, err)
	}
	r, err = s.Bind(ctx, "DISPATCH-STUDIO-501", "dispatcher-a", "thread-123", 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "dispatched" || r.WorkerThreadID != "thread-123" || r.ExpiresAt != 0 {
		t.Fatalf("bind=%+v", r)
	}
	r, err = s.Close(ctx, "DISPATCH-STUDIO-501", "dispatcher-a", "completed", "issue closed", 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "completed" {
		t.Fatalf("close=%+v", r)
	}
}

func TestCanonicalItemCanOnlyAttachToOneReservation(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	_, _ = s.Reserve(ctx, "DISPATCH-STUDIO-601", "github:o/r#601", "dispatcher-a", "", time.Minute)
	_, _ = s.Reserve(ctx, "DISPATCH-STUDIO-602", "github:o/r#602", "dispatcher-a", "", time.Minute)
	if _, err := s.Attach(ctx, "DISPATCH-STUDIO-601", "STUDIO-601", "dispatcher-a", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, "DISPATCH-STUDIO-602", "STUDIO-601", "dispatcher-a", 1); err == nil {
		t.Fatal("expected unique canonical item conflict")
	}
}

func TestConcurrentSourceReservationHasExactlyOneWinner(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	type result struct {
		reservation *Reservation
		err         error
	}
	results := make(chan result, 2)
	for _, candidate := range []struct{ key, actor string }{
		{"DISPATCH-STUDIO-701-A", "dispatcher-a"},
		{"DISPATCH-STUDIO-701-B", "dispatcher-b"},
	} {
		candidate := candidate
		go func() {
			r, err := s.Reserve(ctx, candidate.key, "github:o/r#701", candidate.actor, "race", time.Minute)
			results <- result{r, err}
		}()
	}
	winners, losers := 0, 0
	for range 2 {
		r := <-results
		switch {
		case r.err == nil:
			winners++
		case errors.Is(r.err, ErrAlreadyReserved):
			losers++
		default:
			t.Fatalf("unexpected reservation result: reservation=%+v err=%v", r.reservation, r.err)
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("winners=%d losers=%d, want one each", winners, losers)
	}
}

func TestExpiredReservationCanBeReusedWithNewGeneration(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	_, err := s.Reserve(ctx, "STUDIO-502", "github:o/r#502", "dispatcher-a", "first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	r, err := s.Reserve(ctx, "STUDIO-502", "github:o/r#502", "dispatcher-b", "retry", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if r.Generation != 2 || r.ReservedBy != "dispatcher-b" {
		t.Fatalf("reused=%+v", r)
	}
	if _, err := s.Bind(ctx, "STUDIO-502", "dispatcher-a", "old-thread", 1); !errors.Is(err, ErrNotOwner) && !errors.Is(err, ErrGeneration) {
		t.Fatalf("old dispatcher must be fenced, got %v", err)
	}
}

func TestReserveRejectsItemSourceCrossing(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	_, _ = s.Reserve(ctx, "STUDIO-503", "github:o/r#503", "dispatcher-a", "", time.Minute)
	_, err := s.Reserve(ctx, "STUDIO-503", "github:o/r#DIFFERENT", "dispatcher-a", "", time.Minute)
	if !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("want ErrIdentityConflict, got %v", err)
	}
}

func TestInactiveLegacyItemKeyMigratesToSourceFirstKey(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	_, err := s.Reserve(ctx, "STUDIO-604", "github:o/r#604", "dispatcher-a", "legacy", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	r, err := s.Reserve(ctx, "DISPATCH-STUDIO-604", "github:o/r#604", "dispatcher-b", "source first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if r.ItemID != "DISPATCH-STUDIO-604" || r.Generation != 2 {
		t.Fatalf("migrated=%+v", r)
	}
}
