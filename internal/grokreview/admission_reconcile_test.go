package grokreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/store"
)

type completionTestModel struct {
	a        *Admission
	envelope []byte
	calls    int
	after    func()
}

func (m *completionTestModel) Review(ctx context.Context, bundle []byte) (FindingsResult, CLIAudit, error) {
	m.calls++
	if err := m.a.ReviewerLaunching(ctx); err != nil {
		return FindingsResult{}, CLIAudit{}, err
	}
	if err := m.a.ReviewerStarted(ctx, 999999); err != nil {
		return FindingsResult{}, CLIAudit{}, err
	}
	result, audit, err := ParseCLIEnvelope(m.envelope)
	if err != nil {
		return result, audit, err
	}
	joined := time.Now()
	now := joined.Add(-time.Second)
	exit := 0
	audit.Terminal = &TerminalDiagnostics{InputSHA256: receiptHash(bundle), StartedAt: now, FinishedAt: joined, JoinedAt: &joined, ChildStarted: true, ChildWaited: true, ExitCode: &exit, StdoutBytes: int64(len(m.envelope)), StdoutSHA256: receiptHash(m.envelope), StderrSHA256: receiptHash(nil), SessionEvidence: "unavailable"}
	audit.RequestedModel = m.a.settings.Model
	audit.ReasoningEffort = m.a.settings.Effort
	audit.Duration = time.Second
	if m.after != nil {
		m.after()
	}
	return result, audit, nil
}

type completionTestGateway struct {
	mu              sync.Mutex
	snapshot        PullRequestSnapshot
	publication     Publication
	published       int
	err             error
	acceptedOnError bool
	afterFetch      func(int)
	fetches         int
	audit           CLIAudit
	name            string
}

func (g *completionTestGateway) FetchPullRequest(_ context.Context, _ string, _ int, _ string) (PullRequestSnapshot, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fetches++
	if g.afterFetch != nil {
		g.afterFetch(g.fetches)
	}
	return g.snapshot, nil
}
func (g *completionTestGateway) FetchPullRequestIdentity(ctx context.Context, r string, p int, t string) (PullRequestSnapshot, error) {
	return g.FetchPullRequest(ctx, r, p, t)
}
func (g *completionTestGateway) PublishReview(_ context.Context, _ string, name string, _ PullRequestSnapshot, _ FindingsResult, audit CLIAudit) (Publication, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.published++
	g.audit = audit
	g.name = name
	if g.err != nil && !g.acceptedOnError {
		return Publication{}, g.err
	}
	g.publication = Publication{CheckRunID: 100, CommentID: 101, Conclusion: "success"}
	return g.publication, g.err
}
func (g *completionTestGateway) lookup(context.Context, AttemptReceipt) (Publication, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.publication, nil
}

type completionFixture struct {
	a        *Admission
	c        CompletionCustody
	model    *completionTestModel
	gateway  *completionTestGateway
	snapshot PullRequestSnapshot
	original AttemptReceipt
}

func newCompletionFixture(t *testing.T) *completionFixture {
	t.Helper()
	a := openTestAdmission(t, t.TempDir(), "", admissionSettings())
	snapshot := PullRequestSnapshot{Repository: "owner/repo", Number: 9, BaseRef: "main", BaseSHA: "base", HeadSHA: "head", Title: "fix", Description: "exact body\n", Diff: "+fixed"}
	id := ReviewIdentity{Repository: snapshot.Repository, PR: 9, BaseRef: "main", BaseSHA: "base", HeadSHA: "head"}
	c := CompletionCustody{SchemaVersion: "squad.review-completion.custody.v1", Owner: ReviewOwner{"worker", "native"}, LedgerRepo: "ledger", Item: "BUG", Reservation: "RES", Generation: 1, ClaimGeneration: 1, DecisionRevision: 1, Disclosure: ReviewDisclosure{SchemaVersion: ReviewDisclosureSchema, Reference: "standing-human-grant", Disposition: "granted", Owner: ReviewOwner{"worker", "native"}, Identity: id, Mode: "required", Operation: "managed_review", Provider: "grok", Content: "source_diff_and_review_contract"}}
	guard := func(_ context.Context, got CompletionCustody) error {
		if !reflect.DeepEqual(got, c) {
			return errors.New("actual custody rejected")
		}
		return nil
	}
	a.BindCompletionCustody(c, guard)
	a.processAbsent = func(int) error { return nil }
	g := &completionTestGateway{snapshot: snapshot}
	m := &completionTestModel{a: a, envelope: approvedEnvelope(t), after: func() { g.snapshot.Description = "mutated body" }}
	svc, err := NewLocalReviewService(m, g, "core", "policy")
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAdmission(a)
	report, err := svc.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9)
	if err == nil || report.FailureStage != "identity" || g.published != 0 || m.calls != 1 {
		t.Fatal("fixture did not reproduce genuine completed/stale execution", err)
	}
	original, err := a.load(context.Background(), a.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	a.SetPublicationLookup(g.lookup)
	g.snapshot = snapshot
	return &completionFixture{a, c, m, g, snapshot, original}
}
func (f *completionFixture) complete() (CompletionResult, error) {
	return f.a.Complete(context.Background(), f.original.ID, f.c, f.gateway, "token", "grok-review", "core", "policy")
}

func TestSameExecutionCompletionRestoresExactBodyOnce(t *testing.T) {
	f := newCompletionFixture(t)
	f.gateway.snapshot.Description = strings.TrimSuffix(f.snapshot.Description, "\n")
	if _, err := f.complete(); err == nil {
		t.Fatal("missing terminal newline published")
	}
	if f.gateway.published != 0 {
		t.Fatal("stale publication")
	}
	f.gateway.snapshot = f.snapshot
	result, err := f.complete()
	if err != nil {
		t.Fatal(err)
	}
	if result.Sampled || result.Attempt != f.original.ID || result.Publication.CheckRunID != 100 || f.model.calls != 1 || f.gateway.published != 1 || f.gateway.audit.AttemptID != f.original.ID || f.gateway.audit.BundleSHA256 != f.original.Identity.BundleSHA256 || f.gateway.name != "grok-review" {
		t.Fatal("lost same execution or extra sample", result)
	}
	if _, err := f.complete(); err != nil {
		t.Fatal("idempotent publication failed", err)
	}
	if f.gateway.published != 1 || f.model.calls != 1 {
		t.Fatal("idempotence wrote or sampled again")
	}
	stored, err := f.a.load(context.Background(), f.original.ID)
	if err != nil || !reflect.DeepEqual(stored, f.original) {
		t.Fatal("original stale history rewritten", err)
	}
	for _, table := range []string{"review_flights", "review_recoveries", "review_recovery_roots"} {
		var n int
		if err := f.a.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatal("new flight/root or refunded history", table, n, err)
		}
	}
	var raw string
	if err := f.a.db.QueryRow("SELECT payload FROM review_completion_seals WHERE attempt=?", f.original.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "discard this and never expose it") {
		t.Fatal("raw reasoning retained in seal")
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "exact body") || strings.Contains(string(encoded), "No blocking") || strings.Contains(string(encoded), "input_tokens") {
		t.Fatal("completion output exposes private input/result payload")
	}
}

func TestSameExecutionCompletionRejectsDriftAndCustody(t *testing.T) {
	for _, name := range []string{"base", "head", "title", "body", "diff", "core", "policy", "model", "effort", "mode", "timeout", "app", "output-bound", "owner", "native", "claim-generation", "reservation-generation", "decision", "grant", "grant-reference", "wrong-check", "input-hash", "result", "usage", "joined", "sampling", "missing-seal", "seal-tamper", "seal-owner", "live-wrapper"} {
		t.Run(name, func(t *testing.T) {
			f := newCompletionFixture(t)
			core, policy, check := "core", "policy", "grok-review"
			switch name {
			case "base":
				f.gateway.snapshot.BaseSHA = "changed"
			case "head":
				f.gateway.snapshot.HeadSHA = "changed"
			case "title":
				f.gateway.snapshot.Title = "changed"
			case "body":
				f.gateway.snapshot.Description = "changed"
			case "diff":
				f.gateway.snapshot.Diff = "changed"
			case "core":
				core = "changed"
			case "policy":
				policy = "changed"
			case "model":
				f.a.settings.Model = "changed"
			case "effort":
				f.a.settings.Effort = "high"
			case "mode":
				f.a.settings.Mode = "shadow"
			case "timeout":
				f.a.settings.TimeoutMS++
			case "app":
				f.a.settings.AppID++
			case "output-bound":
				f.a.settings.MaxReviewerOutput++
			case "owner":
				f.c.Owner.Actor = "foreign"
			case "native":
				f.c.Owner.Native = "foreign"
			case "claim-generation":
				f.c.ClaimGeneration++
			case "reservation-generation":
				f.c.Generation++
			case "decision":
				f.c.DecisionRevision++
			case "grant":
				f.c.Disclosure.Disposition = "denied"
			case "grant-reference":
				f.c.Disclosure.Reference = "foreign"
			case "wrong-check":
				check = "grok-review-shadow"
			case "input-hash", "result", "usage", "joined", "sampling":
				r := f.original
				if name == "input-hash" {
					r.Identity.BundleSHA256 = strings.Repeat("a", 64)
				}
				if name == "result" {
					r.Verdict = VerdictError
					r.FailureKind = CLIFailureTimeout
				}
				if name == "usage" {
					r.Usage.TotalTokens++
				}
				if name == "joined" {
					r.Joined = false
				}
				if name == "sampling" {
					r.SamplingCompleted = false
				}
				raw, _ := json.Marshal(r)
				if _, err := f.a.db.Exec("UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), r.ID); err != nil {
					t.Fatal(err)
				}
			case "missing-seal":
				if _, err := f.a.db.Exec("DELETE FROM review_completion_seals WHERE attempt=?", f.original.ID); err != nil {
					t.Fatal(err)
				}
			case "seal-tamper":
				if _, err := f.a.db.Exec("UPDATE review_completion_seals SET payload='{}' WHERE attempt=?", f.original.ID); err != nil {
					t.Fatal(err)
				}
			case "seal-owner":
				var raw string
				if err := f.a.db.QueryRow("SELECT payload FROM review_completion_seals WHERE attempt=?", f.original.ID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var s completionSeal
				_ = json.Unmarshal([]byte(raw), &s)
				s.Custody.Owner.Native = "foreign"
				encoded, _ := json.Marshal(s)
				if _, err := f.a.db.Exec("UPDATE review_completion_seals SET payload=?,digest=? WHERE attempt=?", string(encoded), receiptHash(encoded), f.original.ID); err != nil {
					t.Fatal(err)
				}
			case "live-wrapper":
				f.a.processAbsent = func(int) error { return errors.New("actual process live") }
			}
			if _, err := f.a.Complete(context.Background(), f.original.ID, f.c, f.gateway, "token", check, core, policy); err == nil {
				t.Fatal("invalid completion accepted", name)
			}
			if f.gateway.published != 0 || f.model.calls != 1 {
				t.Fatal("invalid completion published/sampled", name)
			}
		})
	}
}

func TestSameExecutionCompletionUncertainWriteUsesOnlyLookup(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(fmt.Sprint(accepted), func(t *testing.T) {
			f := newCompletionFixture(t)
			f.gateway.err = errors.New("transport response lost")
			f.gateway.acceptedOnError = accepted
			if _, err := f.complete(); err == nil {
				t.Fatal("uncertain response reported success")
			}
			if f.gateway.published != 1 {
				t.Fatal("write not attempted once")
			}
			_, err := f.complete()
			if accepted && err != nil {
				t.Fatal("exact original publication lookup failed", err)
			}
			if !accepted && err == nil {
				t.Fatal("absence retried ambiguous publication")
			}
			if f.gateway.published != 1 || f.model.calls != 1 {
				t.Fatal("unknown publication retried/resampled")
			}
		})
	}
}

func TestSameExecutionCompletionSingleFlightRace(t *testing.T) {
	f := newCompletionFixture(t)
	var wg sync.WaitGroup
	var succeeded atomic.Int32
	for i := 0; i < 8; i++ {
		a := openTestAdmission(t, f.a.dir, "", admissionSettings())
		a.completionGuard = f.a.completionGuard
		a.processAbsent = f.a.processAbsent
		a.SetPublicationLookup(f.gateway.lookup)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.Complete(context.Background(), f.original.ID, f.c, f.gateway, "token", "grok-review", "core", "policy"); err == nil {
				succeeded.Add(1)
			}
		}()
	}
	wg.Wait()
	if succeeded.Load() == 0 || f.gateway.published != 1 || f.model.calls != 1 {
		t.Fatalf("singleflight failed: successful=%d writes=%d samples=%d", succeeded.Load(), f.gateway.published, f.model.calls)
	}
}

func TestSameExecutionCompletionPreservesRootAndCurrentAttemptCAS(t *testing.T) {
	for _, name := range []string{"other-flight", "exhausted-root", "current-cas", "boundary-drift", "boundary-custody"} {
		t.Run(name, func(t *testing.T) {
			f := newCompletionFixture(t)
			switch name {
			case "other-flight":
				if _, err := f.a.db.Exec("INSERT INTO review_flights(repository,pr,attempt) VALUES(?,?,?)", f.original.Identity.Repository, 9, "other"); err != nil {
					t.Fatal(err)
				}
			case "exhausted-root":
				if _, err := f.a.db.Exec("INSERT INTO review_recovery_roots(tuple,parent,child) VALUES(?,?,?)", tupleKey(f.original.Identity), "old-parent", "old-child"); err != nil {
					t.Fatal(err)
				}
			case "current-cas":
				f.a.check = func(_ context.Context, _ AttemptReceipt) error {
					r := f.original
					r.OwnerActor = "foreign"
					raw, _ := json.Marshal(r)
					_, err := f.a.db.Exec("UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), r.ID)
					return err
				}
			case "boundary-drift":
				f.gateway.afterFetch = func(count int) {
					if count == 4 {
						f.gateway.snapshot.Description = "boundary mutation"
					}
				}
			case "boundary-custody":
				var calls int
				originalGuard := f.a.completionGuard
				f.a.completionGuard = func(ctx context.Context, c CompletionCustody) error {
					calls++
					if calls == 3 {
						return errors.New("lost current custody")
					}
					return originalGuard(ctx, c)
				}
			}
			if _, err := f.complete(); err == nil {
				t.Fatal("protected completion gate bypassed", name)
			}
			if f.gateway.published != 0 || f.model.calls != 1 {
				t.Fatal("negative published or sampled")
			}
			if name == "exhausted-root" {
				var parent, child string
				if err := f.a.db.QueryRow("SELECT parent,child FROM review_recovery_roots").Scan(&parent, &child); err != nil || parent != "old-parent" || child != "old-child" {
					t.Fatal("root reset", err)
				}
			}
		})
	}
}

func TestCompletionCustodyReadsActualIsolatedLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "global.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, sql := range []string{
		`INSERT INTO claims(item_id,repo_id,agent_id,claimed_at,last_touch,generation,state) VALUES('BUG','ledger','worker',2,2,1,'held')`,
		`INSERT INTO dispatch_reservations(repo_id,item_id,canonical_item_id,source_ref,reserved_by,reserved_at,updated_at,expires_at,state,generation,worker_thread_id) VALUES('ledger','RES','BUG','repo#9','controller',1,1,9,'dispatched',1,'native')`,
		`INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES('ledger','RES',1,'BUG',1,1,'proceed','scope','worker')`,
	} {
		if _, err := db.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	c := CompletionCustody{Owner: ReviewOwner{"worker", "native"}, LedgerRepo: "ledger", Item: "BUG", Reservation: "RES", Generation: 1, ClaimGeneration: 1, DecisionRevision: 1}
	if err := verifyCompletionCustodyAt(context.Background(), path, c, c.Owner); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"owner", "native", "claim", "generation", "decision", "hold", "released"} {
		t.Run(name, func(t *testing.T) {
			q := c
			owner := c.Owner
			switch name {
			case "owner":
				owner.Actor = "foreign"
			case "native":
				owner.Native = "foreign"
			case "claim":
				q.ClaimGeneration++
			case "generation":
				q.Generation++
			case "decision":
				q.DecisionRevision++
			case "hold":
				_, err = db.Exec("UPDATE dispatch_decisions SET action='hold'")
			case "released":
				_, err = db.Exec("DELETE FROM claims")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyCompletionCustodyAt(context.Background(), path, q, owner); err == nil {
				t.Fatal("ledger custody bypassed", name)
			}
		})
	}
}

func legacyCompletionFixture(t *testing.T) (*completionFixture, string) {
	t.Helper()
	f := newCompletionFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	r := f.original
	r.SamplingCompleted = false
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.db.Exec("UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.db.Exec("DELETE FROM review_completion_seals WHERE attempt=?", r.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.a.writeJoinJournal(r); err != nil {
		t.Fatal(err)
	}
	f.original = r
	write := func(name string, raw []byte) string {
		path := filepath.Join(home, name)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	bundle := write("original-bundle.txt", f.a.frozenBundle)
	envelope := write("original-envelope.json", f.model.envelope)
	root := filepath.Join(home, ".codex", "sessions")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "original.jsonl")
	cmd := fmt.Sprintf("E=%s; export SQUAD_AGENT=worker SQUAD_SESSION_ID=codex:native; /opt/reviewer/squad-grok-review --repo owner/repo --pr 9 --mode required --model grok-4.7 --reasoning-effort medium --timeout 20m --status-dir \"$E/status\" > \"$E/review.log\" 2>&1; result=$?; tail -15 \"$E/review.log\"; exit $result", home)
	encodedCmd, _ := json.Marshal(cmd)
	rows := []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"session_id": "native"}},
		{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "launch", "input": "text(await tools.exec_command({cmd:" + string(encodedCmd) + "}));"}},
		{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call_output", "call_id": "launch", "output": []map[string]string{{"text": `{"session_id":93930}`}}}},
		{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "join", "input": `text(await tools.write_stdin({session_id:93930,chars:"",yield_time_ms:1000}));`}},
		{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call_output", "call_id": "join", "output": "Script running with cell ID 317\n"}},
		{"type": "response_item", "payload": map[string]any{"type": "function_call", "name": "wait", "call_id": "wait", "arguments": `{"cell_id":"317"}`}},
	}
	joined, _ := json.Marshal(map[string]any{"exit_code": 1, "output": "pull request base or head changed during review; result was not published\n"})
	rows = append(rows, map[string]any{"type": "response_item", "payload": map[string]any{"type": "function_call_output", "call_id": "wait", "output": []map[string]string{{"text": string(joined)}}}})
	proof := NativeJoinProof{NativeSession: "native", RolloutPath: path, ToolSessionID: 93930, LaunchCall: "launch", JoinCall: "join", TerminalCall: "wait"}
	var lines []byte
	for i, row := range rows {
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, raw...)
		lines = append(lines, '\n')
		switch i {
		case 1:
			proof.LaunchSHA256 = receiptHash(raw)
		case 2:
			proof.LaunchOutputSHA256 = receiptHash(raw)
		case 3:
			proof.JoinSHA256 = receiptHash(raw)
		case 4:
			proof.JoinOutputSHA256 = receiptHash(raw)
		case 5:
			proof.TerminalSHA256 = receiptHash(raw)
		case 6:
			proof.TerminalOutputSHA256 = receiptHash(raw)
		}
	}
	if err := os.WriteFile(path, lines, 0600); err != nil {
		t.Fatal(err)
	}
	e := LegacyCompletionEvidence{SchemaVersion: "squad.review-completion.original.v1", Attempt: r.ID, BundlePath: bundle, EnvelopePath: envelope, NativeJoin: proof}
	raw, _ = json.Marshal(e)
	return f, write("original-proof.json", raw)
}

func TestAuthenticateOriginalLegacyExecutionWithoutRewritingHistory(t *testing.T) {
	f, path := legacyCompletionFixture(t)
	if err := f.a.AuthenticateLegacyCompletion(context.Background(), f.original.ID, f.c, path); err != nil {
		t.Fatal(err)
	}
	result, err := f.complete()
	if err != nil {
		t.Fatal(err)
	}
	if result.Sampled || f.model.calls != 1 || f.gateway.published != 1 {
		t.Fatal("legacy completion resampled", result)
	}
	old, err := f.a.load(context.Background(), f.original.ID)
	if err != nil || old.SamplingCompleted || !reflect.DeepEqual(old, f.original) {
		t.Fatal("legacy stale receipt overwritten", err)
	}
	if err := f.a.AuthenticateLegacyCompletion(context.Background(), f.original.ID, f.c, path); err != nil {
		t.Fatal("original authentication not idempotent", err)
	}
	if _, err := f.complete(); err != nil {
		t.Fatal("legacy publication not idempotent", err)
	}
	if f.gateway.published != 1 {
		t.Fatal("duplicate legacy publication")
	}
}

func TestLegacyCompletionRejectsMissingOrUnboundEvidence(t *testing.T) {
	for _, name := range []string{"missing-envelope", "sanitized-summary", "bundle-byte-drift", "wrong-schema", "wrong-attempt", "native-owner", "native-session", "launch-hash", "join-hash", "output-hash", "live-child", "denied-grant", "symlink-envelope", "oversized-envelope", "unjoined", "timeout", "usage-tamper"} {
		t.Run(name, func(t *testing.T) {
			f, path := legacyCompletionFixture(t)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var e LegacyCompletionEvidence
			if err := json.Unmarshal(raw, &e); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing-envelope":
				e.EnvelopePath = filepath.Join(t.TempDir(), "missing")
			case "sanitized-summary":
				if err := os.WriteFile(e.EnvelopePath, []byte(`{"verdict":"approved","summary":"no findings"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "bundle-byte-drift":
				raw, err := os.ReadFile(e.BundlePath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(e.BundlePath, append(raw, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong-schema":
				e.SchemaVersion = "invented"
			case "wrong-attempt":
				e.Attempt = "other"
			case "native-owner":
				f.c.Owner.Actor = "foreign"
			case "native-session":
				e.NativeJoin.NativeSession = "foreign"
			case "launch-hash":
				e.NativeJoin.LaunchSHA256 = strings.Repeat("a", 64)
			case "join-hash":
				e.NativeJoin.JoinSHA256 = strings.Repeat("a", 64)
			case "output-hash":
				e.NativeJoin.TerminalOutputSHA256 = strings.Repeat("a", 64)
			case "live-child":
				f.a.processAbsent = func(int) error { return errors.New("original live") }
			case "denied-grant":
				f.c.Disclosure.Disposition = "denied"
			case "symlink-envelope":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(e.EnvelopePath, alias); err != nil {
					t.Fatal(err)
				}
				e.EnvelopePath = alias
			case "oversized-envelope":
				if err := os.WriteFile(e.EnvelopePath, []byte(strings.Repeat("x", f.a.settings.MaxReviewerOutput+1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "unjoined", "timeout", "usage-tamper":
				r := f.original
				if name == "unjoined" {
					r.Joined = false
				}
				if name == "timeout" {
					r.Verdict = VerdictError
					r.FailureKind = CLIFailureTimeout
				}
				if name == "usage-tamper" {
					r.Usage.TotalTokens++
				}
				raw, _ := json.Marshal(r)
				if _, err := f.a.db.Exec("UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), r.ID); err != nil {
					t.Fatal(err)
				}
			}
			raw, _ = json.Marshal(e)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := f.a.AuthenticateLegacyCompletion(context.Background(), f.original.ID, f.c, path); err == nil {
				t.Fatal("unqualified legacy result imported", name)
			}
			var count int
			if err := f.a.db.QueryRow("SELECT count(*) FROM review_completion_seals").Scan(&count); err != nil || count != 0 {
				t.Fatal("failed evidence left a seal", err)
			}
			if f.gateway.published != 0 || f.model.calls != 1 {
				t.Fatal("invalid legacy operation published/sampled")
			}
		})
	}
}

func TestCompletionResumesOnlyProvenPreWriteInterruptedReservation(t *testing.T) {
	f := newCompletionFixture(t)
	f.gateway.afterFetch = func(n int) {
		if n == 4 {
			f.gateway.snapshot.Description = "boundary drift"
		}
	}
	if _, err := f.complete(); err == nil {
		t.Fatal("boundary drift published")
	}
	if f.gateway.published != 0 {
		t.Fatal("pre-write checkpoint escaped")
	}
	var state string
	if err := f.a.db.QueryRow("SELECT state FROM review_completion_seals WHERE attempt=?", f.original.ID).Scan(&state); err != nil || state != "reserved" {
		t.Fatal("lost proven pre-write stage", state, err)
	}
	f.gateway.afterFetch = nil
	f.gateway.snapshot = f.snapshot
	if _, err := f.complete(); err != nil {
		t.Fatal("joined pre-write same-execution reservation could not resume", err)
	}
	if f.gateway.published != 1 || f.model.calls != 1 {
		t.Fatal("reserved resume duplicate/model call")
	}
}

func wrapperCompletionFixture(t *testing.T) (*completionFixture, string) {
	t.Helper()
	f, path := legacyCompletionFixture(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var e LegacyCompletionEvidence
	if err = json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	r := f.original
	parsed, _, err := ParseCLIEnvelope(f.model.envelope)
	if err != nil {
		t.Fatal(err)
	}
	report := originalWrapperReport{r.Terminal, r.ID, r.Identity.Repository, r.Identity.PR, r.Identity.BaseSHA, r.Identity.HeadSHA, r.Verdict, parsed.Summary, parsed.Findings, r.RequestID, r.SessionID, r.Settings.Model, r.Settings.Effort, r.ResolvedModel, r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.ReasoningTokens, r.Usage.TotalTokens, r.CostUSD, r.DurationMS, "identity"}
	reportRaw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	reportRaw = append(reportRaw, []byte("\n"+originalWrapperIdentityError+"\n")...)
	private := filepath.Join(filepath.Dir(path), "private-original-report.log")
	if err = os.WriteFile(private, reportRaw, 0600); err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf("E=%s; cat \"$E/review.log\"; cat \"$E/status\"/*.json;", filepath.Dir(path))
	cmd, _ := json.Marshal(command)
	call, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "capture", "input": "text(await tools.exec_command({cmd:" + string(cmd) + "}));"}})
	tool, _ := json.Marshal(map[string]any{"exit_code": 0, "output": string(reportRaw) + "other bounded metadata\n"})
	output, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call_output", "call_id": "capture", "output": []map[string]string{{"text": "Script completed"}, {"text": string(tool)}}}})
	native, err := os.OpenFile(e.NativeJoin.RolloutPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range [][]byte{call, output} {
		if _, err = native.Write(append(row, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if err = native.Close(); err != nil {
		t.Fatal(err)
	}
	e.EnvelopePath = ""
	e.WrapperReport = &WrapperCompletionEvidence{ReportPath: private, CaptureCall: "capture", CaptureSHA256: receiptHash(call), CaptureOutputSHA256: receiptHash(output)}
	raw, _ = json.Marshal(e)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// Isolated host fixture simulates a independently pinned historical wrapper.
	// Production has no caller-controlled trust override.
	f.a.completionWrapperVerifier = func(path string) error {
		if path != "/opt/reviewer/squad-grok-review" {
			return errors.New("unsupported original wrapper")
		}
		return nil
	}
	return f, path
}

func TestAuthenticateOriginalWrapperReportSameExecution(t *testing.T) {
	f, path := wrapperCompletionFixture(t)
	if err := f.a.AuthenticateLegacyCompletion(context.Background(), f.original.ID, f.c, path); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := f.complete(); err != nil {
			t.Fatal(err)
		}
	}
	if f.gateway.published != 1 || f.model.calls != 1 {
		t.Fatal("duplicate publication or sampling")
	}
	got, err := f.a.load(context.Background(), f.original.ID)
	if err != nil || !reflect.DeepEqual(got, f.original) {
		t.Fatal("original stale history rewritten", err)
	}
}

func TestOriginalWrapperReportRejectsUnboundAndTamperedEvidence(t *testing.T) {
	for _, name := range []string{"binary", "capture-call", "capture-hash", "capture-output", "report-summary", "finding", "tuple", "usage", "terminal", "missing-findings", "truncated", "trailer", "ambiguous", "private"} {
		t.Run(name, func(t *testing.T) {
			f, path := wrapperCompletionFixture(t)
			raw, _ := os.ReadFile(path)
			var e LegacyCompletionEvidence
			_ = json.Unmarshal(raw, &e)
			switch name {
			case "binary":
				f.a.completionWrapperVerifier = func(string) error { return errors.New("original installed hash changed") }
			case "capture-call":
				e.WrapperReport.CaptureCall = "foreign"
			case "capture-hash":
				e.WrapperReport.CaptureSHA256 = strings.Repeat("0", 64)
			case "capture-output":
				e.WrapperReport.CaptureOutputSHA256 = strings.Repeat("0", 64)
			case "ambiguous":
				e.EnvelopePath = "/invented/envelope"
			case "private":
				if err := os.Chmod(e.WrapperReport.ReportPath, 0644); err != nil {
					t.Fatal(err)
				}
			default:
				b, _ := os.ReadFile(e.WrapperReport.ReportPath)
				switch name {
				case "truncated":
					b = b[:len(b)/2]
				case "trailer":
					b = bytes.Replace(b, []byte(originalWrapperIdentityError), []byte("other error"), 1)
				default:
					var report map[string]any
					decoder := json.NewDecoder(bytes.NewReader(b))
					if err := decoder.Decode(&report); err != nil {
						t.Fatal(err)
					}
					switch name {
					case "report-summary":
						report["summary"] = "rewritten"
					case "finding":
						report["findings"] = []any{map[string]any{"message": "invented"}}
					case "tuple":
						report["head_sha"] = "foreign"
					case "usage":
						report["input_tokens"] = 123
					case "terminal":
						report["terminal_diagnostics"].(map[string]any)["stdout_bytes"] = 123
					case "missing-findings":
						delete(report, "findings")
					}
					b, _ = json.Marshal(report)
					b = append(b, []byte("\n"+originalWrapperIdentityError+"\n")...)
				}
				if err := os.WriteFile(e.WrapperReport.ReportPath, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			raw, _ = json.Marshal(e)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := f.a.AuthenticateLegacyCompletion(context.Background(), f.original.ID, f.c, path); err == nil {
				t.Fatal("unbound/tampered report accepted", name)
			}
			var count int
			if err := f.a.db.QueryRow("SELECT count(*) FROM review_completion_seals").Scan(&count); err != nil || count != 0 {
				t.Fatal("negative left a seal", err)
			}
			if f.gateway.published != 0 || f.model.calls != 1 {
				t.Fatal("negative sampled/published")
			}
		})
	}
}

func TestOriginalWrapperReportChecksCanonicalAttribution(t *testing.T) {
	f, path := wrapperCompletionFixture(t)
	raw, _ := os.ReadFile(path)
	var e LegacyCompletionEvidence
	_ = json.Unmarshal(raw, &e)
	report, _ := os.ReadFile(e.WrapperReport.ReportPath)
	for _, name := range []string{"request", "session", "tuple", "usage", "terminal", "settings", "duration", "verdict"} {
		t.Run(name, func(t *testing.T) {
			r := f.original
			switch name {
			case "request":
				r.RequestID = "different"
			case "session":
				r.SessionID = "different"
			case "tuple":
				r.Identity.HeadSHA = "different"
			case "usage":
				r.Usage.TotalTokens++
			case "terminal":
				v := *r.Terminal
				v.StdoutBytes++
				r.Terminal = &v
			case "settings":
				r.Settings.Effort = "high"
			case "duration":
				r.DurationMS++
			case "verdict":
				r.Verdict = VerdictBlocking
			}
			if _, _, err := parseOriginalWrapperReport(report, r); err == nil {
				t.Fatal("attribution drift accepted")
			}
		})
	}
}

func TestReconcileDecodedFullReceiptIsIdempotent(t *testing.T) {
	f, path := legacyCompletionFixture(t)
	_ = path
	before, err := f.a.load(context.Background(), f.original.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := f.a.Reconcile(context.Background(), f.original.ID, "owner/repo", 9); err != nil {
			t.Fatal("decoded-value reconcile failed", err)
		}
	}
	after, err := f.a.load(context.Background(), f.original.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("idempotent reconcile changed history", err)
	}
	if f.model.calls != 1 || f.gateway.published != 0 {
		t.Fatal("reconcile sampled/published")
	}
}

func TestCompletionRecoveryChildRequiresUnchangedConsumedRoot(t *testing.T) {
	for _, root := range []string{"missing", "exact", "foreign"} {
		t.Run(root, func(t *testing.T) {
			f := newCompletionFixture(t)
			r := f.original
			r.Parent = "parent"
			raw, _ := json.Marshal(r)
			if _, err := f.a.db.Exec("UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), r.ID); err != nil {
				t.Fatal(err)
			}
			if err := f.a.writeJoinJournal(r); err != nil {
				t.Fatal(err)
			}
			var payload string
			if err := f.a.db.QueryRow("SELECT payload FROM review_completion_seals WHERE attempt=?", r.ID).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			var seal completionSeal
			_ = json.Unmarshal([]byte(payload), &seal)
			seal.Original.Parent = "parent"
			raw, _ = json.Marshal(seal)
			if _, err := f.a.db.Exec("UPDATE review_completion_seals SET payload=?,digest=? WHERE attempt=?", string(raw), receiptHash(raw), r.ID); err != nil {
				t.Fatal(err)
			}
			if root != "missing" {
				child := r.ID
				if root == "foreign" {
					child = "old-child"
				}
				if _, err := f.a.db.Exec("INSERT INTO review_recovery_roots(tuple,parent,child)VALUES(?,?,?)", tupleKey(r.Identity), "parent", child); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.complete()
			if (err == nil) != (root == "exact") {
				t.Fatal("root qualification incorrect", root, err)
			}
			if f.model.calls != 1 {
				t.Fatal("root resampled")
			}
			if root != "exact" && f.gateway.published != 0 {
				t.Fatal("invalid root published")
			}
		})
	}
}

func TestReconcileFullDiagnosticValuesAndReadbackCAS(t *testing.T) {
	for _, name := range []string{"same", "terminal", "renderer", "session", "owner", "native", "tuple", "settings", "parent", "join", "usage", "publication", "concurrent-readback"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			id := seedDiagnosticTimeout(t, dir)
			a := openTestAdmission(t, dir, "", admissionSettings())
			a.processAbsent = func(int) error { return nil }
			original, err := a.load(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			// Exercise post-readback CAS as well as joined idempotence: the full original
			// receipt has independent pointer-bearing decodes in both durable records.
			original.Publication = Publication{}
			raw, _ := json.Marshal(original)
			if _, err = a.db.Exec("UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), id); err != nil {
				t.Fatal(err)
			}
			if err = a.writeJoinJournal(original); err != nil {
				t.Fatal(err)
			}
			mutation := func(r *AttemptReceipt) {
				switch name {
				case "terminal":
					r.Terminal.StdoutBytes++
				case "renderer":
					r.RendererProvenance.EvidenceSHA256 = "changed"
				case "session":
					r.Terminal.Session.LocalRequestID = "changed"
				case "owner":
					r.OwnerActor = "changed"
				case "native":
					r.OwnerNative = "changed"
				case "tuple":
					r.Identity.HeadSHA = "changed"
				case "settings":
					r.Settings.AppID++
				case "parent":
					r.Parent = "changed"
				case "join":
					r.CompletedAt++
				case "usage":
					r.Usage.TotalTokens++
				case "publication":
					r.Publication.CheckRunID++
				case "concurrent-readback":
					r.Terminal.Session.ReasoningNotifications++
				}
			}
			a.SetPublicationLookup(func(context.Context, AttemptReceipt) (Publication, error) {
				if name == "concurrent-readback" {
					r, err := a.load(context.Background(), id)
					if err != nil {
						return Publication{}, err
					}
					mutation(&r)
					raw, _ := json.Marshal(r)
					_, err = a.db.Exec("UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), id)
					if err != nil {
						return Publication{}, err
					}
				}
				return Publication{CheckRunID: 100, CommentID: 101, Conclusion: "failure"}, nil
			})
			if name != "same" && name != "concurrent-readback" {
				r, err := a.load(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				mutation(&r)
				if err = a.writeJoinJournal(r); err != nil {
					t.Fatal(err)
				}
			}
			err = a.Reconcile(context.Background(), id, "owner/repo", 9)
			if (err == nil) != (name == "same") {
				t.Fatal("full receipt value/CAS guard incorrect", name, err)
			}
			if name == "same" {
				if err = a.Reconcile(context.Background(), id, "owner/repo", 9); err != nil {
					t.Fatal("same-value second join failed", err)
				}
			}
		})
	}
}

func TestCompletedNormalPublicationKeepsMonotonicAuditAndSeal(t *testing.T) {
	a := openTestAdmission(t, t.TempDir(), "", admissionSettings())
	g := &completionTestGateway{snapshot: PullRequestSnapshot{Repository: "owner/repo", Number: 9, BaseRef: "main", BaseSHA: "base", HeadSHA: "head", Title: "title", Description: "body\n", Diff: "diff\n"}}
	m := &completionTestModel{a: a, envelope: approvedEnvelope(t)}
	svc, err := NewLocalReviewService(m, g, "core", "policy")
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAdmission(a)
	if _, err = svc.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9); err != nil {
		t.Fatal("normal publication changed", err)
	}
	r, err := a.load(context.Background(), a.AttemptID())
	if err != nil || !r.Joined || r.Publication.CheckRunID != 100 {
		t.Fatal("original normal custody not joined", err)
	}
	_, _, state, _, pub, err := a.loadCompletion(context.Background(), r.ID)
	if err != nil || state != "published" || pub.CheckRunID != 100 || m.calls != 1 || g.published != 1 {
		t.Fatal("normal completed publication seal invalid", err, state)
	}
}

func TestTimeoutCannotSealOrCompleteVerdict(t *testing.T) {
	a := openTestAdmission(t, t.TempDir(), "", admissionSettings())
	if err := a.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	report := timeoutReport()
	if err := a.Checkpoint(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if err := a.Finish(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM review_completion_seals").Scan(&count); err != nil || count != 0 {
		t.Fatal("no-verdict timeout sealed", err)
	}
	if _, err := a.Complete(context.Background(), a.AttemptID(), CompletionCustody{}, &completionTestGateway{}, "token", "grok-review", "core", "policy"); err == nil {
		t.Fatal("timeout converted to verdict")
	}
}
