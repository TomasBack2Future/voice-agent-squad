package dispatch

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/terminalevents"
)

func handoffFixture(t *testing.T) (*Store, HandoffRequest, string) {
	t.Helper()
	now := time.Now()
	s := newDispatchStore(t, &now)
	ctx := context.Background()
	_, err := s.Reserve(ctx, "D-1", "github:o/r#1", "old", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Attach(ctx, "D-1", "TASK-1", "old", 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Bind(ctx, "D-1", "old", "worker-native", 1); err != nil {
		t.Fatal(err)
	}
	// Unrelated deployment/controller and Worker claims are never changed.
	if _, err = s.db.Exec(`INSERT INTO agents(id,repo_id,display_name,started_at,last_tick_at,status) VALUES('new','repo-test','new',1,1,'working'),('other-new','repo-test','other',1,1,'working'); INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,generation,state) VALUES('repo-test','TASK-1','worker',9999999999,1,7,'held'); INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id,delivered_session,delivered_at) VALUES('repo-test','old','event-1','D-1',1,'worker-native','TASK-1','blocked',9,9,'old-native',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BindController(ctx, "old", "old-native", 0); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	request := HandoffRequest{RequestID: "handoff-1", ExpectedEpoch: 1, OldNative: "old-native", NewActor: "new", NewNative: "new-native", Reservations: rows}
	return s, request, "event-1"
}
func TestControllerHandoffAtomicRoutingAndOldOwnerWriteExclusion(t *testing.T) {
	s, q, id := handoffFixture(t)
	ctx := context.Background()
	receipt, err := s.Handoff(ctx, "old", q)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Epoch != 2 {
		t.Fatal(receipt)
	}
	if _, err = s.Handoff(ctx, "old", q); err != nil {
		t.Fatal("same request not idempotent", err)
	}
	row, _ := s.Get(ctx, "D-1")
	if row.ReservedBy != "new" || row.Generation != 1 || row.WorkerThreadID != "worker-native" {
		t.Fatal(row)
	}
	var holder, state string
	var generation int
	if err = s.db.QueryRow(`SELECT agent_id,state,generation FROM claims WHERE item_id='TASK-1'`).Scan(&holder, &state, &generation); err != nil || holder != "worker" || state != "held" || generation != 7 {
		t.Fatal(holder, state, generation, err)
	}
	if _, err = s.Reserve(ctx, "D-2", "github:o/r#2", "old", "", time.Hour); err == nil {
		t.Fatal("retired owner created new reservation")
	}
	if _, err = s.Close(ctx, "D-1", "old", "completed", "", 1); err == nil {
		t.Fatal("old owner closed transferred reservation")
	}
	old := terminalevents.Store{DB: s.db, Repo: s.repoID, Recipient: "old"}
	if err = old.Ack(ctx, id, "old handling"); err == nil {
		t.Fatal("old owner acked")
	}
	target := terminalevents.Store{DB: s.db, Repo: s.repoID, Recipient: "new", NativeSession: "wrong-native"}
	if events, err := target.Pending(ctx, "wrong-native", time.Second); err == nil && len(events) > 0 {
		t.Fatal("wrong native received controller event")
	}
	target.NativeSession = "new-native"
	if err = s.BindReceiver(ctx, "new", "new-native", "new-incarnation", 2); err != nil {
		t.Fatal(err)
	}
	if err = s.BindReceiver(ctx, "new", "new-native", "second-incarnation", 2); err == nil {
		t.Fatal("two receivers bound")
	}
	events, err := target.Pending(ctx, "new-incarnation", time.Second)
	if err != nil || len(events) != 1 || events[0].DeliveredAt != 0 {
		t.Fatal(events, err)
	}
	if err = target.Delivered(ctx, id, "new-incarnation"); err != nil {
		t.Fatal(err)
	}
	if err = target.Ack(ctx, id, "handled by new"); err != nil {
		t.Fatal(err)
	}
	if err = target.Ack(ctx, id, "handled by new"); err != nil {
		t.Fatal("ack replay", err)
	}
	if _, err = s.Close(ctx, "D-1", "new", "completed", "", 1); err != nil {
		t.Fatal("new closure route", err)
	}
}
func TestControllerHandoffRejectsWrongIdentityInventoryAndConcurrentOwners(t *testing.T) {
	for _, field := range []string{"actor", "native", "epoch", "generation", "worker", "omission"} {
		t.Run(field, func(t *testing.T) {
			s, q, _ := handoffFixture(t)
			actor := "old"
			switch field {
			case "actor":
				actor = "foreign"
			case "native":
				q.OldNative = "wrong"
			case "epoch":
				q.ExpectedEpoch = 2
			case "generation":
				q.Reservations[0].Generation = 2
			case "worker":
				q.Reservations[0].WorkerThreadID = "different"
			case "omission":
				q.Reservations = nil
			}
			if _, err := s.Handoff(context.Background(), actor, q); err == nil {
				t.Fatal("bad handoff admitted")
			}
			row, _ := s.Get(context.Background(), "D-1")
			if row.ReservedBy != "old" {
				t.Fatal("partial write", row)
			}
		})
	}
	s, q, _ := handoffFixture(t)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for _, actor := range []string{"new", "other-new"} {
		wg.Add(1)
		go func(actor string) {
			defer wg.Done()
			copy := q
			copy.NewActor = actor
			copy.RequestID = actor
			if _, err := s.Handoff(context.Background(), "old", copy); err == nil {
				winners.Add(1)
			}
		}(actor)
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal("controllers", winners.Load())
	}
	// An immutable request receipt cannot be reused for different targets.
	row, _ := s.Get(context.Background(), "D-1")
	replay := q
	replay.NewActor = row.ReservedBy
	replay.RequestID = row.ReservedBy
	replay.NewNative = "different-native"
	if _, err := s.Handoff(context.Background(), "old", replay); err == nil {
		t.Fatal("changed replay reused audit")
	}
}

func TestControllerHandoffRetainsHandledWorkerAndUnrelatedCustody(t *testing.T) {
	s, q, _ := handoffFixture(t)
	ctx := context.Background()
	_, err := s.db.Exec(`INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id,processed_at,processed_note) VALUES('repo-test','old','handled-old','D-1',1,'worker-native','TASK-1','blocked',10,10,1,'historical handling'),('repo-test','worker','worker-wake','D-1',1,'worker-native','TASK-1','decision-resolved',11,11,0,''); INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES('repo-test','ENV-D','production','deployer',1,1,0,'dispatched',3,'deployer-native','','TASK-PROD')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Handoff(ctx, "old", q); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"handled-old": "old", "worker-wake": "worker"} {
		var actual string
		if err = s.db.QueryRow(`SELECT recipient FROM terminal_event_receipts WHERE event_id=?`, id).Scan(&actual); err != nil || actual != want {
			t.Fatal(id, actual, err)
		}
	}
	unrelated, err := s.Get(ctx, "ENV-D")
	if err != nil || unrelated.ReservedBy != "deployer" || unrelated.Generation != 3 || unrelated.WorkerThreadID != "deployer-native" {
		t.Fatal(unrelated, err)
	}
	old := terminalevents.Store{DB: s.db, Repo: s.repoID, Recipient: "old", NativeSession: "old-native"}
	if err = old.Ack(ctx, "handled-old", "historical handling"); err == nil {
		t.Fatal("retired owner could use historical acknowledgement route")
	}
	if _, err = s.BindController(ctx, "old", "old-native", 0); err == nil {
		t.Fatal("retired actor reactivated")
	}
}

func TestControllerReceiverReleaseRejectsStaleAcceptance(t *testing.T) {
	s, q, id := handoffFixture(t)
	ctx := context.Background()
	if _, err := s.Handoff(ctx, "old", q); err != nil {
		t.Fatal(err)
	}
	if err := s.BindReceiver(ctx, "new", "new-native", "first", 2); err != nil {
		t.Fatal(err)
	}
	target := terminalevents.Store{DB: s.db, Repo: s.repoID, Recipient: "new", NativeSession: "new-native"}
	if _, err := target.Pending(ctx, "first", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseReceiver(ctx, "new", "new-native", "first", 2); err != nil {
		t.Fatal(err)
	}
	if err := s.BindReceiver(ctx, "new", "new-native", "second", 2); err != nil {
		t.Fatal(err)
	}
	if err := target.Delivered(ctx, id, "first"); err == nil {
		t.Fatal("released receiver recorded acceptance")
	}
	if err := target.Ack(ctx, id, "stale handling"); err == nil {
		t.Fatal("stale undelivered event handled")
	}
	if err := target.Delivered(ctx, id, "second"); err != nil {
		t.Fatal(err)
	}
	if err := target.Ack(ctx, id, "current handling"); err != nil {
		t.Fatal(err)
	}
}
