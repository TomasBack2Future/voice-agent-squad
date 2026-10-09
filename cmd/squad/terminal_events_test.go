package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/chat"
	"github.com/zsiec/squad/internal/notify"
	"github.com/zsiec/squad/internal/terminalevents"
)

func TestTerminalReceiverWakeAndMCPAcknowledge(t *testing.T) {
	env := newTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := env.DB.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES(?,'DISPATCH-1','github:repo#1',?,1,1,0,'dispatched',1,'worker-session','','TASK')`, env.RepoID, env.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.DB.Exec(`INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES(?,'TASK','worker',1,2,'done')`, env.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	c := chat.New(env.DB, env.RepoID)
	if err = c.Post(ctx, chat.PostRequest{AgentID: "worker", Thread: "TASK", Kind: "say", Body: "outcome"}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err = env.DB.QueryRow("SELECT max(id) FROM messages").Scan(&id); err != nil {
		t.Fatal(err)
	}
	event := fmt.Sprintf("worker-terminal-v1/DISPATCH-1/1/worker-session/issue-closed/%d", id)
	s := terminalevents.Store{DB: env.DB, Repo: env.RepoID, Recipient: env.AgentID}
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- listenTerminalEvents(ctx, s, "incarnation", 2*time.Second, &out) }()
	reg := notify.NewRegistry(env.DB)
	var port int
	for ctx.Err() == nil {
		eps, e := reg.LookupRepo(ctx, env.RepoID)
		if e != nil {
			t.Fatal(e)
		}
		if len(eps) > 0 {
			port = eps[0].Port
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if port == 0 {
		t.Fatal("receiver never ready")
	}
	if err = c.Post(ctx, chat.PostRequest{AgentID: "worker", Thread: "TASK", Kind: "say", Body: event}); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), event) {
		t.Fatal("event missing", out.String())
	}
	rows, err := s.Pending(ctx, "restarted", 0)
	if err != nil || len(rows) != 1 {
		t.Fatal("delivery prematurely consumed event", err)
	}
	request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "squad_terminal_events_ack", "arguments": map[string]string{"event_id": event, "note": "reconciled", "agent_id": env.AgentID}}})
	var response bytes.Buffer
	if err = runMCP(ctx, env.DB, env.RepoID, env.Root, strings.NewReader(string(request)+"\n"), &response); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Pending(ctx, "restarted", 0)
	if err != nil || len(rows) != 0 {
		t.Fatal("ack not persisted", err, response.String())
	}
}

func TestListenFallbackReturnsWakeAfterConsumingMailbox(t *testing.T) {
	f := newChatFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan int, 1)
	var out bytes.Buffer
	go func() {
		done <- runListen(ctx, f.chat, f.db, f.agentID, f.repoID, notify.NewRegistry(f.db), listenArgs{Instance: "fallback", FallbackInt: 10 * time.Millisecond, MaxLifetime: time.Second}, &out)
	}()
	time.Sleep(30 * time.Millisecond)
	if err := f.chat.Post(ctx, chat.PostRequest{AgentID: "peer", Thread: "global", Kind: "say", Body: "@" + f.agentID + " result"}); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 2 {
		t.Fatalf("fallback lost wake: code=%d output=%s", code, out.String())
	}
}

func TestMCPStructuredEventPublish(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	_, err := env.DB.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES(?,'DISPATCH-1','github:repo#1',?,1,1,0,'dispatched',1,'worker-session','','TASK')`, env.RepoID, env.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.DB.Exec(`INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES(?,'TASK','worker',1,2,'done')`, env.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	c := chat.New(env.DB, env.RepoID)
	if err = c.Post(ctx, chat.PostRequest{AgentID: "worker", Thread: "TASK", Kind: "ask", Body: "design decision"}); err != nil {
		t.Fatal(err)
	}
	var outcome int64
	if err = env.DB.QueryRow("SELECT max(id) FROM messages").Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"intruder", "worker"} {
		request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "squad_terminal_events_publish", "arguments": map[string]any{"reservation": "DISPATCH-1", "generation": 1, "worker_session": "worker-session", "kind": "decision-request", "outcome_id": outcome, "agent_id": actor}}})
		var out bytes.Buffer
		if err = runMCP(ctx, env.DB, env.RepoID, env.Root, strings.NewReader(string(request)+"\n"), &out); err != nil {
			t.Fatal(err)
		}
		if actor == "intruder" && !strings.Contains(out.String(), "rejected") {
			t.Fatal(out.String())
		}
		if actor == "worker" && !strings.Contains(out.String(), "pending") {
			t.Fatal(out.String())
		}
	}
	s := terminalevents.Store{DB: env.DB, Repo: env.RepoID, Recipient: env.AgentID}
	events, err := s.Pending(ctx, "dispatcher", 0)
	if err != nil || len(events) != 1 || events[0].Kind != "decision-request" {
		t.Fatalf("MCP publish lost %v %v", events, err)
	}
}

func TestMCPAtomicOutcomeSubmit(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	_, err := env.DB.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES(?,'DISPATCH-1','github:repo#1',?,1,1,0,'dispatched',1,'worker-session','','TASK')`, env.RepoID, env.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.DB.Exec(`INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES(?,'TASK','worker',1,2,'done')`, env.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"reservation": "DISPATCH-1", "generation": 1, "worker_session": "worker-session", "kind": "handoff-complete", "body": "factual outcome", "agent_id": "worker"}
	var first string
	for i := 1; i <= 2; i++ {
		request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": i, "method": "tools/call", "params": map[string]any{"name": "squad_terminal_events_submit", "arguments": args}})
		var out bytes.Buffer
		if err = runMCP(ctx, env.DB, env.RepoID, env.Root, strings.NewReader(string(request)+"\n"), &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "pending") || !strings.Contains(out.String(), "message_id") {
			t.Fatalf("MCP submit lost: %s", out.String())
		}
		var decoded struct {
			Result struct {
				StructuredContent map[string]any `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
			t.Fatalf("decode MCP response: %v", err)
		}
		got := fmt.Sprintf("%v/%v", decoded.Result.StructuredContent["message_id"], decoded.Result.StructuredContent["event_id"])
		if first == "" {
			first = got
		} else if got != first {
			t.Fatalf("retry returned different IDs: %s vs %s", got, first)
		}
	}
	s := terminalevents.Store{DB: env.DB, Repo: env.RepoID, Recipient: env.AgentID}
	events, err := s.Pending(ctx, "dispatcher", 0)
	if err != nil || len(events) != 1 || events[0].Kind != "handoff-complete" {
		t.Fatalf("MCP submit not routed %v %v", events, err)
	}
	var n int
	if err = env.DB.QueryRow("SELECT count(*) FROM messages WHERE thread='TASK'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate messages: %d %v", n, err)
	}
	// Wrong repo: reservation exists only in another ledger.
	other := map[string]any{"reservation": "DISPATCH-1", "generation": 1, "worker_session": "worker-session", "kind": "blocked", "body": "x", "agent_id": "worker"}
	request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "squad_terminal_events_submit", "arguments": other}})
	env2 := newTestEnv(t)
	var out bytes.Buffer
	if err = runMCP(ctx, env2.DB, env2.RepoID, env2.Root, strings.NewReader(string(request)+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "reservation-repo-mismatch") && !strings.Contains(out.String(), "absent-reservation") {
		t.Fatalf("ambiguous identity guessed: %s", out.String())
	}
}

func TestMCPDecisionCASAndGet(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	_, err := env.DB.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES(?,'D','github:repo#1',?,1,1,0,'dispatched',1,'native','','TASK')`, env.RepoID, env.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.DB.Exec(`INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES(?,'TASK','worker',1,2,'blocked')`, env.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	c := chat.New(env.DB, env.RepoID)
	if err = c.Post(ctx, chat.PostRequest{AgentID: env.AgentID, Thread: "TASK", Kind: "fyi", Body: "access restored; continue same scope"}); err != nil {
		t.Fatal(err)
	}
	var message int64
	if err = env.DB.QueryRow("SELECT max(id) FROM messages").Scan(&message); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"squad_terminal_decision_set", "squad_terminal_decision_get"} {
		args := map[string]any{"reservation": "D", "generation": 1, "worker_session": "native"}
		if name == "squad_terminal_decision_set" {
			args["expected_revision"] = 0
			args["outcome_id"] = message
			args["action"] = "proceed"
			args["agent_id"] = env.AgentID
		}
		request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		var out bytes.Buffer
		if err = runMCP(ctx, env.DB, env.RepoID, env.Root, strings.NewReader(string(request)+"\n"), &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "proceed") || strings.Contains(out.String(), `"isError":true`) {
			t.Fatal(out.String())
		}
	}
}

func TestDeferredNativeAcceptanceAndMCPParity(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	_, err := env.DB.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES(?,'DISPATCH-1','github:repo#1',?,1,1,0,'dispatched',1,'worker-session','','TASK')`, env.RepoID, env.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.DB.Exec(`INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES(?,'TASK','worker',1,2,'done')`, env.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	c := chat.New(env.DB, env.RepoID)
	if err = c.Post(ctx, chat.PostRequest{AgentID: "worker", Thread: "TASK", Kind: "say", Body: "outcome"}); err != nil {
		t.Fatal(err)
	}
	var outcome int64
	if err = env.DB.QueryRow("SELECT max(id) FROM messages").Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	s := terminalevents.Store{DB: env.DB, Repo: env.RepoID, Recipient: env.AgentID}
	id, err := s.Publish(ctx, "worker", terminalevents.PublishRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", Kind: "handoff-complete", OutcomeID: outcome})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = receiveTerminalEvents(ctx, s, "native-incarnation", time.Second, &out, true); err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Recipient string                 `json:"recipient"`
		Session   string                 `json:"delivery_session"`
		Events    []terminalevents.Event `json:"events"`
	}
	if err = json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Recipient != env.AgentID || receipt.Session != "native-incarnation" || len(receipt.Events) != 1 || receipt.Events[0].DeliveredAt != 0 {
		t.Fatal(out.String())
	}
	if err = s.Ack(ctx, id, "too early"); err == nil {
		t.Fatal("pending pipe read became handling acknowledgement")
	}
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "squad_terminal_events_delivered", "arguments": map[string]string{"event_id": id, "delivery_session": "native-incarnation", "agent_id": env.AgentID}}})
	out.Reset()
	if err = runMCP(ctx, env.DB, env.RepoID, env.Root, strings.NewReader(string(raw)+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "delivered") {
		t.Fatal(out.String())
	}
	var processed int64
	if err = env.DB.QueryRow("SELECT processed_at FROM terminal_event_receipts WHERE event_id=?", id).Scan(&processed); err != nil || processed != 0 {
		t.Fatal(processed, err)
	}
	if err = s.Ack(ctx, id, "recipient reconciled"); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionGetRejectsMissingOrWrongSessionPrecisely(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bc, err := bootClaimContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	repoID := bc.repoID
	bc.Close()
	if _, err = env.DB.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES(?,'D','github:repo#1','dispatcher',1,1,0,'dispatched',1,'native','','TASK')`, repoID); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"missing session": {[]string{"--reservation", "D", "--generation", "1"}, "--worker-session is required"},
		"wrong session":   {[]string{"--reservation", "D", "--generation", "1", "--worker-session", "other"}, "worker-session-mismatch"},
		"missing key":     {[]string{"--generation", "1", "--worker-session", "native"}, "--reservation is required"},
	} {
		root := newRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(append([]string{"terminal-events", "decision-get"}, tc.args...))
		err := root.Execute()
		if err == nil || strings.Contains(err.Error(), "no rows") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want %q, got %v", name, tc.want, err)
		}
	}
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"terminal-events", "decision-get", "--reservation", "D", "--generation", "1", "--worker-session", "native"})
	if err = root.Execute(); err != nil || !strings.Contains(out.String(), `"revision":0`) {
		t.Fatalf("no decision must stay revision 0: %v %s", err, out.String())
	}
	request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "squad_terminal_decision_get", "arguments": map[string]any{"reservation": "D", "generation": 1, "worker_session": "other"}}})
	var mcpOut bytes.Buffer
	if err = runMCP(ctx, env.DB, repoID, env.Root, strings.NewReader(string(request)+"\n"), &mcpOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mcpOut.String(), "worker-session-mismatch") || strings.Contains(mcpOut.String(), "no rows") {
		t.Fatal(mcpOut.String())
	}
}
