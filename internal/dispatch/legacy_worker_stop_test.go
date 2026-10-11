package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// These are isolated custody/state tests with a host-observer test double;
// they do not qualify a Claude native stop or a business donor migration.
func legacyStopFixture(t *testing.T) (*Store, WorkerHandoffRequest, LegacyStopInput) {
	t.Helper()
	s, q := workerHandoffFixture(t)
	if _, err := s.db.Exec(`DELETE FROM execution_authorizations`); err != nil {
		t.Fatal(err)
	}
	q.ExecutionID = ""
	q.LegacyStopID = "legacy-stop-1"
	q.HumanAuthoritySHA256 = strings.Repeat("a", 64)
	q.LegacyExpiresAt = s.now().Unix() + 3600
	q.ConsentOutcomeID = 0
	child := exec.Command("sh", "-c", "exit 0")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	s.legacyOwnerProbe = func(context.Context, string, int) ([]LegacyProcess, error) {
		return []LegacyProcess{{PID: child.Process.Pid, Start: "observed-before-exit", Executable: "isolated-observer-fixture"}}, nil
	}
	s.legacyWorkspaceProbe = func(context.Context, string) (string, error) { return strings.Repeat("b", 64), nil }
	workspace := t.TempDir()
	original, _ := json.Marshal(map[string]any{"schema_version": "agent-loop.assignment.v1", "assignment_id": "isolated/assignment", "repository": "o/r", "branch": "isolated", "base_sha": strings.Repeat("a", 40), "project_profile": map[string]any{"id": "isolated", "version": 1, "path": "isolated-profile.json"}, "evidence_required": []string{"test"}, "issue": "o/r#1", "item": "T", "worktree": workspace, "reservation": map[string]any{"key": "D", "generation": 1}, "role": map[string]any{"id": "worker", "skill": "agent-loop-worker", "version": 1}, "authorization": map[string]bool{"source_mutation": true, "production": false}})
	input := LegacyStopInput{Handoff: q, Workspace: workspace, Assignment: original, LeasePID: child.Process.Pid, Inventory: []LegacyExternalOperation{}}
	return s, q, input
}

func legacyHandoffFixture(t *testing.T, configure ...func(*Store, *WorkerHandoffRequest, *LegacyStopInput)) (*Store, WorkerHandoffRequest) {
	t.Helper()
	s, q, input := legacyStopFixture(t)
	for _, change := range configure {
		change(s, &q, &input)
	}
	ctx := context.Background()
	prepared, err := s.PrepareLegacyWorkerStop(ctx, q.Claim.Actor, input)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := s.ObserveLegacyWorkerStop(ctx, q.Controller.Actor, q.Controller, q.LegacyStopID)
	if err != nil {
		t.Fatal(err)
	}
	q.ConsentOutcomeID = prepared.ConsentOutcomeID
	q.LegacyStopSHA256 = observed.ObservationSHA256
	digest, err := WorkerHandoffDigest(q)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"schema_version": "squad.legacy-worker-stop-attestation.v1", "request_sha256": digest, "stop_sha256": q.LegacyStopSHA256, "human_authority_sha256": q.HumanAuthoritySHA256, "expires_at": q.LegacyExpiresAt})
	message, err := s.db.Exec(`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?, 'supervisory-attestation',?,'[]','high')`, s.repoID, s.now().Unix(), q.Controller.Actor, q.Expected.CanonicalItemID, string(body))
	if err != nil {
		t.Fatal(err)
	}
	q.LegacyAttestationID, err = message.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return s, q
}

func TestLegacyHandoffRetainsTerminalHistoryAndPausedAcceptance(t *testing.T) {
	s, q := legacyHandoffFixture(t, func(s *Store, q *WorkerHandoffRequest, input *LegacyStopInput) {
		if _, err := s.db.Exec(`INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES('repo-test','T','old-worker',2,3,'done'); DELETE FROM claims; UPDATE dispatch_reservations SET state='completed'`); err != nil {
			t.Fatal(err)
		}
		q.Expected.State = "completed"
		q.RetainedPhase = "acceptance"
		input.Handoff = *q
	})
	ctx := context.Background()
	receipt, err := s.WorkerHandoff(ctx, q.Controller.Actor, q)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Previous.State != "completed" || receipt.Reservation.State != "dispatched" || receipt.ClaimGeneration != 2 {
		t.Fatal(receipt)
	}
	var history int
	if err = s.db.QueryRow(`SELECT count(*) FROM claim_history WHERE outcome='done' AND agent_id='old-worker'`).Scan(&history); err != nil || history != 1 {
		t.Fatal("original terminal history changed", history, err)
	}
	var holder, worktree string
	if err = s.db.QueryRow(`SELECT agent_id,worktree FROM claims WHERE item_id='T'`).Scan(&holder, &worktree); err != nil || holder != q.NewActor || worktree != receipt.LegacyStop.Input.Workspace {
		t.Fatal(holder, worktree, err)
	}
	var action, condition string
	if err = s.db.QueryRow(`SELECT action,condition FROM dispatch_decisions WHERE generation=2`).Scan(&action, &condition); err != nil || action != "hold" || condition != "merge paused" {
		t.Fatal(action, condition, err)
	}
}

func TestLegacyNativeFenceRejectsOlderRuntimePinAndCallback(t *testing.T) {
	s, q, input := legacyStopFixture(t)
	ctx := context.Background()
	if _, err := s.PrepareLegacyWorkerStop(ctx, q.Claim.Actor, input); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO execution_authorizations(repo_id,id,item_id,holder,generation,binding,state,created_at,updated_at) VALUES('repo-test','stale','T','old-worker',1,'{"native":"old-session"}','active',1,1)`,
		`INSERT INTO dispatch_reservations(repo_id,item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id) VALUES('repo-test','OTHER','github:o/r#2','old-dispatcher',1,1,0,'dispatched',1,'old-session')`,
		`INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id) VALUES('repo-test','old-dispatcher','stale-callback','D',1,'old-session','T','blocked',1,1)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err == nil {
			t.Fatal("older runtime bypassed the durable fence", statement)
		}
	}
}

func TestLegacyHandoffPreservesHoldAndPersistentOldNativeFence(t *testing.T) {
	s, q := legacyHandoffFixture(t)
	ctx := context.Background()
	receipt, err := s.WorkerHandoff(ctx, q.Controller.Actor, q)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.LegacyStop == nil || receipt.LegacyStop.State != "transferred" || receipt.ExecutionID != "" || receipt.Reservation.Generation != 2 || receipt.ClaimGeneration != 2 {
		t.Fatal(receipt)
	}
	again, err := s.WorkerHandoff(ctx, q.Controller.Actor, q)
	if err != nil || !reflect.DeepEqual(again, receipt) {
		t.Fatal(again, err)
	}
	if err = s.CheckWorkerNative(ctx, q.Claim.Actor, q.Expected.WorkerThreadID); err == nil {
		t.Fatal("old native unfenced after transfer")
	}
	if err = s.CheckWorkerNative(ctx, q.NewActor, q.NewNative); err != nil {
		t.Fatal(err)
	}
	var action, condition string
	if err = s.db.QueryRow(`SELECT action,condition FROM dispatch_decisions WHERE generation=2`).Scan(&action, &condition); err != nil || action != "hold" || condition != "merge paused" {
		t.Fatal(action, condition, err)
	}
	var pins int
	_ = s.db.QueryRow(`SELECT count(*) FROM execution_authorizations`).Scan(&pins)
	if pins != 0 {
		t.Fatal("fabricated source pin", pins)
	}
}

func TestLegacyHandoffRejectsUnqualifiedAuthorityCustodyAndReplay(t *testing.T) {
	for _, kind := range []string{"stop hash", "human authority", "attestation actor", "expired", "unobserved", "native", "controller epoch", "changed claim", "worktree", "forged preparation", "live process", "pending receipt"} {
		t.Run(kind, func(t *testing.T) {
			s, q := legacyHandoffFixture(t)
			switch kind {
			case "stop hash":
				q.LegacyStopSHA256 = strings.Repeat("d", 64)
			case "human authority":
				q.HumanAuthoritySHA256 = strings.Repeat("d", 64)
			case "attestation actor":
				_, _ = s.db.Exec(`UPDATE messages SET agent_id='intruder' WHERE kind='supervisory-attestation'`)
			case "expired":
				q.LegacyExpiresAt = s.now().Unix()
			case "unobserved":
				_, _ = s.db.Exec(`UPDATE legacy_worker_stops SET state='prepared'`)
			case "native":
				q.Expected.WorkerThreadID = "other-native"
			case "controller epoch":
				q.Controller.Epoch++
			case "changed claim":
				_, _ = s.db.Exec(`UPDATE claims SET generation=2`)
			case "worktree":
				s.legacyWorkspaceProbe = func(context.Context, string) (string, error) { return strings.Repeat("d", 64), nil }
			case "forged preparation":
				_, _ = s.db.Exec(`DELETE FROM legacy_worker_stops`)
			case "live process":
				_, _ = s.db.Exec(`UPDATE legacy_worker_stops SET preparation=json_set(preparation,'$.processes[0].pid',?)`, os.Getpid())
			case "pending receipt":
				_, _ = s.db.Exec(`UPDATE terminal_event_receipts SET processed_at=0`)
			}
			if _, err := s.WorkerHandoff(context.Background(), q.Controller.Actor, q); err == nil {
				t.Fatal("unsafe legacy transfer accepted")
			}
			r, err := s.Get(context.Background(), q.Expected.ItemID)
			if err != nil || r.Generation != 1 {
				t.Fatal("partial transfer", r, err)
			}
		})
	}
}

func TestLegacyStopObservesClosedNativeWithoutFabricatingPin(t *testing.T) {
	s, q, input := legacyStopFixture(t)
	ctx := context.Background()
	p, err := s.PrepareLegacyWorkerStop(ctx, q.Claim.Actor, input)
	if err != nil {
		t.Fatal(err)
	}
	if p.State != "prepared" || p.ConsentOutcomeID < 1 {
		t.Fatal(p)
	}
	q.ConsentOutcomeID = p.ConsentOutcomeID
	stopped, err := s.ObserveLegacyWorkerStop(ctx, q.Controller.Actor, q.Controller, q.LegacyStopID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != "observed" || stopped.ObservationSHA256 == "" {
		t.Fatal(stopped)
	}
	var pins int
	if err = s.db.QueryRow(`SELECT count(*) FROM execution_authorizations`).Scan(&pins); err != nil || pins != 0 {
		t.Fatal(pins, err)
	}
	if err = s.CheckWorkerNative(ctx, q.Claim.Actor, q.Expected.WorkerThreadID); err == nil {
		t.Fatal("stopped old native admitted")
	}
}

func TestLegacyStopRejectsUntrustedLiveAndChangedCustody(t *testing.T) {
	for _, kind := range []string{"no owner proof", "live native", "wrong actor", "changed workspace", "unknown operation", "ENV"} {
		t.Run(kind, func(t *testing.T) {
			s, q, input := legacyStopFixture(t)
			ctx := context.Background()
			if kind == "no owner proof" {
				s.legacyOwnerProbe = nil
				input.LeasePID = os.Getpid()
			}
			if kind == "unknown operation" {
				input.Inventory = []LegacyExternalOperation{{Kind: "remote-writer", Identity: "unknown"}}
			}
			if kind == "ENV" {
				_, err := s.db.Exec(`INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo-test','ENV-1','old-worker',2,2)`)
				if err != nil {
					t.Fatal(err)
				}
			}
			actor := q.Claim.Actor
			if kind == "wrong actor" {
				actor = "intruder"
			}
			p, err := s.PrepareLegacyWorkerStop(ctx, actor, input)
			if kind == "no owner proof" || kind == "unknown operation" || kind == "ENV" || kind == "wrong actor" {
				if err == nil {
					t.Fatal("unsafe preparation accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "live native" {
				raw, _ := json.Marshal([]LegacyProcess{{PID: os.Getpid(), Start: "not-a-join", Executable: "fixture"}})
				_, err = s.db.Exec(`UPDATE legacy_worker_stops SET processes=?`, string(raw))
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "changed workspace" {
				s.legacyWorkspaceProbe = func(context.Context, string) (string, error) { return strings.Repeat("c", 64), nil }
			}
			if _, err = s.ObserveLegacyWorkerStop(ctx, q.Controller.Actor, q.Controller, p.ID); err == nil {
				t.Fatal("unsafe stop observation accepted")
			}
		})
	}
}

func TestLegacyHandoffConcurrentReplayAndLateRollback(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "concurrent replay", true: "late rollback"}[rollback], func(t *testing.T) {
			s, q := legacyHandoffFixture(t)
			if rollback {
				if _, err := s.db.Exec(`CREATE TRIGGER reject_legacy_receipt BEFORE INSERT ON worker_handoffs BEGIN SELECT RAISE(ABORT,'late isolated failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := s.WorkerHandoff(context.Background(), q.Controller.Actor, q)
					results <- err
				}()
			}
			wg.Wait()
			close(results)
			for err := range results {
				if (err != nil) != rollback {
					t.Fatal("unexpected transfer result", err)
				}
			}
			var receipts, history, decisions int
			for _, v := range []struct {
				query  string
				target *int
			}{{`SELECT count(*) FROM worker_handoffs`, &receipts}, {`SELECT count(*) FROM claim_history WHERE outcome='worker-handoff'`, &history}, {`SELECT count(*) FROM dispatch_decisions WHERE generation=2`, &decisions}} {
				if err := s.db.QueryRow(v.query).Scan(v.target); err != nil {
					t.Fatal(err)
				}
			}
			want := 1
			if rollback {
				want = 0
			}
			if receipts != want || history != want || decisions != want {
				t.Fatal("partial or duplicate transaction", receipts, history, decisions)
			}
			stop, err := s.LegacyWorkerStopReceipt(context.Background(), q.LegacyStopID)
			if err != nil {
				t.Fatal(err)
			}
			expected := "transferred"
			if rollback {
				expected = "observed"
			}
			if stop.State != expected {
				t.Fatal("stop state escaped transaction", stop.State)
			}
			if err := s.CheckWorkerNative(context.Background(), q.Claim.Actor, q.Expected.WorkerThreadID); err == nil {
				t.Fatal("rollback/replay removed persistent original-native fence")
			}
		})
	}
}
func TestLegacyPreparationRejectsPendingOriginalDecisionAndExpandedAssignment(t *testing.T) {
	for _, kind := range []string{"pending decision", "unknown assignment field", "deployer", "production", "missing assignment identity", "nested authorization field"} {
		t.Run(kind, func(t *testing.T) {
			s, q, input := legacyStopFixture(t)
			var assignment map[string]any
			_ = json.Unmarshal(input.Assignment, &assignment)
			switch kind {
			case "pending decision":
				_, _ = s.db.Exec(`UPDATE terminal_event_receipts SET recipient='old-worker',processed_at=0`)
			case "unknown assignment field":
				assignment["credentials"] = "must reject unknown fields"
			case "deployer":
				assignment["role"].(map[string]any)["id"] = "deployer"
			case "production":
				assignment["authorization"].(map[string]any)["production"] = true
			case "missing assignment identity":
				delete(assignment, "assignment_id")
			case "nested authorization field":
				assignment["authorization"].(map[string]any)["override"] = true
			}
			input.Assignment, _ = json.Marshal(assignment)
			if _, err := s.PrepareLegacyWorkerStop(context.Background(), q.Claim.Actor, input); err == nil {
				t.Fatal("unsafe preparation accepted")
			}
			var stops, fences int
			_ = s.db.QueryRow(`SELECT count(*) FROM legacy_worker_stops`).Scan(&stops)
			_ = s.db.QueryRow(`SELECT count(*) FROM worker_native_fences`).Scan(&fences)
			if stops != 0 || fences != 0 {
				t.Fatal("failed preparation retained partial fence", stops, fences)
			}
		})
	}
}
