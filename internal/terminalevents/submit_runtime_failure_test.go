package terminalevents

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// D84-5 RED: Submit must admit the runtime-failure kind with its sanitized
// message contract. Bodies carry closed-enum pointers only; anything else
// is rejected with a precise condition.
func TestSubmitRuntimeFailureAdmitsSanitizedBody(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO claims(item_id,repo_id,agent_id,claimed_at,last_touch) VALUES('TASK','repo','worker',1,1)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	publisher := s
	publisher.Recipient = ""
	out, err := publisher.Submit(ctx, "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "runtime-failure", "runtime-failure ep-1 exhausted turn=t1 request=r1 attempt=1 provider=meta", 0, "ep-1"})
	if err != nil {
		t.Fatalf("sanitized runtime-failure rejected: %v", err)
	}
	if out.MessageID == 0 || !strings.HasSuffix(out.EventID, "/ep-1") {
		t.Fatalf("no keyed IDs: %+v", out)
	}
	retry, err := publisher.Submit(ctx, "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "runtime-failure", "runtime-failure ep-1 exhausted turn=t1 request=r1 attempt=1 provider=meta", 0, "ep-1"})
	if err != nil || retry != out {
		t.Fatalf("same-episode retry must retain IDs: %+v %+v %v", out, retry, err)
	}
	second, err := publisher.Submit(ctx, "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "runtime-failure", "runtime-failure ep-2 exhausted turn=t3 request=r3 attempt=1 provider=meta", 0, "ep-2"})
	if err != nil || second == out {
		t.Fatalf("new episode must record again: %+v %v", second, err)
	}
	var kind string
	if err := s.DB.QueryRow("SELECT kind FROM messages WHERE id=?", out.MessageID).Scan(&kind); err != nil || kind != "stuck" {
		t.Fatalf("observation kind=%q, want stuck", kind)
	}
}

func TestSubmitRuntimeFailureRejectsUnsanitizedBody(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO claims(item_id,repo_id,agent_id,claimed_at,last_touch) VALUES('TASK','repo','worker',1,1)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	publisher := s
	publisher.Recipient = ""
	for _, body := range []string{
		"runtime-failure ep-1 bogus-class turn=t1 request=r1 attempt=1 provider=meta",
		"runtime-failure ep-1 exhausted turn=t1 request=r1 attempt=1 provider=meta extra prompt text here",
		"please retry the model call now",
		"runtime-failure ep-1 exhausted turn=t1 request=r1 attempt=1 provider=meta\nsecond line injection",
	} {
		_, err := publisher.Submit(ctx, "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "runtime-failure", body, 0, "ep-x"})
		var rejection *Rejection
		if !errors.As(err, &rejection) || rejection.Condition != "malformed-failure-body" {
			t.Fatalf("body %q: want malformed-failure-body, got %v", body, err)
		}
	}
}

func TestSubmitRuntimeFailureKeepsCustodyAndDecisionFences(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	publisher := s
	publisher.Recipient = ""
	body := "runtime-failure ep-1 exhausted turn=t1 request=r1 attempt=1 provider=meta"
	_, err := publisher.Submit(ctx, "intruder", SubmitRequest{"DISPATCH-1", 1, "worker-session", "runtime-failure", body, 0, "ep-1"})
	var rejection *Rejection
	if !errors.As(err, &rejection) || rejection.Condition != "actor-custody" {
		t.Fatalf("no-custody: want actor-custody, got %v", err)
	}
	if _, err := s.DB.Exec(`INSERT INTO claims(item_id,repo_id,agent_id,claimed_at,last_touch) VALUES('TASK','repo','worker',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('repo','DISPATCH-1',1,'TASK',3,1,'proceed','', 'worker')`); err != nil {
		t.Fatal(err)
	}
	_, err = publisher.Submit(ctx, "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "runtime-failure", body, 0, "ep-1"})
	if !errors.As(err, &rejection) || rejection.Condition != "stale-decision" {
		t.Fatalf("stale revision: want stale-decision, got %v", err)
	}
	out, err := publisher.Submit(ctx, "worker", SubmitRequest{"DISPATCH-1", 1, "worker-session", "runtime-failure", body, 3, "ep-1"})
	if err != nil {
		t.Fatalf("live revision rejected: %v", err)
	}
	if out.MessageID == 0 {
		t.Fatalf("no IDs: %+v", out)
	}
}
