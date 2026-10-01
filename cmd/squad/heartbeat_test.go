package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/zsiec/squad/internal/repo"
)

func TestHeartbeatStructuredCLIAndMCPExactCustodyOutcomes(t *testing.T) {
	env := newTestEnv(t)
	canonical, err := repo.Discover(env.Root)
	if err != nil {
		t.Fatal(err)
	}
	env.RepoID, err = repo.IDFor(canonical)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.DB.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,canonical_item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id) VALUES(?,'D-1','BUG-1','test#1','dispatcher',1,1,9999999999,'dispatched',1,'native')`, env.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"held", "recovering", "absent"} {
		if _, err = env.DB.Exec(`DELETE FROM claims WHERE repo_id=? AND item_id='BUG-1'`, env.RepoID); err != nil {
			t.Fatal(err)
		}
		if state != "absent" {
			if _, err = env.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,generation,state) VALUES(?,'BUG-1',?,1,1,1,?)`, env.RepoID, env.AgentID, state); err != nil {
				t.Fatal(err)
			}
		}
		want := "custody-rejected"
		if state == "held" {
			want = "verified"
		}
		var out bytes.Buffer
		cmd := newHeartbeatCmd()
		cmd.SetOut(&out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"--json", "--require-primary", "--check", "--reservation", "D-1", "--generation", "1", "--worker-session", "native"})
		err = cmd.Execute()
		if (err == nil) != (state == "held") {
			t.Fatalf("state %s error %v", state, err)
		}
		var cli map[string]any
		if err = json.Unmarshal(out.Bytes(), &cli); err != nil {
			t.Fatal(err)
		}
		if cli["outcome"] != want || cli["agent"] != env.AgentID || cli["worker_session"] != "native" {
			t.Fatalf("CLI receipt %s", out.String())
		}
		response := callMCPTool(t, env, "squad_heartbeat", fmt.Sprintf(`{"agent_id":%q,"reservation":"D-1","generation":1,"worker_session":"native","check":true,"json":true,"require_primary":true}`, env.AgentID))
		if response.Result.IsError || response.Result.StructuredContent == nil {
			t.Fatalf("MCP outcome unavailable: %#v", response)
		}
		mcpReceipt := response.Result.StructuredContent
		if mcpReceipt["outcome"] != cli["outcome"] || mcpReceipt["agent"] != cli["agent"] {
			t.Fatalf("MCP/CLI mismatch: %#v %#v", mcpReceipt, cli)
		}
	}
}
