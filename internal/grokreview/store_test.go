package grokreview

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "reviewer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testSeed(key string) ReviewSeed {
	return ReviewSeed{
		Key: key, InstallationID: 7, RepositoryID: 11,
		Repository: "owner/repo", PullRequest: 13,
		BaseRef: "main", BaseSHA: "base", HeadSHA: "head",
		CoreHash: "core", PolicyHash: "policy", Release: "v1", ModelHash: "model",
	}
}

func TestAcquireAllowsOneSamplerAndJoinsDuplicates(t *testing.T) {
	store := openTestStore(t)
	now := time.Unix(1000, 0)

	first, err := store.Acquire(context.Background(), testSeed("one"), "worker-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != AcquireWon || first.Lease.Generation != 1 {
		t.Fatalf("first acquire = %#v", first)
	}

	second, err := store.Acquire(context.Background(), testSeed("one"), "worker-b", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != AcquireJoined {
		t.Fatalf("duplicate acquire = %#v", second)
	}
	if err := store.MarkProviderStarted(context.Background(), second.Lease, now); !errors.Is(err, ErrFenceLost) {
		t.Fatalf("joiner provider start = %v", err)
	}
}

func TestExpiredPreProviderLeaseCanBeStolenAndFencesOldOwner(t *testing.T) {
	store := openTestStore(t)
	now := time.Unix(2000, 0)
	first, err := store.Acquire(context.Background(), testSeed("steal"), "worker-a", now, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	second, err := store.Acquire(context.Background(), testSeed("steal"), "worker-b", now.Add(2*time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != AcquireWon || second.Lease.Generation != first.Lease.Generation+1 {
		t.Fatalf("stolen acquire = %#v", second)
	}
	if err := store.Heartbeat(context.Background(), first.Lease, now.Add(2*time.Second), time.Minute); !errors.Is(err, ErrFenceLost) {
		t.Fatalf("old heartbeat = %v", err)
	}
}

func TestExpiredPostProviderLeaseSealsErrorWithoutResampling(t *testing.T) {
	store := openTestStore(t)
	now := time.Unix(3000, 0)
	first, err := store.Acquire(context.Background(), testSeed("started"), "worker-a", now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProviderStarted(context.Background(), first.Lease, now); err != nil {
		t.Fatal(err)
	}

	second, err := store.Acquire(context.Background(), testSeed("started"), "worker-b", now.Add(2*time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != AcquireSealed || second.Record.Verdict != VerdictError {
		t.Fatalf("post-provider recovery = %#v", second)
	}
	if second.Record.Summary != "sampler lease expired after provider start" {
		t.Fatalf("summary = %q", second.Record.Summary)
	}
}

func TestSealIsImmutableAndRequiresCurrentFence(t *testing.T) {
	store := openTestStore(t)
	now := time.Unix(4000, 0)
	acquired, err := store.Acquire(context.Background(), testSeed("seal"), "worker-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	approved := TerminalResult{Verdict: VerdictApproved, Summary: "approved", FindingsJSON: []byte("[]")}
	if err := store.Seal(context.Background(), acquired.Lease, approved, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Seal(context.Background(), acquired.Lease, TerminalResult{Verdict: VerdictBlocking}, now); !errors.Is(err, ErrFenceLost) {
		t.Fatalf("second seal = %v", err)
	}
	joined, err := store.Acquire(context.Background(), testSeed("seal"), "worker-b", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if joined.State != AcquireSealed || joined.Record.Verdict != VerdictApproved {
		t.Fatalf("sealed acquire = %#v", joined)
	}
}

func TestHeadGenerationIsGlobalAcrossReviewKeys(t *testing.T) {
	store := openTestStore(t)
	head := HeadKey{InstallationID: 7, RepositoryID: 11, HeadSHA: "same", CheckName: "grok-review-shadow"}
	first, err := store.NextGeneration(context.Background(), head)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.NextGeneration(context.Background(), head)
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("generations = %d, %d", first, second)
	}
}
