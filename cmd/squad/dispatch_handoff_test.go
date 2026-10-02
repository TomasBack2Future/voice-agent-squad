package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/zsiec/squad/internal/dispatch"
	"github.com/zsiec/squad/internal/repo"
)

func TestControllerHandoffCLIAndMCPUseLegitimateActor(t *testing.T) {
	env := newTestEnv(t)
	root, err := repo.Discover(env.Root)
	if err != nil {
		t.Fatal(err)
	}
	env.RepoID, err = repo.IDFor(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = env.DB.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,canonical_item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id) VALUES(?,'D-1','TASK-1','test#1',?,1,1,0,'dispatched',1,'worker-native'); INSERT INTO agents(id,repo_id,display_name,started_at,last_tick_at,status) VALUES('successor',?,'successor',1,1,'working')`, env.RepoID, env.AgentID, env.RepoID); err != nil {
		t.Fatal(err)
	}
	cmd := newDispatchCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"controller-bind", "--native-session", "old-native"})
	if err = cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var binding dispatch.ControllerBinding
	if err = json.Unmarshal(output.Bytes(), &binding); err != nil || binding.Actor != env.AgentID || binding.Epoch != 1 {
		t.Fatal(binding, err)
	}
	rows, err := dispatch.New(env.DB, env.RepoID, nil).List(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	request := dispatch.HandoffRequest{RequestID: "test-handoff", ExpectedEpoch: 1, OldNative: "old-native", NewActor: "successor", NewNative: "new-native", Reservations: rows}
	raw, _ := json.Marshal(map[string]any{"request": request})
	response := callMCPTool(t, env, "squad_dispatch_handoff", string(raw))
	if response.Error != nil || response.Result.IsError {
		t.Fatal(response)
	}
	row, err := dispatch.New(env.DB, env.RepoID, nil).Get(context.Background(), "D-1")
	if err != nil || row.ReservedBy != "successor" || row.Generation != 1 || row.WorkerThreadID != "worker-native" {
		t.Fatal(row, err)
	}
	cmd = newDispatchCmd()
	output.Reset()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"handoff-get", "--request-id", "test-handoff"})
	if err = cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var receipt dispatch.HandoffReceipt
	if err = json.Unmarshal(output.Bytes(), &receipt); err != nil || receipt.NewActor != "successor" || receipt.Epoch != 2 {
		t.Fatal(receipt, err)
	}
	response = callMCPTool(t, env, "squad_dispatch_controller_bind", `{"native_session":"old-native","expected_epoch":0}`)
	if response.Error == nil && !response.Result.IsError {
		t.Fatal("retired owner reactivated through MCP")
	}
}
