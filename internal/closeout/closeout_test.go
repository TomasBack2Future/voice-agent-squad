package closeout

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/zsiec/squad/internal/store"
)

type fakeBackend struct {
	db                    *sql.DB
	mu                    sync.Mutex
	done, comment, closed bool
	gateFailure           bool
	fail                  string
	calls                 map[string]int
}

func (b *fakeBackend) step(name string) error {
	b.calls[name]++
	if b.fail == name {
		b.fail = ""
		return fmt.Errorf("lost response")
	}
	return nil
}
func (b *fakeBackend) Done(ctx context.Context, item string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gateFailure {
		return fmt.Errorf("required verification failed")
	}
	if b.done {
		return nil
	}
	b.done = true
	_, err := b.db.ExecContext(ctx, `DELETE FROM claims; INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES('repo','T','worker',1,2,'done');UPDATE items SET status='done'`)
	if err != nil {
		return err
	}
	return b.step("done")
}
func (b *fakeBackend) Comment(context.Context, string, string, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.comment {
		return nil
	}
	b.comment = true
	return b.step("comment")
}
func (b *fakeBackend) Close(context.Context, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	return b.step("close")
}
func engineFixture(t *testing.T) (Engine, Request, *fakeBackend) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES('repo','D','github:o/r#1','dispatcher',1,1,0,'dispatched',1,'native','','T');INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','T','worker',1,1);INSERT INTO items(repo_id,item_id,title,type,status,path,updated_at) VALUES('repo','T','test','task','open','/fixture/T.md',1);`)
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBackend{db: db, calls: map[string]int{}}
	return Engine{DB: db, Repo: "repo", Actor: "worker", Backend: b}, Request{Reservation: "D", Generation: 1, WorkerSession: "native", RequestKey: "acceptance", Body: "exact revision accepted; cleanup complete"}, b
}
func TestCloseoutResumesLostResponsesWithoutDuplicatingEffects(t *testing.T) {
	for _, step := range []string{"done", "comment", "close"} {
		t.Run(step, func(t *testing.T) {
			e, q, b := engineFixture(t)
			b.fail = step
			if _, err := e.Run(context.Background(), q); err == nil {
				t.Fatal("expected interrupted result")
			}
			r, err := e.Run(context.Background(), q)
			if err != nil || len(r.Steps) != 4 {
				t.Fatalf("resume %+v %v", r, err)
			}
			if _, err = e.Run(context.Background(), q); err != nil {
				t.Fatal(err)
			}
			for k, n := range b.calls {
				if n != 1 {
					t.Fatalf("effect %s repeated %d", k, n)
				}
			}
			var n int
			_ = e.DB.QueryRow(`SELECT count(*) FROM terminal_event_receipts WHERE kind='issue-closed'`).Scan(&n)
			if n != 1 {
				t.Fatalf("outcome events %d", n)
			}
			var state string
			_ = e.DB.QueryRow(`SELECT state FROM dispatch_reservations`).Scan(&state)
			if state != "dispatched" {
				t.Fatal("Worker retired Dispatcher-owned reservation")
			}
		})
	}
}
func TestCloseoutRejectsChangedPayloadAndCustody(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		mutate    func(*Request)
	}{
		{"other-native", "", func(q *Request) { q.WorkerSession = "other" }},
		{"stale-generation", "", func(q *Request) { q.Generation = 2 }},
		{"resource-held", `INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','ENV-001','worker',1,1)`, nil},
		{"hold", `INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('repo','D',1,'T',1,1,'hold','pause','worker')`, func(q *Request) { q.ExpectedDecision = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, q, b := engineFixture(t)
			if tc.sql != "" {
				if _, err := e.DB.Exec(tc.sql); err != nil {
					t.Fatal(err)
				}
			}
			if tc.mutate != nil {
				tc.mutate(&q)
			}
			if _, err := e.Run(context.Background(), q); err == nil {
				t.Fatal("unsafe closeout accepted")
			}
			if len(b.calls) != 0 {
				t.Fatal("side effect before authorization checks")
			}
		})
	}
	e, q, _ := engineFixture(t)
	if _, err := e.Run(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	q.Body = "different acceptance"
	if _, err := e.Run(context.Background(), q); err == nil {
		t.Fatal("request-key conflict accepted")
	}
}
func TestCloseoutConcurrentRequestHasOneEffect(t *testing.T) {
	e, q, b := engineFixture(t)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = e.Run(context.Background(), q) }()
	}
	wg.Wait()
	if _, err := e.Run(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	for step, n := range b.calls {
		if n != 1 {
			t.Fatalf("%s effects=%d", step, n)
		}
	}
}
func TestMissingCustodyDoesNotCloseIssue(t *testing.T) {
	e, q, b := engineFixture(t)
	_, err := e.DB.Exec(`DELETE FROM claims;DELETE FROM claim_history`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Run(context.Background(), q); err == nil {
		t.Fatal("missing custody accepted")
	}
	if b.closed || b.comment {
		t.Fatal("external action before local eligibility")
	}
}

func TestCloseoutRejectsUnresolvedOperationAndUnhandledExit(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"started", `INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('repo',2,'worker','T','owned-operation','{"request":{"reservation":"D","generation":1,"worker_session":"native","request_key":"op"},"state":"started"}','[]','normal')`},
		{"unhandled-exit", `INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id) VALUES('repo','worker','exit','D',1,'native','T','operation-exit',1,1)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, q, b := engineFixture(t)
			if _, err := e.DB.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Run(context.Background(), q); err == nil {
				t.Fatal("unresolved operation permitted closeout")
			}
			if len(b.calls) > 0 {
				t.Fatal("external effect before operation handling")
			}
		})
	}
}

func TestFailedDoneGateKeepsExternalIssueOpen(t *testing.T) {
	e, q, b := engineFixture(t)
	b.gateFailure = true
	if _, err := e.Run(context.Background(), q); err == nil {
		t.Fatal("failed gate accepted")
	}
	if b.comment || b.closed || b.done {
		t.Fatal("external effect after failed gate")
	}
	b.gateFailure = false
	if _, err := e.Run(context.Background(), q); err != nil {
		t.Fatal(err)
	}
}

func TestSameSecondCustodyAndCompetingHistory(t *testing.T) {
	// Both fixture timestamps are 1, exercising inclusive reservation-second custody.
	e, q, _ := engineFixture(t)
	if _, err := e.Run(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	for _, at := range []int{1, 2} {
		e, q, b := engineFixture(t)
		if _, err := e.DB.Exec(`INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES('repo','T','other',?,2,'released')`, at); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Run(context.Background(), q); err == nil {
			t.Fatalf("ambiguous custody at %d accepted", at)
		}
		if len(b.calls) != 0 {
			t.Fatal("external effect before ambiguous custody rejection")
		}
	}
}
