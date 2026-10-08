package terminalevents

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// An outcome message stored in a different repo than its reservation must
// fail with a precise repo-mismatch reason, not the generic "event rejected"
// that once misled a live Dispatcher into a decision repair that could not work.
func TestPublishRepoMismatchGivesPreciseReason(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	publisher := s
	publisher.Recipient = ""
	// Move the outcome message to another repo, reproducing the live failure.
	if _, err := s.DB.Exec("UPDATE messages SET repo_id='other-repo' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	_, err := publisher.Publish(ctx, "worker", PublishRequest{"DISPATCH-1", 1, "worker-session", "handoff-complete", 1, 0})
	if err == nil {
		t.Fatal("cross-repo outcome accepted")
	}
	var rejection *Rejection
	if !errors.As(err, &rejection) {
		t.Fatalf("generic rejection, want precise reason: %v", err)
	}
	if rejection.Condition != "outcome-repo-mismatch" {
		t.Fatalf("condition=%s, want outcome-repo-mismatch", rejection.Condition)
	}
	if !strings.Contains(rejection.Repair, "repost") {
		t.Fatalf("repair action missing repost guidance: %s", rejection.Repair)
	}
}

// Decision states: unadopted assignments submit with ExpectedDecision 0
// (legacy behavior preserved); adopted assignments require the current
// revision; stale revisions and holds are rejected precisely.
func TestSubmitDecisionStates(t *testing.T) {
	newCase := func(t *testing.T) Store {
		s := fixture(t)
		if _, err := s.DB.Exec(`INSERT INTO claims(item_id,repo_id,agent_id,claimed_at,last_touch) VALUES('TASK','repo','worker',1,1)`); err != nil {
			t.Fatal(err)
		}
		return s
	}
	t.Run("unadopted", func(t *testing.T) {
		s := newCase(t)
		publisher := s
		publisher.Recipient = ""
		out, err := publisher.Submit(context.Background(), "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "blocked", "stuck note", 0, ""})
		if err != nil {
			t.Fatalf("unadopted submit: %v", err)
		}
		if out.MessageID == 0 || out.EventID == "" {
			t.Fatalf("no IDs: %+v", out)
		}
	})
	t.Run("adopted-current", func(t *testing.T) {
		s := newCase(t)
		if _, err := s.DB.Exec(`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('repo','DISPATCH-1',1,'TASK',2,1,'proceed','','worker')`); err != nil {
			t.Fatal(err)
		}
		publisher := s
		publisher.Recipient = ""
		if _, err := publisher.Submit(context.Background(), "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "handoff-complete", "done", 2, ""}); err != nil {
			t.Fatalf("adopted current submit: %v", err)
		}
	})
	t.Run("adopted-stale", func(t *testing.T) {
		s := newCase(t)
		if _, err := s.DB.Exec(`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('repo','DISPATCH-1',1,'TASK',2,9,'proceed','','worker')`); err != nil {
			t.Fatal(err)
		}
		publisher := s
		publisher.Recipient = ""
		_, err := publisher.Submit(context.Background(), "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "handoff-complete", "done", 1, ""})
		var rejection *Rejection
		if !errors.As(err, &rejection) || rejection.Condition != "stale-decision" {
			t.Fatalf("want stale-decision, got %v", err)
		}
	})
	t.Run("adopted-hold", func(t *testing.T) {
		s := newCase(t)
		if _, err := s.DB.Exec(`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('repo','DISPATCH-1',1,'TASK',2,9,'hold','','worker')`); err != nil {
			t.Fatal(err)
		}
		publisher := s
		publisher.Recipient = ""
		_, err := publisher.Submit(context.Background(), "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "handoff-complete", "done", 2, ""})
		var rejection *Rejection
		if !errors.As(err, &rejection) || rejection.Condition != "decision-hold" {
			t.Fatalf("want decision-hold, got %v", err)
		}
		var n int
		if err := s.DB.QueryRow("SELECT count(*) FROM messages WHERE body='done'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("hold left half-commit: %d %v", n, err)
		}
	})
}

// Concurrent duplicate submits collapse to one message and one event.
func TestSubmitConcurrentIsIdempotent(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO claims(item_id,repo_id,agent_id,claimed_at,last_touch) VALUES('TASK','repo','worker',1,1)`); err != nil {
		t.Fatal(err)
	}
	publisher := s
	publisher.Recipient = ""
	ctx := context.Background()
	results := make(chan error, 8)
	for range 8 {
		go func() {
			_, err := publisher.Submit(ctx, "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "blocked", "same stuck", 0, ""})
			results <- err
		}()
	}
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var messages, events int
	if err := s.DB.QueryRow("SELECT count(*) FROM messages WHERE body='same stuck'").Scan(&messages); err != nil || messages != 1 {
		t.Fatalf("messages=%d %v", messages, err)
	}
	if err := s.DB.QueryRow("SELECT count(*) FROM terminal_event_receipts").Scan(&events); err != nil || events != 1 {
		t.Fatalf("events=%d %v", events, err)
	}
}

// Stable request identity: the same request retries to the same IDs; a
// distinct legitimate request on the same live assignment records a new
// event; the same identity with a different payload is a precise conflict.
func TestSubmitDistinctRequests(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO claims(item_id,repo_id,agent_id,claimed_at,last_touch) VALUES('TASK','repo','worker',1,1)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	publisher := s
	publisher.Recipient = ""
	first, err := publisher.Submit(ctx, "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "decision-request", "phase A design question", 0, "phase-a"})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := publisher.Submit(ctx, "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "decision-request", "phase A design question", 0, "phase-a"})
	if err != nil || retry != first {
		t.Fatalf("same-request retry must retain IDs: first=%+v retry=%+v err=%v", first, retry, err)
	}
	second, err := publisher.Submit(ctx, "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "decision-request", "phase B installation question", 0, "phase-b"})
	if err != nil {
		t.Fatalf("second distinct legitimate request must publish: %v", err)
	}
	if second == first {
		t.Fatal("distinct request must have distinct message/event IDs")
	}
	_, err = publisher.Submit(ctx, "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "decision-request", "changed body", 0, "phase-a"})
	var rejection *Rejection
	if !errors.As(err, &rejection) || rejection.Condition != "payload-conflict" {
		t.Fatalf("want payload-conflict, got %v", err)
	}
}

// Request-key matching is exact and case-sensitive: a key equal to a decimal
// message id or differing only by case from an existing key records a new
// event instead of collapsing into the old row.
func TestSubmitRequestKeyMatchIsExact(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO claims(item_id,repo_id,agent_id,claimed_at,last_touch) VALUES('TASK','repo','worker',1,1)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	publisher := s
	publisher.Recipient = ""
	base, err := publisher.Submit(ctx, "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "decision-request", "base body", 0, "Phase-A"})
	if err != nil {
		t.Fatal(err)
	}
	lower, err := publisher.Submit(ctx, "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "decision-request", "lower body", 0, "phase-a"})
	if err != nil {
		t.Fatalf("case-distinct key rejected: %v", err)
	}
	if lower == base {
		t.Fatal("case-distinct key collapsed into existing row")
	}
	numeric, err := publisher.Submit(ctx, "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "decision-request", "numeric body", 0, "2"})
	if err != nil {
		t.Fatalf("numeric key rejected: %v", err)
	}
	if numeric == base || numeric == lower {
		t.Fatal("numeric key collapsed into existing row")
	}
}

// Atomic outcome submission takes the assignment identity plus outcome body,
// stores the canonical message and the durable event in one transaction, and
// returns both IDs. No manual message-ID extraction, no cwd dependence.
func TestSubmitOutcomeAtomically(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	publisher := s
	publisher.Recipient = ""
	out, err := publisher.Submit(ctx, "worker", SubmitRequest{
		Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session",
		Kind: "handoff-complete", Body: "factual outcome", ExpectedDecision: 0,
	})
	if err != nil {
		t.Fatalf("atomic submit: %v", err)
	}
	if out.MessageID == 0 || out.EventID == "" {
		t.Fatalf("submit returned no IDs: %+v", out)
	}
	events, err := s.Pending(ctx, "fresh", 0)
	if err != nil || len(events) != 1 || events[0].ID != out.EventID {
		t.Fatalf("submitted event not routed: %v %v", events, err)
	}
	// Idempotent retry returns the same IDs without duplicates.
	again, err := publisher.Submit(ctx, "worker", SubmitRequest{
		Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session",
		Kind: "handoff-complete", Body: "factual outcome", ExpectedDecision: 0,
	})
	if err != nil {
		t.Fatalf("submit retry: %v", err)
	}
	if again != out {
		t.Fatalf("retry returned different IDs: %+v vs %+v", again, out)
	}
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM messages WHERE thread='TASK' AND body='factual outcome'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate messages: %d %v", n, err)
	}
}
