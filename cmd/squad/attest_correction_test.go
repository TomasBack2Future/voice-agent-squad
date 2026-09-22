package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zsiec/squad/internal/attest"
)

func TestEvidenceCorrectionCLIAndMCP(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bc, e := bootClaimContext(ctx)
	if e != nil {
		t.Fatal(e)
	}
	env.RepoID = bc.repoID
	bc.Close()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"attest", "TASK", "--kind", "test", "--argv", `["printf","%s","$(exit 2)"]`})
	if err := root.Execute(); err != nil {
		t.Fatalf("CLI argv: %v %s", err, out.String())
	}
	rows, err := Attestations(ctx, AttestationsArgs{DB: env.DB, RepoID: env.RepoID, ItemID: "TASK"})
	if err != nil || len(rows.Items) != 1 {
		t.Fatalf("rows %v %v output=%s repo=%s", rows, err, out.String(), env.RepoID)
	}
	id := rows.Items[0].ID
	root = newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"attest", "revoke", fmt.Sprint(id), "--reason", "not an assertion"})
	if err := root.Execute(); err != nil {
		t.Fatalf("CLI revoke: %v %s", err, out.String())
	}
	root = newRootCmd()
	out.Reset()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"attest", "list", "TASK"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var listed AttestationsResult
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil || len(listed.Items) != 1 || listed.Items[0].Revocation == nil {
		t.Fatalf("lost correction: %s %v", out.String(), err)
	}
	// MCP recording and correction use the same library/ledger contract.
	call := func(name string, args map[string]any) {
		t.Helper()
		req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
		raw, e := json.Marshal(req)
		if e != nil {
			t.Fatal(e)
		}
		out.Reset()
		if e = runMCP(ctx, env.DB, env.RepoID, env.Root, strings.NewReader(string(raw)+"\n"), &out); e != nil {
			t.Fatal(e)
		}
	}
	call("squad_attest", map[string]any{"item_id": "MCP", "kind": "test", "argv": []string{"printf", "%s", "literal ; value"}})
	records, err := attest.New(env.DB, env.RepoID, nil).ListForItem(ctx, "MCP")
	if err != nil || len(records) != 1 {
		t.Fatalf("MCP argv: %v %s", err, out.String())
	}
	call("squad_attest_revoke", map[string]any{"id": records[0].ID, "reason": "invalid acceptance"})
	records, err = attest.New(env.DB, env.RepoID, nil).ListForItem(ctx, "MCP")
	if err != nil || records[0].Revocation == nil {
		t.Fatalf("MCP revoke: %v %s", err, out.String())
	}
}

func TestDoneRejectsRevokedEvidenceAndRetainsClaim(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(env.ItemsDir, "BUG-301.md"), []byte(strings.ReplaceAll(evidenceItem, "[test, review]", "[test, lint]")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Claim(ctx, ClaimArgs{DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID, ItemID: "BUG-301", ItemsDir: env.ItemsDir, DoneDir: env.DoneDir}); err != nil {
		t.Fatal(err)
	}
	// Supply every required kind, then revoke the test evidence.
	l := attest.New(env.DB, env.RepoID, nil)
	for _, kind := range []attest.Kind{attest.KindTest, attest.KindLint} {
		r, e := l.Run(ctx, attest.RunOpts{ItemID: "BUG-301", Kind: kind, Command: "printf ok", AgentID: env.AgentID, AttDir: filepath.Join(env.Root, ".squad", "attestations")})
		if e != nil {
			t.Fatal(e)
		}
		if kind == attest.KindTest {
			if _, e = l.Revoke(ctx, r.ID, "masked failure", env.AgentID, 0); e != nil {
				t.Fatal(e)
			}
		}
	}
	_, err := Done(ctx, DoneArgs{DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID, ItemID: "BUG-301", ItemsDir: env.ItemsDir, DoneDir: env.DoneDir, RepoRoot: env.Root})
	if err == nil || !strings.Contains(err.Error(), "test") {
		t.Fatalf("done accepted revoked test: %v", err)
	}
	var n int
	if err = env.DB.QueryRow("SELECT count(*) FROM claims WHERE repo_id=? AND item_id='BUG-301'", env.RepoID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("claim lost: %d %v", n, err)
	}
}
