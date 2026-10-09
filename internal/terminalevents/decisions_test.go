package terminalevents

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func decisionMessage(t *testing.T, s Store) int64 {
	t.Helper()
	post(t, s, "canonical decision", "dispatcher")
	var id int64
	if err := s.DB.QueryRow("SELECT max(id) FROM messages").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDecisionCASAndReleasedWorkerRecovery(t *testing.T) {
	s := fixture(t)
	s.Recipient = ""
	ctx := context.Background()
	q := DecisionRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", OutcomeID: decisionMessage(t, s), Action: "hold", Condition: "dependency: merged candidate"}
	d, err := s.Decide(ctx, "dispatcher", q)
	if err != nil || d.Revision != 1 {
		t.Fatal(d, err)
	}
	if again, err := s.Decide(ctx, "dispatcher", q); err != nil || again != d {
		t.Fatal("retry", again, err)
	}
	worker := s
	worker.Recipient = "worker"
	old, err := worker.Pending(ctx, "session", 0)
	if err != nil || len(old) != 1 {
		t.Fatal("released owner not woken", old, err)
	}
	if err = worker.Delivered(ctx, old[0].ID, "session"); err != nil {
		t.Fatal(err)
	}
	q.ExpectedRevision = 1
	q.OutcomeID = decisionMessage(t, s)
	q.Action = "proceed"
	q.Condition = "dependency: merged candidate confirmed"
	d, err = s.Decide(ctx, "dispatcher", q)
	if err != nil || d.Revision != 2 {
		t.Fatal(d, err)
	}
	if err = worker.Ack(ctx, old[0].ID, "stale hold"); err == nil {
		t.Fatal("stale decision acknowledged")
	}
	current, err := worker.Pending(ctx, "session", 0)
	if err != nil || len(current) != 1 || current[0].OutcomeID != q.OutcomeID {
		t.Fatal(current, err)
	}
	if err = worker.Delivered(ctx, current[0].ID, "session"); err != nil {
		t.Fatal(err)
	}
	if err = worker.Ack(ctx, current[0].ID, "resume within existing scope; reclaim before writes"); err != nil {
		t.Fatal(err)
	}
	read, err := s.CurrentDecision(ctx, q.Reservation, 1, q.WorkerSession)
	if err != nil || read != d {
		t.Fatal(read, err)
	}
	q.OutcomeID = decisionMessage(t, s)
	if _, err = s.Decide(ctx, "dispatcher", q); !errors.Is(err, ErrStaleDecision) {
		t.Fatal("stale CAS", err)
	}
}

func TestDecisionRejectsStaleBlockedAndForeignAuthority(t *testing.T) {
	s := fixture(t)
	s.Recipient = ""
	ctx := context.Background()
	q := DecisionRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", OutcomeID: decisionMessage(t, s), Action: "proceed"}
	if _, err := s.Decide(ctx, "intruder", q); !errors.Is(err, ErrInvalidEvent) {
		t.Fatal(err)
	}
	if _, err := s.Decide(ctx, "dispatcher", q); err != nil {
		t.Fatal(err)
	}
	post(t, s, "blocked using outdated hold", "worker")
	var id int64
	if err := s.DB.QueryRow("SELECT max(id) FROM messages").Scan(&id); err != nil {
		t.Fatal(err)
	}
	event := PublishRequest{Reservation: q.Reservation, Generation: 1, WorkerSession: q.WorkerSession, Kind: "blocked", OutcomeID: id}
	if _, err := s.Publish(ctx, "worker", event); !isExpectedRejection(err) {
		t.Fatal("legacy stale outcome accepted", err)
	} else {
		var rejection *Rejection
		if !errors.As(err, &rejection) || rejection.Condition != "stale-decision" {
			t.Fatalf("want precise stale-decision, got %v", err)
		}
	}
	event.ExpectedDecision = 1
	if _, err := s.Publish(ctx, "worker", event); err != nil {
		t.Fatal("new blocker under current decision", err)
	}
	if _, err := s.DB.Exec("UPDATE dispatch_reservations SET generation=2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CurrentDecision(ctx, q.Reservation, 1, q.WorkerSession); err == nil {
		t.Fatal("old generation")
	}
	if _, err := s.Decide(ctx, "dispatcher", q); err == nil {
		t.Fatal("old generation decided")
	}
}

func TestDecisionAmbiguousCustodyDoesNotWriteOrWake(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo','TASK','someone-else',2,2)`); err != nil {
		t.Fatal(err)
	}
	q := DecisionRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", OutcomeID: decisionMessage(t, s), Action: "proceed"}
	if _, err := s.Decide(ctx, "dispatcher", q); !errors.Is(err, ErrInvalidEvent) {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow("SELECT count(*) FROM dispatch_decisions").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

// TASK-028 follow-up: a self-reserved release owner decides its own
// assignment; the queue Dispatcher cannot CAS a decision on a reservation
// it does not own.
func TestSelfReservedReleaseOwnerDecidesOwnAssignment(t *testing.T) {
	s := releaseFixture(t, "production:studio:bfd4e18c:20261006", "deployer-agent", "TASK-028", "deployer")
	s.Recipient = ""
	ctx := context.Background()
	if _, e := s.DB.Exec(`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('repo',3,'deployer-agent','TASK-028','say','adopt self reservation','[]','normal')`); e != nil {
		t.Fatal(e)
	}
	var outcome int64
	if e := s.DB.QueryRow("SELECT max(id) FROM messages").Scan(&outcome); e != nil {
		t.Fatal(e)
	}
	q := DecisionRequest{Reservation: "production:studio:bfd4e18c:20261006", Generation: 1, WorkerSession: "deployer", OutcomeID: outcome, Action: "proceed"}
	if _, e := s.Decide(ctx, "dispatcher", q); !errors.Is(e, ErrInvalidEvent) {
		t.Fatalf("queue dispatcher decided foreign self-reservation: %v", e)
	}
	d, e := s.Decide(ctx, "deployer-agent", q)
	if e != nil || d.Revision != 1 {
		t.Fatalf("self-reserver cannot decide own assignment: %v %v", d, e)
	}
}

func TestConcurrentDecisionsHaveOneWinner(t *testing.T) {
	s := fixture(t)
	s.Recipient = ""
	ctx := context.Background()
	results := make(chan error, 2)
	for range 2 {
		q := DecisionRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", OutcomeID: decisionMessage(t, s), Action: "proceed"}
		go func() { _, err := s.Decide(ctx, "dispatcher", q); results <- err }()
	}
	wins := 0
	for range 2 {
		err := <-results
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrStaleDecision) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal("lost update", wins)
	}
}

func TestCurrentDecisionLookupFailuresArePrecise(t *testing.T) {
	s := fixture(t)
	s.Recipient = ""
	ctx := context.Background()
	for name, tc := range map[string]struct {
		key     string
		gen     int64
		session string
		want    string
	}{
		"missing session":  {"DISPATCH-1", 1, "", "missing-worker-session"},
		"wrong session":    {"DISPATCH-1", 1, "other-session", "worker-session-mismatch"},
		"unknown":          {"DISPATCH-X", 1, "worker-session", "reservation-not-found"},
		"wrong generation": {"DISPATCH-1", 9, "worker-session", "generation-mismatch"},
	} {
		_, err := s.CurrentDecision(ctx, tc.key, tc.gen, tc.session)
		var lookup *DecisionLookupError
		if errors.Is(err, sql.ErrNoRows) || !errors.As(err, &lookup) || lookup.Condition != tc.want {
			t.Errorf("%s: want %s, got %v", name, tc.want, err)
		}
	}
	for name, tc := range map[string]struct {
		key, want string
		gen       int64
	}{"no reservation": {"", "missing-reservation", 1}, "zero generation": {"DISPATCH-1", "invalid-generation", 0}} {
		_, err := s.CurrentDecision(ctx, tc.key, tc.gen, "worker-session")
		var lookup *DecisionLookupError
		if !errors.As(err, &lookup) || lookup.Condition != tc.want {
			t.Errorf("%s: want %s, got %v", name, tc.want, err)
		}
	}
	d, err := s.CurrentDecision(ctx, "DISPATCH-1", 1, "worker-session")
	if err != nil || d.Revision != 0 || d.Action != "" {
		t.Fatalf("no decision must stay revision 0: %+v %v", d, err)
	}
}

func TestCurrentDecisionReportsNotDispatched(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec("UPDATE dispatch_reservations SET state='completed'"); err != nil {
		t.Fatal(err)
	}
	_, err := s.CurrentDecision(context.Background(), "DISPATCH-1", 1, "worker-session")
	var lookup *DecisionLookupError
	if !errors.As(err, &lookup) || lookup.Condition != "not-dispatched" {
		t.Fatal(err)
	}
}
