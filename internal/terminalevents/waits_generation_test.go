package terminalevents

import (
	"context"
	"strings"
	"testing"
)

// reReserve models the supported lifetime change of one reservation key: a
// failed/partially retired assignment is reserved again (generation+1, new
// reserved_at, new native and Worker), as dispatch.Reserve does.
func reReserve(t *testing.T, s Store, key string) {
	t.Helper()
	if _, err := s.DB.Exec(`UPDATE dispatch_reservations SET generation=generation+1,reserved_at=5,worker_thread_id=?,state='dispatched' WHERE repo_id='repo' AND item_id=?`, "native2-"+key, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`DELETE FROM claims WHERE repo_id='repo' AND item_id=?`, "TASK-"+key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,intent,long) VALUES('repo',?,?,6,6,'',0)`, "TASK-"+key, "worker2-"+key); err != nil {
		t.Fatal(err)
	}
}

func laterSay(t *testing.T, s Store, actor, thread string) int64 {
	t.Helper()
	r, err := s.DB.Exec(`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('repo',7,?,?,'say','m','[]','normal')`, actor, thread)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return id
}

// Review question (2026-11-07): results are keyed by reservation and phase, not
// generation. A result is evidence of the named checks, whoever ran them; the
// phase name, not the custody generation, identifies what was checked.
func TestPhaseResultAcrossGenerationsIsEvidenceNeverAStaleRelease(t *testing.T) {
	s := waitFixture(t, "D1940", "D1949")
	ctx := context.Background()
	if _, err := s.RecordPhaseResult(ctx, "worker-D1940", PhaseResult{Reservation: "D1940", Generation: 1, WorkerSession: "native-D1940", Phase: "acceptance", Checks: "all", Verdict: "passed", OutcomeID: say(t, s, "worker-D1940", "TASK-D1940")}); err != nil {
		t.Fatal(err)
	}
	reReserve(t, s, "D1940") // generation 2: a new lifetime for a new candidate

	// The old custody can no longer record anything for this reservation.
	if _, err := s.RecordPhaseResult(ctx, "worker-D1940", PhaseResult{Reservation: "D1940", Generation: 1, WorkerSession: "native-D1940", Phase: "acceptance", Checks: "all", Verdict: "passed", OutcomeID: laterSay(t, s, "worker-D1940", "TASK-D1940")}); err == nil {
		t.Fatal("generation-1 Worker recorded after the reservation moved to generation 2")
	}
	// A new wait on the reused phase name is refused, not silently released by
	// the earlier generation's result, and the refusal names that provenance.
	_, err := s.Decide(ctx, "dispatcher", hold(s, t, "D1949", 0, "window", Wait{Reservation: "D1940", Phase: "acceptance", Until: "passed"}))
	if rejection(err) != "wait-already-satisfied" || !strings.Contains(err.Error(), "generation 1") || !strings.Contains(err.Error(), "candidate-specific phase") {
		t.Fatalf("stale same-phase result: %v", err)
	}
	// The new candidate's checks use their own phase name and stay unmet.
	d, err := s.Decide(ctx, "dispatcher", hold(s, t, "D1949", 0, "window", Wait{Reservation: "D1940", Phase: "acceptance:s7", Until: "passed"}))
	if err != nil || d.WaitsSatisfied {
		t.Fatalf("candidate-specific wait: %+v %v", d, err)
	}
	// The generation-2 Worker records the new phase; it releases the wait.
	if _, err = s.RecordPhaseResult(ctx, "worker2-D1940", PhaseResult{Reservation: "D1940", Generation: 2, WorkerSession: "native2-D1940", Phase: "acceptance:s7", Checks: "all", Verdict: "passed", OutcomeID: laterSay(t, s, "worker2-D1940", "TASK-D1940")}); err != nil {
		t.Fatal(err)
	}
	if d, err = s.CurrentDecision(ctx, "D1949", 1, "native-D1949"); err != nil || !d.WaitsSatisfied || !strings.Contains(d.WaitsFor[0].Evidence, "generation 2") {
		t.Fatalf("new-generation result: %+v %v", d, err)
	}
	// Re-running the same phase in generation 2 replaces the old result: an
	// incomplete rerun reopens a passed wait instead of leaving the old pass.
	if _, err = s.RecordPhaseResult(ctx, "worker2-D1940", PhaseResult{Reservation: "D1940", Generation: 2, WorkerSession: "native2-D1940", Phase: "acceptance", Checks: "partial", Verdict: "partial", OutcomeID: laterSay(t, s, "worker2-D1940", "TASK-D1940")}); err != nil {
		t.Fatal(err)
	}
	if ok, _, _ := waitSatisfied(ctx, s.DB, "repo", Wait{Reservation: "D1940", Phase: "acceptance", Until: "passed"}); ok {
		t.Fatal("an older generation's pass survived a newer incomplete rerun")
	}
}
