package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/zsiec/squad/internal/dispatch"
	"github.com/zsiec/squad/internal/repo"
)

// Exercise the public command against isolated state: the existing controller
// must stay in place while a joined, consenting source Worker is replaced.
func TestControllerBoundWorkerHandoffCLI(t *testing.T) {
	env := newTestEnv(t)
	root, err := repo.Discover(env.Root)
	if err != nil {
		t.Fatal(err)
	}
	env.RepoID, err = repo.IDFor(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SQUAD_NATIVE_SESSION_ID", "controller-native")
	child := exec.Command("sh", "-c", "exit 0")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	if _, err := env.DB.Exec(`INSERT INTO dispatch_controller_bindings VALUES(?,?,?,2);
INSERT INTO dispatch_reservations(repo_id,item_id,canonical_item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note) VALUES(?,'D','T','github:o/r#1',?,1,1,0,'dispatched',1,'old-native','');
INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,state,generation) VALUES(?,'T','old-worker',2,3,'held',1);
INSERT INTO agents(id,repo_id,display_name,started_at,last_tick_at,status) VALUES('new-worker',?,'new',1,1,'active');
INSERT INTO dispatch_decisions VALUES(?,'D',1,'T',2,4,'hold','acceptance held','old-worker')`, env.RepoID, env.AgentID, "controller-native", env.RepoID, env.AgentID, env.RepoID, env.RepoID, env.RepoID); err != nil {
		t.Fatal(err)
	}
	row, err := dispatch.New(env.DB, env.RepoID, nil).Get(context.Background(), "D")
	if err != nil {
		t.Fatal(err)
	}
	binding := map[string]any{"kind": "worker", "native": "old-native", "actor": "old-worker", "item": "T", "reservation": "D", "source_ref": "github:o/r#1", "generation": 1, "claim_generation": 1, "client_pid": pid}
	bind, _ := json.Marshal(binding)
	if _, err := env.DB.Exec(`INSERT INTO execution_authorizations(repo_id,id,item_id,holder,generation,binding,state,created_at,updated_at,reconciliation) VALUES(?,'old-pin','T','old-worker',1,?,'reconciled',1,2,'verified old native/tools join')`, env.RepoID, string(bind)); err != nil {
		t.Fatal(err)
	}
	q := map[string]any{"request_id": "replace-1", "controller": map[string]any{"actor": env.AgentID, "native_session": "controller-native", "epoch": 2}, "expected": row, "claim": map[string]any{"item": "T", "actor": "old-worker", "generation": 1, "claimed_at": 2, "last_touch": 3}, "execution_id": "old-pin", "new_actor": "new-worker", "new_native": "new-native", "decision_revision": 2, "consent_outcome_id": 0, "custody_evidence": "no external mutation; retained fixture under same item"}
	raw, _ := json.Marshal(q)
	decoded, err := decodeWorkerHandoff(raw)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := dispatch.WorkerHandoffDigest(decoded)
	if err != nil {
		t.Fatal(err)
	}
	consent, _ := json.Marshal(map[string]any{"schema_version": "squad.worker-handoff-consent.v1", "request_sha256": digest})
	result, err := env.DB.Exec(`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,4,'old-worker','T','say',?,'[]','normal')`, env.RepoID, string(consent))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	q["consent_outcome_id"] = id
	raw, _ = json.Marshal(q)
	path := filepath.Join(t.TempDir(), "request.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// MCP and CLI share decoding, legitimate actor and native binding checks.
	t.Setenv("SQUAD_NATIVE_SESSION_ID", "foreign-native")
	outer, _ := json.Marshal(map[string]any{"request": q})
	denied := callMCPTool(t, env, "squad_dispatch_worker_handoff", string(outer))
	if denied.Error == nil && !denied.Result.IsError {
		t.Fatal("foreign native transferred Worker through MCP")
	}
	t.Setenv("SQUAD_NATIVE_SESSION_ID", "controller-native")
	cmd := newDispatchCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"worker-handoff", "--request", path})
	if err = cmd.Execute(); err != nil {
		t.Fatalf("joined source Worker handoff under existing controller failed: %v", err)
	}
	row, err = dispatch.New(env.DB, env.RepoID, nil).Get(context.Background(), "D")
	if err != nil || row.ReservedBy != env.AgentID || row.Generation != 2 || row.WorkerThreadID != "new-native" {
		t.Fatal(row, err)
	}
	var actor, action string
	if err = env.DB.QueryRow(`SELECT agent_id FROM claims WHERE item_id='T'`).Scan(&actor); err != nil || actor != "new-worker" {
		t.Fatal(actor, err)
	}
	if err = env.DB.QueryRow(`SELECT action FROM dispatch_decisions WHERE reservation_key='D' AND generation=2`).Scan(&action); err != nil || action != "hold" {
		t.Fatal(action, err)
	}
	retry := callMCPTool(t, env, "squad_dispatch_worker_handoff", string(outer))
	if retry.Error != nil || retry.Result.IsError {
		t.Fatal("MCP cannot reconcile same immutable receipt", retry)
	}
	got := callMCPTool(t, env, "squad_dispatch_worker_handoff_get", `{"request_id":"replace-1"}`)
	if got.Error != nil || got.Result.IsError {
		t.Fatal("immutable handoff readback failed", got)
	}
}

func TestWorkerHandoffDecoderRejectsUnknownOrUnboundedInput(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`{"agent_id":"foreign"}`), []byte(`{} {}`), bytes.Repeat([]byte(" "), 32769), []byte(`{"controller":{"impersonate":true}}`)} {
		if _, err := decodeWorkerHandoff(raw); err == nil {
			t.Fatal("unsafe request accepted")
		}
	}
}
