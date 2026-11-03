package terminalevents

import (
	"context"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/dispatch"
)

// Reconstruct the legacy race using real publication and retirement receipts,
// then restore the unhandled HOLD state an older retire-partial allowed.
func legacyPartialHold(t *testing.T) (Store, string) {
	t.Helper()
	s := fixture(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(`INSERT INTO items(repo_id,item_id,title,type,status,path,updated_at) VALUES('repo','TASK','partial','task','blocked','/fixture/TASK.md',1)`); err != nil {
		t.Fatal(err)
	}
	d, err := s.Decide(ctx, "dispatcher", DecisionRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", OutcomeID: decisionMessage(t, s), Action: "hold", Condition: "safe partial handoff"})
	if err != nil {
		t.Fatal(err)
	}
	worker := s
	worker.Recipient = "worker"
	worker.NativeSession = "worker-session"
	events, err := worker.Pending(ctx, "native", 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("hold not published: %+v %v", events, err)
	}
	id := events[0].ID
	if err := worker.Delivered(ctx, id, "native"); err != nil {
		t.Fatal(err)
	}
	if err := worker.Ack(ctx, id, "hold read back"); err != nil {
		t.Fatal(err)
	}
	result, err := s.Submit(ctx, "worker", SubmitRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", Kind: "blocked", Body: "Issue remains open; custody released", ExpectedDecision: d.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delivered(ctx, result.EventID, "controller"); err != nil {
		t.Fatal(err)
	}
	if err := s.Ack(ctx, result.EventID, "original blocked result handled"); err != nil {
		t.Fatal(err)
	}
	owner := dispatch.New(s.DB, s.Repo, nil)
	r, err := owner.Get(ctx, "DISPATCH-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.RetirePartial(ctx, "dispatcher", dispatch.PartialRetirement{
		Expected: *r, Issue: dispatch.IssueObservation{Source: r.SourceRef, State: "OPEN", VerifiedAt: time.Now().Unix()}, BlockedOutcome: result.MessageID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE terminal_event_receipts SET processed_at=0,processed_note='' WHERE event_id=?`, id); err != nil {
		t.Fatal(err)
	}
	return worker, id
}

func TestOriginalWorkerCanAcknowledgeDeliveredHoldAfterLegacyPartialRetirement(t *testing.T) {
	worker, id := legacyPartialHold(t)
	ctx := context.Background()
	for range 2 {
		if err := worker.Ack(ctx, id, "checkpoint: original hold reconciled; no new work"); err != nil {
			t.Fatalf("original recipient cannot reconcile its delivered hold: %v", err)
		}
	}
	if _, err := worker.CurrentDecision(ctx, "DISPATCH-1", 1, "worker-session"); err == nil {
		t.Fatal("receipt reconciliation reopened work authority")
	}
	var state string
	if err := worker.DB.QueryRow(`SELECT state FROM dispatch_reservations WHERE item_id='DISPATCH-1'`).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("receipt reconciliation changed custody: %q %v", state, err)
	}
}

func TestPartialRetirementReceiptRecoveryRejectsUnqualifiedCustody(t *testing.T) {
	for _, native := range []string{"", "wrong-native"} {
		t.Run("caller-native-"+native, func(t *testing.T) {
			worker, id := legacyPartialHold(t)
			worker.NativeSession = native
			if err := worker.Ack(context.Background(), id, "wrong native"); err == nil {
				t.Fatal("unqualified native acknowledged original Worker's hold")
			}
		})
	}
	for _, tc := range []struct{ name, sql string }{
		{"no-audit", `DELETE FROM messages WHERE kind='partial-retirement'`},
		{"wrong-owner-audit", `UPDATE messages SET agent_id='intruder' WHERE kind='partial-retirement'`},
		{"not-delivered", `UPDATE terminal_event_receipts SET delivered_at=0 WHERE kind='decision-resolved'`},
		{"not-held", `UPDATE dispatch_decisions SET action='proceed'`},
		{"superseded", `UPDATE dispatch_decisions SET outcome_id=999,revision=2`},
		{"new-generation", `UPDATE dispatch_reservations SET generation=2`},
		{"other-native", `UPDATE dispatch_reservations SET worker_thread_id='successor'`},
		{"ambiguous-custody", `INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES('repo','TASK','intruder',3,4,'partial')`},
		{"retained-claim", `INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','TASK','worker',3,3)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker, id := legacyPartialHold(t)
			if _, err := worker.DB.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			if err := worker.Ack(context.Background(), id, "must not handle"); err == nil {
				t.Fatal("unqualified terminal receipt accepted")
			}
		})
	}
	worker, id := legacyPartialHold(t)
	worker.Recipient = "dispatcher"
	if err := worker.Ack(context.Background(), id, "controller cannot impersonate Worker"); err == nil {
		t.Fatal("controller acknowledged Worker's hold")
	}
}
