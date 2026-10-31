package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"
)

func stateLedger(t *testing.T, reservationState, itemStatus string, extra ...string) *Store {
	t.Helper()
	now := time.Unix(1000, 0)
	s := newDispatchStore(t, &now)
	stmts := append([]string{
		`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES('repo-test','D','github:o/r#1','owner',1,1,0,'` + reservationState + `',1,'native','','T')`,
		`INSERT INTO items(repo_id,item_id,title,type,status,path,updated_at) VALUES('repo-test','T','t','task','` + itemStatus + `','/f/T.md',1)`,
		`INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES('repo-test','T','worker',2,3,'done')`,
	}, extra...)
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	return s
}

func hasDrift(d *DeliveryStatus, code string) bool {
	for _, c := range d.Drift {
		if c == code {
			return true
		}
	}
	return false
}

func TestDeliveryDistinguishesEndedExecutionFromACompletedIssue(t *testing.T) {
	open := IssueObservation{Source: "github:o/r#1", State: "OPEN", VerifiedAt: 1000}
	closed := IssueObservation{Source: "github:o/r#1", State: "CLOSED", VerifiedAt: 1000}
	handled := []string{`INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id,processed_at) VALUES('repo-test','owner','h','D',1,'native','T','handoff-complete',1,1,50)`}
	cases := []struct {
		name, res, item string
		extra           []string
		obs             IssueObservation
		phase, cont     string
		drift           string
		owner           string
	}{
		{"importer phase delivered: item done, claims released, handled outcome, reservation still dispatched", "dispatched", "done", handled, open, "ended", "awaiting-owner", "worker-phase-ended-reservation-active", "owner"},
		{"blocked after a limited phase, handled outcome, reservation still dispatched", "dispatched", "blocked", handled, open, "ended", "awaiting-owner", "worker-phase-ended-reservation-active", "owner"},
		{"blocked item with a completed reservation (partial delivery mislabelled)", "completed", "blocked", nil, open, "ended", "awaiting-owner", "blocked-item-completed-reservation", "owner"},
		{"pre-closeout window: item done, claim released, no handled outcome", "dispatched", "done", nil, open, "live", "in-progress", "", "worker"},
		{"live Worker still holds its claim", "dispatched", "in_progress", []string{`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,state,generation) VALUES('repo-test','T','worker',2,2,'held',1)`}, open, "live", "in-progress", "", "worker"},
		{"issue closed", "completed", "done", nil, closed, "ended", "none", "", "owner"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := stateLedger(t, c.res, c.item, c.extra...)
			got, err := s.Delivery(context.Background(), "D", c.obs)
			if err != nil {
				t.Fatal(err)
			}
			if got.WorkerPhase != c.phase || got.Continuation != c.cont {
				t.Fatalf("phase=%s continuation=%s", got.WorkerPhase, got.Continuation)
			}
			if c.drift != "" && !hasDrift(got, c.drift) {
				t.Fatalf("missing %s: %+v", c.drift, got.Drift)
			}
			if c.owner == "owner" && got.NextOwner != "owner" {
				t.Fatalf("a finished Worker must not be the next owner: %s", got.NextOwner)
			}
			if got.Acceptance.Coverage != "unrecorded" || got.Acceptance.GoalReached {
				t.Fatalf("goal must not be inferred: %+v", got.Acceptance)
			}
		})
	}
}

func TestDeliveryNeverShowsPartialCoverageAsTheGoalReached(t *testing.T) {
	closed := IssueObservation{Source: "github:o/r#1", State: "CLOSED", VerifiedAt: 1000}
	for coverage, reached := range map[string]bool{"source-complete": false, "diagnostic-archived": false, "limited-sample": false, "full": true} {
		s := stateLedger(t, "completed", "done", `INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('repo-test',5,'worker','T','acceptance-coverage','{"reservation":"D","generation":1,"coverage":"`+coverage+`","ref":"report.md"}','[]','normal')`)
		got, err := s.Delivery(context.Background(), "D", closed)
		if err != nil || got.Acceptance.Coverage != coverage || got.Acceptance.GoalReached != reached || got.Acceptance.Ref != "report.md" {
			t.Fatalf("%s: %+v %v", coverage, got.Acceptance, err)
		}
	}
	// A coverage recorded for another generation or an unknown value is ignored.
	s := stateLedger(t, "completed", "done",
		`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('repo-test',5,'w','T','acceptance-coverage','{"reservation":"D","generation":2,"coverage":"full"}','[]','normal')`,
		`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('repo-test',6,'w','T','acceptance-coverage','{"reservation":"D","generation":1,"coverage":"everything"}','[]','normal')`)
	got, _ := s.Delivery(context.Background(), "D", closed)
	if got.Acceptance.Coverage != "unrecorded" || got.Acceptance.GoalReached {
		t.Fatalf("%+v", got.Acceptance)
	}
}

func TestDeliveryProjectsHandledAndUnroutedOperationExits(t *testing.T) {
	exit := `INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('repo-test',10,'worker','T','owned-operation','{"request":{"reservation":"D","generation":1,"worker_session":"native","request_key":"ci"},"state":"exited","attestation_id":3}','[]','normal')`
	// Exit with no routed receipt: drift, never silently "nothing pending".
	s := stateLedger(t, "completed", "done", exit)
	got, err := s.Delivery(context.Background(), "D", IssueObservation{})
	if err != nil || !hasDrift(got, "operation-exit-unrouted") {
		t.Fatalf("%+v %v", got.Drift, err)
	}
	// Routed and handled: the note is visible, no drift.
	s = stateLedger(t, "completed", "done", exit,
		`INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id,processed_at,processed_note) SELECT 'repo-test','worker','e','D',1,'native','T','operation-exit',id,id,99,'checkpoint: CI green, next phase acceptance' FROM messages WHERE kind='owned-operation'`)
	got, err = s.Delivery(context.Background(), "D", IssueObservation{})
	if err != nil || hasDrift(got, "operation-exit-unrouted") || got.OperationHandledAt != 99 || !strings.Contains(got.OperationHandledNote, "next phase") {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestPartialRetirementRecordsCoverageAndRejectsUnknownValues(t *testing.T) {
	s, q := partialFixture(t)
	bad := q
	bad.Coverage = "mostly"
	if _, err := s.RetirePartial(context.Background(), q.Expected.ReservedBy, bad); err == nil {
		t.Fatal("unknown coverage accepted")
	}
	q.Coverage, q.CoverageRef = "diagnostic-archived", "evidence-1717.json"
	full := q
	full.Coverage = "full"
	if _, err := s.RetirePartial(context.Background(), q.Expected.ReservedBy, full); err == nil {
		t.Fatal("partial retirement recorded full coverage")
	}
	if _, err := s.RetirePartial(context.Background(), q.Expected.ReservedBy, q); err != nil {
		t.Fatal(err)
	}
	got, err := s.Delivery(context.Background(), q.Expected.ItemID, IssueObservation{})
	if err != nil || got.Acceptance.Coverage != "diagnostic-archived" || got.Acceptance.GoalReached || got.Continuation != "partial-retired" {
		t.Fatalf("%+v %v", got, err)
	}
}
