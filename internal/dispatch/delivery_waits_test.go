package dispatch

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestDeliveryProjectsStructuredWaitsAsDrift(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	for _, k := range []string{"W", "P"} {
		if _, err := s.Reserve(ctx, k, "github:o/r#"+k, "owner", "", time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE dispatch_reservations SET canonical_item_id='T-'||item_id,state='dispatched'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent,waits_for,blocks_phase,wait_deadline) VALUES('repo-test','W',1,'T-W',1,1,'hold','after P acceptance','worker','[{"reservation":"P","phase":"s6-acceptance","until":"executed"}]','relaxed-window',1500)`); err != nil {
		t.Fatal(err)
	}
	got, err := s.Delivery(ctx, "W", IssueObservation{})
	if err != nil || len(got.Waits) != 1 || got.Waits[0].Satisfied || slices.Contains(got.Drift, "hold-waits-satisfied") || slices.Contains(got.Drift, "hold-time-box-expired") {
		t.Fatalf("pending wait: %+v %v", got, err)
	}
	now = time.Unix(1600, 0)
	if got, err = s.Delivery(ctx, "W", IssueObservation{}); err != nil || !slices.Contains(got.Drift, "hold-time-box-expired") {
		t.Fatalf("expired time box hidden: %+v %v", got, err)
	}
	if _, err = s.db.Exec(`INSERT INTO dispatch_phase_results(repo_id,reservation_key,phase,generation,item_id,checks,verdict,deferred,outcome_id,recorded_by,recorded_at) VALUES('repo-test','P','s6-acceptance',1,'T-P','all','failed','replay to S7',2,'worker',1000)`); err != nil {
		t.Fatal(err)
	}
	if got, err = s.Delivery(ctx, "W", IssueObservation{}); err != nil || !got.Waits[0].Satisfied || !slices.Contains(got.Drift, "hold-waits-satisfied") || got.DecisionAction != "hold" {
		t.Fatalf("consumable result hidden: %+v %v", got, err)
	}
}
