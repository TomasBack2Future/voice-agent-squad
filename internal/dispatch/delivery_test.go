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
