package closeout

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

const relRef = "studio-prod-v0.7.52-9f8139a1"

func releaseFixture(t *testing.T) (Engine, Request, *fakeBackend) {
	t.Helper()
	e, q, b := engineFixture(t)
	if _, err := e.DB.Exec(`UPDATE dispatch_reservations SET source_ref=?`, "release:"+relRef); err != nil {
		t.Fatal(err)
	}
	return e, q, b
}

func recordVerdict(t *testing.T, e Engine, mut func(*releaseevidence.Document)) {
	t.Helper()
	var d releaseevidence.Document
	d.Schema, d.ReleaseRef = releaseevidence.Schema, relRef
	d.Candidate.ApplicationSHA, d.Candidate.Version, d.Candidate.ManifestSHA256 = strings.Repeat("9", 40), "v0.7.52", strings.Repeat("a", 64)
	d.Execution.Result, d.Execution.Ref = "succeeded", "run/1"
	d.Acceptance = []releaseevidence.Check{{Name: "api", Result: "passed", Ref: "e/api"}}
	d.Observation = releaseevidence.Check{Result: "passed", Ref: "e/obs"}
	d.Cleanup.Result, d.Cleanup.Ref = "complete", "e/clean"
	d.Custody.Disposition, d.Custody.Ref = "none", ""
	if mut != nil {
		mut(&d)
	}
	v := releaseevidence.Evaluate(d, relRef)
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
	if _, err := e.DB.Exec(`INSERT INTO attestations(item_id,kind,command,exit_code,output_hash,output_path,created_at,agent_id,repo_id) VALUES('T','release','release-evidence',?,?,?,?,'worker','repo')`, exit, h, p, 10+int64(len(h))); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseCloseoutRefusesUntilVerdictComplete(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mut   func(*releaseevidence.Document)
		want  string
		setup bool
	}{
		{"unrecorded", nil, "not recorded", false},
		{"acceptance-incomplete", func(d *releaseevidence.Document) { d.Acceptance[0].Result = "pending" }, "partial", true},
		{"cleanup-failed", func(d *releaseevidence.Document) { d.Cleanup.Result = "failed" }, "blocked", true},
		{"custody-retained", func(d *releaseevidence.Document) {
			d.Custody.Disposition, d.Custody.Retained = "retained", []string{"ENV-002"}
		}, "custody-retained", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, q, b := releaseFixture(t)
			if tc.setup {
				recordVerdict(t, e, tc.mut)
			}
			_, err := e.Run(context.Background(), q)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
			if len(b.calls) != 0 {
				t.Fatalf("effect before evidence: %v", b.calls)
			}
			var n int
			_ = e.DB.QueryRow(`SELECT count(*) FROM messages WHERE kind='closeout'`).Scan(&n)
			if n != 0 {
				t.Fatal("receipts written for a refused release closeout")
			}
		})
	}
}

func TestReleaseCloseoutCompletesWithoutGitHubAndIsIdempotent(t *testing.T) {
	e, q, b := releaseFixture(t)
	recordVerdict(t, e, nil)
	r, err := e.Run(context.Background(), q)
	if err != nil || strings.Join(r.Steps, ",") != "item-done,outcome" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err = e.Run(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if b.comment || b.closed {
		t.Fatal("release closeout touched GitHub")
	}
	for k, n := range b.calls {
		if n != 1 {
			t.Fatalf("%s repeated %d", k, n)
		}
	}
	var n int
	_ = e.DB.QueryRow(`SELECT count(*) FROM terminal_event_receipts WHERE kind='handoff-complete'`).Scan(&n)
	var wrong int
	_ = e.DB.QueryRow(`SELECT count(*) FROM terminal_event_receipts WHERE kind='issue-closed'`).Scan(&wrong)
	if n != 1 || wrong != 0 {
		t.Fatalf("handoff-complete=%d issue-closed=%d", n, wrong)
	}
	var state string
	_ = e.DB.QueryRow(`SELECT state FROM dispatch_reservations`).Scan(&state)
	if state != "dispatched" {
		t.Fatal("Worker changed Dispatcher-owned reservation")
	}
}

func TestReleaseCloseoutResumesAfterInterruptedDone(t *testing.T) {
	e, q, b := releaseFixture(t)
	recordVerdict(t, e, nil)
	b.fail = "done"
	if _, err := e.Run(context.Background(), q); err == nil {
		t.Fatal("expected interruption")
	}
	r, err := e.Run(context.Background(), q)
	if err != nil || len(r.Steps) != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	if b.calls["done"] != 1 {
		t.Fatalf("done repeated %d", b.calls["done"])
	}
}

func TestReleaseCloseoutKeepsCustodyAndGateChecks(t *testing.T) {
	e, q, b := releaseFixture(t)
	recordVerdict(t, e, nil)
	if _, err := e.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','ENV-002','worker',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), q); err == nil || len(b.calls) != 0 {
		t.Fatalf("held ENV did not block release closeout: %v %v", err, b.calls)
	}
	e2, q2, b2 := releaseFixture(t)
	recordVerdict(t, e2, nil)
	b2.gateFailure = true
	if _, err := e2.Run(context.Background(), q2); err == nil {
		t.Fatal("done gate failure ignored")
	}
}

func TestReleaseCloseoutRejectsTamperedArtifact(t *testing.T) {
	e, q, b := releaseFixture(t)
	recordVerdict(t, e, nil)
	var p string
	_ = e.DB.QueryRow(`SELECT output_path FROM attestations`).Scan(&p)
	if err := os.WriteFile(p, []byte(`{"verdict":{"schema":"squad.release-evidence.v1","state":"complete","satisfied":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), q); err == nil || len(b.calls) != 0 {
		t.Fatalf("tampered artifact accepted: %v", err)
	}
}
