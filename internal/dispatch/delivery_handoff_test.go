package dispatch

import (
	"context"
	"slices"
	"testing"
	"time"
)

// A Worker report that produced no formal event, and a decision request the
// Dispatcher acknowledged without a newer decision, are visible drift; owner
// activity is exposed for recovery checks.
func TestDeliveryShowsUnroutedReportsAndAcknowledgedRequests(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	if _, err := s.Reserve(ctx, "R", "github:o/r#1940", "owner", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE dispatch_reservations SET canonical_item_id='T',state='dispatched',worker_thread_id='native'`)
	exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,intent,long) VALUES('repo-test','T','worker',1000,1000,'',0)`)
	exec(`INSERT INTO messages(id,repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(10,'repo-test',1000,'owner','T','say','decision','[]','normal')`)
	exec(`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('repo-test','R',1,'T',1,10,'proceed','merge under release','worker')`)
	got, err := s.Delivery(ctx, "R", IssueObservation{})
	if err != nil || slices.Contains(got.Drift, "worker-report-without-event") {
		t.Fatalf("no report yet: %+v %v", got.Drift, err)
	}
	// The 2026-11-07 shape: "MERGED PR #1998" as a plain message.
	exec(`INSERT INTO messages(id,repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(11,'repo-test',1100,'worker','T','say','MERGED','[]','normal')`)
	exec(`INSERT INTO messages(id,repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(12,'repo-test',1200,'supervisor','T','say','note','[]','normal')`)
	if got, _ = s.Delivery(ctx, "R", IssueObservation{}); !slices.Contains(got.Drift, "worker-report-without-event") || got.LastOwnerActivityAt != 1100 {
		t.Fatalf("plain report not flagged: %+v activity=%d", got.Drift, got.LastOwnerActivityAt)
	}
	// The report becomes a formal event: no longer drift.
	exec(`INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id) VALUES('repo-test','owner','worker-terminal-v1/R/1/native/reconcile-needed/11','R',1,'native','T','reconcile-needed',11,11)`)
	if got, _ = s.Delivery(ctx, "R", IssueObservation{}); slices.Contains(got.Drift, "worker-report-without-event") {
		t.Fatalf("routed report still flagged: %+v", got.Drift)
	}
	// A request acknowledged without a newer decision stays visible.
	exec(`INSERT INTO messages(id,repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(13,'repo-test',1300,'worker','T','ask','next phase?','["owner"]','normal')`)
	exec(`INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id,delivered_at,processed_at,processed_note) VALUES('repo-test','owner','worker-terminal-v1/R/1/native/decision-request/13','R',1,'native','T','decision-request',13,13,1301,1302,'seen')`)
	if got, _ = s.Delivery(ctx, "R", IssueObservation{}); !slices.Contains(got.Drift, "decision-request-acknowledged-without-decision") {
		t.Fatalf("acknowledged request without decision hidden: %+v", got.Drift)
	}
	exec(`INSERT INTO messages(id,repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(14,'repo-test',1400,'owner','T','say','next decision','[]','normal')`)
	exec(`UPDATE dispatch_decisions SET revision=2,outcome_id=14`)
	if got, _ = s.Delivery(ctx, "R", IssueObservation{}); slices.Contains(got.Drift, "decision-request-acknowledged-without-decision") || slices.Contains(got.Drift, "worker-report-without-event") {
		t.Fatalf("answered request still flagged: %+v", got.Drift)
	}
}
