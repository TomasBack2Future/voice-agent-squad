package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/zsiec/squad/internal/dispatch"
	"github.com/zsiec/squad/internal/notify"
)

func TestMCPDispatchAdmissionUsesCallerAndReadiness(t *testing.T) {
	env := newTestEnv(t)
	reserved := callMCPTool(t, env, "squad_dispatch_reserve", `{"reservation":"D-MCP","source":"github:example/repo#1","note":"fixture"}`)
	if reserved.Error != nil {
		t.Fatalf("reserve: %s", reserved.Error.Message)
	}
	if got := reserved.Result.StructuredContent["reserved_by"]; got != env.AgentID {
		t.Fatalf("reservation actor = %v, want %s", got, env.AgentID)
	}
	attached := callMCPTool(t, env, "squad_dispatch_attach", `{"reservation":"D-MCP","item":"BUG-001","generation":1}`)
	if attached.Error != nil {
		t.Fatalf("attach: %s", attached.Error.Message)
	}
	store := dispatch.New(env.DB, env.RepoID, nil)
	if _, err := store.BindController(context.Background(), env.AgentID, "dispatcher-native", 0); err != nil {
		t.Fatal(err)
	}
	args := `{"reservation":"D-MCP","worker_session":"worker-native","generation":1}`
	missing := callMCPTool(t, env, "squad_dispatch_bind", args)
	if missing.Error == nil || !strings.Contains(missing.Error.Message, "receiver not ready") {
		t.Fatalf("missing receiver admitted: %+v", missing)
	}
	for _, bad := range []string{
		`{"reservation":"D-MCP","worker_session":"worker-native","generation":2}`,
		`{"reservation":"D-MCP","worker_session":"worker-native","generation":1,"actor":"other"}`,
		`{"reservation":"D-MCP","worker_session":"worker-native","generation":1,"supervised":true}`,
	} {
		if got := callMCPTool(t, env, "squad_dispatch_bind", bad); got.Error == nil {
			t.Fatalf("invalid binding admitted: %s", bad)
		}
	}
	t.Setenv("SQUAD_AGENT", "foreign-dispatcher")
	if got := callMCPTool(t, env, "squad_dispatch_attach", `{"reservation":"D-MCP","item":"BUG-002","generation":1}`); got.Error == nil {
		t.Fatal("foreign actor attached reservation")
	}
	t.Setenv("SQUAD_AGENT", env.AgentID)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := store.BindReceiverReady(context.Background(), env.AgentID, "dispatcher-native", "incarnation", 1, os.Getpid(), "native"); err != nil {
		t.Fatal(err)
	}
	registry := notify.NewRegistry(env.DB)
	if err := registry.Register(context.Background(), notify.Endpoint{RepoID: env.RepoID,
		Instance: "terminal:" + env.AgentID + ":incarnation", Kind: notify.KindRewake,
		Port: listener.Addr().(*net.TCPAddr).Port}); err != nil {
		t.Fatal(err)
	}
	bound := callMCPTool(t, env, "squad_dispatch_bind", args)
	if bound.Error != nil || bound.Result.StructuredContent["worker_thread_id"] != "worker-native" {
		t.Fatalf("ready binding failed: %+v", bound)
	}
	listed := callMCPTool(t, env, "squad_dispatch_list", `{}`)
	if listed.Error != nil {
		t.Fatalf("list: %s", listed.Error.Message)
	}
	raw, err := json.Marshal(listed.Result.StructuredContent["reservations"])
	if err != nil {
		t.Fatal(err)
	}
	var rows []dispatch.Reservation
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) != 1 || rows[0].State != "dispatched" {
		t.Fatalf("list lost binding: %s, %v", raw, err)
	}
	if rows[0].ExpiresAt != 0 {
		t.Fatal("bound reservation must retain the core non-expiring custody semantics")
	}
}

func TestMCPDispatchAdmissionRejectsSpoofedReserveActor(t *testing.T) {
	env := newTestEnv(t)
	result := callMCPTool(t, env, "squad_dispatch_reserve", `{"reservation":"D-MCP","source":"github:example/repo#1","actor":"other"}`)
	if result.Error == nil {
		t.Fatal("caller-supplied actor accepted")
	}
	var count int
	if err := env.DB.QueryRow(`SELECT count(*) FROM dispatch_reservations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("spoofed call changed reservations: count=%d err=%v", count, err)
	}
}
