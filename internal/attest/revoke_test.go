package attest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zsiec/squad/internal/store"
)

func TestRevocationGatesAuditAndReplacement(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l := New(db, "repo", nil)
	run := func(command string) Record {
		t.Helper()
		r, e := l.Run(ctx, RunOpts{ItemID: "TASK", Kind: KindTest, Command: command, AgentID: "worker", AttDir: dir})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	// Reproduce the incident: a failed substitution is hidden by outer printf.
	bad := run(`printf 'VERIFICATION OK %s\n' "$(exit 2)"`)
	if bad.ExitCode != 0 {
		t.Fatal("reproduction must show shell false positive")
	}
	good := run(`printf 'PASS all assertions\n'`)
	for _, repo := range []string{"", "other"} {
		if _, e := New(db, repo, nil).Revoke(ctx, bad.ID, "wrong shell", "dispatcher", good.ID); e == nil {
			t.Fatal("cross-repo correction accepted")
		}
	}
	correction, err := l.Revoke(ctx, bad.ID, "wrong shell", "dispatcher", good.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := l.Revoke(ctx, bad.ID, "wrong shell", "dispatcher", good.ID)
	if err != nil || *again != *correction {
		t.Fatalf("not idempotent %v", err)
	}
	if _, err = l.Revoke(ctx, bad.ID, "changed history", "dispatcher", good.ID); err == nil {
		t.Fatal("correction overwritten")
	}
	records, err := l.ListForItem(ctx, "TASK")
	if err != nil || len(records) != 2 || records[0].ExitCode != 0 || records[0].Revocation == nil || records[0].Revocation.ReplacementID != good.ID {
		t.Fatalf("lost audit: %+v %v", records, err)
	}
	if _, err = os.Stat(bad.OutputPath); err != nil {
		t.Fatal("original artifact deleted")
	}
	missing, err := l.MissingKinds(ctx, "TASK", []Kind{KindTest})
	if err != nil || len(missing) != 0 {
		t.Fatalf("valid replacement lost: %v %v", missing, err)
	}
	if _, err = l.Revoke(ctx, good.ID, "replacement also invalid", "dispatcher", 0); err != nil {
		t.Fatal(err)
	}
	missing, err = l.MissingKinds(ctx, "TASK", []Kind{KindTest})
	if err != nil || len(missing) != 1 {
		t.Fatalf("revoked evidence still counted: %v %v", missing, err)
	}
	if err = os.Remove(bad.OutputPath); err != nil {
		t.Fatal(err)
	}
	if err = l.Verify(ctx, "TASK"); err != nil {
		t.Fatalf("revoked artifact blocked active verification: %v", err)
	}
	if _, err = l.Insert(ctx, bad); err == nil {
		t.Fatal("dedupe resurrected revoked evidence")
	}
}

func TestRevocationRejectsInvalidReplacementsAndRaces(t *testing.T) {
	ctx := context.Background()
	db, e := store.Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	l := New(db, "repo", nil)
	add := func(repo, item string, kind Kind, code int) int64 {
		t.Helper()
		id, err := New(db, repo, nil).Insert(ctx, Record{ItemID: item, Kind: kind, ExitCode: code, Command: "review by reviewer", OutputHash: fmt.Sprintf("%s-%s-%s-%d", repo, item, kind, code), AgentID: "worker"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	old := add("repo", "TASK", KindReview, 0)
	for _, id := range []int64{old, old + 999, add("other", "TASK", KindReview, 0), add("repo", "OTHER", KindReview, 0), add("repo", "TASK", KindTest, 0), add("repo", "TASK", KindReview, 1)} {
		if _, err := l.Revoke(ctx, old, "bad", "actor", id); err == nil {
			t.Fatalf("accepted invalid replacement %d", id)
		}
	}
	if _, err := l.Revoke(ctx, old, " ", "actor", 0); err == nil {
		t.Fatal("blank reason accepted")
	}
	revokedReplacement, err := l.Insert(ctx, Record{ItemID: "TASK", Kind: KindReview, Command: "review by second", OutputHash: "second", AgentID: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Revoke(ctx, revokedReplacement, "bad replacement", "actor", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Revoke(ctx, old, "bad", "actor", revokedReplacement); err == nil {
		t.Fatal("revoked replacement accepted")
	}

	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := l.Revoke(ctx, old, fmt.Sprintf("reason-%d", i), "actor", 0); err == nil {
				successes.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("%d competing corrections won", successes.Load())
	}
	reviewers, err := l.DistinctReviewers(ctx, "TASK")
	if err != nil || len(reviewers) != 0 {
		t.Fatalf("revoked reviewer still counted: %v %v", reviewers, err)
	}
}

func TestArgvDoesNotExpandShellAndPropagatesScriptFailure(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, e := store.Open(filepath.Join(dir, "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	l := New(db, "repo", nil)
	opts := RunOpts{ItemID: "TASK", Kind: KindTest, AgentID: "worker", AttDir: dir, WorkDir: dir, Argv: []string{"printf", "%s", "$(exit 2) 'quoted' ; false"}}
	r, e := l.Run(ctx, opts)
	if e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(r.OutputPath)
	if e != nil || !strings.Contains(string(data), opts.Argv[2]) {
		t.Fatalf("argv expanded: %s %v", data, e)
	}
	script := filepath.Join(dir, "acceptance script.sh")
	if e = os.WriteFile(script, []byte("#!/bin/sh\nexit 2\n"), 0600); e != nil {
		t.Fatal(e)
	}
	opts.Argv = []string{"sh", script}
	r, e = l.Run(ctx, opts)
	if e != nil || r.ExitCode != 2 {
		t.Fatalf("lost failure: %+v %v", r, e)
	}
	opts.Command = "true"
	if _, e = l.Run(ctx, opts); e == nil {
		t.Fatal("command and argv both accepted")
	}
}
