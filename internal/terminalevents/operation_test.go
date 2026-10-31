package terminalevents

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/attest"
	"github.com/zsiec/squad/internal/store"
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

func TestKnownOperationFailuresDoNotLeaveUnresolvedCustody(t *testing.T) {
	for _, before := range []bool{false, true} {
		t.Run(map[bool]string{false: "evidence-write", true: "before-start"}[before], func(t *testing.T) {
			s := fixture(t)
			if _, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','TASK','worker',1,1)`); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			evidence := filepath.Join(root, "evidence")
			if err := os.WriteFile(evidence, []byte("occupied"), 0600); err != nil {
				t.Fatal(err)
			}
			q := OperationRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", RequestKey: "failure"}
			opts := attest.RunOpts{ItemID: "TASK", AgentID: "worker", Kind: attest.KindTest, Command: "echo ran >> marker; exit 7", RepoRoot: root, AttDir: evidence}
			state, exit := "exited-without-evidence", 7
			if before {
				opts.WorkDir = "relative"
				state, exit = "not-started", -1
			}
			r, err := s.RunOperation(context.Background(), "worker", q, opts)
			if err == nil || r.State != state || r.ExitCode != exit || r.EventID == "" {
				t.Fatalf("lost known outcome %+v %v", r, err)
			}
			worker := Store{DB: s.DB, Repo: s.Repo, Recipient: "worker"}
			if err = worker.Delivered(context.Background(), r.EventID, "inc"); err != nil {
				t.Fatal(err)
			}
			if err = worker.Ack(context.Background(), r.EventID, "checkpoint: known command failure handled; evidence missing, retry only under a new authorized key"); err != nil {
				t.Fatal(err)
			}
			unresolved, err := store.UnresolvedOperation(context.Background(), s.DB, s.Repo, "worker", q.Reservation, q.WorkerSession, q.Generation)
			if err != nil || unresolved {
				t.Fatalf("known joined/not-started result wedges custody: %v %v", unresolved, err)
			}
			if _, err = s.RunOperation(context.Background(), "worker", q, opts); err != nil {
				t.Fatal(err)
			}
			data, readErr := os.ReadFile(filepath.Join(root, "marker"))
			if before {
				if !os.IsNotExist(readErr) {
					t.Fatalf("command ran before validation: %q %v", data, readErr)
				}
			} else if readErr != nil || string(data) != "ran\n" {
				t.Fatalf("same key reran or lost process: %q %v", data, readErr)
			}
		})
	}
}

func TestOwnedExitRemainsHandleableAfterCompletion(t *testing.T) {
	for _, deliveredBefore := range []bool{false, true} {
		t.Run(map[bool]string{false: "late-delivery", true: "late-ack"}[deliveredBefore], func(t *testing.T) {
			s := fixture(t)
			if _, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','TASK','worker',1,1)`); err != nil {
				t.Fatal(err)
			}
			q := OperationRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", RequestKey: "completed"}
			r, err := s.RunOperation(context.Background(), "worker", q, attest.RunOpts{ItemID: "TASK", AgentID: "worker", Kind: attest.KindTest, Command: "exit 0", RepoRoot: t.TempDir(), AttDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			worker := Store{DB: s.DB, Repo: s.Repo, Recipient: "worker"}
			if deliveredBefore {
				if err = worker.Delivered(context.Background(), r.EventID, "inc"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.DB.Exec(`UPDATE dispatch_reservations SET state='completed'; INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES('repo','TASK','worker',1,2,'partial');DELETE FROM claims`); err != nil {
				t.Fatal(err)
			}
			if !deliveredBefore {
				events, err := worker.Pending(context.Background(), "inc", 0)
				if err != nil || len(events) != 1 {
					t.Fatalf("completed exit hidden: %+v %v", events, err)
				}
				if err = worker.Delivered(context.Background(), r.EventID, "inc"); err != nil {
					t.Fatal(err)
				}
			}
			if err = worker.Ack(context.Background(), r.EventID, "checkpoint: original completed execution result handled; no new work"); err != nil {
				t.Fatal(err)
			}
			if _, err = s.RunOperation(context.Background(), "worker", q, attest.RunOpts{ItemID: "TASK", AgentID: "worker", Kind: attest.KindTest, Command: "exit 0", RepoRoot: t.TempDir(), AttDir: t.TempDir()}); err == nil {
				t.Fatal("result handling granted new execution after completion")
			}
		})
	}
}

func TestLiveDescendantWaitDelayRetainsUnresolvedCustody(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix descendant fixture; Windows supervision unqualified")
	}
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','TASK','worker',1,1)`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	q := OperationRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", RequestKey: "descendant"}
	r, err := s.RunOperation(context.Background(), "worker", q, attest.RunOpts{ItemID: "TASK", AgentID: "worker", Kind: attest.KindTest, Command: "sleep 30 & echo $! > descendant.pid", RepoRoot: root, AttDir: filepath.Join(root, "evidence")})
	raw, readErr := os.ReadFile(filepath.Join(root, "descendant.pid"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if parseErr != nil || pid <= 0 {
		t.Fatalf("invalid owned fixture PID: %q %v", raw, parseErr)
	}
	child, findErr := os.FindProcess(pid)
	if findErr != nil {
		t.Fatal(findErr)
	}
	defer func() { _ = child.Kill() }()
	if aliveErr := child.Signal(syscall.Signal(0)); aliveErr != nil {
		t.Fatalf("fixture descendant unexpectedly terminal: %v", aliveErr)
	}
	if err == nil || r.State != "result-unavailable" || r.ExitCode != -1 || r.EventID == "" {
		t.Fatalf("incomplete group treated as joined: %+v %v", r, err)
	}
	worker := Store{DB: s.DB, Repo: s.Repo, Recipient: "worker"}
	if err = worker.Delivered(context.Background(), r.EventID, "inc"); err != nil {
		t.Fatal(err)
	}
	if err = worker.Ack(context.Background(), r.EventID, "checkpoint: WaitDelay with live owned descendant; retain custody, no retry or closure"); err != nil {
		t.Fatal(err)
	}
	unresolved, err := store.UnresolvedOperation(context.Background(), s.DB, s.Repo, "worker", q.Reservation, q.WorkerSession, q.Generation)
	if err != nil || !unresolved {
		t.Fatalf("ACK removed unknown descendant custody fence: %v %v", unresolved, err)
	}
}

// An exit that arrives after the reservation completed is still routed to its
// original Worker; it is neither dropped nor left without a receipt.
func TestExitArrivingAfterCompletionStillReachesTheOriginalWorker(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','TASK','worker',1,1)`); err != nil {
		t.Fatal(err)
	}
	q := OperationRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", RequestKey: "late"}
	done := make(chan struct{})
	var r OperationResult
	var runErr error
	go func() {
		defer close(done)
		r, runErr = s.RunOperation(context.Background(), "worker", q, attest.RunOpts{ItemID: "TASK", AgentID: "worker", Kind: attest.KindTest, Command: "sleep 1; exit 0", RepoRoot: t.TempDir(), AttDir: t.TempDir()})
	}()
	time.Sleep(300 * time.Millisecond)
	if _, err := s.DB.Exec(`UPDATE dispatch_reservations SET state='completed'; INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES('repo','TASK','worker',1,2,'done');DELETE FROM claims`); err != nil {
		t.Fatal(err)
	}
	<-done
	if runErr != nil || r.EventID == "" {
		t.Fatalf("late exit not routed: %+v %v", r, runErr)
	}
	worker := Store{DB: s.DB, Repo: s.Repo, Recipient: "worker"}
	events, err := worker.Pending(context.Background(), "inc", 0)
	if err != nil || len(events) != 1 || events[0].Kind != "operation-exit" {
		t.Fatalf("%+v %v", events, err)
	}
}
