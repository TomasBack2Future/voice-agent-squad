package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zsiec/squad/internal/attest"
)

func TestMCPAttestExecutesInExplicitWorkDir(t *testing.T) {
	env := newTestEnv(t)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "worker-suite"), []byte("correct suite"), 0600); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "squad_attest", "arguments": map[string]any{"item_id": "TASK-1", "agent_id": "agent-mcp", "kind": "test", "command": "cat worker-suite", "work_dir": work}}}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, strings.NewReader(string(raw)+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	records, err := attest.New(env.DB, env.RepoID, nil).ListForItem(context.Background(), "TASK-1")
	if err != nil || len(records) != 1 || records[0].ExitCode != 0 {
		t.Fatalf("wrong evidence: %v %v %s", records, err, out.String())
	}
	data, err := os.ReadFile(records[0].OutputPath)
	if err != nil || !strings.Contains(string(data), "correct suite") {
		t.Fatalf("wrong output: %s %v", data, err)
	}
}

func TestAttestWorkDirFlagAndReviewRejection(t *testing.T) {
	cmd := newAttestCmd()
	if err := cmd.ParseFlags([]string{"--work-dir", "/owned/worktree"}); err != nil {
		t.Fatal(err)
	}
	env := newTestEnv(t)
	_, err := Attest(context.Background(), AttestArgs{DB: env.DB, RepoID: env.RepoID, AgentID: "worker", ItemID: "TASK-1", Kind: "review", WorkDir: t.TempDir()})
	if err == nil {
		t.Fatal("review ignored work_dir")
	}
}
