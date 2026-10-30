package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type atomicString struct {
	mu sync.Mutex
	v  string
}

func (a *atomicString) set(v string) { a.mu.Lock(); a.v = v; a.mu.Unlock() }
func (a *atomicString) get() string  { a.mu.Lock(); defer a.mu.Unlock(); return a.v }

// fakeGH installs a gh stand-in on PATH only; HOME and GH_CONFIG_DIR are cleared
// so each case controls exactly the environment a launchd job would see.
func fakeGH(t *testing.T, script string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "gh"), []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+"/usr/bin:/bin")
}

func TestGitHubLookupFailureClasses(t *testing.T) {
	_, _, b := fixture(t)
	for name, tc := range map[string]struct {
		script string
		path   string
		want   Class
	}{
		// launchd without HOME: gh answers "not logged in" with exit status 4.
		"credentials missing": {"echo 'To get started with GitHub CLI, please run:  gh auth login' >&2; exit 4", "", ClassCredentialMissing},
		"token rejected":      {"echo 'gh: Bad credentials (HTTP 401)' >&2; exit 1", "", ClassCredentialMissing},
		"transport":           {"echo 'error connecting to api.github.com' >&2; exit 1", "", ClassGitHubUnavailable},
		"server error":        {"echo 'gh: Server Error (HTTP 502)' >&2; exit 1", "", ClassGitHubUnavailable},
		"gh not on PATH":      {"", "/nonexistent", ClassToolMissing},
		"run absent":          {"echo 'gh: Not Found (HTTP 404)' >&2; exit 1", "", ClassRunMismatch},
	} {
		t.Run(name, func(t *testing.T) {
			fakeGH(t, tc.script)
			if tc.path != "" {
				t.Setenv("PATH", tc.path)
			}
			err := VerifyGitHubRun(context.Background(), b, 100, 1, false)
			if got := ClassOf(err); got != tc.want {
				t.Fatalf("class %q want %q (err %v)", got, tc.want, err)
			}
			// The class text is the whole message: stderr and paths never travel.
			if strings.Contains(err.Error(), "gh auth") || strings.Contains(err.Error(), "HTTP") {
				t.Fatalf("unsanitized error %q", err)
			}
		})
	}
}

func TestRunMismatchIsNotAnOutage(t *testing.T) {
	_, _, b := fixture(t)
	fakeGH(t, `echo '{"id":101,"run_attempt":1}'`)
	err := VerifyGitHubRun(context.Background(), b, 100, 1, false)
	if err == nil || ClassOf(err) != ClassRunMismatch {
		t.Fatalf("got %v class %q", err, ClassOf(err))
	}
}

func post(h http.Handler, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func classOfBody(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Class string `json:"class"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q is not a class document: %v", w.Body.String(), err)
	}
	return out.Class
}

func TestBeginReportsDistinctSanitizedClasses(t *testing.T) {
	s, _, b := fixture(t)
	token := strings.Repeat("x", 32)
	raw, _ := json.Marshal(req(b))
	verify := func(err error) http.Handler {
		return Handler(s, token, func(*http.Request, Request) error { return err })
	}
	for name, tc := range map[string]struct {
		verifier error
		status   int
		class    string
	}{
		"credentials missing": {&Error{ClassCredentialMissing}, 503, "credential_missing"},
		"tool missing":        {&Error{ClassToolMissing}, 503, "tool_missing"},
		"github unavailable":  {&Error{ClassGitHubUnavailable}, 503, "github_unavailable"},
		"run mismatch":        {&Error{ClassRunMismatch}, 403, "run_mismatch"},
		"unclassified":        {os.ErrInvalid, 403, "run_mismatch"},
	} {
		t.Run(name, func(t *testing.T) {
			w := post(verify(tc.verifier), "/v1/executions/begin", token, string(raw))
			if w.Code != tc.status || classOfBody(t, w) != tc.class {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), token) {
				t.Fatal("token echoed")
			}
		})
	}
	// An authorization or claim denial stays a 409 with its own class.
	denied := req(b)
	denied.Binding.Generation++
	body, _ := json.Marshal(denied)
	w := post(verify(nil), "/v1/executions/begin", token, string(body))
	if w.Code != 409 || classOfBody(t, w) != "authorization_rejected" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestReadinessIsReadOnlyAndSeesTheRealEnvironment(t *testing.T) {
	s, db, b := fixture(t)
	token := strings.Repeat("x", 32)
	h := HandlerWithDiagnostics(s, token, nil, func(ctx context.Context, repository string) error {
		return VerifyGitHubLookup(ctx, repository)
	})
	body := `{"repository":"` + b.Repository + `"}`

	fakeGH(t, `echo '{"total_count":0,"workflow_runs":[]}'`)
	w := post(h, "/v1/executions/readiness", token, body)
	var ready Readiness
	if err := json.Unmarshal(w.Body.Bytes(), &ready); err != nil || w.Code != 200 || !ready.Ready || ready.Service != ServiceName || ready.RepoID != "repo" {
		t.Fatalf("ready %d %s %v", w.Code, w.Body.String(), err)
	}

	// The same listener with launchd's broken environment is not ready, and the
	// verdict names the failed condition rather than a generic rejection.
	fakeGH(t, "echo 'gh auth login' >&2; exit 4")
	w = post(h, "/v1/executions/readiness", token, body)
	if w.Code != 503 || classOfBody(t, w) != "credential_missing" {
		t.Fatalf("not-ready %d %s", w.Code, w.Body.String())
	}

	// Probing never creates, activates or releases a pin and never touches claims.
	var active, authorizations int
	_ = db.QueryRow(`SELECT count(*) FROM execution_authorizations WHERE state='active'`).Scan(&active)
	_ = db.QueryRow(`SELECT count(*) FROM execution_authorizations WHERE state='authorized'`).Scan(&authorizations)
	if active != 0 || authorizations != 1 {
		t.Fatalf("probe changed the ledger: active=%d authorized=%d", active, authorizations)
	}
	if _, err := db.Exec(`DELETE FROM claims WHERE item_id='ENV-002'`); err != nil {
		t.Fatal("probe left a pin:", err)
	}

	for _, bad := range []struct{ token, body string }{{"wrong-token-wrong-token-wrong-token-", body}, {token, `{"repository":"not a repo"}`}, {token, `{"repository":"o/r","x":1}`}} {
		if w := post(h, "/v1/executions/readiness", bad.token, bad.body); w.Code == 200 {
			t.Fatalf("accepted %v", bad)
		}
	}
}

func TestReadinessReportsUnreadableLedger(t *testing.T) {
	s, db, b := fixture(t)
	token := strings.Repeat("x", 32)
	h := HandlerWithDiagnostics(s, token, nil, func(context.Context, string) error { return nil })
	_ = db.Close()
	w := post(h, "/v1/executions/readiness", token, `{"repository":"`+b.Repository+`"}`)
	if w.Code != 503 || classOfBody(t, w) != "ledger_unavailable" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestEmptyCheckIsNotReadiness(t *testing.T) {
	s, _, _ := fixture(t)
	token := strings.Repeat("x", 32)
	h := Handler(s, token, nil)
	// Handler without diagnostics cannot claim readiness; the 409 an empty /check
	// returns must never be mistaken for it.
	if w := post(h, "/v1/executions/readiness", token, `{"repository":"o/r"}`); w.Code != 503 || classOfBody(t, w) != "diagnostics_unavailable" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestListenerIdentityChallenge(t *testing.T) {
	s, _, _ := fixture(t)
	token := strings.Repeat("x", 32)
	good := httptest.NewServer(Handler(s, token, nil))
	defer good.Close()
	addr := strings.TrimPrefix(good.URL, "http://")

	if got := ProbeListener(context.Background(), addr, token, "repo"); got != ListenerSameService {
		t.Fatalf("same service reported as %q", got)
	}
	// Same service shape but another ledger or another token is a different instance.
	if got := ProbeListener(context.Background(), addr, token, "other-repo"); got != ListenerForeign {
		t.Fatalf("other ledger reported as %q", got)
	}
	if got := ProbeListener(context.Background(), addr, strings.Repeat("y", 32), "repo"); got != ListenerForeign {
		t.Fatalf("other token reported as %q", got)
	}

	// A listener that answers anything (including a perfect copy of the 401/404
	// shape) without the proof is foreign; the token is never sent to it.
	var seenAuth atomicString
	impostor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth.set(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"service":"` + ServiceName + `","proof":"00"}`))
	}))
	defer impostor.Close()
	if got := ProbeListener(context.Background(), strings.TrimPrefix(impostor.URL, "http://"), token, "repo"); got != ListenerForeign {
		t.Fatalf("impostor reported as %q", got)
	}
	if seenAuth.get() != "" {
		t.Fatal("bearer token disclosed to an unverified listener")
	}

	free, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := free.Addr().String()
	_ = free.Close()
	if got := ProbeListener(context.Background(), closed, token, "repo"); got != ListenerUnreachable {
		t.Fatalf("closed port reported as %q", got)
	}
}

func TestCheckStartupEnvironment(t *testing.T) {
	t.Run("gh missing", func(t *testing.T) {
		t.Setenv("PATH", "/nonexistent")
		if got := ClassOf(CheckStartup(context.Background())); got != ClassToolMissing {
			t.Fatalf("class %q", got)
		}
	})
	t.Run("minimal launchd environment without HOME", func(t *testing.T) {
		fakeGH(t, "echo 'gh auth login' >&2; exit 4")
		if got := ClassOf(CheckStartup(context.Background())); got != ClassCredentialMissing {
			t.Fatalf("class %q", got)
		}
	})
	t.Run("transient GitHub outage does not block start", func(t *testing.T) {
		fakeGH(t, "echo 'error connecting to api.github.com' >&2; exit 1")
		err := CheckStartup(context.Background())
		if ClassOf(err) != ClassGitHubUnavailable {
			t.Fatalf("class %q", ClassOf(err))
		}
		if StartupFatal(err) {
			t.Fatal("outage must not be fatal")
		}
	})
	t.Run("ready", func(t *testing.T) {
		fakeGH(t, "exit 0")
		if err := CheckStartup(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("fatal classes", func(t *testing.T) {
		if !StartupFatal(&Error{ClassToolMissing}) || !StartupFatal(&Error{ClassCredentialMissing}) {
			t.Fatal("local misconfiguration must stop the service")
		}
	})
}
