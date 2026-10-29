package dispatch

import (
	"context"
	"testing"
	"time"
)

func partialFixture(t *testing.T) (*Store, PartialRetirement) {
	t.Helper()
	now := time.Unix(1000, 0)
	s := newDispatchStore(t, &now)
	_, err := s.db.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES('repo-test','D','github:o/r#1','owner',1,1,0,'completed',1,'native','','T');
 INSERT INTO items(repo_id,item_id,title,type,status,path,updated_at) VALUES('repo-test','T','test','task','blocked','/fixture/T.md',1000);
 INSERT INTO messages(id,repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(1,'repo-test',2,'worker','T','stuck','partial','[]','normal');
 INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('repo-test','D',1,'T',1,1,'hold','remaining diagnosis','worker');
 INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id,delivered_at,processed_at) VALUES('repo-test','owner','event','D',1,'native','T','blocked',1,1,2,3);`)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(context.Background(), "D")
	if err != nil {
		t.Fatal(err)
	}
	return s, PartialRetirement{Expected: *r, Issue: IssueObservation{"github:o/r#1", "OPEN", 1000}, BlockedOutcome: 1}
}

func TestPartialRetirementAllowsSupportedContinuation(t *testing.T) {
	s, q := partialFixture(t)
	ctx := context.Background()
	for range 2 {
		r, err := s.RetirePartial(ctx, "owner", q)
		if err != nil || r.State != "failed" {
			t.Fatalf("retire %+v %v", r, err)
		}
	}
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM messages WHERE kind='partial-retirement'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit duplicates %d", n)
	}
	r, err := s.Reserve(ctx, "D", "github:o/r#1", "owner", "continue diagnosis", time.Minute)
	if err != nil || r.Generation != 2 {
		t.Fatalf("cannot continue: %+v %v", r, err)
	}
	if _, err = s.RetirePartial(ctx, "owner", q); err == nil {
		t.Fatal("old request modified new generation")
	}
}
func TestPartialRetirementRejectsUnsafeAndStaleInputs(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		change    func(*PartialRetirement)
	}{
		{"other-primary-owner", `INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo-test','T','intruder',1,1)`, nil},
		{"retained-env", `INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo-test','ENV-001','worker',1,1)`, nil},
		{"unhandled-event", `UPDATE terminal_event_receipts SET processed_at=0`, nil},
		{"other-worker", `UPDATE messages SET agent_id='intruder'`, nil},
		{"not-held", `UPDATE dispatch_decisions SET action='proceed'; UPDATE dispatch_reservations SET state='dispatched'`, func(q *PartialRetirement) { q.Expected.State = "dispatched" }},
		{"item-done", `UPDATE items SET status='done'`, nil},
		{"changed-generation", `UPDATE dispatch_reservations SET generation=2`, nil},
		{"closed-issue", "", func(q *PartialRetirement) { q.Issue.State = "CLOSED" }},
		{"stale-observation", "", func(q *PartialRetirement) { q.Issue.VerifiedAt = 1 }},
		{"other-repo", "", func(q *PartialRetirement) { q.Issue.Source = "github:other/r#1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, q := partialFixture(t)
			if tc.sql != "" {
				if _, err := s.db.Exec(tc.sql); err != nil {
					t.Fatal(err)
				}
			}
			if tc.change != nil {
				tc.change(&q)
			}
			if _, err := s.RetirePartial(context.Background(), "owner", q); err == nil {
				t.Fatal("unsafe retirement accepted")
			}
			var n int
			_ = s.db.QueryRow(`SELECT count(*) FROM messages WHERE kind='partial-retirement'`).Scan(&n)
			if n != 0 {
				t.Fatal("rejected request wrote audit")
			}
		})
	}
	s, q := partialFixture(t)
	if _, err := s.RetirePartial(context.Background(), "intruder", q); err == nil {
		t.Fatal("wrong owner accepted")
	}
}

func TestPartialCompletedExecutionNeedsNoNewHoldDecision(t *testing.T) {
	s, q := partialFixture(t)
	if _, err := s.db.Exec(`UPDATE dispatch_decisions SET action='proceed'`); err != nil {
		t.Fatal(err)
	}
	r, err := s.RetirePartial(context.Background(), "owner", q)
	if err != nil || r.State != "failed" {
		t.Fatalf("terminal execution cannot reconcile original blocked result: %+v %v", r, err)
	}
}

func TestPartialRetirementRollsBackAuditWhenUpdateIsNotApplied(t *testing.T) {
	s, q := partialFixture(t)
	if _, err := s.db.Exec(`CREATE TRIGGER suppress_retirement BEFORE UPDATE ON dispatch_reservations BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetirePartial(context.Background(), "owner", q); err == nil {
		t.Fatal("zero-row retirement reported success")
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM messages WHERE kind='partial-retirement'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("unapplied transition left audit: %d %v", n, err)
	}
}
