package terminalevents

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/attest"
)

func autoHold(s Store, t *testing.T, key string, waits ...Wait) DecisionRequest {
	q := hold(s, t, key, 0, "", waits...)
	q.OnSatisfied = "proceed"
	return q
}

func storedDecision(t *testing.T, s Store, key string) (revision int64, action, onSatisfied string) {
	t.Helper()
	if err := s.DB.QueryRow(`SELECT revision,action,CASE WHEN on_satisfied_revision=revision THEN 'proceed' ELSE '' END FROM dispatch_decisions WHERE repo_id='repo' AND reservation_key=?`, key).Scan(&revision, &action, &onSatisfied); err != nil {
		t.Fatal(err)
	}
	return
}

// The 2026-11 stalls: a dependency result arrived and the held Worker stayed
// idle until the Dispatcher noticed. A pre-authorized hold releases itself in
// the transaction that records the result and wakes the held Worker.
func TestPreAuthorizedHoldReleasesWhenItsLastWaitIsRecorded(t *testing.T) {
	s := waitFixture(t, "W", "P", "Q")
	ctx := context.Background()
	if _, err := s.Decide(ctx, "dispatcher", autoHold(s, t, "W",
		Wait{Reservation: "P", Phase: "deploy:staging", Until: "passed"}, Wait{Reservation: "Q", Phase: "merge", Until: "executed"})); err != nil {
		t.Fatal(err)
	}
	record := func(key, phase, verdict string) {
		t.Helper()
		p := PhaseResult{Reservation: key, Generation: 1, WorkerSession: "native-" + key, Phase: phase, Checks: "all", Verdict: verdict, OutcomeID: say(t, s, "worker-"+key, "TASK-"+key)}
		if _, err := s.RecordPhaseResult(ctx, "worker-"+key, p); err != nil {
			t.Fatal(err)
		}
	}
	record("Q", "merge", "passed")
	if rev, action, _ := storedDecision(t, s, "W"); rev != 1 || action != "hold" {
		t.Fatalf("released with one wait unmet: revision %d action %s", rev, action)
	}
	// A failed deploy does not satisfy a "passed" wait.
	record("P", "deploy:staging", "failed")
	if rev, action, _ := storedDecision(t, s, "W"); rev != 1 || action != "hold" {
		t.Fatalf("released on a failed verdict: revision %d action %s", rev, action)
	}
	record("P", "deploy:staging", "passed")
	rev, action, marker := storedDecision(t, s, "W")
	if rev != 2 || action != "proceed" || marker != "" {
		t.Fatalf("not released: revision %d action %s marker %q", rev, action, marker)
	}
	d, err := s.CurrentDecision(ctx, "W", 1, "native-W")
	if err != nil || d.Action != "proceed" || d.Revision != 2 || len(d.WaitsFor) != 0 || !strings.Contains(d.Condition, "waits satisfied") {
		t.Fatalf("owner read: %+v %v", d, err)
	}
	worker := Store{DB: s.DB, Repo: s.Repo, Recipient: "worker-W"}
	events, err := worker.Pending(ctx, "incarnation", 0)
	found := false
	for _, e := range events {
		found = found || (e.Kind == "decision-resolved" && strings.HasSuffix(e.ID, "/waits-released-r2"))
	}
	if err != nil || !found {
		t.Fatalf("held Worker was not woken: %+v %v", events, err)
	}
	var history, automatic int
	_ = s.DB.QueryRow(`SELECT count(*),sum(decided_by='waits-satisfied') FROM dispatch_decision_history WHERE reservation_key='W'`).Scan(&history, &automatic)
	if history != 2 || automatic != 1 {
		t.Fatalf("history rows %d automatic %d", history, automatic)
	}
	// The release is one transition: recording the same fact again changes nothing.
	if n, err := s.ReleaseSatisfied(ctx); err != nil || n != 0 {
		t.Fatalf("second release %d %v", n, err)
	}
	// The Dispatcher's stale CAS is refused, so it learns the hold was released.
	if _, err = s.Decide(ctx, "dispatcher", proceed(s, t, "W", 1)); err == nil {
		t.Fatal("stale revision accepted after automatic release")
	}
}

// A hold without the marker is a Dispatcher or human gate: it never releases.
func TestUnmarkedHoldStillNeedsADecision(t *testing.T) {
	s := waitFixture(t, "W", "P")
	ctx := context.Background()
	if _, err := s.Decide(ctx, "dispatcher", hold(s, t, "W", 0, "", Wait{Reservation: "P", Phase: "merge", Until: "executed"})); err != nil {
		t.Fatal(err)
	}
	p := PhaseResult{Reservation: "P", Generation: 1, WorkerSession: "native-P", Phase: "merge", Checks: "all", Verdict: "passed", OutcomeID: say(t, s, "worker-P", "TASK-P")}
	if _, err := s.RecordPhaseResult(ctx, "worker-P", p); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ReleaseSatisfied(ctx); err != nil || n != 0 {
		t.Fatalf("unmarked hold released: %d %v", n, err)
	}
	d, err := s.CurrentDecision(ctx, "W", 1, "native-W")
	if err != nil || d.Action != "hold" || !d.WaitsSatisfied {
		t.Fatalf("unmarked hold: %+v %v", d, err)
	}
}

// A release the recording transaction could not perform (custody was ambiguous
// then) is applied by a later read and delivered to the held Worker, who can
// acknowledge it like any decision.
func TestDeferredReleaseIsAppliedOnALaterReadAndAcknowledged(t *testing.T) {
	s := waitFixture(t, "W", "P")
	ctx := context.Background()
	if _, err := s.Decide(ctx, "dispatcher", autoHold(s, t, "W", Wait{Reservation: "P", Phase: "merge", Until: "executed"})); err != nil {
		t.Fatal(err)
	}
	// A second claimant makes custody ambiguous: no release, and the result is
	// still recorded.
	if _, err := s.DB.Exec(`INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES('repo','TASK-W','intruder',1,2,'released')`); err != nil {
		t.Fatal(err)
	}
	p := PhaseResult{Reservation: "P", Generation: 1, WorkerSession: "native-P", Phase: "merge", Checks: "all", Verdict: "failed", OutcomeID: say(t, s, "worker-P", "TASK-P")}
	if _, err := s.RecordPhaseResult(ctx, "worker-P", p); err != nil {
		t.Fatalf("an unreleasable dependent failed the producer's result: %v", err)
	}
	if rev, action, marker := storedDecision(t, s, "W"); rev != 1 || action != "hold" || marker != "proceed" {
		t.Fatalf("released under ambiguous custody: %d %s %q", rev, action, marker)
	}
	if _, err := s.DB.Exec(`DELETE FROM claim_history WHERE agent_id='intruder'`); err != nil {
		t.Fatal(err)
	}
	// Any recipient's read applies it; only the held Worker receives it.
	dispatcher := Store{DB: s.DB, Repo: s.Repo, Recipient: "dispatcher"}
	events, err := dispatcher.Pending(ctx, "controller", 0)
	for _, e := range events {
		if strings.Contains(e.ID, "/waits-released-") {
			t.Fatalf("release delivered to the Dispatcher: %+v", e)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if rev, action, _ := storedDecision(t, s, "W"); rev != 2 || action != "proceed" {
		t.Fatalf("not released on a later read: %d %s", rev, action)
	}
	worker := Store{DB: s.DB, Repo: s.Repo, Recipient: "worker-W"}
	events, err = worker.Pending(ctx, "incarnation", 0)
	id := ""
	for _, e := range events {
		if strings.HasSuffix(e.ID, "/waits-released-r2") {
			id = e.ID
		}
	}
	if err != nil || id == "" {
		t.Fatalf("held Worker not woken: %+v %v", events, err)
	}
	if err = worker.Delivered(ctx, id, "incarnation"); err != nil {
		t.Fatal(err)
	}
	if err = worker.Ack(ctx, id, "resumed at revision 2"); err != nil {
		t.Fatal(err)
	}
}

// Whether a successor may go when its predecessor ended is a judgment: a
// reservation also ends by failing, being cancelled or being partially retired.
func TestTerminalWaitsAreNeverPreAuthorized(t *testing.T) {
	s := waitFixture(t, "W", "P")
	ctx := context.Background()
	if _, err := s.Decide(ctx, "dispatcher", autoHold(s, t, "W", Wait{Reservation: "P", Until: "terminal"})); rejection(err) != "invalid-wait" {
		t.Fatalf("terminal wait accepted with a release marker: %v", err)
	}
	// A row written another way is still not released.
	if _, err := s.Decide(ctx, "dispatcher", hold(s, t, "W", 0, "", Wait{Reservation: "P", Until: "terminal"})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE dispatch_decisions SET on_satisfied_revision=revision WHERE reservation_key='W'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE dispatch_reservations SET state='failed' WHERE item_id='P'`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ReleaseSatisfied(ctx); err != nil || n != 0 {
		t.Fatalf("released on a failed predecessor: %d %v", n, err)
	}
}

// The authorization belongs to the Dispatcher and the time box that carried it.
func TestReleaseDoesNotSurviveAnExpiredTimeBoxOrAControllerChange(t *testing.T) {
	ctx := context.Background()
	satisfy := func(s Store) {
		t.Helper()
		if _, err := s.DB.Exec(`INSERT INTO dispatch_phase_results(repo_id,reservation_key,phase,generation,item_id,checks,verdict,deferred,outcome_id,recorded_by,recorded_at) VALUES('repo','P','merge',1,'TASK-P','all','passed','',1,'worker-P',5)`); err != nil {
			t.Fatal(err)
		}
	}
	s := waitFixture(t, "W", "P")
	q := autoHold(s, t, "W", Wait{Reservation: "P", Phase: "merge", Until: "executed"})
	q.WaitDeadline = time.Now().Add(time.Hour).Unix()
	if _, err := s.Decide(ctx, "dispatcher", q); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE dispatch_decisions SET wait_deadline=? WHERE reservation_key='W'`, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	satisfy(s)
	if n, err := s.ReleaseSatisfied(ctx); err != nil || n != 0 {
		t.Fatalf("released after its time box expired: %d %v", n, err)
	}
	d, err := s.CurrentDecision(ctx, "W", 1, "native-W")
	if err != nil || d.Action != "hold" || !d.WaitsSatisfied {
		t.Fatalf("expired hold not left for the Dispatcher: %+v %v", d, err)
	}

	s = waitFixture(t, "W", "P")
	if _, err := s.Decide(ctx, "dispatcher", autoHold(s, t, "W", Wait{Reservation: "P", Phase: "merge", Until: "executed"})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE dispatch_reservations SET reserved_by='successor' WHERE item_id='W'`); err != nil {
		t.Fatal(err)
	}
	satisfy(s)
	if n, err := s.ReleaseSatisfied(ctx); err != nil || n != 0 {
		t.Fatalf("a successor Dispatcher inherited the authorization: %d %v", n, err)
	}
}

// Many readers and a Dispatcher decision race for one hold: exactly one
// transition wins, with one history row and at most one wake for it.
func TestConcurrentReleasesAndADecisionYieldOneTransition(t *testing.T) {
	for round := 0; round < 5; round++ {
		s := waitFixture(t, "W", "P")
		ctx := context.Background()
		if _, err := s.Decide(ctx, "dispatcher", autoHold(s, t, "W", Wait{Reservation: "P", Phase: "merge", Until: "executed"})); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec(`INSERT INTO dispatch_phase_results(repo_id,reservation_key,phase,generation,item_id,checks,verdict,deferred,outcome_id,recorded_by,recorded_at) VALUES('repo','P','merge',1,'TASK-P','all','passed','',1,'worker-P',5)`); err != nil {
			t.Fatal(err)
		}
		next := proceed(s, t, "W", 1)
		var wg sync.WaitGroup
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = s.ReleaseSatisfied(ctx) }()
		}
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.Decide(ctx, "dispatcher", next) }()
		wg.Wait()
		rev, action, _ := storedDecision(t, s, "W")
		var history, wakes int
		_ = s.DB.QueryRow(`SELECT count(*) FROM dispatch_decision_history WHERE reservation_key='W'`).Scan(&history)
		_ = s.DB.QueryRow(`SELECT count(*) FROM terminal_event_receipts WHERE reservation_key='W' AND event_id LIKE '%/waits-released-%'`).Scan(&wakes)
		if rev != 2 || action != "proceed" || history != 2 || wakes > 1 {
			t.Fatalf("round %d: revision %d action %s history %d release wakes %d", round, rev, action, history, wakes)
		}
	}
}

func TestOnSatisfiedRequiresAStructuredHold(t *testing.T) {
	s := waitFixture(t, "W", "P")
	ctx := context.Background()
	noWait := hold(s, t, "W", 0, "")
	noWait.OnSatisfied = "proceed"
	if _, err := s.Decide(ctx, "dispatcher", noWait); rejection(err) != "invalid-wait" {
		t.Fatalf("marker without a wait: %v", err)
	}
	bad := autoHold(s, t, "W", Wait{Reservation: "P", Until: "terminal"})
	bad.OnSatisfied = "close"
	if _, err := s.Decide(ctx, "dispatcher", bad); rejection(err) != "invalid-wait" {
		t.Fatalf("unknown marker: %v", err)
	}
	go1 := proceed(s, t, "W", 0)
	go1.OnSatisfied = "proceed"
	if _, err := s.Decide(ctx, "dispatcher", go1); rejection(err) != "invalid-wait" {
		t.Fatalf("marker on a proceed: %v", err)
	}
}

// During a rollout a session's pinned binary can still set decisions. Neither a
// writer that knows the wait columns (the previous release) nor one that does
// not may inherit an earlier revision's release authorization.
func TestOlderWritersNeverInheritTheReleaseAuthorization(t *testing.T) {
	writers := map[string]string{
		// The exact upsert of the previous release (waits, no authorization column).
		"knows waits": `INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent,merge_policy,waits_for,blocks_phase,wait_deadline,waits_revision) VALUES('repo','W',1,'TASK-W',2,?,'hold','judge P first','worker-W','standing','[{"reservation":"P","phase":"merge","until":"executed"}]','',0,2)
 ON CONFLICT(repo_id,reservation_key,generation,item_id) DO UPDATE SET revision=excluded.revision,outcome_id=excluded.outcome_id,action=excluded.action,condition=excluded.condition,worker_agent=excluded.worker_agent,merge_policy=excluded.merge_policy,waits_for=excluded.waits_for,blocks_phase=excluded.blocks_phase,wait_deadline=excluded.wait_deadline,waits_revision=excluded.waits_revision`,
		// The older upsert that knows neither.
		"plain": `INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent,merge_policy) VALUES('repo','W',1,'TASK-W',2,?,'hold','plain hold','worker-W','standing')
 ON CONFLICT(repo_id,reservation_key,generation,item_id) DO UPDATE SET revision=excluded.revision,outcome_id=excluded.outcome_id,action=excluded.action,condition=excluded.condition,worker_agent=excluded.worker_agent,merge_policy=excluded.merge_policy`,
	}
	for name, upsert := range writers {
		t.Run(name, func(t *testing.T) {
			s := waitFixture(t, "W", "P")
			ctx := context.Background()
			if _, err := s.Decide(ctx, "dispatcher", autoHold(s, t, "W", Wait{Reservation: "P", Phase: "merge", Until: "executed"})); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(upsert, say(t, s, "dispatcher", "TASK-W")); err != nil {
				t.Fatal(err)
			}
			p := PhaseResult{Reservation: "P", Generation: 1, WorkerSession: "native-P", Phase: "merge", Checks: "all", Verdict: "failed", OutcomeID: say(t, s, "worker-P", "TASK-P")}
			if _, err := s.RecordPhaseResult(ctx, "worker-P", p); err != nil {
				t.Fatal(err)
			}
			if n, err := s.ReleaseSatisfied(ctx); err != nil || n != 0 {
				t.Fatalf("inherited authorization honored: %d %v", n, err)
			}
			if rev, action, marker := storedDecision(t, s, "W"); rev != 2 || action != "hold" || marker != "" {
				t.Fatalf("revision 2 was released or reads as authorized: %d %s %q", rev, action, marker)
			}
			d, err := s.CurrentDecision(ctx, "W", 1, "native-W")
			if err != nil || d.OnSatisfied != "" {
				t.Fatalf("owner read shows an authorization nobody gave: %+v %v", d, err)
			}
		})
	}
}

// A hold whose waits cannot be decoded is skipped; it never fails another
// owner's phase result.
func TestUndecodableHoldDoesNotFailAnUnrelatedPhaseResult(t *testing.T) {
	s := waitFixture(t, "W", "P")
	ctx := context.Background()
	if _, err := s.Decide(ctx, "dispatcher", autoHold(s, t, "W", Wait{Reservation: "P", Phase: "merge", Until: "executed"})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE dispatch_decisions SET waits_for='not json' WHERE reservation_key='W'`); err != nil {
		t.Fatal(err)
	}
	p := PhaseResult{Reservation: "P", Generation: 1, WorkerSession: "native-P", Phase: "review", Checks: "all", Verdict: "passed", OutcomeID: say(t, s, "worker-P", "TASK-P")}
	if _, err := s.RecordPhaseResult(ctx, "worker-P", p); err != nil {
		t.Fatalf("an undecodable hold failed an unrelated result: %v", err)
	}
}

// A hold scoped to one phase stops only that phase's operations.
func TestPhaseScopedHoldAllowsOperationsOfOtherPhases(t *testing.T) {
	s := waitFixture(t, "W", "P")
	ctx := context.Background()
	if _, err := s.Decide(ctx, "dispatcher", hold(s, t, "W", 0, "merge", Wait{Reservation: "P", Phase: "merge", Until: "executed"})); err != nil {
		t.Fatal(err)
	}
	run := func(key, phase string) error {
		q := OperationRequest{Reservation: "W", Generation: 1, WorkerSession: "native-W", ExpectedDecision: 1, RequestKey: key, Phase: phase}
		opts := attest.RunOpts{ItemID: "TASK-W", Kind: attest.KindTest, AgentID: "worker-W", Command: "exit 0", RepoRoot: t.TempDir(), AttDir: filepath.Join(t.TempDir(), "evidence")}
		_, err := s.RunOperation(ctx, "worker-W", q, opts)
		return err
	}
	if err := run("unit", "test"); err != nil {
		t.Fatalf("operation of an unblocked phase refused: %v", err)
	}
	// The held phase is refused however it is spelled: a candidate-specific
	// name under it, a different case, padding, or a name that is not a phase.
	for i, phase := range []string{"merge", "merge:pr1972", "Merge", "merge ", "not a phase!"} {
		if err := run("held-"+string(rune('a'+i)), phase); err == nil {
			t.Fatalf("operation with phase %q ran under a hold on merge", phase)
		}
	}
	if err := run("unnamed", ""); err == nil {
		t.Fatal("operation naming no phase ran under a hold")
	}
	// An unscoped hold stops every operation, as before.
	if _, err := s.Decide(ctx, "dispatcher", hold(s, t, "W", 1, "", Wait{Reservation: "P", Phase: "merge", Until: "executed"})); err != nil {
		t.Fatal(err)
	}
	q := OperationRequest{Reservation: "W", Generation: 1, WorkerSession: "native-W", ExpectedDecision: 2, RequestKey: "unit2", Phase: "test"}
	opts := attest.RunOpts{ItemID: "TASK-W", Kind: attest.KindTest, AgentID: "worker-W", Command: "exit 0", RepoRoot: t.TempDir(), AttDir: filepath.Join(t.TempDir(), "evidence")}
	if _, err := s.RunOperation(ctx, "worker-W", q, opts); err == nil {
		t.Fatal("operation ran under an unscoped hold")
	}
}
