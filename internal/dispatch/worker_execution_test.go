package dispatch

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestWorkerExecutionPinsReservationAndController(t *testing.T) {
	for _, statement := range []string{
		`UPDATE dispatch_reservations SET reserved_by='new-controller' WHERE item_id='D'`,
		`UPDATE dispatch_reservations SET generation=2 WHERE item_id='D'`,
		`UPDATE dispatch_reservations SET worker_thread_id='new-native' WHERE item_id='D'`,
		`DELETE FROM dispatch_reservations WHERE item_id='D'`,
		`UPDATE dispatch_controller_bindings SET epoch=2 WHERE actor='old-dispatcher'`,
		`DELETE FROM dispatch_controller_bindings WHERE actor='old-dispatcher'`,
		`INSERT INTO dispatch_retired_controllers VALUES('repo-test','old-dispatcher',1,'new-controller')`,
	} {
		t.Run(statement, func(t *testing.T) {
			s, _ := takeoverFixture(t)
			_, err := s.db.Exec(`INSERT INTO dispatch_controller_bindings VALUES('repo-test','old-dispatcher','controller-native',1);
INSERT INTO execution_authorizations(repo_id,id,item_id,holder,generation,binding,state,created_at,updated_at) VALUES('repo-test','worker-pin','T','old-worker',1,'{"kind":"worker","reservation":"D","controller":"old-dispatcher"}','active',1,1)`)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(context.Background(), statement); err == nil {
				t.Fatal("active worker execution allowed custody mutation")
			}
		})
	}
}

func workerExecutionFixture(t *testing.T) (*Store, WorkerExecutionBinding) {
	t.Helper()
	s, _ := takeoverFixture(t)
	if _, err := s.db.Exec(`INSERT INTO dispatch_controller_bindings VALUES('repo-test','old-dispatcher','controller-native',1)`); err != nil {
		t.Fatal(err)
	}
	return s, WorkerExecutionBinding{ToolRuntime: WorkerToolRuntime{Executable: "/docker", Context: "local", DaemonID: "daemon", Image: "sha256:" + strings.Repeat("a", 64)}, Kind: "worker", ID: "worker-pin", Item: "T", Actor: "old-worker", Native: "old-session",
		ClaimGeneration: 1, Reservation: "D", SourceRef: "github:o/r#1", Generation: 1, Controller: "old-dispatcher", ControllerNative: "controller-native", Epoch: 1, ExpiresAt: 11000, ClientPID: 123}
}

func TestWorkerExecutionRejectsStaleTupleAndClosedReplay(t *testing.T) {
	s, b := workerExecutionFixture(t)
	ctx := context.Background()
	if err := s.AcquireWorkerExecution(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerExecution(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*WorkerExecutionBinding){
		func(q *WorkerExecutionBinding) { q.Actor = "other" },
		func(q *WorkerExecutionBinding) { q.Native = "other" },
		func(q *WorkerExecutionBinding) { q.Generation++ },
		func(q *WorkerExecutionBinding) { q.ClaimGeneration++ },
		func(q *WorkerExecutionBinding) { q.Epoch++ },
		func(q *WorkerExecutionBinding) { q.SourceRef = "github:o/r#2" },
		func(q *WorkerExecutionBinding) { q.ClientPID++ },
		func(q *WorkerExecutionBinding) { q.ExpiresAt = 9999 },
	} {
		q := b
		mutate(&q)
		if err := s.CheckWorkerExecution(ctx, b.Actor, q); err == nil {
			t.Fatalf("changed tuple admitted: %+v", q)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM claims WHERE item_id='T'`); err == nil {
		t.Fatal("claim release succeeded during execution")
	}
	if err := s.CloseWorkerExecution(ctx, b.Actor, b, "unjoined"); err == nil {
		t.Fatal("open gate was reconciled")
	}
	if err := s.SuspendWorkerExecution(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerExecution(ctx, b.Actor, b); err == nil {
		t.Fatal("suspended execution admitted a future tool")
	}
	if _, err := s.db.Exec(`DELETE FROM claims WHERE item_id='T'`); err == nil {
		t.Fatal("suspension released unresolved custody")
	}
	if err := s.CloseWorkerExecution(ctx, b.Actor, b, "owned native/tool handles joined"); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerExecution(ctx, b.Actor, b); err == nil {
		t.Fatal("closed execution replay admitted")
	}
	if _, err := s.db.Exec(`DELETE FROM claims WHERE item_id='T'`); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerExecutionConcurrentAdmissionHasOneOwner(t *testing.T) {
	s, b := workerExecutionFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"first", "second"} {
		q := b
		q.ID = id
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- s.AcquireWorkerExecution(context.Background(), q.Actor, q)
		}()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d executions", accepted)
	}
}

func TestWorkerExecutionRejectsWrongCustodyBeforeInsertion(t *testing.T) {
	for _, statement := range []string{
		`UPDATE claims SET agent_id='other'`, `UPDATE claims SET generation=2`,
		`UPDATE dispatch_reservations SET worker_thread_id='other'`, `UPDATE dispatch_controller_bindings SET epoch=2`,
	} {
		s, b := workerExecutionFixture(t)
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
		if err := s.AcquireWorkerExecution(context.Background(), b.Actor, b); err == nil {
			t.Fatal("changed custody admitted")
		}
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM execution_authorizations`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial insertion: count=%d err=%v", count, err)
		}
	}
}

func TestWorkerExecutionDuplicateNativeAcrossItemsRejected(t *testing.T) {
	s, b := workerExecutionFixture(t)
	ctx := context.Background()
	if err := s.AcquireWorkerExecution(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES('repo-test','D2','github:o/r#2','old-dispatcher',1,1,0,'dispatched',1,'old-session','','T2');
 INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,state,generation) VALUES('repo-test','T2','second-worker',2,2,'held',1)`); err != nil {
		t.Fatal(err)
	}
	q := b
	q.ID = "second-pin"
	q.Item = "T2"
	q.Reservation = "D2"
	q.SourceRef = "github:o/r#2"
	q.Actor = "second-worker"
	if err := s.AcquireWorkerExecution(ctx, q.Actor, q); err == nil {
		t.Fatal("duplicate native identity admitted across items")
	}
}

func TestWorkerExecutionHoldBlocksMutationButAllowsDecisionHandling(t *testing.T) {
	s, b := workerExecutionFixture(t)
	ctx := context.Background()
	if err := s.AcquireWorkerExecution(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerWrite(ctx, b.Actor, b); err == nil {
		t.Fatal("hold allowed source mutation")
	}
	if err := s.CheckWorkerExecution(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE dispatch_decisions SET action='proceed'`); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerWrite(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerOutcomeRequiresJoinedGateAndPreservesReplay(t *testing.T) {
	s, b := workerExecutionFixture(t)
	ctx := context.Background()
	if err := s.AcquireWorkerExecution(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.WorkerOutcome(ctx, b.Actor, b, "completed", "actual test output"); err == nil {
		t.Fatal("open gate accepted outcome")
	}
	if err := s.SuspendWorkerExecution(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.WorkerOutcome(ctx, b.Actor, b, "completed", "actual test output"); err == nil {
		t.Fatal("hold accepted completion")
	}
	id, revision, err := s.WorkerOutcome(ctx, b.Actor, b, "blocked", "waiting on adopted hold")
	if err != nil || id == 0 || revision != 2 {
		t.Fatalf("%d %d %v", id, revision, err)
	}
	again, _, err := s.WorkerOutcome(ctx, b.Actor, b, "blocked", "waiting on adopted hold")
	if err != nil || again != id {
		t.Fatalf("outcome replay: %d %v", again, err)
	}
	if _, _, err := s.WorkerOutcome(ctx, b.Actor, b, "blocked", "changed report"); err == nil {
		t.Fatal("changed outcome replay accepted")
	}
	if _, err := s.db.Exec(`DELETE FROM claims WHERE item_id='T'`); err == nil {
		t.Fatal("reporting released custody")
	}
}

func TestWorkerExecutionSuspendedReadRetainsExactCustody(t *testing.T) {
	s, b := workerExecutionFixture(t)
	ctx := context.Background()
	if err := s.AcquireWorkerExecution(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	if err := s.SuspendWorkerExecution(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerRead(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerWrite(ctx, b.Actor, b); err == nil {
		t.Fatal("suspended write admitted")
	}
	q := b
	q.Native = "changed"
	if err := s.CheckWorkerRead(ctx, b.Actor, q); err == nil {
		t.Fatal("changed custody read admitted")
	}
	if err := s.CloseWorkerExecution(ctx, b.Actor, b, "joined"); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerRead(ctx, b.Actor, b); err == nil {
		t.Fatal("reconciled read admitted")
	}
}
