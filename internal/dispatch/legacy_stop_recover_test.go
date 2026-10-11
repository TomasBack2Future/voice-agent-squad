package dispatch

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func legacyRecoveryFixture(q WorkerHandoffRequest, id, previous, observation string, now time.Time) LegacyStopRecoveryInput {
	q.ConsentOutcomeID, q.LegacyAttestationID = 0, 0
	q.LegacyStopSHA256 = ""
	q.LegacyRecoveryID, q.LegacyExpiresAt = id, now.Unix()+3600
	return LegacyStopRecoveryInput{ID: id, ExpectedRecoveryID: previous, ExpectedObservationSHA: observation, Reason: "current controller reconciled outage; retain original scope and fence", Handoff: q}
}

func recoveredFinalAuthority(t *testing.T, s *Store, stop LegacyStopReceipt) WorkerHandoffRequest {
	t.Helper()
	q := stop.Recovery.Handoff
	q.ConsentOutcomeID, q.LegacyStopSHA256 = stop.ConsentOutcomeID, stop.ObservationSHA256
	digest, err := WorkerHandoffDigest(q)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"schema_version": "squad.legacy-worker-stop-attestation.v1", "request_sha256": digest, "stop_sha256": q.LegacyStopSHA256, "human_authority_sha256": q.HumanAuthoritySHA256, "expires_at": q.LegacyExpiresAt})
	message, err := s.db.Exec(`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?, 'supervisory-attestation',?,'[]','high')`, s.repoID, s.now().Unix(), q.Controller.Actor, q.Expected.CanonicalItemID, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	q.LegacyAttestationID, _ = message.LastInsertId()
	return q
}

func TestLegacyRecoveryAmendsCurrentEpochDecisionAndHeartbeat(t *testing.T) {
	s, q, input := legacyStopFixture(t)
	ctx := context.Background()
	prepared, err := s.PrepareLegacyWorkerStop(ctx, q.Claim.Actor, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE dispatch_controller_bindings SET epoch=2; UPDATE dispatch_decisions SET revision=3; UPDATE claims SET last_touch=4`); err != nil {
		t.Fatal(err)
	}
	q.Controller.Epoch, q.DecisionRevision, q.Claim.LastTouch = 2, 3, 4
	recovery := legacyRecoveryFixture(q, "recover-epoch", "", "", s.now())
	stop, err := s.RecoverLegacyWorkerStop(ctx, q.Controller.Actor, recovery)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stop.Input, prepared.Input) || !reflect.DeepEqual(stop.NativeAdmission, prepared.NativeAdmission) || !reflect.DeepEqual(stop.Processes, prepared.Processes) || stop.ConsentOutcomeID != prepared.ConsentOutcomeID {
		t.Fatal("amendment replaced immutable original consent/source/native history")
	}
	final := recoveredFinalAuthority(t, s, stop)
	if _, err = s.WorkerHandoff(ctx, q.Controller.Actor, final); err != nil {
		t.Fatal(err)
	}
	var action string
	if err = s.db.QueryRow(`SELECT action FROM dispatch_decisions WHERE generation=2`).Scan(&action); err != nil || action != "hold" {
		t.Fatal("recovery released the original hold", action, err)
	}
	if s.CheckWorkerNative(ctx, q.Claim.Actor, q.Expected.WorkerThreadID) == nil {
		t.Fatal("recovery reopened original writer")
	}
}

func TestLegacyRecoveryRejectsUnsafeScopeAndStaleCAS(t *testing.T) {
	for _, scenario := range []string{"foreign actor", "epoch", "item", "owner", "claim generation", "replacement", "authority", "decision", "expired", "overlong", "source", "previous", "observation", "missing fence"} {
		t.Run(scenario, func(t *testing.T) {
			s, q, input := legacyStopFixture(t)
			ctx := context.Background()
			if _, err := s.PrepareLegacyWorkerStop(ctx, q.Claim.Actor, input); err != nil {
				t.Fatal(err)
			}
			recovery := legacyRecoveryFixture(q, "recover-guard", "", "", s.now())
			actor := q.Controller.Actor
			switch scenario {
			case "foreign actor":
				actor = "intruder"
			case "epoch":
				recovery.Handoff.Controller.Epoch++
			case "item":
				recovery.Handoff.Claim.Item = "other"
			case "owner":
				recovery.Handoff.Claim.Actor = "other"
			case "claim generation":
				recovery.Handoff.Claim.Generation++
			case "replacement":
				recovery.Handoff.NewNative = "other"
			case "authority":
				recovery.Handoff.HumanAuthoritySHA256 = strings.Repeat("d", 64)
			case "decision":
				recovery.Handoff.DecisionRevision++
			case "expired":
				recovery.Handoff.LegacyExpiresAt = s.now().Unix()
			case "overlong":
				recovery.Handoff.LegacyExpiresAt = s.now().Unix() + 86401
			case "source":
				s.legacyWorkspaceProbe = func(context.Context, string) (string, error) { return strings.Repeat("d", 64), nil }
			case "previous":
				recovery.ExpectedRecoveryID = "other"
			case "observation":
				recovery.ExpectedObservationSHA = strings.Repeat("d", 64)
			case "missing fence":
				if _, err := s.db.Exec(`DELETE FROM worker_native_fences`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.RecoverLegacyWorkerStop(ctx, actor, recovery); err == nil {
				t.Fatal("unsafe recovery accepted", scenario)
			}
			var count int
			if err := s.db.QueryRow(`SELECT count(*) FROM messages WHERE kind='worker-stop-recovery'`).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed recovery retained an authority audit", count, err)
			}
			stop, err := s.LegacyWorkerStopReceipt(ctx, q.LegacyStopID)
			if err != nil || stop.Recovery != nil || stop.State != "prepared" {
				t.Fatal("failed recovery changed retained stop", stop, err)
			}
		})
	}
}

func TestLegacyRecoveryConcurrentIdenticalReplayAndLateRollback(t *testing.T) {
	for _, lateFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "concurrent replay", true: "late rollback"}[lateFailure], func(t *testing.T) {
			s, q, input := legacyStopFixture(t)
			ctx := context.Background()
			if _, err := s.PrepareLegacyWorkerStop(ctx, q.Claim.Actor, input); err != nil {
				t.Fatal(err)
			}
			if lateFailure {
				if _, err := s.db.Exec(`CREATE TRIGGER fail_recovery BEFORE UPDATE ON legacy_worker_stops WHEN json_extract(NEW.preparation,'$.recovery') IS NOT NULL BEGIN SELECT RAISE(ABORT,'late isolated failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			recovery := legacyRecoveryFixture(q, "recover-replay", "", "", s.now())
			var group sync.WaitGroup
			errors := make(chan error, 8)
			for i := 0; i < 8; i++ {
				group.Add(1)
				go func() {
					defer group.Done()
					_, err := s.RecoverLegacyWorkerStop(ctx, q.Controller.Actor, recovery)
					errors <- err
				}()
			}
			group.Wait()
			close(errors)
			for err := range errors {
				if (err != nil) != lateFailure {
					t.Fatal("unexpected concurrent recovery result", lateFailure, err)
				}
			}
			var count int
			if err := s.db.QueryRow(`SELECT count(*) FROM messages WHERE kind='worker-stop-recovery'`).Scan(&count); err != nil || count != map[bool]int{false: 1, true: 0}[lateFailure] {
				t.Fatal("recovery audit was duplicated or not rolled back", count, err)
			}
			if s.CheckWorkerNative(ctx, q.Claim.Actor, q.Expected.WorkerThreadID) == nil {
				t.Fatal("recovery lost the permanent fence")
			}
			if !lateFailure {
				recovery.Reason = "changed replay"
				if _, err := s.RecoverLegacyWorkerStop(ctx, q.Controller.Actor, recovery); err == nil {
					t.Fatal("changed recovery replay accepted")
				}
			}
		})
	}
}

func TestLegacyRecoveryReplayAfterHandoffReturnsTransferredReceipt(t *testing.T) {
	s, q, input := legacyStopFixture(t)
	ctx := context.Background()
	if _, err := s.PrepareLegacyWorkerStop(ctx, q.Claim.Actor, input); err != nil {
		t.Fatal(err)
	}
	recovery := legacyRecoveryFixture(q, "recover-transferred", "", "", s.now())
	stop, err := s.RecoverLegacyWorkerStop(ctx, q.Controller.Actor, recovery)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.WorkerHandoff(ctx, q.Controller.Actor, recoveredFinalAuthority(t, s, stop)); err != nil {
		t.Fatal(err)
	}
	replay, err := s.RecoverLegacyWorkerStop(ctx, q.Controller.Actor, recovery)
	if err != nil || replay.State != "transferred" {
		t.Fatalf("committed handoff replay lost transferred state: %s, %v", replay.State, err)
	}
	actual, err := s.LegacyWorkerStopReceipt(ctx, q.LegacyStopID)
	if err != nil || !reflect.DeepEqual(replay, actual) {
		t.Fatalf("replay differs from authoritative winning receipt: %v", err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM messages WHERE kind='worker-stop-recovery'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replay created duplicate recovery audit: %d, %v", count, err)
	}
}
