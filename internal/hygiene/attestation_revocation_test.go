package hygiene

import (
	"context"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/attest"
)

func TestSweepShowsRevocationWithoutRequiredEvidence(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	l := attest.New(db, "repo", nil)
	id, err := l.Insert(ctx, attest.Record{ItemID: "TASK", Kind: attest.KindTest, Command: "false; true", OutputHash: "hash", AgentID: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Revoke(ctx, id, "masked failure", "dispatcher", 0); err != nil {
		t.Fatal(err)
	}
	for _, required := range [][]string{nil, {"test"}} {
		sw := NewWithClock(db, "repo", fakeItems{refs: []ItemRef{{ID: "TASK", Status: "done", EvidenceRequired: required}}}, time.Now)
		findings, err := sw.Sweep(ctx)
		if err != nil {
			t.Fatal(err)
		}
		revoked, missing := 0, 0
		for _, f := range findings {
			if f.Code == "attestation_revoked" {
				revoked++
			}
			if f.Code == "evidence_missing" {
				missing++
			}
		}
		if revoked != 1 || missing != len(required) {
			t.Fatalf("invisible revoked evidence: %+v", findings)
		}
	}
	findings, err := NewWithClock(db, "other", emptyItems{}, time.Now).Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Code == "attestation_revoked" {
			t.Fatal("leaked correction across repos")
		}
	}
}
