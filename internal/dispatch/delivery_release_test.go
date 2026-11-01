package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zsiec/squad/internal/releaseevidence"
)

const projRef = "studio-prod-v0.7.52-9f8139a1"

func releaseLedger(t *testing.T, resState, itemStatus string) *Store {
	t.Helper()
	s := stateLedger(t, resState, itemStatus)
	if _, err := s.db.Exec(`UPDATE dispatch_reservations SET source_ref=?`, "release:"+projRef); err != nil {
		t.Fatal(err)
	}
	return s
}

func recordProjection(t *testing.T, s *Store, mut func(*releaseevidence.Document)) {
	t.Helper()
	var d releaseevidence.Document
	d.Schema, d.ReleaseRef = releaseevidence.Schema, projRef
	d.Candidate.ApplicationSHA, d.Candidate.Version, d.Candidate.ManifestSHA256 = strings.Repeat("9", 40), "v0.7.52", strings.Repeat("a", 64)
	d.Execution.Result, d.Execution.Ref = "succeeded", "run/1"
	d.Acceptance = []releaseevidence.Check{{Name: "api", Result: "passed", Ref: "e/api"}}
	d.Observation = releaseevidence.Check{Result: "passed", Ref: "e/obs"}
	d.Cleanup.Result, d.Cleanup.Ref = "complete", "e/clean"
	d.Custody.Disposition = "none"
	d.Current = "reports/y4/release-checkpoint.json"
	if mut != nil {
		mut(&d)
	}
	v := releaseevidence.Evaluate(d, projRef)
	body, _ := json.Marshal(releaseevidence.Artifact{Document: d, Verdict: v})
	sum := sha256.Sum256(body)
	h := hex.EncodeToString(sum[:])
	p := filepath.Join(t.TempDir(), h+".txt")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	exit := 1
	if v.Satisfied {
		exit = 0
	}
	if _, err := s.db.Exec(`INSERT INTO attestations(item_id,kind,command,exit_code,output_hash,output_path,created_at,agent_id,repo_id) VALUES('T','release','release-evidence',?,?,?,500,'worker','repo-test')`, exit, h, p); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryProjectsReleaseEvidenceNotReservationState(t *testing.T) {
	// The reported case: reservation completed, item still open, no release evidence.
	s := releaseLedger(t, "completed", "open")
	got, err := s.Delivery(context.Background(), "D", IssueObservation{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Release == nil || got.Release.State != "unrecorded" || got.Acceptance.GoalReached || !hasDrift(got, "release-completed-without-complete-evidence") {
		t.Fatalf("%+v %+v", got.Release, got.Drift)
	}

	// Incomplete acceptance stays partial; the failed verdict is not a command result.
	recordProjection(t, s, func(d *releaseevidence.Document) { d.Acceptance[0].Result = "pending" })
	got, _ = s.Delivery(context.Background(), "D", IssueObservation{})
	if got.Release.State != "partial" || got.Acceptance.GoalReached || got.LastResult != nil {
		t.Fatalf("%+v lastResult=%v", got.Release, got.LastResult)
	}

	// Complete evidence on a still-open item is the resumable closeout position.
	recordProjection(t, s, nil)
	got, _ = s.Delivery(context.Background(), "D", IssueObservation{})
	if got.Release.State != "complete" || !got.Acceptance.GoalReached || !hasDrift(got, "release-evidence-complete-item-open") || hasDrift(got, "release-completed-without-complete-evidence") {
		t.Fatalf("%+v %+v", got.Release, got.Drift)
	}
	if got.Release.CurrentRef != "reports/y4/release-checkpoint.json" || got.Release.Candidate != strings.Repeat("9", 40) || got.Release.EvidenceHash == "" {
		t.Fatalf("missing provenance: %+v", got.Release)
	}

	// An issue reservation carries no release block.
	plain := stateLedger(t, "completed", "done")
	if g, _ := plain.Delivery(context.Background(), "D", IssueObservation{}); g.Release != nil {
		t.Fatal("release block on a non-release assignment")
	}
}
