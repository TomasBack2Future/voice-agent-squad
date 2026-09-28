package dispatch

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/claims"
	"github.com/zsiec/squad/internal/terminalevents"
)

func takeoverFixture(t *testing.T) (*Store, TakeoverRequest) {
	t.Helper()
	now := time.Unix(10000, 0)
	s := newDispatchStore(t, &now)
	_, e := s.db.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES('repo-test','D','github:o/r#1','old-dispatcher',1,1,0,'dispatched',1,'old-session','','T');
 INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,state,generation) VALUES('repo-test','T','old-worker',2,2,'held',1);
 INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('repo-test','D',1,'T',2,4,'hold','merge paused','old-worker');
 INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id) VALUES('repo-test','old-dispatcher','old-event','D',1,'old-session','T','decision-request',3,3);`)
	if e != nil {
		t.Fatal(e)
	}
	return s, TakeoverRequest{Reservation: "D", ExpectedDispatcher: "old-dispatcher", DispatcherSession: "old-dispatcher-session", ExpectedWorkerSession: "old-session", ExpectedGeneration: 1, NewDispatcher: "new-dispatcher", NewWorkerSession: "new-session", ExpectedHolder: "old-worker", ExpectedClaimGeneration: 1, NewHolder: "new-worker", ConfirmDispatcherStopped: true, ConfirmWorkerStopped: true, Reason: "quota exhausted", Evidence: "exact old processes stopped; external workflows terminal"}
}
func TestTakeoverFencesOldWriterAndPreservesHold(t *testing.T) {
	s, q := takeoverFixture(t)
	ctx := context.Background()
	r, e := s.Takeover(ctx, "operator", q)
	if e != nil {
		t.Fatal(e)
	}
	if r.Reservation.Generation != 2 || r.ClaimGeneration != 2 || len(r.PendingEvents) != 1 || r.AuditMessageID == 0 {
		t.Fatalf("%+v", r)
	}
	c := claims.New(s.db, s.repoID, nil)
	if e = c.WorkerHeartbeat(ctx, "old-worker", "D", "old-session", 1, true); e == nil {
		t.Fatal("old heartbeat accepted")
	}
	if e = c.WorkerHeartbeat(ctx, "new-worker", "D", "new-session", 2, true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Close(ctx, "D", "old-dispatcher", "completed", "stale", 1); e == nil {
		t.Fatal("old owner closed")
	}
	ev := terminalevents.Store{DB: s.db, Repo: s.repoID, Recipient: "new-worker"}
	d, e := ev.CurrentDecision(ctx, "D", 2, "new-session")
	if e != nil || d.Action != "hold" || d.WorkerAgent != "new-worker" || d.Revision != 2 {
		t.Fatalf("%+v %v", d, e)
	}
	if _, e = s.Takeover(ctx, "operator", q); e == nil {
		t.Fatal("replay accepted")
	}
}
func TestTakeoverRejectsWithoutPartialWrites(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*TakeoverRequest)
		sql    string
	}{
		{"wrong generation", func(q *TakeoverRequest) { q.ExpectedGeneration = 2 }, ""},
		{"wrong dispatcher", func(q *TakeoverRequest) { q.ExpectedDispatcher = "other" }, ""},
		{"wrong session", func(q *TakeoverRequest) { q.ExpectedWorkerSession = "other" }, ""},
		{"wrong claim", func(q *TakeoverRequest) { q.ExpectedClaimGeneration = 2 }, ""},
		{"wrong holder", func(q *TakeoverRequest) { q.ExpectedHolder = "other" }, ""},
		{"not stopped", func(q *TakeoverRequest) { q.ConfirmWorkerStopped = false }, ""},
		{"no evidence", func(q *TakeoverRequest) { q.Evidence = "" }, ""},
		{"recent renewal", nil, "UPDATE claims SET last_touch=9999"},
		{"protected lock", nil, "INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo-test','ENV-1','old-worker',2,2)"},
		{"pinned execution", nil, "INSERT INTO execution_authorizations(repo_id,id,item_id,holder,generation,binding,state,created_at,updated_at) VALUES('repo-test','pin','T','old-worker',1,'{}','active',1,1)"},
		{"fresh agent", nil, "INSERT INTO agents(id,repo_id,display_name,started_at,last_tick_at,status) VALUES('old-dispatcher','repo-test','D',1,9999,'active')"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, q := takeoverFixture(t)
			if tc.mutate != nil {
				tc.mutate(&q)
			}
			if tc.sql != "" {
				if _, e := s.db.Exec(tc.sql); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := s.Takeover(context.Background(), "operator", q); e == nil {
				t.Fatal("unsafe takeover accepted")
			}
			r, e := s.Get(context.Background(), "D")
			if e != nil || r.Generation != 1 || r.ReservedBy != "old-dispatcher" {
				t.Fatalf("partial reservation write: %+v %v", r, e)
			}
			var owner string
			var audits, history int
			if e = s.db.QueryRow("SELECT agent_id FROM claims WHERE item_id='T'").Scan(&owner); e != nil {
				t.Fatal(e)
			}
			if e = s.db.QueryRow("SELECT count(*) FROM messages WHERE kind='session-takeover'").Scan(&audits); e != nil {
				t.Fatal(e)
			}
			if e = s.db.QueryRow("SELECT count(*) FROM claim_history").Scan(&history); e != nil {
				t.Fatal(e)
			}
			if owner != "old-worker" || audits != 0 || history != 0 {
				t.Fatalf("partial write: %s %d %d", owner, audits, history)
			}
		})
	}
}
func TestTakeoverConcurrentExactlyOneWinner(t *testing.T) {
	s, q := takeoverFixture(t)
	ch := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			a := q
			a.NewHolder = fmt.Sprint("new-", i)
			_, e := s.Takeover(context.Background(), "operator", a)
			ch <- e
		}(i)
	}
	wins := 0
	for i := 0; i < 2; i++ {
		if <-ch == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("wins=%d", wins)
	}
}
func TestDispatcherCustodyKeepsWorkerFenceAndReroutesPending(t *testing.T) {
	s, q := takeoverFixture(t)
	q.NewWorkerSession = ""
	q.NewHolder = ""
	q.ExpectedHolder = ""
	q.ExpectedClaimGeneration = 0
	q.ConfirmWorkerStopped = false
	r, e := s.Takeover(context.Background(), "operator", q)
	if e != nil {
		t.Fatal(e)
	}
	if r.Reservation.Generation != 1 || r.Reservation.WorkerThreadID != "old-session" {
		t.Fatalf("live worker fence changed: %+v", r)
	}
	var recipient string
	if e = s.db.QueryRow("SELECT recipient FROM terminal_event_receipts").Scan(&recipient); e != nil {
		t.Fatal(e)
	}
	if recipient != "new-dispatcher" {
		t.Fatal(recipient)
	}
}
