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
