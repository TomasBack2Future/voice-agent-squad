package terminalevents

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/store"
)

func fixture(t *testing.T) Store {
	t.Helper()
	db, e := store.Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	_, e = db.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id)
 VALUES('repo','DISPATCH-1','github:repo#1','dispatcher',1,1,0,'dispatched',1,'worker-session','','TASK')`)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`INSERT INTO messages(id,repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(1,'repo',1,'worker','TASK','say','outcome','[]','normal')`)
	if e != nil {
		t.Fatal(e)
	}
	return Store{DB: db, Repo: "repo", Recipient: "dispatcher"}
}

const eventID = "worker-terminal-v1/DISPATCH-1/1/worker-session/issue-closed/1"

func post(t *testing.T, s Store, body, actor string) {
	t.Helper()
	_, e := s.DB.Exec(`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('repo',2,?,'TASK','say',?,'[]','normal')`, actor, body)
	if e != nil {
		t.Fatal(e)
	}
}

func TestDiscoveryDeliveryAndExplicitAck(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	for _, prefix := range []string{"callback-intent ", "payload event_id=", "pending-reconciliation "} {
		post(t, s, prefix+eventID, "worker")
	}
	if e := s.Discover(ctx); e != nil {
		t.Fatal(e)
	}
	events, e := s.Pending(ctx, "start1", time.Hour)
	if e != nil || len(events) != 1 {
		t.Fatalf("dedupe %v %v", events, e)
	}
	if e = s.Ack(ctx, eventID, "done"); e == nil {
		t.Fatal("unseen event acked")
	}
	if e = s.Delivered(ctx, eventID, "start1"); e != nil {
		t.Fatal(e)
	}
	events, e = s.Pending(ctx, "start1", time.Hour)
	if e != nil || len(events) != 0 {
		t.Fatal("delivery loop", e)
	}
	events, e = s.Pending(ctx, "resumed", time.Hour)
	if e != nil || len(events) != 1 {
		t.Fatal("unacknowledged event lost after restart", e)
	}
	if e = s.Ack(ctx, eventID, "reservation completed; evidence checked"); e != nil {
		t.Fatal(e)
	}
	if e = s.Ack(ctx, eventID, "reservation completed; evidence checked"); e != nil {
		t.Fatal("not idempotent", e)
	}
	if e = s.Ack(ctx, eventID, "overwrite"); e == nil {
		t.Fatal("overwrote receipt")
	}
	events, e = s.Pending(ctx, "third-start", 0)
	if e != nil || len(events) != 0 {
		t.Fatal("processed replay", e)
	}
	var n int
	if e = s.DB.QueryRow("SELECT count(*) FROM reads").Scan(&n); e != nil || n != 0 {
		t.Fatal("ordinary mailbox consumed", e)
	}
}

func TestRejectWrongOwnerGenerationSessionAndOutcome(t *testing.T) {
	for _, tc := range []struct{ name, body, actor string }{
		{"old generation", "worker-terminal-v1/DISPATCH-1/2/worker-session/issue-closed/1", "worker"},
		{"other worker", "worker-terminal-v1/DISPATCH-1/1/other/issue-closed/1", "worker"},
		{"missing outcome", "worker-terminal-v1/DISPATCH-1/1/worker-session/issue-closed/999", "worker"},
		{"other actor", eventID, "someone-else"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture(t)
			post(t, s, tc.body, tc.actor)
			if e := s.Discover(context.Background()); e != nil {
				t.Fatal(e)
			}
			rows, e := s.Pending(context.Background(), "start", 0)
			if e != nil || len(rows) > 0 {
				t.Fatalf("accepted %v %v", rows, e)
			}
		})
	}
	s := fixture(t)
	post(t, s, eventID, "worker")
	ctx := context.Background()
	if e := s.Discover(ctx); e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"generation=2", "reserved_by='other'", "worker_thread_id='other'"} {
		t.Run(field, func(t *testing.T) {
			if _, e := s.DB.Exec("UPDATE dispatch_reservations SET " + field); e != nil {
				t.Fatal(e)
			}
			rows, e := s.Pending(ctx, "start", 0)
			if e != nil || len(rows) > 0 {
				t.Fatalf("stale delivery %v %v", rows, e)
			}
			if e = s.Ack(ctx, eventID, "done"); e == nil {
				t.Fatal("stale ack")
			}
			_, e = s.DB.Exec("UPDATE dispatch_reservations SET generation=1,reserved_by='dispatcher',worker_thread_id='worker-session'")
			if e != nil {
				t.Fatal(e)
			}
		})
	}
	other := s
	other.Recipient = "other"
	if rows, e := other.Pending(ctx, "start", 0); e != nil || len(rows) > 0 {
		t.Fatal("other recipient", e)
	}
	other = s
	other.Repo = "other"
	if rows, e := other.Pending(ctx, "start", 0); e != nil || len(rows) > 0 {
		t.Fatal("other repo", e)
	}
}

func TestUnacknowledgedDeliveryRetriesAfterDeadline(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	post(t, s, eventID, "worker")
	if e := s.Discover(ctx); e != nil {
		t.Fatal(e)
	}
	if e := s.Delivered(ctx, eventID, "start"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB.Exec("UPDATE terminal_event_receipts SET delivered_at=?", time.Now().Add(-3*time.Minute).Unix()); e != nil {
		t.Fatal(e)
	}
	rows, e := s.Pending(ctx, "start", 2*time.Minute)
	if e != nil || len(rows) != 1 {
		t.Fatal(fmt.Sprint(rows), e)
	}
}
