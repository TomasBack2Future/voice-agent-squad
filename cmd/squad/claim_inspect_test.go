package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zsiec/squad/internal/repo"
	"github.com/zsiec/squad/internal/store"
)

func TestClaimInspectScopedReadOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".squad"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".squad/config.yaml"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("SQUAD_HOME", filepath.Join(t.TempDir(), "state space"))
	t.Setenv("SQUAD_NO_HYGIENE", "")
	canonical, err := repo.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := repo.IDFor(canonical)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, rid := range []string{id, "other-repo"} {
		if _, err := db.Exec(`INSERT INTO claims (repo_id,item_id,agent_id,claimed_at,last_touch,generation,state) VALUES (?, 'ENV-001', ?, 1700000000, 1700000001, 7, 'recovering')`, rid, rid); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	t.Chdir(t.TempDir())
	cmd.SetArgs([]string{"claim-inspect", "ENV-001", "--repo", root})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got ClaimInspection
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := InspectedClaim{Item: "ENV-001", Holder: id, Generation: 7, ClaimedAt: "2023-11-14T22:13:20Z", State: "recovering"}
	if got.EnvClaim == nil || *got.EnvClaim != want {
		t.Fatalf("got %s", out.String())
	}
	var touch int64
	if err := db.QueryRow(`SELECT last_touch FROM claims WHERE repo_id=?`, id).Scan(&touch); err != nil || touch != 1700000001 {
		t.Fatalf("touch=%d err=%v", touch, err)
	}
	home, _ := store.Home()
	if _, err := os.Stat(filepath.Join(home, "hygiene.lock")); !os.IsNotExist(err) {
		t.Fatalf("inspection invoked hygiene: %v", err)
	}
	missing, err := inspectClaim(context.Background(), db, id, "ENV-999")
	if err != nil || missing.EnvClaim != nil {
		t.Fatalf("missing=%v err=%v", missing, err)
	}
	raw, _ := json.Marshal(missing)
	if string(raw) != `{"env_claim":null}` {
		t.Fatal(string(raw))
	}
	if _, err := db.Exec(`DROP TABLE claims`); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectClaim(context.Background(), db, id, "ENV-001"); err == nil {
		t.Fatal("query failure became unclaimed")
	}
}

func TestClaimInspectMissingDatabaseDoesNotCreateState(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".squad"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".squad/config.yaml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	home := filepath.Join(t.TempDir(), "absent")
	t.Setenv("SQUAD_HOME", home)
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	t.Chdir(t.TempDir())
	cmd.SetArgs([]string{"claim-inspect", "ENV-001", "--repo", root})
	if err := cmd.Execute(); err == nil {
		t.Fatal("missing database accepted")
	}
	if out.Len() != 0 {
		t.Fatalf("success payload on failure: %s", out.String())
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("created state: %v", err)
	}
}

func TestMCPClaimInspect(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.DB.Exec(`INSERT INTO claims (repo_id,item_id,agent_id,claimed_at,last_touch,generation,state) VALUES (?, 'ENV-001', 'holder', 1700000000, 1700000000, 4, 'held')`, env.RepoID); err != nil {
		t.Fatal(err)
	}
	resp := callMCPTool(t, env, "squad_claim_inspect", `{"item_id":"ENV-001"}`)
	if resp.Error != nil {
		t.Fatalf("%+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result.StructuredContent)
	var got ClaimInspection
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.EnvClaim == nil || got.EnvClaim.Holder != "holder" || got.EnvClaim.Generation != 4 {
		t.Fatalf("%s", raw)
	}
}
