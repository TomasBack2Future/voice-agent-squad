package terminalevents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitFixture models the 2026-11-06 S6 queue: three live assignments under one
// Dispatcher, each with its own canonical item, native session and Worker.
func waitFixture(t *testing.T, keys ...string) Store {
	t.Helper()
	s := fixture(t)
	for i, k := range keys {
		item := "TASK-" + k
		if _, err := s.DB.Exec(`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id)
 VALUES('repo',?,?,'dispatcher',1,1,0,'dispatched',1,?,'',?)`, k, fmt.Sprintf("github:repo#%d", 100+i), "native-"+k, item); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,intent,long) VALUES('repo',?,?,1,1,'',0)`, item, "worker-"+k); err != nil {
			t.Fatal(err)
		}
	}
	s.Recipient = ""
	return s
}

func say(t *testing.T, s Store, actor, thread string) int64 {
	t.Helper()
	r, err := s.DB.Exec(`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('repo',2,?,?,'say','m','[]','normal')`, actor, thread)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return id
}

func hold(s Store, t *testing.T, key string, expected int64, blocks string, waits ...Wait) DecisionRequest {
	return DecisionRequest{Reservation: key, Generation: 1, WorkerSession: "native-" + key, ExpectedRevision: expected,
		OutcomeID: say(t, s, "dispatcher", "TASK-"+key), Action: "hold", Condition: "structured wait", BlocksPhase: blocks, WaitsFor: waits}
}

func proceed(s Store, t *testing.T, key string, expected int64) DecisionRequest {
	return DecisionRequest{Reservation: key, Generation: 1, WorkerSession: "native-" + key, ExpectedRevision: expected,
		OutcomeID: say(t, s, "dispatcher", "TASK-"+key), Action: "proceed", Condition: "go first"}
}

func rejection(err error) string {
	var r *Rejection
	if errors.As(err, &r) {
		return r.Condition
	}
	return ""
}

func TestWaitCycleBetweenMergePhasesIsRejected(t *testing.T) {
	// #1925 waits for #1927's merge while #1927 is told to wait for #1925's merge.
	s := waitFixture(t, "D1925", "D1927")
	ctx := context.Background()
	if _, err := s.Decide(ctx, "dispatcher", hold(s, t, "D1925", 0, "merge", Wait{Reservation: "D1927", Phase: "merge", Until: "executed"})); err != nil {
		t.Fatal(err)
	}
	_, err := s.Decide(ctx, "dispatcher", hold(s, t, "D1927", 0, "merge", Wait{Reservation: "D1925", Phase: "merge", Until: "executed"}))
	if rejection(err) != "wait-cycle" || !strings.Contains(err.Error(), "D1927 -> D1925 merge executed -> D1927 merge executed") {
		t.Fatalf("cycle accepted or undiagnosed: %v", err)
	}
	// A hold on an unrelated phase is not a current-phase cycle.
	if _, err = s.Decide(ctx, "dispatcher", hold(s, t, "D1927", 0, "acceptance", Wait{Reservation: "D1925", Phase: "merge", Until: "executed"})); err != nil {
		t.Fatalf("independent phase wait refused: %v", err)
	}
}

func TestReorderPublishesPredecessorFirstAtomically(t *testing.T) {
	s := waitFixture(t, "D1925", "D1927")
	ctx := context.Background()
	if _, err := s.Decide(ctx, "dispatcher", hold(s, t, "D1925", 0, "merge", Wait{Reservation: "D1927", Phase: "merge", Until: "executed"})); err != nil {
		t.Fatal(err)
	}
	// Wrong order inside one reorder: the successor wait is checked first and the
	// whole batch is refused, leaving D1925 at its old revision.
	bad := []DecisionRequest{hold(s, t, "D1927", 0, "merge", Wait{Reservation: "D1925", Phase: "merge", Until: "executed"}), proceed(s, t, "D1925", 1)}
	_, err := s.DecideBatch(ctx, "dispatcher", bad)
	var be *BatchError
	if !errors.As(err, &be) || be.Index != 0 || rejection(err) != "wait-cycle" {
		t.Fatalf("misordered reorder: %v", err)
	}
	if d, _ := s.CurrentDecision(ctx, "D1925", 1, "native-D1925"); d.Revision != 1 || d.Action != "hold" {
		t.Fatalf("half-applied reorder: %+v", d)
	}
	// A failing later step (stale revision) also rolls back the earlier step.
	half := []DecisionRequest{proceed(s, t, "D1925", 1), hold(s, t, "D1927", 5, "merge", Wait{Reservation: "D1925", Phase: "merge", Until: "executed"})}
	if _, err = s.DecideBatch(ctx, "dispatcher", half); !errors.As(err, &be) || be.Index != 1 || !errors.Is(err, ErrStaleDecision) {
		t.Fatalf("stale step: %v", err)
	}
	if d, _ := s.CurrentDecision(ctx, "D1925", 1, "native-D1925"); d.Revision != 1 || d.Action != "hold" {
		t.Fatalf("earlier step survived a failed batch: %+v", d)
	}
	good := []DecisionRequest{proceed(s, t, "D1925", 1), hold(s, t, "D1927", 0, "merge", Wait{Reservation: "D1925", Phase: "merge", Until: "executed"})}
	out, err := s.DecideBatch(ctx, "dispatcher", good)
	if err != nil || out[0].Revision != 2 || out[0].Action != "proceed" || out[1].Revision != 1 || len(out[1].WaitsFor) != 1 || out[1].WaitsFor[0].Satisfied {
		t.Fatalf("reorder: %+v %v", out, err)
	}
	// Retrying the identical reorder after a lost response is idempotent.
	again, err := s.DecideBatch(ctx, "dispatcher", good)
	if err != nil || again[0].Revision != 2 || again[1].Revision != 1 {
		t.Fatalf("retry: %+v %v", again, err)
	}
	// Both owners have exactly one wake for their new revision.
	for _, k := range []string{"D1925", "D1927"} {
		w := s
		w.Recipient = "worker-" + k
		ev, err := w.Pending(ctx, "native-"+k, 0)
		if err != nil || len(ev) == 0 || ev[len(ev)-1].Kind != "decision-resolved" {
			t.Fatalf("%s wake: %+v %v", k, ev, err)
		}
	}
}

func TestDecisionWaitIdentityAndDeadline(t *testing.T) {
	s := waitFixture(t, "D1925", "D1927")
	ctx := context.Background()
	w := Wait{Reservation: "D1927", Phase: "merge", Until: "executed"}
	q := hold(s, t, "D1925", 0, "merge", w)
	q.WorkerSession = "native-D1927"
	if _, err := s.Decide(ctx, "dispatcher", q); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("wrong native accepted: %v", err)
	}
	q = hold(s, t, "D1925", 0, "merge", w)
	q.Generation = 2
	if _, err := s.Decide(ctx, "dispatcher", q); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("wrong generation accepted: %v", err)
	}
	q = hold(s, t, "D1925", 0, "merge", w)
	q.WaitDeadline = time.Now().Add(-time.Minute).Unix()
	if _, err := s.Decide(ctx, "dispatcher", q); rejection(err) != "expired-wait-deadline" {
		t.Fatalf("expired time box accepted: %v", err)
	}
	for _, bad := range []Wait{{Reservation: "D1925", Phase: "merge", Until: "executed"}, {Reservation: "NOPE", Phase: "merge", Until: "executed"}, {Reservation: "D1927", Until: "executed"}, {Reservation: "D1927", Phase: "merge", Until: "soon"}} {
		if _, err := s.Decide(ctx, "dispatcher", hold(s, t, "D1925", 0, "merge", bad)); err == nil {
			t.Fatalf("invalid wait accepted: %+v", bad)
		}
	}
	p := proceed(s, t, "D1925", 0)
	p.WaitsFor = []Wait{w}
	if _, err := s.Decide(ctx, "dispatcher", p); rejection(err) != "invalid-wait" {
		t.Fatalf("proceed with wait: %v", err)
	}
	q = hold(s, t, "D1925", 0, "merge", w)
	q.WaitDeadline = time.Now().Add(2 * time.Second).Unix()
	if _, err := s.Decide(ctx, "dispatcher", q); err != nil {
		t.Fatal(err)
	}
	d, err := s.CurrentDecision(ctx, "D1925", 1, "native-D1925")
	if err != nil || d.WaitOverdue || d.WaitsSatisfied {
		t.Fatalf("fresh time box: %+v %v", d, err)
	}
	time.Sleep(3 * time.Second)
	d, err = s.CurrentDecision(ctx, "D1925", 1, "native-D1925")
	if err != nil || !d.WaitOverdue || d.Action != "hold" {
		t.Fatalf("expired time box must be visible and never auto-proceed: %+v %v", d, err)
	}
	// The old revision cannot be used to answer the expired time box.
	if _, err = s.Decide(ctx, "dispatcher", proceed(s, t, "D1925", 0)); !errors.Is(err, ErrStaleDecision) {
		t.Fatalf("old revision: %v", err)
	}
	if d, err = s.Decide(ctx, "dispatcher", proceed(s, t, "D1925", 1)); err != nil || d.Revision != 2 {
		t.Fatalf("time-box resolution: %+v %v", d, err)
	}
}

func TestConcurrentConflictingWaitsCannotBothLand(t *testing.T) {
	s := waitFixture(t, "D1925", "D1927")
	ctx := context.Background()
	a := hold(s, t, "D1925", 0, "merge", Wait{Reservation: "D1927", Phase: "merge", Until: "executed"})
	b := hold(s, t, "D1927", 0, "merge", Wait{Reservation: "D1925", Phase: "merge", Until: "executed"})
	for round := 0; round < 5; round++ {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, q := range []DecisionRequest{a, b} {
			wg.Add(1)
			go func(i int, q DecisionRequest) {
				defer wg.Done()
				_, errs[i] = s.Decide(ctx, "dispatcher", q)
			}(i, q)
		}
		wg.Wait()
		landed := 0
		for _, err := range errs {
			if err == nil {
				landed++
			} else if rejection(err) != "wait-cycle" {
				t.Fatalf("unexpected error: %v", err)
			}
		}
		if landed != 1 {
			t.Fatalf("round %d: %d conflicting waits landed", round, landed)
		}
		// Reset for the next round: release whichever landed.
		for _, k := range []string{"D1925", "D1927"} {
			if d, _ := s.CurrentDecision(ctx, k, 1, "native-"+k); d.Revision > 0 && d.Action == "hold" {
				if _, err := s.Decide(ctx, "dispatcher", proceed(s, t, k, d.Revision)); err != nil {
					t.Fatal(err)
				}
			}
		}
		da, _ := s.CurrentDecision(ctx, "D1925", 1, "native-D1925")
		db, _ := s.CurrentDecision(ctx, "D1927", 1, "native-D1927")
		a = hold(s, t, "D1925", da.Revision, "merge", Wait{Reservation: "D1927", Phase: "merge", Until: "executed"})
		b = hold(s, t, "D1927", db.Revision, "merge", Wait{Reservation: "D1925", Phase: "merge", Until: "executed"})
	}
}

// The #1940 case: the owner reported that every S6 check ran (replay failed and
// moved to S7) while the window owner still waited for "remaining S6 checks".
func TestPhaseResultConsumptionReleasesOnlyTheRealDependency(t *testing.T) {
	s := waitFixture(t, "D1940", "D1949", "D1980")
	ctx := context.Background()
	s6 := Wait{Reservation: "D1940", Phase: "s6-acceptance", Until: "executed"}
	pass := Wait{Reservation: "D1940", Phase: "s6-acceptance", Until: "passed"}
	if _, err := s.Decide(ctx, "dispatcher", hold(s, t, "D1949", 0, "relaxed-window", s6)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Decide(ctx, "dispatcher", hold(s, t, "D1980", 0, "close", pass)); err != nil {
		t.Fatal(err)
	}
	// A foreign actor cannot record another owner's result.
	if _, err := s.RecordPhaseResult(ctx, "worker-D1949", PhaseResult{Reservation: "D1940", Generation: 1, WorkerSession: "native-D1940", Phase: "s6-acceptance", Checks: "all", Verdict: "failed", OutcomeID: say(t, s, "worker-D1949", "TASK-D1940")}); rejection(err) != "invalid-phase-result" {
		t.Fatalf("foreign result: %v", err)
	}
	if _, err := s.RecordPhaseResult(ctx, "worker-D1940", PhaseResult{Reservation: "D1940", Generation: 1, WorkerSession: "native-D1940", Phase: "s6-acceptance", Checks: "partial", Verdict: "passed", OutcomeID: say(t, s, "worker-D1940", "TASK-D1940")}); rejection(err) != "invalid-phase-result" {
		t.Fatalf("partial checks cannot pass: %v", err)
	}
	res := PhaseResult{Reservation: "D1940", Generation: 1, WorkerSession: "native-D1940", Phase: "s6-acceptance", Checks: "all", Verdict: "failed", Deferred: "replay re-verification on S7 (PR #1980)", OutcomeID: say(t, s, "worker-D1940", "TASK-D1940")}
	got, err := s.RecordPhaseResult(ctx, "worker-D1940", res)
	if err != nil || got.EventID == "" {
		t.Fatalf("record: %+v %v", got, err)
	}
	if again, err := s.RecordPhaseResult(ctx, "worker-D1940", res); err != nil || again.EventID != got.EventID {
		t.Fatalf("idempotent retry: %+v %v", again, err)
	}
	// The executed wait is met although the verdict failed; the passed wait is not.
	d49, _ := s.CurrentDecision(ctx, "D1949", 1, "native-D1949")
	d80, _ := s.CurrentDecision(ctx, "D1980", 1, "native-D1980")
	if !d49.WaitsSatisfied || d80.WaitsSatisfied {
		t.Fatalf("executed vs passed: %+v %+v", d49, d80)
	}
	// A new wait on a result that already exists is a stale condition.
	if _, err = s.Decide(ctx, "dispatcher", hold(s, t, "D1949", 1, "relaxed-window", s6)); rejection(err) != "wait-already-satisfied" {
		t.Fatalf("stale wait re-established: %v", err)
	}
	// The Dispatcher is woken through the existing receipt route, and its ACK is
	// refused until the fully satisfied dependent has a new decision.
	disp := s
	disp.Recipient = "dispatcher"
	ev, err := disp.Pending(ctx, "session", 0)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, e := range ev {
		if e.Kind == "phase-result" {
			id = e.ID
		}
	}
	if id == "" {
		t.Fatalf("no phase-result wake: %+v", ev)
	}
	if err = disp.Delivered(ctx, id, "session"); err != nil {
		t.Fatal(err)
	}
	if err = disp.Ack(ctx, id, "seen"); rejection(err) != "phase-result-unconsumed" || !strings.Contains(err.Error(), "D1949") || strings.Contains(err.Error(), "D1980") {
		t.Fatalf("ACK without consumption: %v", err)
	}
	if _, err = s.Decide(ctx, "dispatcher", proceed(s, t, "D1949", 1)); err != nil {
		t.Fatal(err)
	}
	if err = disp.Ack(ctx, id, "D1949 released by rev2; D1980 still waits for a passing S7 replay"); err != nil {
		t.Fatalf("ACK after consumption: %v", err)
	}
	// D1980 stays held: a failed verdict blocks only what required a pass.
	if d80, _ = s.CurrentDecision(ctx, "D1980", 1, "native-D1980"); d80.Action != "hold" || d80.WaitsSatisfied {
		t.Fatalf("passed-wait dependent released by a failure: %+v", d80)
	}
	// An older outcome cannot overwrite the recorded result.
	older := res
	older.OutcomeID = res.OutcomeID - 1
	older.Checks, older.Verdict = "partial", "partial"
	if _, err = s.RecordPhaseResult(ctx, "worker-D1940", older); rejection(err) != "stale-phase-result" && !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("older result replaced newer: %v", err)
	}
}

func TestPhaseResultEventRequiresRecordedResult(t *testing.T) {
	s := waitFixture(t, "D1940")
	ctx := context.Background()
	out := say(t, s, "worker-D1940", "TASK-D1940")
	if _, err := s.Publish(ctx, "worker-D1940", PublishRequest{Reservation: "D1940", Generation: 1, WorkerSession: "native-D1940", Kind: "phase-result", OutcomeID: out}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("prose minted a phase result: %v", err)
	}
}
