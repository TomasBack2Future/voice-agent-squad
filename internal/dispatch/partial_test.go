package dispatch

import (
	"context"
	"fmt"
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

func TestPartialRetirementWaitsForWorkerHoldReadback(t *testing.T) {
	for _, delivered := range []int{0, 4} {
		t.Run(map[int]string{0: "not-delivered", 4: "delivered-not-handled"}[delivered], func(t *testing.T) {
			s, q := partialFixture(t)
			ctx := context.Background()
			if _, err := s.db.Exec(`UPDATE dispatch_reservations SET state='dispatched';
 INSERT INTO messages(id,repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(2,'repo-test',4,'owner','T','say','hold for partial handoff','[]','normal');
 UPDATE dispatch_decisions SET revision=2,outcome_id=2;
 INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id,delivered_at,processed_at) VALUES('repo-test','worker','hold','D',1,'native','T','decision-resolved',2,2,?,0)`, delivered); err != nil {
				t.Fatal(err)
			}
			q.Expected.State = "dispatched"
			if _, err := s.RetirePartial(ctx, "owner", q); err == nil {
				t.Fatal("retired before the original Worker could acknowledge its hold")
			}
			current, err := s.Get(ctx, "D")
			if err != nil || current.State != "dispatched" {
				t.Fatalf("rejected retirement changed custody: %+v %v", current, err)
			}
			var audits int
			if err := s.db.QueryRow(`SELECT count(*) FROM messages WHERE kind='partial-retirement'`).Scan(&audits); err != nil || audits != 0 {
				t.Fatalf("rejected retirement left an audit: %d %v", audits, err)
			}
			if _, err := s.db.Exec(`UPDATE terminal_event_receipts SET delivered_at=4,processed_at=5 WHERE event_id='hold'`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RetirePartial(ctx, "owner", q); err != nil {
				t.Fatalf("handled hold cannot retire: %v", err)
			}
		})
	}
}

func TestPartialRetirementIgnoresSupersededDecisionReceipts(t *testing.T) {
	s, q := partialFixture(t)
	if _, err := s.db.Exec(`INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id,delivered_at,processed_at) VALUES('repo-test','worker','obsolete','D',1,'native','T','decision-resolved',99,99,4,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetirePartial(context.Background(), "owner", q); err != nil {
		t.Fatalf("obsolete decision stranded retirement: %v", err)
	}
	var handled int
	if err := s.db.QueryRow(`SELECT processed_at FROM terminal_event_receipts WHERE event_id='obsolete'`).Scan(&handled); err != nil || handled != 0 {
		t.Fatalf("retirement manufactured an acknowledgement: %d %v", handled, err)
	}
}

// handedOffPartialFixture reproduces the production shape: the previous
// Dispatcher ("old") processed the Worker's blocked receipt, then handed its
// whole cohort to the current owner through the atomic Handoff.
func handedOffPartialFixture(t *testing.T) (*Store, PartialRetirement) {
	t.Helper()
	s, q := partialFixture(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`UPDATE terminal_event_receipts SET recipient='old' WHERE event_id='event';
 UPDATE dispatch_decisions SET worker_agent='worker'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO dispatch_retired_controllers(repo_id,actor,retired_at,successor) VALUES('repo-test','old',500,'owner');
 INSERT INTO dispatch_handoffs(repo_id,request_id,request_sha256,receipt) VALUES('repo-test','h1','x','{"old_actor":"old","new_actor":"owner","epoch":2,"request_id":"h1"}')`); err != nil {
		t.Fatal(err)
	}
	_ = ctx
	return s, q
}

func TestPartialRetirementReusesPredecessorReceiptAfterHandoff(t *testing.T) {
	s, q := handedOffPartialFixture(t)
	ctx := context.Background()
	r, err := s.RetirePartial(ctx, "owner", q)
	if err != nil || r.State != "failed" {
		t.Fatalf("legitimate successor cannot reuse the handled receipt: %+v %v", r, err)
	}
	var recipient string
	var processed int
	if err = s.db.QueryRow(`SELECT recipient,processed_at FROM terminal_event_receipts WHERE event_id='event'`).Scan(&recipient, &processed); err != nil || recipient != "old" || processed != 3 {
		t.Fatalf("original recipient/processing record was rewritten: %q %d %v", recipient, processed, err)
	}
	var evidence string
	if err = s.db.QueryRow(`SELECT body FROM messages WHERE kind='partial-retirement-receipt'`).Scan(&evidence); err != nil || evidence == "" {
		t.Fatalf("handoff-derived receipt use left no audit: %v", err)
	}
	if _, err = s.Reserve(ctx, "D", "github:o/r#1", "owner", "continue", time.Minute); err != nil {
		t.Fatalf("cannot open next generation: %v", err)
	}
}

func TestPartialRetirementRejectsReceiptWithoutDurableHandoff(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"no-retired-record", `DELETE FROM dispatch_retired_controllers`},
		{"no-handoff-receipt", `DELETE FROM dispatch_handoffs`},
		{"handoff-to-other-actor", `UPDATE dispatch_handoffs SET receipt='{"old_actor":"old","new_actor":"other"}'`},
		{"successor-is-other-actor", `UPDATE dispatch_retired_controllers SET successor='other'`},
		{"unrelated-actor-name-only", `UPDATE terminal_event_receipts SET recipient='stranger'`},
		{"retired-before-reservation", `UPDATE dispatch_retired_controllers SET retired_at=0`},
		{"unhandled", `UPDATE terminal_event_receipts SET processed_at=0`},
		{"old-generation", `UPDATE terminal_event_receipts SET generation=0`},
		{"other-reservation", `UPDATE terminal_event_receipts SET reservation_key='OTHER'`},
		{"other-worker-native", `UPDATE terminal_event_receipts SET worker_session='another-native'`},
		{"other-item", `UPDATE terminal_event_receipts SET item_id='OTHER'`},
		{"other-outcome", `UPDATE terminal_event_receipts SET outcome_id=2`},
		{"not-blocked-kind", `UPDATE terminal_event_receipts SET kind='decision-request'`},
		{"worker-still-claims", `INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo-test','ENV-001','worker',1,1)`},
		{"delivered-item", `UPDATE items SET status='done'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, q := handedOffPartialFixture(t)
			if _, err := s.db.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RetirePartial(context.Background(), "owner", q); err == nil {
				t.Fatal("receipt reused without a valid durable handoff")
			}
			var n int
			_ = s.db.QueryRow(`SELECT count(*) FROM messages WHERE kind LIKE 'partial-retirement%'`).Scan(&n)
			if n != 0 {
				t.Fatalf("rejected request wrote %d audit rows", n)
			}
		})
	}
}

func TestPartialRetirementAcceptsReleasedOpenItemOnlyWithWorkerReleaseEvidence(t *testing.T) {
	release := `INSERT INTO messages(id,repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(5,'repo-test',5,'%s','T','release','released T','[]','normal')`
	for _, tc := range []struct {
		name, setup string
		ok          bool
	}{
		{"worker-released-after-blocked", fmt.Sprintf(release, "worker"), true},
		{"open-without-release", ``, false},
		{"released-by-someone-else", fmt.Sprintf(release, "intruder"), false},
		{"finished-then-released", `INSERT INTO messages(id,repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(6,'repo-test',5,'worker','T','done','done T','[]','normal'),(7,'repo-test',5,'worker','T','release','released T (done)','[]','normal')`, false},
		{"release-before-blocked", `INSERT INTO messages(id,repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(0,'repo-test',1,'worker','T','release','early','[]','normal')`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, q := handedOffPartialFixture(t)
			stmt := `UPDATE items SET status='open';` + tc.setup
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
			_, err := s.RetirePartial(context.Background(), "owner", q)
			if tc.ok != (err == nil) {
				t.Fatalf("ok=%v err=%v", tc.ok, err)
			}
		})
	}
	// An open item with a live claim stays rejected even with release evidence.
	s, q := handedOffPartialFixture(t)
	if _, err := s.db.Exec(`UPDATE items SET status='open';` + fmt.Sprintf(release, "worker") + `;INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo-test','T','someone',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetirePartial(context.Background(), "owner", q); err == nil {
		t.Fatal("reclaimed item accepted")
	}
}
