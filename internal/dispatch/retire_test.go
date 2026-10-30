package dispatch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/store"
)

var (
	fixtureOnce sync.Once
	fixturePath string
	fixtureErr  error
)

// retireFixtureBinary builds a real executable named "claude" once per run.
func retireFixtureBinary(t *testing.T) string {
	t.Helper()
	fixtureOnce.Do(func() {
		dir, err := os.MkdirTemp("", "retire-fixture")
		if err != nil {
			fixtureErr = err
			return
		}
		fixturePath = filepath.Join(dir, "claude")
		out, err := exec.Command("go", "build", "-o", fixturePath, "testdata/retireclient/main.go").CombinedOutput()
		if err != nil {
			fixtureErr = errors.New(string(out))
		}
	})
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	return fixturePath
}

type fixtureClient struct {
	cmd    *exec.Cmd
	exited chan struct{}
}

func startClient(t *testing.T, native string, env ...string) *fixtureClient {
	t.Helper()
	cmd := exec.Command(retireFixtureBinary(t), "--session-id", native, "--model", "x")
	cmd.Env = append(os.Environ(), append([]string{"FIXTURE_NATIVE=" + native}, env...)...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &fixtureClient{cmd: cmd, exited: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(c.exited) }()
	var seen []int
	t.Cleanup(func() {
		for _, pid := range append(seen, c.helperPIDs()...) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
	})
	// The helper child must exist before the test judges descendants.
	deadline := time.Now().Add(5 * time.Second)
	for {
		parents, _ := legacyProcessParents(context.Background())
		if seen = descendants(parents, cmd.Process.Pid); len(seen) > 0 {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture helper did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (c *fixtureClient) alive() bool {
	select {
	case <-c.exited:
		return false
	default:
		return true
	}
}

func (c *fixtureClient) helperPIDs() []int {
	parents, _ := legacyProcessParents(context.Background())
	return descendants(parents, c.cmd.Process.Pid)
}

func pidGone(pid int) bool {
	stat, err := legacyPS(context.Background(), pid, "stat")
	return err != nil || stat == "" || strings.HasPrefix(stat, "Z")
}

// retireLedger stores one finished assignment: reservation D generation 1 bound to native.
func retireLedger(t *testing.T, native, state string, extra ...string) *Store {
	t.Helper()
	now := time.Unix(10000, 0)
	s := newDispatchStore(t, &now)
	s.retireWait = 3 * time.Second
	stmts := []string{
		`INSERT INTO dispatch_controller_bindings VALUES('repo-test','dispatcher','controller-native',1)`,
		`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES('repo-test','D','github:o/r#1','dispatcher',1,1,0,'` + state + `',1,'` + native + `','','T')`,
		`INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES('repo-test','T','worker',2,3,'done')`,
	}
	for _, q := range append(stmts, extra...) {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	return s
}

func req(native string) RetirementRequest {
	return RetirementRequest{Reservation: "D", Generation: 1, WorkerSession: native}
}

func TestRetireClientEndsOnlyTheFinishedNativesClientAndHelpers(t *testing.T) {
	target, bystander := "native-finished", "native-active"
	a, b := startClient(t, target), startClient(t, bystander)
	targetHelpers := a.helperPIDs()
	s := retireLedger(t, target, "completed")
	out, err := s.RetireClient(context.Background(), "dispatcher", true, req(target))
	if err != nil || out.State != "exited" {
		t.Fatal(out, err)
	}
	if a.alive() {
		t.Fatal("finished client survived")
	}
	for _, pid := range targetHelpers {
		if !pidGone(pid) {
			t.Fatal("own helper survived", pid)
		}
	}
	if !b.alive() || len(b.helperPIDs()) == 0 {
		t.Fatal("another native's client or helper was touched")
	}
	// A repeat or late request neither signals again nor changes the result.
	again, err := s.RetireClient(context.Background(), "dispatcher", true, req(target))
	if err != nil || again.State != "exited" {
		t.Fatal(again, err)
	}
	st, _ := s.Retirement(context.Background(), req(target))
	if st.ClientExit.State != "exited" || st.Color.State != "pending" || !st.Eligible {
		t.Fatalf("%+v", st)
	}
}

func TestRetireClientRefusesUnfinishedOrUnsafeAssignmentsWithoutSignalling(t *testing.T) {
	native := "native-guarded"
	cases := []struct {
		name, state string
		extra       []string
		mutate      func(*RetirementRequest)
		reason      string
	}{
		{"dispatched even with Issue closed", "dispatched", nil, nil, "reservation-not-terminal"},
		{"retained claim", "completed", []string{`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,state,generation) VALUES('repo-test','ENV-1','worker',2,2,'held',1)`}, nil, "claims-retained"},
		{"item still claimed", "completed", []string{`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,state,generation) VALUES('repo-test','T','worker',2,2,'held',1)`}, nil, "canonical-item-claimed"},
		{"active execution", "completed", []string{`INSERT INTO execution_authorizations(repo_id,id,item_id,holder,generation,binding,state,created_at,updated_at) VALUES('repo-test','e','X','worker',1,'{}','active',1,1)`}, nil, "execution-custody-active"},
		{"unresolved operation", "completed", []string{`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('repo-test',5,'worker','T','owned-operation','{"state":"started","request":{"reservation":"D","generation":1,"worker_session":"` + native + `","request_key":"k"}}','[]','normal')`}, nil, "owned-operation-unresolved"},
		{"unhandled event", "completed", []string{`INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id) VALUES('repo-test','dispatcher','ev','D',1,'` + native + `','T','issue-closed',1,1)`}, nil, "terminal-event-unhandled"},
		{"human hold", "completed", []string{`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('repo-test','D',1,'T',1,0,'hold','human pause','worker')`}, nil, "decision-hold"},
		{"fenced native", "completed", []string{`INSERT INTO worker_native_fences(repo_id,native_session,actor,reservation_key,generation,stop_id) VALUES('repo-test','` + native + `','worker','D',1,1)`}, nil, "native-fenced"},
		{"failed without handoff evidence", "failed", nil, nil, "failed-without-handoff-evidence"},
		{"wrong native", "completed", nil, func(q *RetirementRequest) { q.WorkerSession = "other-native" }, "worker-session-mismatch"},
		{"stale generation", "completed", nil, func(q *RetirementRequest) { q.Generation = 2 }, "stale-generation"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := startClient(t, native)
			s := retireLedger(t, native, c.state, c.extra...)
			q := req(native)
			if c.mutate != nil {
				c.mutate(&q)
			}
			_, err := s.RetireClient(context.Background(), "dispatcher", true, q)
			if err == nil || !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("want %s, got %v", c.reason, err)
			}
			if !client.alive() || len(client.helperPIDs()) == 0 {
				t.Fatal("client or helper was signalled")
			}
			var n int
			if e := s.db.QueryRow(`SELECT count(*) FROM messages WHERE kind='client-retirement'`).Scan(&n); e != nil || n != 0 {
				t.Fatal("refusal wrote a receipt", n, e)
			}
		})
	}
}

func TestRetireClientRejectsForeignCallers(t *testing.T) {
	native := "native-owner"
	client := startClient(t, native)
	s := retireLedger(t, native, "completed")
	for _, row := range []struct {
		caller     string
		controller bool
	}{{"intruder", false}, {"intruder", true}, {"dispatcher", false}} {
		if _, err := s.RetireClient(context.Background(), row.caller, row.controller, req(native)); err == nil {
			t.Fatal("foreign caller admitted", row)
		}
	}
	if !client.alive() {
		t.Fatal("client signalled for a foreign caller")
	}
	if out, err := s.RetireClient(context.Background(), "worker", false, req(native)); err != nil || out.State != "exited" {
		t.Fatal("original Worker could not retire its own finished client", out, err)
	}
}

func TestPartialRetirementIsEligibleDespiteItsFencingHold(t *testing.T) {
	native := "native-partial"
	client := startClient(t, native)
	s := retireLedger(t, native, "failed",
		`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('repo-test','D',1,'T',2,0,'hold','blocked','worker')`,
		`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('repo-test',5,'dispatcher','T','partial-retirement','{"request":{"expected":{"reservation_key":"D","generation":1,"worker_thread_id":"`+native+`"}}}','[]','normal')`)
	st, err := s.Retirement(context.Background(), req(native))
	if err != nil || !st.Eligible || st.Disposition != "partial" {
		t.Fatalf("%+v %v", st, err)
	}
	if out, err := s.RetireClient(context.Background(), "dispatcher", true, req(native)); err != nil || out.State != "exited" || client.alive() {
		t.Fatal(out, err)
	}
}

func TestRetireClientKeepsAnUnresponsiveClientAndRecordsTheTimeout(t *testing.T) {
	native := "native-stuck"
	client := startClient(t, native, "FIXTURE_IGNORE_TERM=1")
	s := retireLedger(t, native, "completed")
	s.retireWait = 500 * time.Millisecond
	out, err := s.RetireClient(context.Background(), "dispatcher", true, req(native))
	if err != nil || out.State != "exit-timeout" || !client.alive() {
		t.Fatal(out, err)
	}
	st, _ := s.Retirement(context.Background(), req(native))
	if st.ClientExit.State != "exit-timeout" || retirementSuccess(st.ClientExit.State) {
		t.Fatalf("%+v", st.ClientExit)
	}
}

func TestOrphanedHelperIsReportedAsPartialClientRetirement(t *testing.T) {
	native := "native-orphan"
	client := startClient(t, native, "FIXTURE_ORPHAN_HELPER=1")
	helpers := client.helperPIDs()
	t.Cleanup(func() {
		for _, pid := range helpers {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	s := retireLedger(t, native, "completed")
	s.retireWait = 500 * time.Millisecond
	out, err := s.RetireClient(context.Background(), "dispatcher", true, req(native))
	if err != nil || out.State != "helpers-remain" || client.alive() {
		t.Fatal(out, err)
	}
	if !strings.Contains(out.Detail, "receiver:") && !strings.Contains(out.Detail, "descendant:") {
		t.Fatal(out.Detail)
	}
}

func TestInterruptedRetirementResumesWithoutDuplicateSideEffects(t *testing.T) {
	native := "native-resume"
	s := retireLedger(t, native, "completed")
	now := time.Unix(10000, 0)
	s.now = func() time.Time { return now }
	intent := `INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('repo-test',10000,'dispatcher','T','client-retirement','{"schema":"squad.client-retirement.v1","reservation":"D","generation":1,"worker_session":"` + native + `","step":"client-exit","state":"running","token":"crashed"}','[]','normal')`
	if _, err := s.db.Exec(intent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetireClient(context.Background(), "dispatcher", true, req(native)); err == nil || !strings.Contains(err.Error(), "in flight") {
		t.Fatalf("live lease must hold, got %v", err)
	}
	now = now.Add(3 * time.Minute)
	client := startClient(t, native)
	out, err := s.RetireClient(context.Background(), "dispatcher", true, req(native))
	if err != nil || out.State != "exited" || client.alive() {
		t.Fatal(out, err)
	}
	var n int
	if e := s.db.QueryRow(`SELECT count(*) FROM messages WHERE kind='client-retirement' AND json_extract(body,'$.state')='exited'`).Scan(&n); e != nil || n != 1 {
		t.Fatal("expected exactly one terminal receipt", n, e)
	}
	// Crash after the client already left: the next attempt probes and succeeds.
	other := "native-gone"
	s2 := retireLedger(t, other, "completed")
	if out, err := s2.RetireClient(context.Background(), "dispatcher", true, req(other)); err != nil || out.State != "already-exited" {
		t.Fatal(out, err)
	}
}

func TestColorStepIsRecordedSeparatelyFromClientExit(t *testing.T) {
	native := "native-color"
	workspace := "2DCB442A-9B79-4FC2-9BF6-125E4073DBA6"
	client := startClient(t, native)
	s := retireLedger(t, native, "completed")
	calls := 0
	failing := func(context.Context, string) error { calls++; return errors.New("cmux unavailable") }
	if out, err := s.RetireColor(context.Background(), "dispatcher", req(native), workspace, failing); err != nil || out.State != "failed" {
		t.Fatal(out, err)
	}
	st, _ := s.Retirement(context.Background(), req(native))
	if st.Color.State != "failed" || st.ClientExit.State != "pending" || !client.alive() {
		t.Fatalf("a color failure must not imply or block client exit: %+v", st)
	}
	if out, err := s.RetireClient(context.Background(), "dispatcher", true, req(native)); err != nil || out.State != "exited" {
		t.Fatal(out, err)
	}
	st, _ = s.Retirement(context.Background(), req(native))
	if st.Color.State != "failed" || st.ClientExit.State != "exited" {
		t.Fatalf("client exit must not repair color: %+v", st)
	}
	ok := func(context.Context, string) error { calls++; return nil }
	if out, err := s.RetireColor(context.Background(), "dispatcher", req(native), workspace, ok); err != nil || out.State != "cleared" {
		t.Fatal(out, err)
	}
	if _, err := s.RetireColor(context.Background(), "dispatcher", req(native), workspace, ok); err != nil || calls != 2 {
		t.Fatal("cleared color was repeated", calls, err)
	}
	if _, err := s.RetireColor(context.Background(), "worker", req(native), workspace, ok); err == nil {
		t.Fatal("only the owning controller may clear a workspace color")
	}
	if _, err := s.RetireColor(context.Background(), "dispatcher", req(native), "not-a-uuid", ok); err == nil {
		t.Fatal("workspace must be an explicit UUID")
	}
}

func TestColorIsNotClearedForNonTerminalAssignments(t *testing.T) {
	native := "native-live"
	s := retireLedger(t, native, "dispatched")
	called := false
	run := func(context.Context, string) error { called = true; return nil }
	if _, err := s.RetireColor(context.Background(), "dispatcher", req(native), "2DCB442A-9B79-4FC2-9BF6-125E4073DBA6", run); err == nil || called {
		t.Fatal("blue workspace of live assignment must stay", err)
	}
}

// A Worker (or launcher supervisor) that runs retirement from inside the client's
// own process tree is an ancestor/descendant of the target; it must not wait on itself.
func TestRetireFromInsideTheClientsTreeDoesNotWaitOnItself(t *testing.T) {
	if os.Getenv("RETIRE_CALLER_DB") != "" {
		db, err := store.Open(os.Getenv("RETIRE_CALLER_DB"))
		if err != nil {
			t.Fatal(err)
		}
		s := New(db, "repo-test", nil)
		s.retireWait = 3 * time.Second
		out, err := s.RetireClient(context.Background(), "worker", false, req(os.Getenv("RETIRE_CALLER_NATIVE")))
		if err != nil || out.State != "exited" {
			t.Fatal(out, err)
		}
		return
	}
	native := "native-inside"
	path := filepath.Join(t.TempDir(), "global.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(10000, 0)
	s := New(db, "repo-test", func() time.Time { return now })
	t.Cleanup(func() { _ = db.Close() })
	for _, q := range []string{
		`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id,note,canonical_item_id) VALUES('repo-test','D','github:o/r#1','dispatcher',1,1,0,'completed',1,'` + native + `','','T')`,
		`INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES('repo-test','T','worker',2,3,'done')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	caller := "RETIRE_CALLER_DB=" + path + " RETIRE_CALLER_NATIVE=" + native + " " + exe + " -test.run=TestRetireFromInsideTheClientsTreeDoesNotWaitOnItself -test.count=1"
	client := startClient(t, native, "FIXTURE_CALLER="+caller)
	select {
	case <-client.exited:
	case <-time.After(30 * time.Second):
		t.Fatal("the in-tree caller never retired its client")
	}
	var st *RetirementStatus
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if st, _ = s.Retirement(context.Background(), req(native)); st != nil && st.ClientExit.State != "running" {
			break
		}
	}
	if st == nil || st.ClientExit.State != "exited" {
		t.Fatalf("%+v", st)
	}
}
