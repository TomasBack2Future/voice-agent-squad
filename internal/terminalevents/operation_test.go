package terminalevents

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/attest"
)

func TestOwnedOperationExitWakesOriginalWorkerAndRemainsPendingUntilHandled(t *testing.T) {
	for _, code := range []int{0, 3} {
		t.Run(string(rune('0'+code)), func(t *testing.T) {
			s := fixture(t)
			if _, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','TASK','worker',1,1)`); err != nil {
				t.Fatal(err)
			}
			q := OperationRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", RequestKey: "cleanup", Phase: "cleanup", Revision: "exact-sha", ResumeStep: "inspect cleanup result"}
			opts := attest.RunOpts{ItemID: "TASK", Kind: attest.KindTest, AgentID: "worker", Command: "exit " + string(rune('0'+code)), RepoRoot: t.TempDir(), AttDir: filepath.Join(t.TempDir(), "evidence")}
			r, err := s.RunOperation(context.Background(), "worker", q, opts)
			if err != nil || r.ExitCode != code || r.EventID == "" {
				t.Fatalf("result %+v %v", r, err)
			}
			worker := Store{DB: s.DB, Repo: s.Repo, Recipient: "worker"}
			events, err := worker.Pending(context.Background(), "native-incarnation", 0)
			if err != nil || len(events) != 1 || events[0].Kind != "operation-exit" {
				t.Fatalf("missing owner delivery %+v %v", events, err)
			}
			if err = worker.Ack(context.Background(), r.EventID, "handled"); err == nil {
				t.Fatal("acked before delivery")
			}
			if err = worker.Delivered(context.Background(), r.EventID, "native-incarnation"); err != nil {
				t.Fatal(err)
			}
			if err = worker.Ack(context.Background(), r.EventID, "checkpoint: cleanup failed, bounded retry needed"); err != nil {
				t.Fatal(err)
			}
			events, err = worker.Pending(context.Background(), "resume", 0)
			if err != nil || len(events) != 0 {
				t.Fatalf("handled replay: %+v %v", events, err)
			}
			if _, err = s.RunOperation(context.Background(), "worker", q, opts); err != nil {
				t.Fatal(err)
			}
			var n int
			_ = s.DB.QueryRow(`SELECT count(*) FROM attestations`).Scan(&n)
			if n != 1 {
				t.Fatalf("command reran %d", n)
			}
			changed := q
			changed.Revision = "other-sha"
			if _, err = s.RunOperation(context.Background(), "worker", changed, opts); err == nil {
				t.Fatal("changed revision reused operation key")
			}
			opts.Command = "exit 1"
			if _, err = s.RunOperation(context.Background(), "worker", q, opts); err == nil {
				t.Fatal("changed command reused operation key")
			}
			var state string
			_ = s.DB.QueryRow(`SELECT state FROM dispatch_reservations`).Scan(&state)
			if state != "dispatched" {
				t.Fatal("exit/ACK changed business completion")
			}
		})
	}
}
func TestOwnedOperationCancelledResultAndResumeNoRerun(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','TASK','worker',1,1)`); err != nil {
		t.Fatal(err)
	}
	q := OperationRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", RequestKey: "timeout"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := s.RunOperation(ctx, "worker", q, attest.RunOpts{ItemID: "TASK", Kind: attest.KindTest, AgentID: "worker", Argv: []string{"sleep", "5"}, RepoRoot: t.TempDir(), AttDir: filepath.Join(t.TempDir(), "evidence")})
	if err != nil || r.ExitCode == 0 || r.AttestationID == 0 || r.EventID == "" {
		t.Fatalf("lost canceled result %+v %v", r, err)
	}
}
func TestOwnedOperationHoldsAndWrongIdentityCannotExecute(t *testing.T) {
	s := fixture(t)
	q := OperationRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", RequestKey: "run"}
	opts := attest.RunOpts{ItemID: "TASK", Kind: attest.KindTest, AgentID: "worker", Command: "exit 0", RepoRoot: t.TempDir(), AttDir: t.TempDir()}
	if _, err := s.RunOperation(context.Background(), "worker", q, opts); err == nil {
		t.Fatal("executed after primary release")
	}
	if _, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','TASK','worker',1,1); INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('repo','DISPATCH-1',1,'TASK',1,1,'hold','pause','worker')`); err != nil {
		t.Fatal(err)
	}
	q.ExpectedDecision = 1
	if _, err := s.RunOperation(context.Background(), "worker", q, opts); err == nil {
		t.Fatal("executed during hold")
	}
}

func TestUnresolvedOperationDoesNotExecuteAgain(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','TASK','worker',1,1)`); err != nil {
		t.Fatal(err)
	}
	q := OperationRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", RequestKey: "interrupted"}
	opts := attest.RunOpts{ItemID: "TASK", AgentID: "worker", Kind: attest.KindTest, Command: "exit 0", RepoRoot: t.TempDir(), AttDir: t.TempDir()}
	r, err := s.RunOperation(context.Background(), "worker", q, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate loss of the wrapper after the durable start, retaining actual
	// command evidence; a resume must reconcile it rather than execute again.
	if _, err = s.DB.Exec(`DELETE FROM terminal_event_receipts WHERE kind='operation-exit';DELETE FROM messages WHERE kind='owned-operation' AND json_extract(body,'$.state')!='started'`); err != nil {
		t.Fatal(err)
	}
	again, err := s.RunOperation(context.Background(), "worker", q, opts)
	if err == nil || again.State != "started" {
		t.Fatalf("unresolved start rerun: %+v %v", again, err)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT count(*) FROM attestations`).Scan(&n)
	if n != 1 || r.AttestationID == 0 {
		t.Fatal("command evidence changed on unresolved resume")
	}
}
func TestOperationEventCannotBeForgedOrDeliveredToSuccessor(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','TASK','worker',1,1)`); err != nil {
		t.Fatal(err)
	}
	q := OperationRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", RequestKey: "exit"}
	r, err := s.RunOperation(context.Background(), "worker", q, attest.RunOpts{ItemID: "TASK", AgentID: "worker", Kind: attest.KindTest, Command: "exit 0", RepoRoot: t.TempDir(), AttDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Publish(context.Background(), "worker", PublishRequest{Reservation: q.Reservation, Generation: 1, WorkerSession: q.WorkerSession, Kind: "operation-exit", OutcomeID: 1}); err == nil {
		t.Fatal("forged operation accepted")
	}
	if _, err = s.DB.Exec(`UPDATE dispatch_reservations SET generation=2,worker_thread_id='successor'`); err != nil {
		t.Fatal(err)
	}
	worker := Store{DB: s.DB, Repo: s.Repo, Recipient: "worker"}
	events, err := worker.Pending(context.Background(), "old-native", 0)
	if err != nil || len(events) != 0 {
		t.Fatalf("stale exit delivered %+v %v", events, err)
	}
	if err = worker.Delivered(context.Background(), r.EventID, "old-native"); err == nil {
		t.Fatal("recorded stale delivery")
	}
}
