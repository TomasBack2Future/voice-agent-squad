package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const testToken = "client-a-token-with-more-than-32-characters"

func fixture(t *testing.T, run Runner) (Config, *Server) {
	t.Helper()
	dir := t.TempDir()
	sum := sha256.Sum256([]byte(testToken))
	c := Config{Workspace: dir, Home: filepath.Join(dir, "home"), Receipts: filepath.Join(dir, "receipts"), Clients: []Client{{ID: "a", TokenSHA256: hex.EncodeToString(sum[:]), Agent: "agent-a", Session: "native-a", Role: "worker"}}}
	s, e := NewServer(c, "test-sha", run)
	if e != nil {
		t.Fatal(e)
	}
	return c, s
}
func request(s *Server, token, id, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Idempotency-Key", id)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestAuthAndRoleDenyBeforeExecution(t *testing.T) {
	var calls atomic.Int32
	c, s := fixture(t, func(context.Context, Client, []string, []byte) (Result, error) { calls.Add(1); return Result{}, nil })
	cases := []struct {
		token, path, body string
		status            int
	}{
		{"bad", "/v1/command", `{"args":["status"]}`, 401},
		{testToken, "/v1/command", `{"args":["attest","--command","touch /tmp/escape"]}`, 403},
		{testToken, "/v1/command", `{"args":["register","--as=agent-victim"]}`, 403},
		{testToken, "/v1/command", `{"args":["dispatch","reserve","foreign"]}`, 403},
		{testToken, "/v1/command", `{"args":["claim-inspect","TASK-1","--repo=/elsewhere"]}`, 403},
		{testToken, "/v1/command", `{"args":["claim","TASK-1","--worktree"]}`, 403},
		{testToken, "/v1/command", `{"args":["done","TASK-1","--force"]}`, 403},
		{testToken, "/v1/command", `{"args":["register","--owner-pid=1"]}`, 403},
		{testToken, "/v1/command", `{"args":["heartbeat","TASK-1","--worker-session=foreign"]}`, 403},
		{testToken, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"squad_claim","arguments":{"agent_id":"victim"}}}`, 403},
		{testToken, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"squad_register","arguments":{"aſ":"victim"}}}`, 403},
		{testToken, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"squad_claim","arguments":{"AGENT_ID":"victim"}}}`, 403},
		{testToken, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"squad_release","arguments":{"item_id":"TASK-001","agent_id":"victim","agent_id":null}}}`, 403},
		{testToken, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"squad_attest","arguments":{}}}`, 403},
	}
	for i, x := range cases {
		w := request(s, x.token, fmt.Sprintf("request-%016d", i), x.path, x.body)
		if w.Code != x.status {
			t.Fatalf("case %d: %d %s", i, w.Code, w.Body)
		}
	}
	c.Clients[0].Role = "observer"
	obs, e := NewServer(c, "test", s.run)
	if e != nil {
		t.Fatal(e)
	}
	if w := request(obs, testToken, "observer-request-1", "/v1/command", `{"args":["claim","TASK-001"]}`); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized request executed")
	}
}
func TestDurableReplayAndChangedInput(t *testing.T) {
	var calls atomic.Int32
	run := func(_ context.Context, c Client, args []string, _ []byte) (Result, error) {
		calls.Add(1)
		if c.Agent != "agent-a" || args[0] != "new" {
			t.Fatal(c, args)
		}
		return Result{Stdout: "created TASK-001"}, nil
	}
	cfg, s := fixture(t, run)
	id := "same-request-123456"
	body := `{"args":["new","task","once"]}`
	first := request(s, testToken, id, "/v1/command", body)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body)
	}
	restarted, e := NewServer(cfg, "next-sha", run)
	if e != nil {
		t.Fatal(e)
	}
	again := request(restarted, testToken, id, "/v1/command", body)
	if again.Code != 200 || again.Body.String() != first.Body.String() || calls.Load() != 1 {
		t.Fatal(again.Code, again.Body, calls.Load())
	}
	changed := request(s, testToken, id, "/v1/command", `{"args":["new","task","different"]}`)
	if changed.Code != 409 || calls.Load() != 1 {
		t.Fatal(changed.Code)
	}
}
func TestInFlightAndInterruptedNeverReexecute(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	cfg, s := fixture(t, func(context.Context, Client, []string, []byte) (Result, error) {
		calls.Add(1)
		close(entered)
		<-release
		return Result{}, errors.New("process interrupted")
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	body := `{"args":["claim","TASK-001"]}`
	id := "interrupted-request-1"
	go func() { done <- request(s, testToken, id, "/v1/command", body) }()
	<-entered
	// A second instance observes the durable admission while the first runs.
	other, err := NewServer(cfg, "test", s.run)
	if err != nil {
		t.Fatal(err)
	}
	if w := request(other, testToken, id, "/v1/command", body); w.Code != 409 {
		t.Fatal(w.Code)
	}
	close(release)
	if w := <-done; w.Code != 409 {
		t.Fatal(w.Code)
	}
	restarted, e := NewServer(cfg, "test", s.run)
	if e != nil {
		t.Fatal(e)
	}
	if w := request(restarted, testToken, id, "/v1/command", body); w.Code != 409 || calls.Load() != 1 {
		t.Fatal(w.Code, calls.Load())
	}
}
func TestMCPNotificationAndPrincipal(t *testing.T) {
	var calls atomic.Int32
	_, s := fixture(t, func(_ context.Context, c Client, args []string, in []byte) (Result, error) {
		calls.Add(1)
		if c.Agent != "agent-a" || args[0] != "mcp" || !bytes.HasSuffix(in, []byte("\n")) {
			t.Fatal(c, args)
		}
		return Result{Stdout: `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`}, nil
	})
	n := request(s, testToken, "", "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if n.Code != 202 || calls.Load() != 0 {
		t.Fatal(n.Code)
	}
	w := request(s, testToken, "", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"squad_say","arguments":{"message":"hi"}}}`)
	if w.Code != 200 || calls.Load() != 1 || !json.Valid(w.Body.Bytes()) {
		t.Fatal(w.Code, w.Body)
	}
}
func TestAdmissionJournalIsPrivate(t *testing.T) {
	cfg, s := fixture(t, func(context.Context, Client, []string, []byte) (Result, error) { return Result{}, nil })
	w := request(s, testToken, "private-request-1", "/v1/command", `{"args":["status"]}`)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	paths, e := filepath.Glob(filepath.Join(cfg.Receipts, "*.json"))
	if e != nil || len(paths) != 1 {
		t.Fatal(paths, e)
	}
	info, e := os.Stat(paths[0])
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, e)
	}
}
func TestClientRejectsUnencryptedRemoteAndRedirect(t *testing.T) {
	if _, e := NewClient("http://example.com", testToken); e == nil {
		t.Fatal("insecure remote accepted")
	}
	if _, e := NewClient("https://user:password@example.com", testToken); e == nil {
		t.Fatal("userinfo accepted")
	}
	var leaked atomic.Bool
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer dest.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	c, e := NewClient(redirect.URL, testToken)
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.Command(context.Background(), []string{"status"}, "redirect-request-1")
	if e == nil || leaked.Load() {
		t.Fatal("redirect followed", e)
	}
}
