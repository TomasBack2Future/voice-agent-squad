package grokreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/zsiec/squad/internal/store"
)

func restartFixture(t *testing.T) (string, string, HumanRestart, []byte) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".squad"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(home, ".squad", "global.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`INSERT INTO claims(item_id,repo_id,agent_id,claimed_at,last_touch,generation,state) VALUES('BUG','ledger','worker',2,2,1,'held')`,
		`INSERT INTO dispatch_reservations(repo_id,item_id,canonical_item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id) VALUES('ledger','RES','BUG','owner/repo#9','controller',1,1,9,'dispatched',1,'native')`,
		`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('ledger','RES',1,'BUG',1,1,'proceed','human restart','worker')`,
		`INSERT INTO dispatch_controller_bindings(repo_id,actor,native_session,epoch) VALUES('ledger','controller','controller-native',3)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	parent := seedTimeout(t, dir)
	child := openTestAdmission(t, dir, parent, admissionSettings())
	if err := child.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if err := child.Finish(context.Background(), timeoutReport()); err != nil {
		t.Fatal(err)
	}
	id, _ := identityFor(admissionBundle())
	owner := ReviewOwner{"worker", "native"}
	disclosure := ReviewDisclosure{SchemaVersion: ReviewDisclosureSchema, Reference: "human:message", Disposition: "granted", Operation: "managed_review", Provider: "grok", Content: "source_diff_and_review_contract", Owner: owner, Identity: id, Mode: "required"}
	custody := CompletionCustody{SchemaVersion: "squad.review-completion.custody.v1", Owner: owner, LedgerRepo: "ledger", Item: "BUG", Reservation: "RES", Generation: 1, ClaimGeneration: 1, DecisionRevision: 1, Disclosure: disclosure}
	evidence := humanMessageEvidence{Source: "authenticated local operator readback", Thread: "controller-native", Host: "local", Turn: "turn"}
	evidence.Message.Type = "userMessage"
	evidence.Message.ID = "message"
	evidence.Message.Content = append(evidence.Message.Content, struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{"text", "Restart this review once."})
	evidencePath := filepath.Join(t.TempDir(), "human.json")
	writeRestartJSON(t, evidencePath, evidence)
	directive := restartDirective{SchemaVersion: "squad.human-review-restart.directive.v1", Source: "authenticated operator readback", HumanMessageID: "message", HumanThread: evidence.Thread, HumanTurn: evidence.Turn, HumanMessageHash: receiptHash([]byte(evidence.Message.Content[0].Text)), HumanEvidencePath: evidencePath, Controller: restartController{"ledger", "controller", "controller-native", 3}, ProductionUnchanged: true, Scope: []restartScope{{Repo: "repo", PR: 9, Item: "BUG", Key: "RES", Worker: "native", Agent: "worker", Expected: 1, Base: "base", Head: "head", Mode: "required", Old: parent + "/" + child.AttemptID(), Check: 1, BodyHash: receiptHash([]byte("contract")), Title: "repair", DiffHash: receiptHash([]byte("+fixed"))}}}
	directivePath := filepath.Join(t.TempDir(), "directive.json")
	raw := writeRestartJSON(t, directivePath, directive)
	grant := HumanRestart{SchemaVersion: "squad.review-human-restart.v1", DirectivePath: directivePath, DirectiveSHA256: receiptHash(raw), HumanMessageID: "message", PreviousAttempt: child.AttemptID(), Custody: custody, Identity: id, Settings: admissionSettings()}
	grantPath := filepath.Join(t.TempDir(), "grant.json")
	writeRestartJSON(t, grantPath, grant)
	return dir, grantPath, grant, admissionBundle()
}
func writeRestartJSON(t *testing.T, path string, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestHumanRestartPreservesHistoryAndOneUse(t *testing.T) {
	dir, path, g, bundle := restartFixture(t)
	ctx := context.Background()
	a := openTestAdmission(t, dir, "", g.Settings)
	prior, err := a.load(ctx, g.PreviousAttempt)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ImportHumanRestart(path, g.Custody.Owner); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx, bundle); err != nil {
		t.Fatal(err)
	}
	if a.Receipt().HumanGrantID == "" || a.Receipt().Parent != prior.ID || a.Receipt().UsageKnown {
		t.Fatal("lineage/unknown usage lost")
	}
	if err := a.Finish(ctx, timeoutReport()); err != nil {
		t.Fatal(err)
	}
	after, _ := a.load(ctx, prior.ID)
	if !reflect.DeepEqual(prior, after) {
		t.Fatal("old evidence rewritten")
	}
	var rootParent, rootChild string
	if err := a.db.QueryRow("SELECT parent,child FROM review_recovery_roots").Scan(&rootParent, &rootChild); err != nil || rootParent != prior.Parent || rootChild != prior.ID {
		t.Fatal("root changed", err)
	}
	retry := openTestAdmission(t, dir, "", g.Settings)
	if err := retry.ImportHumanRestart(path, g.Custody.Owner); err != nil {
		t.Fatal(err)
	}
	if err := retry.Start(ctx, bundle); err == nil {
		t.Fatal("human grant replayed")
	}
	if err := openTestAdmission(t, dir, "", g.Settings).Start(ctx, bundle); err == nil {
		t.Fatal("ordinary retry bypass")
	}
}
func TestHumanRestartNegativeScopeBeforeAdmission(t *testing.T) {
	for _, name := range []string{"absent", "human", "owner", "native", "decision", "generation", "body", "settings", "input", "root", "epoch", "forged-message", "valid-verdict", "flight", "root-custody", "unjoined", "disclosure", "app", "model", "diff"} {
		t.Run(name, func(t *testing.T) {
			dir, path, g, bundle := restartFixture(t)
			a := openTestAdmission(t, dir, "", g.Settings)
			switch name {
			case "absent":
				path = ""
			case "human":
				g.HumanMessageID = "foreign"
			case "owner":
				g.Custody.Owner.Actor = "foreign"
			case "native":
				g.Custody.Owner.Native = "foreign"
			case "decision":
				g.Custody.DecisionRevision++
			case "generation":
				g.Custody.Generation++
			case "body":
				var f FrozenReviewBundle
				_ = json.Unmarshal(bundle, &f)
				f.Description += "mutated"
				bundle, _ = json.Marshal(f)
			case "settings":
				g.Settings.TimeoutMS++
			case "input":
				g.Identity.BundleSHA256 = "wrong"
			case "root":
				g.PreviousAttempt = "foreign"
			case "epoch", "forged-message":
				var d restartDirective
				raw, _ := os.ReadFile(g.DirectivePath)
				_ = json.Unmarshal(raw, &d)
				if name == "epoch" {
					d.Controller.Epoch++
				} else {
					var e humanMessageEvidence
					raw, _ := os.ReadFile(d.HumanEvidencePath)
					_ = json.Unmarshal(raw, &e)
					e.Message.Type = "assistantMessage"
					writeRestartJSON(t, d.HumanEvidencePath, e)
				}
				g.DirectiveSHA256 = receiptHash(writeRestartJSON(t, g.DirectivePath, d))
			case "valid-verdict":
				r, _ := a.load(context.Background(), g.PreviousAttempt)
				r.Verdict = VerdictApproved
				raw, _ := json.Marshal(r)
				_, _ = a.db.Exec("UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), r.ID)
			case "root-custody":
				_, _ = a.db.Exec("UPDATE review_recovery_roots SET child='foreign'")
			case "unjoined":
				r, _ := a.load(context.Background(), g.PreviousAttempt)
				r.Joined = false
				raw, _ := json.Marshal(r)
				_, _ = a.db.Exec("UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), r.ID)
			case "disclosure":
				g.Custody.Disclosure.Disposition = "pending"
			case "app":
				g.Settings.AppID++
			case "model":
				g.Settings.Model = "other"
			case "diff":
				var f FrozenReviewBundle
				_ = json.Unmarshal(bundle, &f)
				f.Diff += "mutated"
				bundle, _ = json.Marshal(f)
			case "flight":
				_, _ = a.db.Exec("INSERT INTO review_flights(repository,pr,attempt) VALUES('owner/repo',9,'inflight')")
			}
			if path != "" {
				writeRestartJSON(t, path, g)
			}
			err := a.ImportHumanRestart(path, ReviewOwner{"worker", "native"})
			if err == nil {
				err = a.Start(context.Background(), bundle)
			}
			if err == nil {
				t.Fatal("negative admitted")
			}
			var uses int
			if err := a.db.QueryRow("SELECT count(*) FROM review_human_restarts").Scan(&uses); err != nil || uses != 0 {
				t.Fatal("negative consumed grant", err)
			}
		})
	}
}
func TestHumanRestartConcurrentGrantAndInterruptedCommit(t *testing.T) {
	dir, path, g, bundle := restartFixture(t)
	ctx := context.Background()
	var owners []*Admission
	for i := 0; i < 2; i++ {
		a := openTestAdmission(t, dir, "", g.Settings)
		if err := a.ImportHumanRestart(path, g.Custody.Owner); err != nil {
			t.Fatal(err)
		}
		owners = append(owners, a)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := owners[0].Start(cancelled, bundle); err == nil {
		t.Fatal("cancelled admitted")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
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
		t.Fatalf("single flight/use successes=%d", success)
	}
	// A lost response after commit cannot create another sample, even if its
	// terminal outcome is unavailable. The original flight/handle stays owned.
	again := openTestAdmission(t, dir, "", g.Settings)
	if err := again.ImportHumanRestart(path, g.Custody.Owner); err != nil {
		t.Fatal(err)
	}
	if err := again.Start(ctx, bundle); err == nil {
		t.Fatal("uncertain commit replayed")
	}
}

func TestHumanRestartServiceSamplesOnceAndPublishesExactApp(t *testing.T) {
	dir, path, g, _ := restartFixture(t)
	a := openTestAdmission(t, dir, "", g.Settings)
	if err := a.ImportHumanRestart(path, g.Custody.Owner); err != nil {
		t.Fatal(err)
	}
	snapshot := PullRequestSnapshot{Repository: "owner/repo", Number: 9, BaseRef: "main", BaseSHA: "base", HeadSHA: "head", Title: "repair", Description: "contract", Diff: "+fixed"}
	gateway := &completionTestGateway{snapshot: snapshot}
	model := &fakeModelReviewer{result: approvedFindings()}
	service, err := NewLocalReviewService(model, gateway, "core", "policy")
	if err != nil {
		t.Fatal(err)
	}
	service.SetAdmission(a)
	report, err := service.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9)
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 || gateway.published != 1 || gateway.name != "grok-review" || report.Publication.CheckRunID != 100 || a.Receipt().HumanGrantID == "" || !a.Receipt().Joined {
		t.Fatal("actual managed result not retained")
	}
	replay := openTestAdmission(t, dir, "", g.Settings)
	if err := replay.ImportHumanRestart(path, g.Custody.Owner); err != nil {
		t.Fatal(err)
	}
	service.SetAdmission(replay)
	if _, err := service.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9); err == nil {
		t.Fatal("valid verdict replay")
	}
	if model.calls != 1 || gateway.published != 1 {
		t.Fatal("replay sampled/published")
	}
}
func TestHumanRestartLostCustodyBeforeLaunchRetainsSpentGrant(t *testing.T) {
	dir, path, g, bundle := restartFixture(t)
	a := openTestAdmission(t, dir, "", g.Settings)
	ctx := context.Background()
	if err := a.ImportHumanRestart(path, g.Custody.Owner); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx, bundle); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	db, err := store.Open(filepath.Join(home, ".squad", "global.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE dispatch_decisions SET action='hold'"); err != nil {
		t.Fatal(err)
	}
	if err := a.ReviewerLaunching(ctx); err == nil {
		t.Fatal("lost custody launched")
	}
	var uses, flights int
	_ = a.db.QueryRow("SELECT count(*) FROM review_human_restarts").Scan(&uses)
	_ = a.db.QueryRow("SELECT count(*) FROM review_flights").Scan(&flights)
	if uses != 1 || flights != 1 {
		t.Fatal("lost custody refunded grant or flight")
	}
}

func TestHumanRestartTransactionRollbackDoesNotSampleOrConsume(t *testing.T) {
	dir, path, g, bundle := restartFixture(t)
	a := openTestAdmission(t, dir, "", g.Settings)
	ctx := context.Background()
	if err := a.ImportHumanRestart(path, g.Custody.Owner); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`CREATE TRIGGER reject_attempt BEFORE INSERT ON review_attempts BEGIN SELECT RAISE(ABORT,'isolated fault'); END`); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx, bundle); err == nil {
		t.Fatal("partial transaction accepted")
	}
	var grants, flights int
	_ = a.db.QueryRow("SELECT count(*) FROM review_human_restarts").Scan(&grants)
	_ = a.db.QueryRow("SELECT count(*) FROM review_flights").Scan(&flights)
	if grants != 0 || flights != 0 {
		t.Fatal("rolled-back transaction leaked grant/flight")
	}
	if _, err := a.db.Exec("DROP TRIGGER reject_attempt"); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx, bundle); err != nil {
		t.Fatal("confirmed rollback did not preserve unused grant", err)
	}
}
