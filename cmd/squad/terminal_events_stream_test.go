package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/chat"
	"github.com/zsiec/squad/internal/notify"
	"github.com/zsiec/squad/internal/terminalevents"
)

func TestTerminalListenExposesOptionalStream(t *testing.T) {
	command := newTerminalEventsCmd()
	listen, _, err := command.Find([]string{"listen"})
	if err != nil {
		t.Fatal(err)
	}
	flag := listen.Flags().Lookup("stream")
	if flag == nil || flag.DefValue != "false" {
		t.Fatal("terminal-events listen needs an opt-in --stream flag")
	}
}

func TestTerminalStreamForgetsExpiredEmissionHistory(t *testing.T) {
	now := time.Now()
	emitted := map[string]time.Time{"old": now.Add(-2 * time.Minute), "recent": now.Add(-time.Minute)}
	pruneTerminalEmissions(emitted, now)
	if _, ok := emitted["old"]; ok || len(emitted) != 1 {
		t.Fatalf("expired delivery history remains: %+v", emitted)
	}
	if _, ok := emitted["recent"]; !ok {
		t.Fatal("recent emission history was lost")
	}
}

func TestTerminalStreamRetainsEndpointAndExplicitAcknowledgements(t *testing.T) {
	env := newTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := env.DB.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES(?,'D-STREAM','github:example/repo#1',?,1,1,0,'dispatched',1,'worker-native','','TASK')`, env.RepoID, env.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = env.DB.Exec(`INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES(?,'TASK','worker',1,2,'done')`, env.RepoID); err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	done := make(chan error, 1)
	joined := false
	store := terminalevents.Store{DB: env.DB, Repo: env.RepoID, Recipient: env.AgentID}
	go func() {
		err := receiveTerminalEventBatches(ctx, store, "stream-incarnation", time.Minute, writer, true, true)
		_ = writer.Close()
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		_ = reader.Close()
		if joined {
			return
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("stream did not join")
		}
	})
	registry := notify.NewRegistry(env.DB)
	endpoint := func() notify.Endpoint {
		for ctx.Err() == nil {
			endpoints, err := registry.LookupRepo(ctx, env.RepoID)
			if err != nil {
				t.Fatal(err)
			}
			for _, endpoint := range endpoints {
				if endpoint.Instance == "terminal:"+env.AgentID+":stream-incarnation" {
					return endpoint
				}
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("stream endpoint unavailable")
		return notify.Endpoint{}
	}
	firstEndpoint := endpoint()
	decoder := json.NewDecoder(reader)
	chatStore := chat.New(env.DB, env.RepoID)
	for i := 0; i < 2; i++ {
		if err := chatStore.Post(ctx, chat.PostRequest{AgentID: "worker", Thread: "TASK", Kind: "say", Body: fmt.Sprintf("stream result %d", i)}); err != nil {
			t.Fatal(err)
		}
		var outcome int64
		if err := env.DB.QueryRow(`SELECT max(id) FROM messages`).Scan(&outcome); err != nil {
			t.Fatal(err)
		}
		id, err := store.Publish(ctx, "worker", terminalevents.PublishRequest{Reservation: "D-STREAM", Generation: 1, WorkerSession: "worker-native", Kind: "blocked", OutcomeID: outcome})
		if err != nil {
			t.Fatal(err)
		}
		if err := notify.WakeKind(ctx, registry, env.RepoID, notify.KindRewake, time.Second); err != nil {
			t.Fatal(err)
		}
		var batch struct {
			Events []terminalevents.Event `json:"events"`
		}
		if err := decoder.Decode(&batch); err != nil || len(batch.Events) != 1 || batch.Events[0].ID != id {
			t.Fatalf("stream batch %d: %+v, %v", i, batch, err)
		}
		var delivered, handled int64
		if err := env.DB.QueryRow(`SELECT delivered_at,processed_at FROM terminal_event_receipts WHERE event_id=?`, id).Scan(&delivered, &handled); err != nil || delivered != 0 || handled != 0 {
			t.Fatalf("stream implicitly accepted/handled: %d/%d, %v", delivered, handled, err)
		}
		if current := endpoint(); current != firstEndpoint {
			t.Fatalf("endpoint changed between batches: %+v -> %+v", firstEndpoint, current)
		}
		if err := store.Delivered(ctx, id, "stream-incarnation"); err != nil {
			t.Fatal(err)
		}
		if err := store.Ack(ctx, id, "isolated native handled result"); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("stream cancellation: %v", err)
	}
	joined = true
	endpoints, err := registry.LookupRepo(context.Background(), env.RepoID)
	if err != nil || len(endpoints) != 0 {
		t.Fatalf("cancelled stream left endpoint: %+v, %v", endpoints, err)
	}
}
