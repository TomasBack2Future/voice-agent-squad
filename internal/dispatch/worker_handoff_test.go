package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/zsiec/squad/internal/claims"
	"github.com/zsiec/squad/internal/terminalevents"
)

func workerHandoffFixture(t *testing.T) (*Store, WorkerHandoffRequest) {
	t.Helper()
	s, b := workerExecutionFixture(t)
	// Original execution close normally verifies these handles through the CLI;
	// this fixture also uses a real exited PID, never an arbitrary guessed PID.
	child := exec.Command("sh", "-c", "exit 0")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	b.ClientPID = child.Process.Pid
	ctx := context.Background()
	if err := s.AcquireWorkerExecution(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	if err := s.SuspendWorkerExecution(ctx, b.Actor, b); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseWorkerExecution(ctx, b.Actor, b, "verified original native/container/bridge joins"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO agents(id,repo_id,display_name,started_at,last_tick_at,status) VALUES('new-worker','repo-test','new',1,1,'active'); UPDATE terminal_event_receipts SET processed_at=3,processed_note='original controller handled pending result'`); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(ctx, "D")
	if err != nil {
		t.Fatal(err)
	}
	q := WorkerHandoffRequest{RequestID: "handoff-1", Controller: ControllerBinding{"old-dispatcher", "controller-native", 1}, Expected: *r, Claim: HandoffClaim{"T", "old-worker", 1, 2, 2}, ExecutionID: b.ID, NewActor: "new-worker", NewNative: "new-native", DecisionRevision: 2, CustodyEvidence: "Retained fixture under item T; no external mutations remain"}
	workerHandoffConsent(t, s, &q)
	return s, q
}

func workerHandoffConsent(t *testing.T, s *Store, q *WorkerHandoffRequest) {
	t.Helper()
	digest, err := WorkerHandoffDigest(*q)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"schema_version": "squad.worker-handoff-consent.v1", "request_sha256": digest})
	r, err := s.db.Exec(`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?, 'say',?,'[]','normal')`, s.repoID, 5, q.Claim.Actor, q.Expected.CanonicalItemID, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	q.ConsentOutcomeID, err = r.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkerHandoffPreservesHoldAndRoutesActualDecision(t *testing.T) {
	s, q := workerHandoffFixture(t)
	ctx := context.Background()
	r, err := s.WorkerHandoff(ctx, q.Controller.Actor, q)
	if err != nil {
		t.Fatal(err)
	}
	if r.Reservation.Generation != 2 || r.Reservation.ReservedBy != q.Controller.Actor || r.ClaimGeneration != 2 || r.Previous.WorkerThreadID != "old-session" {
		t.Fatal(r)
	}
	if err = claims.New(s.db, s.repoID, nil).WorkerHeartbeat(ctx, "old-worker", "D", "old-session", 1, true); err == nil {
		t.Fatal("old writer renewed after transfer")
	}
	if err = claims.New(s.db, s.repoID, nil).WorkerHeartbeat(ctx, "new-worker", "D", "new-native", 2, true); err != nil {
		t.Fatal(err)
	}
	events := terminalevents.Store{DB: s.db, Repo: s.repoID, Recipient: "new-worker"}
	d, err := events.CurrentDecision(ctx, "D", 2, "new-native")
	if err != nil || d.Action != "hold" || d.Condition != "merge paused" || d.Revision != 2 {
		t.Fatal(d, err)
	}
	pending, err := events.Pending(ctx, "replacement-delivery", 0)
	if err != nil || len(pending) != 1 || pending[0].OutcomeID != r.DecisionOutcomeID {
		t.Fatal(pending, err)
	}
	if err = events.Delivered(ctx, pending[0].ID, "replacement-delivery"); err != nil {
		t.Fatal(err)
	}
	if err = events.Ack(ctx, pending[0].ID, "read transferred hold; no source mutation until controller release"); err != nil {
		t.Fatal(err)
	}
	// Retry returns the immutable receipt, even after heartbeat updated the claim.
	again, err := s.WorkerHandoff(ctx, q.Controller.Actor, q)
	if err != nil || again != r {
		t.Fatal(again, err)
	}
	q.NewNative = "changed-native"
	if _, err = s.WorkerHandoff(ctx, q.Controller.Actor, q); err == nil {
		t.Fatal("changed replay admitted")
	}
}

func TestWorkerHandoffAllowsRenewalWithoutChangingCustody(t *testing.T) {
	s, q := workerHandoffFixture(t)
	if _, err := s.db.Exec(`UPDATE claims SET last_touch=3`); err != nil {
		t.Fatal(err)
	}
	r, err := s.WorkerHandoff(context.Background(), q.Controller.Actor, q)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.WorkerHandoffReceipt(context.Background(), q.RequestID)
	if err != nil || got != r || got.ExecutionID != q.ExecutionID {
		t.Fatal(got, err)
	}
}

func TestWorkerHandoffRejectsUnsafeTupleWithoutPartialWrites(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func(*WorkerHandoffRequest)
		statement string
		actor     string
	}{
		{"foreign caller", nil, "", "intruder"},
		{"controller epoch", func(q *WorkerHandoffRequest) { q.Controller.Epoch++ }, "", ""},
		{"stale reservation", func(q *WorkerHandoffRequest) { q.Expected.Generation++ }, "", ""},
		{"changed claim", nil, `UPDATE claims SET generation=2`, ""},
		{"changed decision", nil, `UPDATE dispatch_decisions SET revision=3`, ""},
		{"not held", nil, `UPDATE dispatch_decisions SET action='proceed'`, ""},
		{"forged consent", nil, `UPDATE messages SET body='{}' WHERE kind='say'`, ""},
		{"missing original consent", func(q *WorkerHandoffRequest) { q.ConsentOutcomeID = 999 }, "", ""},
		{"active pin", nil, `UPDATE execution_authorizations SET state='active'`, ""},
		{"unjoined native", nil, `UPDATE execution_authorizations SET binding=json_set(binding,'$.client_pid',` + jsonPID(os.Getpid()) + `)`, ""},
		{"legacy unfenced writer", nil, `DELETE FROM execution_authorizations`, ""},
		{"protected ENV", nil, `INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo-test','ENV-1','old-worker',2,2)`, ""},
		{"another old claim", nil, `INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo-test','OTHER','old-worker',2,2)`, ""},
		{"pending event", nil, `UPDATE terminal_event_receipts SET processed_at=0`, ""},
		{"ambiguous custody", nil, `INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES('repo-test','T','other',2,3,'done')`, ""},
		{"new holder has claim", nil, `INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo-test','OTHER','new-worker',2,2)`, ""},
		{"unregistered replacement", nil, `DELETE FROM agents WHERE id='new-worker'`, ""},
		{"retired controller", nil, `INSERT INTO dispatch_retired_controllers VALUES('repo-test','old-dispatcher',3,'other')`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, q := workerHandoffFixture(t)
			if tc.change != nil {
				tc.change(&q)
			}
			if tc.statement != "" {
				if _, err := s.db.Exec(tc.statement); err != nil {
					t.Fatal(err)
				}
			}
			actor := q.Controller.Actor
			if tc.actor != "" {
				actor = tc.actor
			}
			if _, err := s.WorkerHandoff(context.Background(), actor, q); err == nil {
				t.Fatal("unsafe transfer accepted")
			}
			var holder string
			var receipts, history int
			if err := s.db.QueryRow(`SELECT agent_id FROM claims WHERE item_id='T'`).Scan(&holder); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM worker_handoffs`).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM claim_history WHERE outcome='worker-handoff'`).Scan(&history); err != nil {
				t.Fatal(err)
			}
			r, err := s.Get(context.Background(), "D")
			if err != nil || r.Generation != 1 || holder != "old-worker" || receipts != 0 || history != 0 {
				t.Fatal("partial transfer", r, holder, receipts, history, err)
			}
		})
	}
}

func jsonPID(pid int) string { raw, _ := json.Marshal(pid); return string(raw) }

func TestWorkerHandoffRejectsCrossItemTerminalContinuationWithoutPartialTransfer(t *testing.T) {
	s, q := workerHandoffFixture(t)
	if _, err := s.db.Exec(`UPDATE dispatch_reservations SET state='completed'; DELETE FROM claims WHERE item_id='T'; INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES('repo-test','T','old-worker',2,4,'done'); INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,state,generation) VALUES('repo-test','ACCEPTANCE','old-worker',5,5,'held',3)`); err != nil {
		t.Fatal(err)
	}
	q.Expected.State = "completed"
	q.Claim = HandoffClaim{"ACCEPTANCE", "old-worker", 3, 5, 5}
	workerHandoffConsent(t, s, &q)
	if _, err := s.WorkerHandoff(context.Background(), q.Controller.Actor, q); err == nil || !strings.Contains(err.Error(), "canonical assignment") {
		t.Fatal("unusable cross-item replacement admitted", err)
	}
	row, err := s.Get(context.Background(), "D")
	if err != nil || *row != q.Expected {
		t.Fatal("partial reservation transfer", row, err)
	}
	var holder string
	var receipts int
	if err = s.db.QueryRow(`SELECT agent_id FROM claims WHERE item_id='ACCEPTANCE'`).Scan(&holder); err != nil || holder != "old-worker" {
		t.Fatal("partial claim transfer", holder, err)
	}
	_ = s.db.QueryRow(`SELECT count(*) FROM worker_handoffs`).Scan(&receipts)
	if receipts != 0 {
		t.Fatal("unexpected receipt", receipts)
	}
}

func TestWorkerHandoffConcurrentCASHasOneWinner(t *testing.T) {
	s, q := workerHandoffFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.WorkerHandoff(context.Background(), q.Controller.Actor, q)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal("identical retry must reconcile", err)
		}
	}
	var receipts, history int
	if err := s.db.QueryRow(`SELECT count(*) FROM worker_handoffs`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM claim_history WHERE outcome='worker-handoff'`).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || history != 1 {
		t.Fatal(receipts, history)
	}
	other := q
	other.RequestID = "different-request"
	other.NewNative = "different-native"
	workerHandoffConsent(t, s, &other)
	if _, err := s.WorkerHandoff(context.Background(), q.Controller.Actor, other); err == nil || !strings.Contains(err.Error(), "reservation fence") {
		t.Fatal(err)
	}
}

func TestWorkerHandoffFailureRollsBackClaimsDecisionAndWake(t *testing.T) {
	s, q := workerHandoffFixture(t)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_handoff_receipt BEFORE INSERT ON worker_handoffs BEGIN SELECT RAISE(ABORT,'fixture rejects final receipt'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WorkerHandoff(context.Background(), q.Controller.Actor, q); err == nil {
		t.Fatal("late failure was accepted")
	}
	r, err := s.Get(context.Background(), "D")
	if err != nil || *r != q.Expected {
		t.Fatal(r, err)
	}
	var actor string
	var decisions, history int
	if err = s.db.QueryRow(`SELECT agent_id FROM claims WHERE item_id='T'`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM dispatch_decisions WHERE generation=2`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM claim_history WHERE outcome='worker-handoff'`).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if actor != "old-worker" || decisions != 0 || history != 0 {
		t.Fatal(actor, decisions, history)
	}
}

func TestWorkerHandoffAndReacquiringWriterCannotBothWin(t *testing.T) {
	s, q := workerHandoffFixture(t)
	var raw string
	if err := s.db.QueryRow(`SELECT binding FROM execution_authorizations WHERE id=?`, q.ExecutionID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var b WorkerExecutionBinding
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	b.ID = "reacquired-pin"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := s.WorkerHandoff(context.Background(), q.Controller.Actor, q)
		results <- err
	}()
	go func() { defer wg.Done(); results <- s.AcquireWorkerExecution(context.Background(), b.Actor, b) }()
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatal("writer and replacement admission were not mutually exclusive", wins)
	}
}
