package execution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/store"
)

func fixture(t *testing.T) (*Store, *sql.DB, Binding) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().Unix()
	s := New(db, "repo", func() time.Time { return time.Unix(now, 0) })
	_, err = db.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','ENV-002','holder',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{ID: "release-1", ItemID: "ENV-002", Holder: "holder", Generation: 1, Scope: "*", ManifestSHA256: strings.Repeat("a", 64), Repository: "owner/repo", ApplicationSHA: strings.Repeat("b", 40), WorkflowSHA: strings.Repeat("c", 40), WorkflowPath: ".github/workflows/deploy-go-uap-production.yml", Operation: "candidate-upgrade", ExpiresAt: now + 3600, ApprovalRef: "user-authorized-release", Target: map[string]string{"environment": "production", "cluster_context": "prod", "namespace": "studio-prod", "public_host": "studio.example", "go_release": "studio-go-production-candidate", "frontend_release": "studio-frontend-production-candidate"}}
	if err := s.Authorize(context.Background(), b, "holder"); err != nil {
		t.Fatal(err)
	}
	return s, db, b
}
func req(b Binding) Request { return Request{b, 100, 1, "rollout"} }
func TestBindingAndReplay(t *testing.T) {
	for _, mutation := range []string{"holder", "generation", "manifest", "target", "sha", "operation", "expired", "attempt", "missing-begin"} {
		t.Run(mutation, func(t *testing.T) {
			s, _, b := fixture(t)
			r := req(b)
			begin := true
			switch mutation {
			case "holder":
				r.Binding.Holder = "another"
			case "generation":
				r.Binding.Generation++
			case "manifest":
				r.Binding.ManifestSHA256 = strings.Repeat("d", 64)
			case "target":
				r.Binding.Target["namespace"] = "other"
			case "sha":
				r.Binding.ApplicationSHA = strings.Repeat("d", 40)
			case "operation":
				r.Binding.Operation = "cutover"
			case "expired":
				s.now = func() time.Time { return time.Unix(b.ExpiresAt, 0) }
			case "attempt":
				r.RunAttempt = 2
			case "missing-begin":
				begin = false
			}
			if _, err := s.Admit(context.Background(), r, begin); err == nil {
				t.Fatal("admitted invalid binding")
			}
		})
	}
	s, _, b := fixture(t)
	r := req(b)
	ctx := context.Background()
	if _, err := s.Admit(ctx, r, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(ctx, r, true); err != nil {
		t.Fatal("same run should be idempotent", err)
	}
	r.RunID++
	if _, err := s.Admit(ctx, r, true); err == nil {
		t.Fatal("replayed another run")
	}
}
func TestPinGuardsLegacyPathsAndExpiry(t *testing.T) {
	for _, statement := range []string{
		`DELETE FROM claims WHERE item_id='ENV-002'`,
		`UPDATE claims SET agent_id='other',generation=generation+1 WHERE item_id='ENV-002'`,
		`UPDATE claims SET resource_scope='studio' WHERE item_id='ENV-002'`,
		`UPDATE claims SET state='recovering' WHERE item_id='ENV-002'`,
		`INSERT OR REPLACE INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','ENV-002','other',2,2)`,
	} {
		t.Run(statement, func(t *testing.T) {
			s, db, b := fixture(t)
			if _, err := s.Admit(context.Background(), req(b), true); err != nil {
				t.Fatal(err)
			}
			s.now = func() time.Time { return time.Unix(b.ExpiresAt+100, 0) }
			if _, err := s.Admit(context.Background(), req(b), false); err == nil {
				t.Fatal("expired write admitted")
			}
			if _, err := db.Exec(statement); err == nil {
				t.Fatal("active expired pin bypassed")
			}
			if _, err := db.Exec(`UPDATE claims SET last_touch=42 WHERE item_id='ENV-002'`); err != nil {
				t.Fatal("heartbeat blocked", err)
			}
		})
	}
}
func TestReleaseReclaimRevokesPermit(t *testing.T) {
	for _, replace := range []bool{false, true} {
		s, db, b := fixture(t)
		if !replace {
			if _, err := db.Exec(`DELETE FROM claims WHERE item_id='ENV-002'`); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`INSERT OR REPLACE INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','ENV-002','holder',1,1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Admit(context.Background(), req(b), true); err == nil {
			t.Fatal("ABA permit reused")
		}
	}
}
func TestConcurrentBeginAndRelease(t *testing.T) {
	s, db, b := fixture(t)
	ctx := context.Background()
	var winners atomic.Int64
	var wg sync.WaitGroup
	for i := int64(100); i < 108; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			r := req(b)
			r.RunID = id
			if _, err := s.Admit(ctx, r, true); err == nil {
				winners.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("winners %d", winners.Load())
	}
	if _, err := db.Exec(`DELETE FROM claims WHERE item_id='ENV-002'`); err == nil {
		t.Fatal("release bypass")
	}
}
func TestReconcileRequiresExactStoppedRun(t *testing.T) {
	s, db, b := fixture(t)
	ctx := context.Background()
	if _, err := s.Admit(ctx, req(b), true); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		actor   string
		run     int64
		stopped bool
	}{{"holder", 100, false}, {"other", 100, true}, {"holder", 101, true}} {
		if err := s.Reconcile(ctx, b.ID, r.actor, "recovered", r.run, 1, r.stopped); err == nil {
			t.Fatal("bad reconciliation accepted")
		}
	}
	if err := s.Reconcile(ctx, b.ID, "holder", "verified terminal run and safe cluster", 100, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(ctx, req(b), true); err == nil {
		t.Fatal("reconciled replay")
	}
	if _, err := db.Exec(`DELETE FROM claims WHERE item_id='ENV-002'`); err != nil {
		t.Fatal(err)
	}
}
func TestHTTPAuthStrictnessAndNoRemoteUnpin(t *testing.T) {
	s, db, b := fixture(t)
	token := strings.Repeat("x", 32)
	h := Handler(s, token, func(*http.Request, Request) error { return nil })
	raw, _ := json.Marshal(req(b))
	for _, tc := range []struct {
		path, token, body string
		status            int
	}{{"begin", "bad", string(raw), 401}, {"finish", token, string(raw), 404}, {"begin", token, string(raw) + " {}", 400}, {"begin", token, `{"unexpected":true}`, 400}, {"begin", token, string(raw), 200}, {"check", token, string(raw), 200}} {
		r := httptest.NewRequest("POST", "/v1/executions/"+tc.path, bytes.NewBufferString(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s got %d want %d: %s", tc.path, w.Code, tc.status, w.Body.String())
		}
	}
	// Losing the response/connection never releases the operation.
	if _, err := db.Exec(`DELETE FROM claims WHERE item_id='ENV-002'`); err == nil {
		t.Fatal("HTTP pin lost")
	}
}

func TestGitHubIdentityCannotBeSuppliedByRunner(t *testing.T) {
	_, _, b := fixture(t)
	directory := t.TempDir()
	file := filepath.Join(directory, "run.json")
	if err := os.WriteFile(filepath.Join(directory, "gh"), []byte("#!/bin/sh\ncat \"$TEST_RUN_FILE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_RUN_FILE", file)
	for _, bad := range []string{"", "sha", "repository", "attempt", "status", "path", "event"} {
		run := map[string]any{"id": 100, "run_attempt": 1, "head_sha": b.WorkflowSHA, "path": b.WorkflowPath, "event": "workflow_dispatch", "status": "in_progress", "repository": map[string]string{"full_name": b.Repository}}
		switch bad {
		case "sha":
			run["head_sha"] = strings.Repeat("d", 40)
		case "repository":
			run["repository"] = map[string]string{"full_name": "other/repo"}
		case "attempt":
			run["run_attempt"] = 2
		case "status":
			run["status"] = "completed"
		case "path":
			run["path"] = "other.yml"
		case "event":
			run["event"] = "push"
		}
		raw, _ := json.Marshal(run)
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		err := VerifyGitHubRun(context.Background(), b, 100, 1, false)
		if (err == nil) != (bad == "") {
			t.Fatalf("%s: %v", bad, err)
		}
		if bad == "status" {
			if err := VerifyGitHubRun(context.Background(), b, 100, 1, true); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestBeginRacesReleaseWithoutGap(t *testing.T) {
	for i := 0; i < 8; i++ {
		s, db, b := fixture(t)
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-start; _, err := s.Admit(context.Background(), req(b), true); results <- err }()
		go func() { <-start; _, err := db.Exec(`DELETE FROM claims WHERE item_id='ENV-002'`); results <- err }()
		close(start)
		a, c := <-results, <-results
		if (a == nil) == (c == nil) {
			t.Fatalf("exactly one transition must win: %v, %v", a, c)
		}
	}
}

func TestTerminalReconcileAfterRejectedRerun(t *testing.T) {
	_, _, b := fixture(t)
	directory := t.TempDir()
	script := "#!/bin/sh\ncase \"$2\" in */attempts/1) cat \"$TEST_RUN_DIR/original\" ;; *) cat \"$TEST_RUN_DIR/latest\" ;; esac\n"
	if err := os.WriteFile(filepath.Join(directory, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_RUN_DIR", directory)
	original := map[string]any{"id": 100, "run_attempt": 1, "head_sha": b.WorkflowSHA, "path": b.WorkflowPath, "event": "workflow_dispatch", "status": "completed", "repository": map[string]string{"full_name": b.Repository}}
	raw, _ := json.Marshal(original)
	if err := os.WriteFile(filepath.Join(directory, "original"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"in_progress", "completed"} {
		original["run_attempt"] = 2
		original["status"] = status
		raw, _ = json.Marshal(original)
		if err := os.WriteFile(filepath.Join(directory, "latest"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		err := VerifyGitHubRun(context.Background(), b, 100, 1, true)
		if (err == nil) != (status == "completed") {
			t.Fatalf("%s: %v", status, err)
		}
	}
}
