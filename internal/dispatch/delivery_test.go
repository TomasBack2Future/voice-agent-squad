package dispatch

import (
	"context"
	"testing"
	"time"
)

func TestDeliveryProjectionSeparatesIssueAndExecution(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	_, err := s.Reserve(ctx, "D", "github:o/r#1", "owner", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('repo-test','D',1,'T',1,1,'proceed','old execution scope','worker')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE dispatch_reservations SET canonical_item_id='T',state='completed'; INSERT INTO items(repo_id,item_id,title,type,status,path,updated_at) VALUES('repo-test','T','test','task','blocked','/fixture/T.md',1000)`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		obs          IssueObservation
		state, drift string
	}{{IssueObservation{}, "unknown", "issue-delivery-pending"}, {IssueObservation{"github:o/r#1", "OPEN", 1000}, "open", "open-issue-completed-reservation"}, {IssueObservation{"github:o/r#1", "CLOSED", 600}, "unknown", "issue-observation-stale"}} {
		got, err := s.Delivery(ctx, "D", tc.obs)
		if err != nil {
			t.Fatal(err)
		}
		if got.IssueState != tc.state {
			t.Fatalf("%+v", got)
		}
		if got.NextOwner != "owner" {
			t.Fatalf("terminal reconciliation routed to former Worker: %+v", got)
		}
		found := false
		for _, d := range got.Drift {
			found = found || d == tc.drift
		}
		if !found {
			t.Fatalf("missing %s: %+v", tc.drift, got)
		}
	}
	if _, err = s.Delivery(ctx, "D", IssueObservation{"github:other/r#1", "OPEN", 1000}); err == nil {
		t.Fatal("accepted observation from another repo")
	}
	var n int
	if err = s.db.QueryRow(`SELECT count(*) FROM messages`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("read projection wrote messages: %d %v", n, err)
	}
}

func TestDeliveryProjectsRecordedOperationProvenance(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	if _, err := s.Reserve(ctx, "D", "github:o/r#1", "owner", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE dispatch_reservations SET canonical_item_id='T',worker_thread_id='native'; INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('repo-test',1000,'worker','T','owned-operation','{"request":{"reservation":"D","generation":1,"worker_session":"native","request_key":"cleanup-1","phase":"cleanup","revision":"exact-sha","resume_step":"inspect cleanup failure"},"state":"exited","attestation_id":42}','[]','normal')`); err != nil {
		t.Fatal(err)
	}
	got, err := s.Delivery(ctx, "D", IssueObservation{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "cleanup" || got.Revision != "exact-sha" || got.NextAction != "inspect cleanup failure" || got.OperationReference != "cleanup-1" || got.OperationAttestation != 42 {
		t.Fatalf("missing provenance: %+v", got)
	}
	// Another generation's metadata must never become this assignment's state.
	if _, err = s.db.Exec(`UPDATE dispatch_reservations SET generation=2`); err != nil {
		t.Fatal(err)
	}
	got, err = s.Delivery(ctx, "D", IssueObservation{})
	if err != nil || got.Phase != "unknown" || got.Revision != "unknown" || got.NextAction != "unknown" || got.OperationAttestation != 0 {
		t.Fatalf("stale provenance: %+v %v", got, err)
	}
}
