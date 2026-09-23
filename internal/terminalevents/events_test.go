package terminalevents

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/dispatch"
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
	_, e = db.Exec(`INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES('repo','TASK','worker',1,2,'done')`)
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

// Regression: the old Worker posted both the outcome and event to global.
func TestLegacyGlobalOutcomeSurvivesCompletedReservation(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	if _, e := s.DB.Exec("UPDATE messages SET thread='global'; UPDATE dispatch_reservations SET state='completed'"); e != nil {
		t.Fatal(e)
	}
	post(t, s, eventID, "worker")
	if _, e := s.DB.Exec("UPDATE messages SET thread='global'"); e != nil {
		t.Fatal(e)
	}
	if e := s.Discover(ctx); e != nil {
		t.Fatal(e)
	}
	events, e := s.Pending(ctx, "fresh", 0)
	if e != nil || len(events) != 1 {
		t.Fatalf("lost legacy global event %v %v", events, e)
	}
	if e = s.Delivered(ctx, eventID, "fresh"); e != nil {
		t.Fatal(e)
	}
	if e = s.Ack(ctx, eventID, "already reconciled; no duplicate dispatch"); e != nil {
		t.Fatal(e)
	}
}

func TestGlobalEventNeedsCurrentTaskCustody(t *testing.T) {
	for _, change := range []string{"DELETE FROM claim_history", "UPDATE claim_history SET claimed_at=0", "UPDATE messages SET repo_id='other'", "UPDATE messages SET thread='OTHER'"} {
		t.Run(change, func(t *testing.T) {
			s := fixture(t)
			post(t, s, eventID, "worker")
			if _, e := s.DB.Exec(change); e != nil {
				t.Fatal(e)
			}
			if e := s.Discover(context.Background()); e != nil {
				t.Fatal(e)
			}
			events, e := s.Pending(context.Background(), "fresh", 0)
			if e != nil || len(events) != 0 {
				t.Fatalf("accepted unrelated evidence %v %v", events, e)
			}
		})
	}
}

func TestDoneAndAddressedAskWakeWithoutHandwrittenCallback(t *testing.T) {
	for _, tc := range []struct{ kind, mentions, want string }{{"done", "[]", "reconcile-needed"}, {"ask", `["dispatcher"]`, "decision-request"}, {"ask", `["other"]`, ""}, {"progress", `["dispatcher"]`, ""}} {
		t.Run(tc.kind+tc.mentions, func(t *testing.T) {
			s := fixture(t)
			ctx := context.Background()
			if _, e := s.DB.Exec("UPDATE messages SET kind=?,mentions=?", tc.kind, tc.mentions); e != nil {
				t.Fatal(e)
			}
			if e := s.Discover(ctx); e != nil {
				t.Fatal(e)
			}
			events, e := s.Pending(ctx, "fresh", 0)
			if e != nil {
				t.Fatal(e)
			}
			if tc.want == "" {
				if len(events) != 0 {
					t.Fatal(events)
				}
				return
			}
			if len(events) != 1 || events[0].Kind != tc.want {
				t.Fatal(events)
			}
		})
	}
}

func TestPublishDecisionRoundTripAndFencing(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	if _, e := s.DB.Exec(`INSERT INTO claims(item_id,repo_id,agent_id,claimed_at,last_touch) VALUES('TASK','repo','worker',1,1)`); e != nil {
		t.Fatal(e)
	}
	q := PublishRequest{"DISPATCH-1", 1, "worker-session", "decision-request", 1}
	publisher := s
	publisher.Recipient = ""
	id, e := publisher.Publish(ctx, "worker", q)
	if e != nil {
		t.Fatal(e)
	}
	if again, e := publisher.Publish(ctx, "worker", q); e != nil || again != id {
		t.Fatalf("idempotence %v %s", e, again)
	}
	if _, e = publisher.Publish(ctx, "other", q); e == nil {
		t.Fatal("foreign sender accepted")
	}
	post(t, s, "decision d2 recorded in canonical Issue", "dispatcher")
	var outcome int64
	if e = s.DB.QueryRow("SELECT max(id) FROM messages").Scan(&outcome); e != nil {
		t.Fatal(e)
	}
	q.Kind = "decision-resolved"
	q.OutcomeID = outcome
	reply, e := publisher.Publish(ctx, "dispatcher", q)
	if e != nil {
		t.Fatal(e)
	}
	worker := s
	worker.Recipient = "worker"
	events, e := worker.Pending(ctx, "worker-start", 0)
	if e != nil || len(events) != 1 || events[0].ID != reply {
		t.Fatalf("reply not routed %v %v", events, e)
	}
	if e = worker.Delivered(ctx, reply, "worker-start"); e != nil {
		t.Fatal(e)
	}
	if e = worker.Ack(ctx, reply, "read d2; continuing existing assignment"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec("UPDATE dispatch_reservations SET generation=2"); e != nil {
		t.Fatal(e)
	}
	if _, e = publisher.Publish(ctx, "dispatcher", q); e == nil {
		t.Fatal("stale reply accepted")
	}
	events, e = s.Pending(ctx, "dispatcher-start", 0)
	if e != nil || len(events) != 0 {
		t.Fatalf("stale request delivered %v %v", events, e)
	}
}

func TestConcurrentPublishIsIdempotent(t *testing.T) {
	s := fixture(t)
	s.Recipient = ""
	ctx := context.Background()
	results := make(chan error, 8)
	for range 8 {
		go func() {
			_, e := s.Publish(ctx, "worker", PublishRequest{"DISPATCH-1", 1, "worker-session", "blocked", 1})
			results <- e
		}()
	}
	for range 8 {
		if e := <-results; e != nil {
			t.Fatal(e)
		}
	}
	var n int
	if e := s.DB.QueryRow("SELECT count(*) FROM terminal_event_receipts").Scan(&n); e != nil || n != 1 {
		t.Fatalf("duplicates %d %v", n, e)
	}
}

func TestLegacyNullMessageMetadataDoesNotStopReceiver(t *testing.T) {
	s := fixture(t)
	post(t, s, eventID, "worker")
	if _, e := s.DB.Exec("UPDATE messages SET mentions=NULL; UPDATE messages SET kind='done',body=NULL WHERE id=1"); e != nil {
		t.Fatal(e)
	}
	if e := s.Discover(context.Background()); e != nil {
		t.Fatal(e)
	}
	events, e := s.Pending(context.Background(), "fresh", 0)
	if e != nil || len(events) != 2 {
		t.Fatalf("legacy NULL disables receiver %v %v", events, e)
	}
}

func TestContinuationRestoresDecisionRouteWithoutRelaxingCustody(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	_, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,intent,long,state,generation) VALUES('repo','NEXT','worker',3,3,'continuation',1,'held',1)`)
	if err != nil {
		t.Fatal(err)
	}
	d := dispatch.New(s.DB, "repo", nil)
	for _, actor := range []string{"other"} {
		if _, err = d.Continue(ctx, "DISPATCH-1", actor, "TASK", "NEXT", "worker-session", 1); err == nil {
			t.Fatal("wrong dispatcher accepted")
		}
	}
	if _, err = d.Continue(ctx, "DISPATCH-1", "dispatcher", "TASK", "NEXT", "wrong-session", 1); err == nil {
		t.Fatal("wrong Worker accepted")
	}
	if _, err = d.Continue(ctx, "DISPATCH-1", "dispatcher", "TASK", "NEXT", "worker-session", 2); err == nil {
		t.Fatal("stale generation accepted")
	}
	if _, err = d.Continue(ctx, "DISPATCH-1", "dispatcher", "TASK", "NEXT", "worker-session", 1); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Continue(ctx, "DISPATCH-1", "dispatcher", "TASK", "NEXT", "worker-session", 1); err == nil {
		t.Fatal("stale from item accepted")
	}
	if _, err = s.Publish(ctx, "worker", PublishRequest{"DISPATCH-1", 1, "worker-session", "issue-closed", 1}); err == nil {
		t.Fatal("old item event accepted")
	}
	_, err = s.DB.Exec(`INSERT INTO messages(id,repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(2,'repo',4,'worker','NEXT','ask','need decision','["dispatcher"]','normal'),(3,'repo',5,'dispatcher','NEXT','fyi','resolved','[]','normal')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Publish(ctx, "worker", PublishRequest{"DISPATCH-1", 1, "worker-session", "decision-request", 2}); err != nil {
		t.Fatal(err)
	}
	s.Recipient = "worker"
	if _, err = s.Publish(ctx, "dispatcher", PublishRequest{"DISPATCH-1", 1, "worker-session", "decision-resolved", 3}); err != nil {
		t.Fatal(err)
	}
}

func TestContinuationRejectsUnprovenCustody(t *testing.T) {
	for _, mode := range []string{"different-actor", "original-held", "no-continuation"} {
		t.Run(mode, func(t *testing.T) {
			s := fixture(t)
			ctx := context.Background()
			if mode != "no-continuation" {
				actor := "worker"
				if mode == "different-actor" {
					actor = "other"
				}
				_, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,intent,long,state,generation) VALUES('repo','NEXT',?,3,3,'continuation',1,'held',1)`, actor)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "original-held" {
				_, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,intent,long,state,generation) VALUES('repo','TASK','worker',3,3,'old',1,'held',1)`)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := dispatch.New(s.DB, "repo", nil).Continue(ctx, "DISPATCH-1", "dispatcher", "TASK", "NEXT", "worker-session", 1); err == nil {
				t.Fatal("unproven continuation accepted")
			}
			row, err := dispatch.New(s.DB, "repo", nil).Get(ctx, "DISPATCH-1")
			if err != nil || row.CanonicalItemID != "TASK" {
				t.Fatal("failed transition mutated reservation")
			}
		})
	}
}
