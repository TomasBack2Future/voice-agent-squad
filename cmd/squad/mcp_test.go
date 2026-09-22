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
	"time"

	"github.com/zsiec/squad/internal/items"
)

func TestMCP_ListsAllTools(t *testing.T) {
	env := newTestEnv(t)

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 responses, got %d:\n%s", len(lines), out.String())
	}
	var toolsResp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &toolsResp); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	want := []string{
		"squad_register", "squad_whoami", "squad_next", "squad_claim",
		"squad_release", "squad_done", "squad_blocked", "squad_say",
		"squad_ask", "squad_tick", "squad_progress", "squad_review_request",
		"squad_list_items", "squad_get_item",
		"squad_attest", "squad_attestations", "squad_attest_revoke",
		"squad_learning_propose", "squad_learning_quick", "squad_learning_list", "squad_learning_approve", "squad_learning_reject",
		"squad_learning_agents_md_suggest", "squad_learning_agents_md_approve", "squad_learning_agents_md_reject",
		"squad_handoff",
		"squad_force_release", "squad_reassign", "squad_archive",
		"squad_history", "squad_who", "squad_status",
		"squad_touch", "squad_untouch", "squad_touches_list_others",
		"squad_pr_link", "squad_pr_close",
		"squad_doctor", "squad_stats",
		"squad_ready", "squad_analyze",
		"squad_standup",
		"squad_auto_refine_apply",
	}
	have := map[string]bool{}
	for _, tt := range toolsResp.Result.Tools {
		have[tt.Name] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("missing tool: %s", w)
		}
	}
}

func TestMCP_ClaimRoundTrip(t *testing.T) {
	env := newTestEnv(t)

	mustWriteItem(t, env.Root, "BUG-001", "broken")

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_claim","arguments":{"item_id":"BUG-001","intent":"fix it","agent_id":"agent-mcp"}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	var resp struct {
		Result struct {
			StructuredContent struct {
				ItemID  string `json:"item_id"`
				AgentID string `json:"agent_id"`
			} `json:"structuredContent"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: %+v (line: %s)", resp.Error, lines[1])
	}
	if resp.Result.IsError {
		t.Fatalf("tool error: %s", lines[1])
	}
	if resp.Result.StructuredContent.ItemID != "BUG-001" {
		t.Errorf("item_id: %q", resp.Result.StructuredContent.ItemID)
	}
}

func TestMCP_NextOnEmptyQueueReturnsEmpty(t *testing.T) {
	env := newTestEnv(t)
	// setupSquadRepo seeds EXAMPLE-001 as a tutorial item; remove it so the
	// queue is genuinely empty for this regression test.
	_ = os.Remove(filepath.Join(env.ItemsDir, "EXAMPLE-001-try-the-loop.md"))

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_next","arguments":{}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines:\n%s", len(lines), out.String())
	}
	var resp struct {
		Result struct {
			StructuredContent struct {
				Items []map[string]any `json:"items"`
				Total int              `json:"total"`
			} `json:"structuredContent"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, lines[1])
	}
	if resp.Error != nil {
		t.Fatalf("rpc error on empty queue: code=%d msg=%q", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result.IsError {
		t.Fatalf("tool error on empty queue: %s", lines[1])
	}
	if resp.Result.StructuredContent.Total != 0 {
		t.Fatalf("total = %d, want 0", resp.Result.StructuredContent.Total)
	}
	if len(resp.Result.StructuredContent.Items) != 0 {
		t.Fatalf("items len = %d, want 0", len(resp.Result.StructuredContent.Items))
	}
}

// TestMCP_WhoamiDoesNotLeakOtherRepoClaim guards a cross-repo isolation bug:
// when an agent holds a claim in repo A and runs an MCP whoami from repo B
// (or from a directory with no .squad/ at all), the response must not echo
// the foreign claim. Previously the SELECT filtered by agent_id only and
// happily returned a claim row tied to a different repo_id.
func TestMCP_WhoamiDoesNotLeakOtherRepoClaim(t *testing.T) {
	env := newTestEnv(t)

	// Plant a claim in env.RepoID (repo A).
	if _, err := env.DB.Exec(`
		INSERT INTO claims (repo_id, item_id, agent_id, claimed_at, last_touch, intent, long)
		VALUES (?, ?, ?, ?, ?, ?, 0)`,
		env.RepoID, "BUG-foreign", env.AgentID, 1700000000, 1700000000, "foreign work"); err != nil {
		t.Fatalf("plant claim: %v", err)
	}

	// Run MCP with empty repoID/repoRoot — i.e. invoked from a directory
	// with no .squad/. The agent identity still resolves, but no per-repo
	// state should bleed into the response.
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_whoami","arguments":{}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, "", "", in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines:\n%s", len(lines), out.String())
	}
	var resp struct {
		Result struct {
			StructuredContent struct {
				AgentID string `json:"id"`
				ItemID  string `json:"item_id"`
				Intent  string `json:"intent"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, lines[1])
	}
	if resp.Result.StructuredContent.AgentID == "" {
		t.Fatalf("expected agent id; got %q", resp.Result.StructuredContent.AgentID)
	}
	if resp.Result.StructuredContent.ItemID != "" {
		t.Errorf("cross-repo claim leak: item_id = %q (must be empty when repo not discovered)",
			resp.Result.StructuredContent.ItemID)
	}
	if resp.Result.StructuredContent.Intent != "" {
		t.Errorf("cross-repo claim leak: intent = %q", resp.Result.StructuredContent.Intent)
	}
}

// TestMCP_WhoamiInDifferentRepoIsolatesClaim plants a claim in repo A and
// invokes MCP with repo B's id; the response must show no claim — the
// caller's repo doesn't hold one.
func TestMCP_WhoamiInDifferentRepoIsolatesClaim(t *testing.T) {
	env := newTestEnv(t)

	if _, err := env.DB.Exec(`
		INSERT INTO claims (repo_id, item_id, agent_id, claimed_at, last_touch, intent, long)
		VALUES (?, ?, ?, ?, ?, ?, 0)`,
		env.RepoID, "BUG-foreign", env.AgentID, 1700000000, 1700000000, "foreign work"); err != nil {
		t.Fatalf("plant claim: %v", err)
	}

	otherRepo := "repo-other-not-the-one-with-the-claim"
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_whoami","arguments":{}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, otherRepo, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var resp struct {
		Result struct {
			StructuredContent struct {
				ItemID string `json:"item_id"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, lines[1])
	}
	if resp.Result.StructuredContent.ItemID != "" {
		t.Errorf("cross-repo claim leak: item_id = %q (claim is in %q, not %q)",
			resp.Result.StructuredContent.ItemID, env.RepoID, otherRepo)
	}
}

// TestMCP_ClaimResponseCarriesTipsForHighStakesItem exercises the parity fix:
// MCP-using agents must see the same nudges the cobra CLI prints to stderr.
// A P1 multi-AC item should get all three claim-time tips; a P3 single-AC
// item gets none (the optional field is omitted from JSON).
func TestMCP_ClaimResponseCarriesTipsForHighStakesItem(t *testing.T) {
	env := newTestEnv(t)
	t.Setenv("SQUAD_NO_CADENCE_NUDGES", "")

	mustWriteItemDetailed(t, env.Root, "BUG-100", itemSpec{
		Title: "high-stakes", Type: "bug", Priority: "P1", AC: 3,
	})

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_claim","arguments":{"item_id":"BUG-100","intent":"fix it","agent_id":"agent-mcp-tips"}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines:\n%s", len(lines), out.String())
	}
	var resp struct {
		Result struct {
			StructuredContent struct {
				ItemID string   `json:"item_id"`
				Tips   []string `json:"tips"`
			} `json:"structuredContent"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, lines[1])
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: code=%d msg=%q", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result.IsError {
		t.Fatalf("tool error: %s", lines[1])
	}
	if resp.Result.StructuredContent.ItemID != "BUG-100" {
		t.Fatalf("item_id = %q, want BUG-100", resp.Result.StructuredContent.ItemID)
	}
	tips := resp.Result.StructuredContent.Tips
	if len(tips) != 3 {
		t.Fatalf("tips len = %d, want 3 (cadence + second-opinion + milestone-target); raw=%s", len(tips), lines[1])
	}
	if !strings.Contains(tips[0], "squad thinking") {
		t.Errorf("tips[0] should be the cadence claim nudge, got %q", tips[0])
	}
	if !strings.Contains(tips[1], "squad ask @") {
		t.Errorf("tips[1] should be the second-opinion nudge, got %q", tips[1])
	}
	if !strings.Contains(tips[2], "3 AC") {
		t.Errorf("tips[2] should be the milestone-target nudge naming the AC total, got %q", tips[2])
	}
}

func TestMCP_ClaimResponseOmitsTipsForLowStakesItem(t *testing.T) {
	env := newTestEnv(t)
	t.Setenv("SQUAD_NO_CADENCE_NUDGES", "")

	// P3 single-AC: cadence still fires, but second-opinion and
	// milestone-target both stay quiet — so the tips array has exactly the
	// claim cadence line.
	mustWriteItemDetailed(t, env.Root, "BUG-101", itemSpec{
		Title: "low-stakes", Type: "bug", Priority: "P3", AC: 1,
	})

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_claim","arguments":{"item_id":"BUG-101","intent":"trivial","agent_id":"agent-mcp-low"}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var resp struct {
		Result struct {
			StructuredContent struct {
				ItemID string   `json:"item_id"`
				Tips   []string `json:"tips"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, lines[1])
	}
	tips := resp.Result.StructuredContent.Tips
	if len(tips) != 1 {
		t.Fatalf("tips len = %d, want 1 (cadence only); raw=%s", len(tips), lines[1])
	}
	if !strings.Contains(tips[0], "squad thinking") {
		t.Errorf("tips[0] should be the cadence claim nudge, got %q", tips[0])
	}
	// Verify the JSON wire actually omits the field when nil — assert on
	// the raw bytes that "tips" appears (because it's populated here),
	// then assert on a separate suppressed call that it does NOT appear.
	if !strings.Contains(lines[1], `"tips"`) {
		t.Errorf("expected tips field in JSON output, got: %s", lines[1])
	}
}

// TestMCP_LearningQuickRoundTrip exercises the MCP-side parity for
// `squad learning quick` — agents using MCP tools (per BUG-019 lineage) must
// see the same follow-up nudge the cobra path writes to stderr, surfaced as
// the Tips slice on the structured response.
func TestMCP_LearningQuickRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	t.Setenv("SQUAD_NO_CADENCE_NUDGES", "")

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_learning_quick","arguments":{"one_liner":"interface{} in claims store breaks Go 1.25","agent_id":"agent-mcp-quick"}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines:\n%s", len(lines), out.String())
	}
	var resp struct {
		Result struct {
			StructuredContent struct {
				Path string   `json:"path"`
				Tips []string `json:"tips"`
			} `json:"structuredContent"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, lines[1])
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: code=%d msg=%q", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result.IsError {
		t.Fatalf("tool error: %s", lines[1])
	}
	wantSlug := "interface-in-claims-store-breaks-go-1-25"
	if !strings.Contains(resp.Result.StructuredContent.Path, wantSlug) {
		t.Errorf("path = %q, want it to contain %q", resp.Result.StructuredContent.Path, wantSlug)
	}
	if got, err := os.ReadFile(resp.Result.StructuredContent.Path); err != nil {
		t.Fatalf("stub not on disk: %v", err)
	} else if !strings.Contains(string(got), "> captured via squad learning quick") {
		t.Errorf("stub missing via marker:\n%s", got)
	}
	tips := resp.Result.StructuredContent.Tips
	if len(tips) != 1 || !strings.Contains(tips[0], "edit the stub") {
		t.Errorf("tips = %v, want one line mentioning 'edit the stub'", tips)
	}
}

func TestMCP_LearningQuickRoundTripSilenced(t *testing.T) {
	env := newTestEnv(t)
	t.Setenv("SQUAD_NO_CADENCE_NUDGES", "1")

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_learning_quick","arguments":{"one_liner":"silent capture path","agent_id":"agent-silent-quick"}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if strings.Contains(lines[1], `"tips"`) {
		t.Errorf("env=1 should suppress tips entirely (json:omitempty), got: %s", lines[1])
	}
}

func TestMCP_ClaimResponseOmitsTipsWhenSilenced(t *testing.T) {
	env := newTestEnv(t)
	t.Setenv("SQUAD_NO_CADENCE_NUDGES", "1")

	mustWriteItemDetailed(t, env.Root, "BUG-102", itemSpec{
		Title: "silenced", Type: "bug", Priority: "P1", AC: 3,
	})

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_claim","arguments":{"item_id":"BUG-102","intent":"silenced","agent_id":"agent-silenced"}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if strings.Contains(lines[1], `"tips"`) {
		t.Errorf("env=1 should suppress tips entirely (json:omitempty), got: %s", lines[1])
	}
}

func TestMCP_DoneResponseCarriesTipForBugItem(t *testing.T) {
	env := newTestEnv(t)
	t.Setenv("SQUAD_NO_CADENCE_NUDGES", "")

	mustWriteItemDetailed(t, env.Root, "BUG-110", itemSpec{
		Title: "ship it", Type: "bug", Priority: "P2", AC: 1,
	})
	// Plant a claim so squad_done has something to release.
	if _, err := env.DB.Exec(`
		INSERT INTO claims (repo_id, item_id, agent_id, claimed_at, last_touch, intent, long)
		VALUES (?, ?, ?, ?, ?, ?, 0)`,
		env.RepoID, "BUG-110", "agent-mcp-done", 1700000000, 1700000000, "fix"); err != nil {
		t.Fatalf("plant claim: %v", err)
	}

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_done","arguments":{"item_id":"BUG-110","summary":"fixed","agent_id":"agent-mcp-done","force":true}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var resp struct {
		Result struct {
			StructuredContent struct {
				ItemID string   `json:"item_id"`
				Tips   []string `json:"tips"`
			} `json:"structuredContent"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, lines[1])
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: code=%d msg=%q", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result.IsError {
		t.Fatalf("tool error: %s", lines[1])
	}
	if len(resp.Result.StructuredContent.Tips) != 1 {
		t.Fatalf("tips len = %d, want 1 (bug-typed done nudge); raw=%s",
			len(resp.Result.StructuredContent.Tips), lines[1])
	}
	if !strings.Contains(resp.Result.StructuredContent.Tips[0], "gotcha") {
		t.Errorf("done tip should be the bug-typed gotcha nudge, got %q",
			resp.Result.StructuredContent.Tips[0])
	}
}

type itemSpec struct {
	Title    string
	Type     string
	Priority string
	Risk     string
	AC       int
}

func mustWriteItemDetailed(t *testing.T, repoRoot, id string, spec itemSpec) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".squad", "items")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("id: " + id + "\n")
	b.WriteString("title: " + spec.Title + "\n")
	if spec.Type != "" {
		b.WriteString("type: " + spec.Type + "\n")
	}
	if spec.Priority != "" {
		b.WriteString("priority: " + spec.Priority + "\n")
	}
	if spec.Risk != "" {
		b.WriteString("risk: " + spec.Risk + "\n")
	}
	b.WriteString("status: ready\n")
	b.WriteString("created: 2026-04-25\n")
	b.WriteString("updated: 2026-04-25\n")
	b.WriteString("---\n\n## Acceptance criteria\n")
	for i := 0; i < spec.AC; i++ {
		b.WriteString("- [ ] step\n")
	}
	path := filepath.Join(dir, id+".md")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustWriteItem(t *testing.T, repoRoot, id, title string) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".squad", "items")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\n" +
		"id: " + id + "\n" +
		"title: " + title + "\n" +
		"status: ready\n" +
		"created: 2026-04-25\n" +
		"updated: 2026-04-25\n" +
		"---\n\n## Problem\n" + title + "\n"
	path := filepath.Join(dir, id+".md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMCP_NoRepoReturnsInvalidParams covers the JSON-RPC error mapping for
// the "no .squad/ here" condition. Per the spec, -32602 (Invalid params) is
// the right code: the call's arguments imply a context (a repo) that does
// not exist. Previously every per-repo handler returned the catch-all
// -32603 (Internal error), which is what JSON-RPC reserves for unexpected
// server-side faults — the opposite of a user-input issue.
func TestMCP_NoRepoReturnsInvalidParams(t *testing.T) {
	env := newTestEnv(t)

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_next","arguments":{}}}` + "\n")
	var out bytes.Buffer
	// Call with empty repoID/repoRoot to simulate a non-squad working dir.
	if err := runMCP(context.Background(), env.DB, "", "", in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines:\n%s", len(lines), out.String())
	}
	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, lines[1])
	}
	if resp.Error == nil {
		t.Fatalf("expected an error on no-repo call; got: %s", lines[1])
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602 (Invalid params); message=%q",
			resp.Error.Code, resp.Error.Message)
	}
}

// TestMCP_DoctorRoundTrip ensures squad_doctor is registered and returns
// a structured findings list. README documents this tool but it was missing
// from registerTools, so tools/list omitted it and tools/call returned
// method-not-found.
func TestMCP_DoctorRoundTrip(t *testing.T) {
	env := newTestEnv(t)

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_doctor","arguments":{}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines:\n%s", len(lines), out.String())
	}
	var resp struct {
		Result struct {
			StructuredContent struct {
				Findings []map[string]any `json:"findings"`
				Total    int              `json:"total"`
			} `json:"structuredContent"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, lines[1])
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: code=%d msg=%q", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result.IsError {
		t.Fatalf("tool error: %s", lines[1])
	}
	// A fresh repo may have zero findings; assert the response shape is sane
	// (Findings is non-nil and Total matches len) rather than the count.
	if resp.Result.StructuredContent.Total != len(resp.Result.StructuredContent.Findings) {
		t.Errorf("total = %d but findings len = %d",
			resp.Result.StructuredContent.Total,
			len(resp.Result.StructuredContent.Findings))
	}
}

// TestMCP_StatusRoundTrip is a smoke test for the new coordination tools:
// it calls squad_status (a read-only tool from the registerCoordinationTools
// batch) and confirms the structured response shape decodes cleanly.
func TestMCP_StatusRoundTrip(t *testing.T) {
	env := newTestEnv(t)

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_status","arguments":{}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines:\n%s", len(lines), out.String())
	}
	var resp struct {
		Result struct {
			StructuredContent struct {
				Claimed int `json:"claimed"`
				Ready   int `json:"ready"`
				Blocked int `json:"blocked"`
				Done    int `json:"done"`
			} `json:"structuredContent"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, lines[1])
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: %+v", resp.Error)
	}
	if resp.Result.IsError {
		t.Fatalf("tool error: %s", lines[1])
	}
	// EXAMPLE-001 ships as a ready item by default, so Ready should be ≥ 1.
	if resp.Result.StructuredContent.Ready < 1 {
		t.Errorf("expected ≥1 ready item; got %+v", resp.Result.StructuredContent)
	}
}

func TestMCP_StatsRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	mustWriteItem(t, env.Root, "BUG-501", "stats fixture")

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_stats","arguments":{}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines:\n%s", len(lines), out.String())
	}
	var resp struct {
		Result struct {
			StructuredContent struct {
				SchemaVersion int    `json:"schema_version"`
				RepoID        string `json:"repo_id"`
				Items         struct {
					Total int64 `json:"total"`
				} `json:"items"`
				Claims struct {
					Active int64 `json:"active"`
				} `json:"claims"`
			} `json:"structuredContent"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, lines[1])
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: %+v", resp.Error)
	}
	if resp.Result.IsError {
		t.Fatalf("tool error: %s", lines[1])
	}
	if resp.Result.StructuredContent.SchemaVersion < 1 {
		t.Errorf("schema_version=%d want >=1", resp.Result.StructuredContent.SchemaVersion)
	}
	if resp.Result.StructuredContent.RepoID != env.RepoID {
		t.Errorf("repo_id=%q want %q", resp.Result.StructuredContent.RepoID, env.RepoID)
	}
}

func TestMCP_StatsOnEmptyRepoIsValid(t *testing.T) {
	env := newTestEnv(t)
	// Drop the seeded EXAMPLE-001 so we genuinely have an empty repo.
	if _, err := env.DB.Exec(`DELETE FROM items WHERE repo_id = ?`, env.RepoID); err != nil {
		t.Fatalf("clear items: %v", err)
	}

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_stats","arguments":{"window_seconds":3600}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var resp struct {
		Result struct {
			StructuredContent struct {
				Items struct {
					Total int64 `json:"total"`
				} `json:"items"`
				Window struct {
					Label string `json:"label"`
				} `json:"window"`
			} `json:"structuredContent"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, lines[1])
	}
	if resp.Result.IsError {
		t.Fatalf("tool error: %s", lines[1])
	}
	if resp.Result.StructuredContent.Items.Total != 0 {
		t.Errorf("empty repo: items.total=%d want 0", resp.Result.StructuredContent.Items.Total)
	}
	if resp.Result.StructuredContent.Window.Label == "" {
		t.Errorf("window.label should be set; got empty")
	}
}

const mcpReadyItemPasses = `---
id: FEAT-100
title: Wire up the new ready verb plumbing for inbox lint
type: feature
priority: P1
area: cli
status: captured
created: 2026-04-26
updated: 2026-04-26
---

## Acceptance criteria
- [ ] the rule does the thing as specified
`

const mcpReadyItemFailsDoR = `---
id: FEAT-101
title: tiny
type: feature
priority: P1
area: <fill-in>
status: captured
created: 2026-04-26
updated: 2026-04-26
---

## Acceptance criteria
no checkboxes here
`

func TestMCPSquadReady_HappyPath(t *testing.T) {
	env := newTestEnv(t)
	if err := os.WriteFile(filepath.Join(env.ItemsDir, "FEAT-100.md"), []byte(mcpReadyItemPasses), 0o644); err != nil {
		t.Fatal(err)
	}
	mcpPersistFixture(t, env, "FEAT-100.md")

	resp := callMCPTool(t, env, "squad_ready",
		`{"ids":["FEAT-100"],"promote":true,"agent_id":"agent-mcp"}`)
	if resp.Error != nil {
		t.Fatalf("rpc error: code=%d msg=%q", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result.IsError {
		t.Fatalf("tool error: %+v", resp.Result.StructuredContent)
	}
	reports := mcpObjSlice(t, resp.Result.StructuredContent["reports"], "reports")
	if len(reports) != 1 {
		t.Fatalf("want 1 report, got %d", len(reports))
	}
	r := reports[0]
	if r["id"] != "FEAT-100" {
		t.Errorf("id=%v want FEAT-100", r["id"])
	}
	if r["pass"] != true {
		t.Errorf("pass=%v want true; violations=%v", r["pass"], r["violations"])
	}
	if r["promoted"] != true {
		t.Errorf("promoted=%v want true", r["promoted"])
	}
	if r["status"] != "open" {
		t.Errorf("status=%v want open after promote", r["status"])
	}

	parsed, err := items.Parse(filepath.Join(env.ItemsDir, "FEAT-100.md"))
	if err != nil {
		t.Fatalf("parse after promote: %v", err)
	}
	if parsed.Status != "open" {
		t.Errorf("file frontmatter status=%q want open", parsed.Status)
	}
}

func TestMCPSquadReady_DoRFailDoesNotPromote(t *testing.T) {
	env := newTestEnv(t)
	if err := os.WriteFile(filepath.Join(env.ItemsDir, "FEAT-101.md"), []byte(mcpReadyItemFailsDoR), 0o644); err != nil {
		t.Fatal(err)
	}
	mcpPersistFixture(t, env, "FEAT-101.md")

	resp := callMCPTool(t, env, "squad_ready",
		`{"ids":["FEAT-101"],"promote":true,"agent_id":"agent-mcp"}`)
	if resp.Error != nil {
		t.Fatalf("rpc error: code=%d msg=%q", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result.IsError {
		t.Fatalf("tool error: %+v", resp.Result.StructuredContent)
	}
	reports := mcpObjSlice(t, resp.Result.StructuredContent["reports"], "reports")
	if len(reports) != 1 {
		t.Fatalf("want 1 report, got %d", len(reports))
	}
	r := reports[0]
	if r["pass"] != false {
		t.Errorf("pass=%v want false", r["pass"])
	}
	if r["promoted"] == true {
		t.Errorf("promoted=%v; DoR-failing item must NOT be promoted", r["promoted"])
	}
	if r["status"] != "captured" {
		t.Errorf("status=%v want captured (no promotion)", r["status"])
	}
	violations := mcpObjSlice(t, r["violations"], "violations")
	if len(violations) == 0 {
		t.Errorf("want at least one violation; got %v", r)
	}
	rules := map[string]bool{}
	for _, v := range violations {
		if rule, ok := v["rule"].(string); ok {
			rules[rule] = true
		}
	}
	for _, want := range []string{"area-set", "acceptance-criterion"} {
		if !rules[want] {
			t.Errorf("missing expected violation %q; got rules=%v", want, rules)
		}
	}

	parsed, err := items.Parse(filepath.Join(env.ItemsDir, "FEAT-101.md"))
	if err != nil {
		t.Fatalf("parse after fail: %v", err)
	}
	if parsed.Status != "captured" {
		t.Errorf("file frontmatter status=%q want captured (no promotion)", parsed.Status)
	}
}

func writeAnalyzeFixture(t *testing.T, env *testEnv, epicName string) {
	t.Helper()
	specPath := filepath.Join(env.Root, ".squad", "specs", "x.md")
	if err := os.MkdirAll(filepath.Dir(specPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, []byte("---\ntitle: X\nmotivation: y\nacceptance: [y]\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	epicPath := filepath.Join(env.Root, ".squad", "epics", epicName+".md")
	if err := os.MkdirAll(filepath.Dir(epicPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(epicPath, []byte("---\nspec: x\nstatus: open\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkItem := func(id, glob string) {
		body := "---\nid: " + id + "\ntitle: t\ntype: feature\npriority: P1\narea: core\n" +
			"status: open\nestimate: 1h\nrisk: low\ncreated: 2026-04-25\n" +
			"updated: 2026-04-25\nepic: " + epicName + "\nparallel: true\n" +
			"conflicts_with:\n  - " + glob + "\n---\n\n## Problem\nx\n"
		if err := os.WriteFile(filepath.Join(env.ItemsDir, id+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkItem("FEAT-201", "internal/a.go")
	mkItem("FEAT-202", "internal/a.go")
	mkItem("FEAT-203", "internal/b.go")
}

func TestMCPSquadAnalyze_HappyPath(t *testing.T) {
	env := newTestEnv(t)
	writeAnalyzeFixture(t, env, "demo-epic")

	resp := callMCPTool(t, env, "squad_analyze", `{"epic_name":"demo-epic"}`)
	if resp.Error != nil {
		t.Fatalf("rpc error: code=%d msg=%q", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result.IsError {
		t.Fatalf("tool error: %+v", resp.Result.StructuredContent)
	}
	got := resp.Result.StructuredContent
	if got["epic"] != "demo-epic" {
		t.Errorf("epic=%v want demo-epic", got["epic"])
	}
	if got["spec"] != "x" {
		t.Errorf("spec=%v want x", got["spec"])
	}
	streams := mcpObjSlice(t, got["streams"], "streams")
	if len(streams) < 2 {
		t.Fatalf("want >=2 streams; got %d: %+v", len(streams), streams)
	}
	allItems := map[string]bool{}
	for _, s := range streams {
		ids := mcpStringSlice(t, s["item_ids"], "item_ids")
		for _, id := range ids {
			allItems[id] = true
		}
	}
	for _, want := range []string{"FEAT-201", "FEAT-202", "FEAT-203"} {
		if !allItems[want] {
			t.Errorf("missing %s in streams; got %v", want, allItems)
		}
	}
	if pf, ok := got["parallelism_factor"].(float64); !ok || pf <= 0 {
		t.Errorf("parallelism_factor=%v want >0", got["parallelism_factor"])
	}
}

func TestMCPSquadAnalyze_UnknownEpic(t *testing.T) {
	env := newTestEnv(t)
	resp := callMCPTool(t, env, "squad_analyze", `{"epic_name":"does-not-exist"}`)
	if resp.Error == nil && !resp.Result.IsError {
		t.Fatalf("expected error for unknown epic; got: %+v", resp.Result.StructuredContent)
	}
}

func TestMCP_StandupRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	// Plant a "done" event in claim_history attributable to the env agent.
	if _, err := env.DB.Exec(`
		INSERT INTO claim_history (repo_id, item_id, agent_id, claimed_at, released_at, outcome)
		VALUES (?, ?, ?, ?, ?, 'done')`,
		env.RepoID, "BUG-7001", env.AgentID, 1700000000, 1700000005); err != nil {
		t.Fatalf("plant: %v", err)
	}

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_standup","arguments":{"since":0}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines:\n%s", len(lines), out.String())
	}
	var resp struct {
		Result struct {
			StructuredContent struct {
				Agent  string           `json:"agent"`
				Repo   string           `json:"repo"`
				Closed []map[string]any `json:"closed"`
			} `json:"structuredContent"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, lines[1])
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: %+v", resp.Error)
	}
	if resp.Result.IsError {
		t.Fatalf("tool error: %s", lines[1])
	}
	if resp.Result.StructuredContent.Agent != env.AgentID {
		t.Errorf("agent=%q want %q", resp.Result.StructuredContent.Agent, env.AgentID)
	}
	if resp.Result.StructuredContent.Repo != env.RepoID {
		t.Errorf("repo=%q want %q", resp.Result.StructuredContent.Repo, env.RepoID)
	}
	if len(resp.Result.StructuredContent.Closed) != 1 {
		t.Errorf("closed=%d want 1: %+v", len(resp.Result.StructuredContent.Closed), resp.Result.StructuredContent.Closed)
	}
}

func TestMCP_StandupEmptyWindow(t *testing.T) {
	env := newTestEnv(t)
	// Plant a done event but request a tiny since-window after it.
	if _, err := env.DB.Exec(`
		INSERT INTO claim_history (repo_id, item_id, agent_id, claimed_at, released_at, outcome)
		VALUES (?, ?, ?, ?, ?, 'done')`,
		env.RepoID, "BUG-7002", env.AgentID, 1700000000, 1700000005); err != nil {
		t.Fatalf("plant: %v", err)
	}

	// since = now → window collapses to ~0s; nothing in window.
	now := time.Now().Unix()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_standup","arguments":{"since":%d}}}`, now)
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" + body + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatalf("runMCP: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var resp struct {
		Result struct {
			StructuredContent struct {
				Closed []map[string]any `json:"closed"`
			} `json:"structuredContent"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, lines[1])
	}
	if resp.Result.IsError {
		t.Fatalf("tool error: %s", lines[1])
	}
	if len(resp.Result.StructuredContent.Closed) != 0 {
		t.Errorf("expected empty closed in tiny window; got %+v", resp.Result.StructuredContent.Closed)
	}
}

func TestMCP_ResourceScopeRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	mustWriteItem(t, env.Root, "ENV-002", "production")
	mustWriteItem(t, env.Root, "ENV-004", "importer")
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"squad_resources_define","arguments":{"definitions":[{"item_id":"ENV-002","group":"production","default_scope":"*","service_scope":"studio"},{"item_id":"ENV-004","group":"production","default_scope":"importer","service_scope":"importer"}]}}}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"squad_claim","arguments":{"item_id":"ENV-002","intent":"studio only","scope":"studio","agent_id":"agent-studio"}}}` + "\n" +
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"squad_claim","arguments":{"item_id":"ENV-004","intent":"importer only","agent_id":"agent-importer"}}}` + "\n")
	var out bytes.Buffer
	if err := runMCP(context.Background(), env.DB, env.RepoID, env.Root, in, &out); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := env.DB.QueryRow(`SELECT count(*) FROM claims WHERE repo_id=?`, env.RepoID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("independent claims=%d response=%s", n, out.String())
	}
	var scope string
	_ = env.DB.QueryRow(`SELECT resource_scope FROM claims WHERE item_id='ENV-002'`).Scan(&scope)
	if scope != "studio" {
		t.Fatalf("scope=%s", scope)
	}
}
