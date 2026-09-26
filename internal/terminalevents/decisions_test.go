package terminalevents

import (
	"context"
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
	if _, err := s.Publish(ctx, "worker", event); !errors.Is(err, ErrStaleDecision) {
		t.Fatal("legacy stale outcome accepted", err)
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
