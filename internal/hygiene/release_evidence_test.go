package hygiene

import (
	"context"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/attest"
)

func TestDoneReleaseItemWithReleaseVerdictIsNotFlaggedMissingTest(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	l := attest.New(db, "repo", nil)
	if _, err := l.Insert(ctx, attest.Record{ItemID: "REL", Kind: attest.KindRelease, Command: "release-evidence r state=complete", OutputHash: "h", AgentID: "owner"}); err != nil {
		t.Fatal(err)
	}
	sw := NewWithClock(db, "repo", fakeItems{refs: []ItemRef{
		{ID: "REL", Status: "done", Area: "release", EvidenceRequired: []string{"test"}},
		{ID: "ENG", Status: "done", Area: "core", EvidenceRequired: []string{"test"}},
	}}, time.Now)
	findings, err := sw.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Code == "evidence_missing" && f.Message != "" && !contains(f.Message, "ENG") {
			t.Fatalf("release item flagged: %+v", f)
		}
	}
	n := 0
	for _, f := range findings {
		if f.Code == "evidence_missing" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want only the engineering item flagged, got %d: %+v", n, findings)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestHygieneUsesNewestReleaseVerdictAndReleaseAreaOnly(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	l := attest.New(db, "repo", nil)
	for i, exit := range []int{0, 1} {
		if _, err := l.Insert(ctx, attest.Record{ItemID: "REL", Kind: attest.KindRelease, Command: "release-evidence r", ExitCode: exit, OutputHash: string(rune('a' + i)), AgentID: "owner", CreatedAt: int64(100 + i)}); err != nil {
			t.Fatal(err)
		}
	}
	sw := NewWithClock(db, "repo", fakeItems{refs: []ItemRef{
		{ID: "REL", Status: "done", Area: "release", EvidenceRequired: []string{"test"}},
	}}, time.Now)
	findings, err := sw.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Code == "evidence_missing" {
			return
		}
	}
	t.Fatalf("older complete verdict outlived a newer incomplete one: %+v", findings)
}
