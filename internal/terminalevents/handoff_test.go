package terminalevents

import (
	"context"
	"testing"
)

func receiptsOf(t *testing.T, s Store, kind string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`SELECT count(*) FROM terminal_event_receipts WHERE kind=?`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// One submit call stores the report, the phase result and the Dispatcher wake;
// a retry after a lost response returns the same identities without a second
// message, result or event.
func TestSubmitPhaseResultIsOneAtomicIdempotentHandoff(t *testing.T) {
	s := waitFixture(t, "D1940")
	ctx := context.Background()
	q := SubmitRequest{Reservation: "D1940", Generation: 1, WorkerSession: "native-D1940", Kind: "phase-result", Body: "PR #1998 merged at 0072bd08; not deployed", RequestKey: "merge-pr1998",
		Phase: "merge:pr1998", Checks: "all", Verdict: "passed"}
	first, err := s.Submit(ctx, "worker-D1940", q)
	if err != nil || first.EventID == "" {
		t.Fatalf("submit: %+v %v", first, err)
	}
	again, err := s.Submit(ctx, "worker-D1940", q)
	if err != nil || again != first {
		t.Fatalf("retry: %+v %v", again, err)
	}
	if n := receiptsOf(t, s, "phase-result"); n != 1 {
		t.Fatalf("phase-result events = %d", n)
	}
	if ok, ev, _ := waitSatisfied(ctx, s.DB, "repo", Wait{Reservation: "D1940", Phase: "merge:pr1998", Until: "passed"}); !ok || ev == "" {
		t.Fatal("submitted phase result is not visible to waits")
	}
	for _, bad := range []SubmitRequest{
		{Reservation: "D1940", Generation: 1, WorkerSession: "native-D1940", Kind: "phase-result", Body: "x", RequestKey: "k2", Phase: "merge:pr1998", Checks: "partial", Verdict: "passed"},
		{Reservation: "D1940", Generation: 1, WorkerSession: "native-D1940", Kind: "phase-result", Body: "x", RequestKey: "k3"},
		{Reservation: "D1940", Generation: 1, WorkerSession: "native-D1940", Kind: "blocked", Body: "x", RequestKey: "k4", Phase: "merge"},
		{Reservation: "D1940", Generation: 2, WorkerSession: "native-D1940", Kind: "phase-result", Body: "x", RequestKey: "k5", Phase: "merge:pr1998", Checks: "all", Verdict: "passed"},
	} {
		if _, err := s.Submit(ctx, "worker-D1940", bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	foreign := q
	foreign.RequestKey, foreign.Body = "foreign", "forged merge report"
	if _, err := s.Submit(ctx, "worker-D1949", foreign); err == nil {
		t.Fatal("a foreign actor submitted another owner's phase result")
	}
}

func releaseDecision(t *testing.T, s Store, key string, expected int64) Decision {
	t.Helper()
	d, err := s.Decide(context.Background(), "dispatcher", proceed(s, t, key, expected))
	if err != nil {
		t.Fatal(err)
	}
	// The release row Decide would write after a live PR check.
	if _, err = s.DB.Exec(`INSERT INTO dispatch_merge_releases(repo_id,reservation_key,generation,item_id,decision_revision,pr_repo,pr_number,head_sha,base_sha,evidence_outcome,expires_at) VALUES('repo',?,1,?,?,'o/r',1998,?,?,?,9999999999)`,
		key, "TASK-"+key, d.Revision, "dfad98ec621f0464c917002ff3c9798d2337dabb", "ac3079c465b62faaf5ee09fe30874e760527103f", d.OutcomeID); err != nil {
		t.Fatal(err)
	}
	return d
}

// 2026-11-07: the Worker merged under a release and reported it with a plain
// message; nothing woke the Dispatcher. The report now becomes one
// reconcile-needed wake, only for the released assignment's own Worker.
func TestPlainReportAfterMergeReleaseWakesTheDispatcherOnce(t *testing.T) {
	s := waitFixture(t, "D1940", "D1949")
	ctx := context.Background()
	d := releaseDecision(t, s, "D1940", 0)
	disp := s
	disp.Recipient = "dispatcher"
	// Messages that must not wake: another Worker, a supervisor, the Dispatcher itself.
	say(t, s, "worker-D1949", "TASK-D1940")
	say(t, s, "supervisor", "TASK-D1940")
	say(t, s, "dispatcher", "TASK-D1940")
	if err := disp.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if n := receiptsOf(t, s, "reconcile-needed"); n != 0 {
		t.Fatalf("non-owner messages woke the Dispatcher: %d", n)
	}
	report := say(t, s, "worker-D1940", "TASK-D1940") // "MERGED PR #1998"
	for i := 0; i < 3; i++ {                          // repeated receiver passes
		if err := disp.Discover(ctx); err != nil {
			t.Fatal(err)
		}
	}
	events, err := disp.Pending(ctx, "session", 0)
	if err != nil {
		t.Fatal(err)
	}
	var wake []Event
	for _, e := range events {
		if e.Kind == "reconcile-needed" {
			wake = append(wake, e)
		}
	}
	if len(wake) != 1 || wake[0].OutcomeID != report {
		t.Fatalf("plain post-merge report did not produce exactly one wake: %+v", events)
	}
	// The Dispatcher answers with a new revision: the release is revoked and
	// later chatter no longer produces wakes.
	if err = disp.Delivered(ctx, wake[0].ID, "session"); err != nil {
		t.Fatal(err)
	}
	if err = disp.Ack(ctx, wake[0].ID, "merge verified; next phase decided"); err != nil {
		t.Fatal(err)
	}
	next := proceed(s, t, "D1940", d.Revision)
	next.Condition = "merged at 0072bd08; wait for S9"
	if _, err = s.Decide(ctx, "dispatcher", next); err != nil {
		t.Fatal(err)
	}
	say(t, s, "worker-D1940", "TASK-D1940")
	if err = disp.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if n := receiptsOf(t, s, "reconcile-needed"); n != 1 {
		t.Fatalf("chatter after the answering decision woke again: %d", n)
	}
	// A release of an earlier generation never wakes for the next lifetime.
	releaseDecision(t, s, "D1949", 0)
	reReserve(t, s, "D1949")
	say(t, s, "worker-D1949", "TASK-D1949")  // generation-1 Worker
	say(t, s, "worker2-D1949", "TASK-D1949") // generation-2 Worker, no release of its own
	if err = disp.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if n := receiptsOf(t, s, "reconcile-needed"); n != 1 {
		t.Fatalf("an old-generation Worker woke the Dispatcher: %d", n)
	}
}

// A formal result interrupted at each boundary is resumed, never duplicated:
// stored but not delivered, delivered but not processed, processed.
func TestFormalResultSurvivesEachDeliveryBoundary(t *testing.T) {
	s := waitFixture(t, "D1940")
	ctx := context.Background()
	q := SubmitRequest{Reservation: "D1940", Generation: 1, WorkerSession: "native-D1940", Kind: "phase-result", Body: "S9 replay re-verification: all checks ran, passed", RequestKey: "s9-replay",
		Phase: "acceptance:s9-replay", Checks: "all", Verdict: "passed"}
	out, err := s.Submit(ctx, "worker-D1940", q)
	if err != nil {
		t.Fatal(err)
	}
	disp := s
	disp.Recipient = "dispatcher"
	// Crash before delivery: the event is still pending.
	ev, err := disp.Pending(ctx, "session-a", 0)
	if err != nil || len(ev) != 1 || ev[0].ID != out.EventID {
		t.Fatalf("stored-not-delivered: %+v %v", ev, err)
	}
	// Delivered to one receiver incarnation, which then died before handling.
	if err = disp.Delivered(ctx, out.EventID, "session-a"); err != nil {
		t.Fatal(err)
	}
	if ev, _ = disp.Pending(ctx, "session-b", 0); len(ev) != 1 {
		t.Fatalf("delivered-not-processed must be redelivered to the restarted receiver: %+v", ev)
	}
	// The Worker retries its submit after a network failure: no second result.
	if again, err := s.Submit(ctx, "worker-D1940", q); err != nil || again != out {
		t.Fatalf("worker retry: %+v %v", again, err)
	}
	if err = disp.Delivered(ctx, out.EventID, "session-b"); err != nil {
		t.Fatal(err)
	}
	if err = disp.Ack(ctx, out.EventID, "result consumed; next decision published"); err != nil {
		t.Fatal(err)
	}
	if ev, _ = disp.Pending(ctx, "session-b", 0); len(ev) != 0 {
		t.Fatalf("processed result delivered again: %+v", ev)
	}
	var results int
	if err = s.DB.QueryRow(`SELECT count(*) FROM dispatch_phase_results`).Scan(&results); err != nil || results != 1 {
		t.Fatalf("results=%d %v", results, err)
	}
}
