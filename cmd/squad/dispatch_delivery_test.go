package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zsiec/squad/internal/repo"
)

func TestDeliveryCLIAndMCPReportSameDrift(t *testing.T) {
	env := newTestEnv(t)
	canonical, err := filepath.EvalSymlinks(env.Root)
	if err != nil {
		t.Fatal(err)
	}
	env.Root = canonical
	env.RepoID, err = repo.IDFor(canonical)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.DB.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES(?,'D','github:o/r#1',?,1,1,0,'completed',1,'native','','T')`, env.RepoID, env.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd := newDispatchDeliveryCmd()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"D"})
	if err = cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var cli map[string]any
	if err = json.Unmarshal(output.Bytes(), &cli); err != nil {
		t.Fatal(err)
	}
	if cli["issue_state"] != "unknown" {
		t.Fatalf("invented Issue state: %v", cli)
	}
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"squad_dispatch_delivery","arguments":{"reservation":"D"}}}`
	var response bytes.Buffer
	if err = runMCP(context.Background(), env.DB, env.RepoID, env.Root, strings.NewReader(request+"\n"), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.String(), `unknown`) || !strings.Contains(response.String(), `completed`) {
		t.Fatalf("MCP lost independent state: %s", response.String())
	}
	var n int
	_ = env.DB.QueryRow(`SELECT count(*) FROM messages`).Scan(&n)
	if n != 0 {
		t.Fatal("delivery query wrote state")
	}
}
func TestDeliveryInputBound(t *testing.T) {
	p := filepath.Join(t.TempDir(), "large.json")
	if err := os.WriteFile(p, bytes.Repeat([]byte(" "), 65537), 0600); err != nil {
		t.Fatal(err)
	}
	var q any
	if err := readDeliveryJSON(p, &q); err == nil {
		t.Fatal("oversized input accepted")
	}
}
