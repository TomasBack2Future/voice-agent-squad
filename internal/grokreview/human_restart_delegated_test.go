package grokreview

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
)

func delegatedTimeoutFixture(t *testing.T) (string, string, HumanRestart, []byte) {
	t.Helper()
	dir, path, grant, bundle := restartFixture(t)
	var directive map[string]any
	raw, err := os.ReadFile(grant.DirectivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &directive); err != nil {
		t.Fatal(err)
	}
	var evidence humanMessageEvidence
	raw, err = os.ReadFile(directive["human_evidence_path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	evidence.Thread = "native"
	writeRestartJSON(t, directive["human_evidence_path"].(string), evidence)
	directive["human_thread"] = "native"
	directive["delegation"] = map[string]any{
		"authorizing_worker_native": "native",
		"authorizing_worker_actor":  "worker",
	}
	directive["timeout_override"] = map[string]any{
		"from_ms": int64(1200000),
		"to_ms":   int64(2400000),
	}
	grant.DirectiveSHA256 = receiptHash(writeRestartJSON(t, grant.DirectivePath, directive))
	grant.Settings.TimeoutMS = 2400000
	writeRestartJSON(t, path, grant)
	return dir, path, grant, bundle
}

func TestHumanRestartDelegatedTimeoutOverride(t *testing.T) {
	dir, path, grant, bundle := delegatedTimeoutFixture(t)
	a := openTestAdmission(t, dir, "", grant.Settings)
	prior, err := a.load(context.Background(), grant.PreviousAttempt)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ImportHumanRestart(path, grant.Custody.Owner); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), bundle); err != nil {
		t.Fatal(err)
	}
	if a.Receipt().Settings.TimeoutMS != 2400000 || a.Receipt().Parent != grant.PreviousAttempt || a.Receipt().HumanGrantID == "" {
		t.Fatal("delegated timeout grant lost exact settings or lineage")
	}
	after, err := a.load(context.Background(), grant.PreviousAttempt)
	if err != nil || after != prior {
		t.Fatal("old timeout receipt changed", err)
	}
	var lineage string
	if err := a.db.QueryRow("SELECT lineage FROM review_human_restarts WHERE grant_id=?", a.Receipt().HumanGrantID).Scan(&lineage); err != nil {
		t.Fatal(err)
	}
	var retained struct {
		Prior AttemptReceipt `json:"prior"`
	}
	if err := json.Unmarshal([]byte(lineage), &retained); err != nil || retained.Prior != prior {
		t.Fatal("immutable prior settings missing from lineage", err)
	}
}

func TestHumanRestartSameChatCannotChangeTimeout(t *testing.T) {
	dir, path, grant, bundle := restartFixture(t)
	grant.Settings.TimeoutMS = 2400000
	writeRestartJSON(t, path, grant)
	a := openTestAdmission(t, dir, "", grant.Settings)
	if err := a.ImportHumanRestart(path, grant.Custody.Owner); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), bundle); err == nil {
		t.Fatal("same-chat grant changed old timeout without delegated override")
	}
	var grants int
	if err := a.db.QueryRow("SELECT count(*) FROM review_human_restarts").Scan(&grants); err != nil || grants != 0 {
		t.Fatal("unapproved timeout consumed grant", err)
	}
}

func TestHumanRestartDelegatedTimeoutRejectsChangedScope(t *testing.T) {
	for _, name := range []string{"missing-delegation", "missing-override", "wrong-worker-native", "wrong-worker-actor", "same-controller-native", "old-timeout", "over-40-minutes", "mismatched-new-timeout", "model", "effort", "mode", "app", "output-bound", "stale-decision", "stale-controller", "forged-human", "changed-input", "valid-verdict", "active-flight"} {
		t.Run(name, func(t *testing.T) {
			dir, path, grant, bundle := delegatedTimeoutFixture(t)
			var d restartDirective
			raw, err := os.ReadFile(grant.DirectivePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &d); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing-delegation":
				d.Delegation = nil
			case "missing-override":
				d.TimeoutOverride = nil
			case "wrong-worker-native":
				d.Delegation.AuthorizingWorkerNative = "foreign"
			case "wrong-worker-actor":
				d.Delegation.AuthorizingWorkerActor = "foreign"
			case "same-controller-native":
				d.Controller.Native = d.HumanThread
			case "old-timeout":
				d.TimeoutOverride.FromMS = 600000
			case "over-40-minutes":
				d.TimeoutOverride.ToMS = 2400001
				grant.Settings.TimeoutMS = 2400001
			case "mismatched-new-timeout":
				d.TimeoutOverride.ToMS = 2100000
			case "model":
				grant.Settings.Model = "different"
			case "effort":
				grant.Settings.Effort = "low"
			case "mode":
				grant.Settings.Mode = "shadow"
			case "app":
				grant.Settings.AppID++
			case "output-bound":
				grant.Settings.MaxReviewerOutput++
			case "stale-decision":
				grant.Custody.DecisionRevision++
			case "stale-controller":
				d.Controller.Epoch++
			case "forged-human":
				var e humanMessageEvidence
				raw, err := os.ReadFile(d.HumanEvidencePath)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &e); err != nil {
					t.Fatal(err)
				}
				e.Message.Type = "assistantMessage"
				writeRestartJSON(t, d.HumanEvidencePath, e)
			case "changed-input":
				var f FrozenReviewBundle
				if err := json.Unmarshal(bundle, &f); err != nil {
					t.Fatal(err)
				}
				f.Diff += "other"
				bundle, err = json.Marshal(f)
				if err != nil {
					t.Fatal(err)
				}
			}
			grant.DirectiveSHA256 = receiptHash(writeRestartJSON(t, grant.DirectivePath, d))
			writeRestartJSON(t, path, grant)
			a := openTestAdmission(t, dir, "", grant.Settings)
			if name == "valid-verdict" || name == "active-flight" {
				if name == "valid-verdict" {
					prior, err := a.load(context.Background(), grant.PreviousAttempt)
					if err != nil {
						t.Fatal(err)
					}
					prior.Verdict = VerdictApproved
					raw, err := json.Marshal(prior)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := a.db.Exec("UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), prior.ID); err != nil {
						t.Fatal(err)
					}
				} else if _, err := a.db.Exec("INSERT INTO review_flights(repository,pr,attempt) VALUES('owner/repo',9,'active')"); err != nil {
					t.Fatal(err)
				}
			}
			err = a.ImportHumanRestart(path, grant.Custody.Owner)
			if err == nil {
				err = a.Start(context.Background(), bundle)
			}
			if err == nil {
				t.Fatal("invalid delegated grant admitted")
			}
			var grants int
			if err := a.db.QueryRow("SELECT count(*) FROM review_human_restarts").Scan(&grants); err != nil || grants != 0 {
				t.Fatal("invalid grant consumed", err)
			}
		})
	}
}

func TestHumanRestartDelegatedTimeoutServiceSamplesOnce(t *testing.T) {
	dir, path, grant, _ := delegatedTimeoutFixture(t)
	a := openTestAdmission(t, dir, "", grant.Settings)
	if err := a.ImportHumanRestart(path, grant.Custody.Owner); err != nil {
		t.Fatal(err)
	}
	gateway := &completionTestGateway{snapshot: PullRequestSnapshot{Repository: "owner/repo", Number: 9, BaseRef: "main", BaseSHA: "base", HeadSHA: "head", Title: "repair", Description: "contract", Diff: "+fixed"}}
	model := &fakeModelReviewer{result: approvedFindings()}
	service, err := NewLocalReviewService(model, gateway, "core", "policy")
	if err != nil {
		t.Fatal(err)
	}
	service.SetAdmission(a)
	if _, err := service.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9); err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 || gateway.published != 1 || !a.Receipt().Joined || a.Receipt().Settings.TimeoutMS != 2400000 {
		t.Fatal("delegated sample was not joined and published exactly once")
	}
	replay := openTestAdmission(t, dir, "", grant.Settings)
	if err := replay.ImportHumanRestart(path, grant.Custody.Owner); err != nil {
		t.Fatal(err)
	}
	service.SetAdmission(replay)
	if _, err := service.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9); err == nil || model.calls != 1 || gateway.published != 1 {
		t.Fatal("delegated replay sampled or published")
	}
}

func TestHumanRestartDelegatedTimeoutTransactionRollback(t *testing.T) {
	dir, path, grant, bundle := delegatedTimeoutFixture(t)
	a := openTestAdmission(t, dir, "", grant.Settings)
	if err := a.ImportHumanRestart(path, grant.Custody.Owner); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`CREATE TRIGGER reject_attempt BEFORE INSERT ON review_attempts BEGIN SELECT RAISE(ABORT,'isolated fault'); END`); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), bundle); err == nil {
		t.Fatal("partial transaction accepted")
	}
	var grants, flights int
	if err := a.db.QueryRow("SELECT count(*) FROM review_human_restarts").Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM review_flights").Scan(&flights); err != nil {
		t.Fatal(err)
	}
	if grants != 0 || flights != 0 {
		t.Fatal("rolled-back grant or flight leaked")
	}
	if _, err := a.db.Exec("DROP TRIGGER reject_attempt"); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), bundle); err != nil {
		t.Fatal("unused grant failed after rollback", err)
	}
}

func TestHumanRestartDelegatedTimeoutSingleFlightAndReplay(t *testing.T) {
	dir, path, grant, bundle := delegatedTimeoutFixture(t)
	ctx := context.Background()
	owners := make([]*Admission, 2)
	for i := range owners {
		owners[i] = openTestAdmission(t, dir, "", grant.Settings)
		if err := owners[i].ImportHumanRestart(path, grant.Custody.Owner); err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, len(owners))
	var wg sync.WaitGroup
	for _, a := range owners {
		wg.Add(1)
		go func(a *Admission) { defer wg.Done(); results <- a.Start(ctx, bundle) }(a)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("delegated grant admitted %d concurrent samples", success)
	}
	var grants, flights int
	if err := owners[0].db.QueryRow("SELECT count(*) FROM review_human_restarts").Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err := owners[0].db.QueryRow("SELECT count(*) FROM review_flights").Scan(&flights); err != nil {
		t.Fatal(err)
	}
	if grants != 1 || flights != 1 {
		t.Fatalf("grant/flight = %d/%d", grants, flights)
	}
}
