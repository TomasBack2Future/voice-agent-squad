package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zsiec/squad/internal/dispatch"
	"github.com/zsiec/squad/internal/repo"
)

func TestWorkerExecutionInspectProvesAbsentAndMatchesMCP(t *testing.T) {
	env := newTestEnv(t)
	root, err := repo.Discover(env.Root)
	if err != nil {
		t.Fatal(err)
	}
	env.RepoID, err = repo.IDFor(root)
	if err != nil {
		t.Fatal(err)
	}
	b := dispatch.WorkerExecutionBinding{Kind: "worker", ID: "inspect-pin", Actor: env.AgentID, Item: "BUG-001", Native: "native",
		ClaimGeneration: 1, Reservation: "D", SourceRef: "github:o/r#1", Generation: 1, Controller: "controller", ControllerNative: "controller-native", Epoch: 1, ExpiresAt: 1, ClientPID: os.Getpid(),
		ToolRuntime: dispatch.WorkerToolRuntime{Executable: "/docker", Context: "local", DaemonID: "daemon", Image: "sha256:" + strings.Repeat("a", 64)}}
	path := filepath.Join(t.TempDir(), "binding.json")
	raw, _ := json.Marshal(b)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	inspect := func() (map[string]any, error) {
		var output bytes.Buffer
		cmd := newWorkerExecutionCmd()
		cmd.SetOut(&output)
		cmd.SetArgs([]string{"inspect", "--binding", path})
		if err := cmd.Execute(); err != nil {
			return nil, err
		}
		var result map[string]any
		err := json.Unmarshal(output.Bytes(), &result)
		return result, err
	}
	got, err := inspect()
	if err != nil {
		t.Fatal("acquire-response loss needs an exact absent/readback proof:", err)
	}
	if got["status"] != "ok" || got["state"] != "absent" || got["action"] != "inspect" || got["id"] != b.ID {
		t.Fatal(got)
	}
	outer, _ := json.Marshal(map[string]any{"binding": b})
	mcp := callMCPTool(t, env, "squad_worker_execution_inspect", string(outer))
	if mcp.Error != nil || mcp.Result.IsError || mcp.Result.StructuredContent["state"] != "absent" {
		t.Fatal(mcp)
	}
	if _, err := env.DB.Exec(`INSERT INTO execution_authorizations(repo_id,id,item_id,holder,generation,binding,state,last_step,created_at,updated_at) VALUES(?,?,?,?,?,?,'active','gate-open',1,1)`, env.RepoID, b.ID, b.Item, b.Actor, b.ClaimGeneration, string(raw)); err != nil {
		t.Fatal(err)
	}
	got, err = inspect()
	if err != nil || got["state"] != "active" || got["gate"] != "gate-open" {
		t.Fatal(got, err)
	}
	mcp = callMCPTool(t, env, "squad_worker_execution_inspect", string(outer))
	if mcp.Error != nil || mcp.Result.IsError || mcp.Result.StructuredContent["state"] != got["state"] || mcp.Result.StructuredContent["gate"] != got["gate"] {
		t.Fatal(mcp, got)
	}
	b.Native = "changed"
	raw, _ = json.Marshal(b)
	_ = os.WriteFile(path, raw, 0600)
	if _, err := inspect(); err == nil {
		t.Fatal("changed immutable binding inspected")
	}
	outer, _ = json.Marshal(map[string]any{"binding": b})
	mcp = callMCPTool(t, env, "squad_worker_execution_inspect", string(outer))
	if mcp.Error == nil && !mcp.Result.IsError {
		t.Fatal("MCP inspected changed binding")
	}
}
