package terminalevents

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zsiec/squad/internal/attest"
)

func autoHold(s Store, t *testing.T, key string, waits ...Wait) DecisionRequest {
	q := hold(s, t, key, 0, "", waits...)
	q.OnSatisfied = "proceed"
	return q
}

func storedDecision(t *testing.T, s Store, key string) (revision int64, action, onSatisfied string) {
	t.Helper()
	if err := s.DB.QueryRow(`SELECT revision,action,on_satisfied FROM dispatch_decisions WHERE repo_id='repo' AND reservation_key=?`, key).Scan(&revision, &action, &onSatisfied); err != nil {
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

// A terminal reservation records no event, so the held Worker's own listener
// read performs the release.
func TestTerminalWaitReleasesOnTheHeldWorkersOwnRead(t *testing.T) {
	s := waitFixture(t, "W", "P")
	ctx := context.Background()
	if _, err := s.Decide(ctx, "dispatcher", autoHold(s, t, "W", Wait{Reservation: "P", Until: "terminal"})); err != nil {
		t.Fatal(err)
	}
	worker := Store{DB: s.DB, Repo: s.Repo, Recipient: "worker-W"}
	if _, err := worker.Pending(ctx, "incarnation", 0); err != nil {
		t.Fatal(err)
	}
	if _, action, _ := storedDecision(t, s, "W"); action != "hold" {
		t.Fatal("released before the predecessor ended")
	}
	if _, err := s.DB.Exec(`UPDATE dispatch_reservations SET state='completed' WHERE item_id='P'`); err != nil {
		t.Fatal(err)
	}
	events, err := worker.Pending(ctx, "incarnation", 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("no wake after predecessor ended: %+v %v", events, err)
	}
	if rev, action, _ := storedDecision(t, s, "W"); rev != 2 || action != "proceed" {
		t.Fatalf("not released: revision %d action %s", rev, action)
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

// A marker left behind by a rewrite from an older binary is never honored.
func TestStaleMarkerFromAnOlderRewriteIsIgnored(t *testing.T) {
	s := waitFixture(t, "W", "P")
	ctx := context.Background()
	if _, err := s.Decide(ctx, "dispatcher", autoHold(s, t, "W", Wait{Reservation: "P", Until: "terminal"})); err != nil {
		t.Fatal(err)
	}
	// An older binary's upsert bumps the revision without knowing the wait columns.
	if _, err := s.DB.Exec(`UPDATE dispatch_decisions SET revision=2,condition='plain hold' WHERE reservation_key='W'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE dispatch_reservations SET state='completed' WHERE item_id='P'`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ReleaseSatisfied(ctx); err != nil || n != 0 {
		t.Fatalf("stale marker honored: %d %v", n, err)
	}
	if _, action, _ := storedDecision(t, s, "W"); action != "hold" {
		t.Fatal("plain hold was released")
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
	if err := run("merge", "merge"); err == nil {
		t.Fatal("operation of the held phase ran")
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
