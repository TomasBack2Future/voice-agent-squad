package grokreview

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type controllerCompletionFixture struct {
	dir      string
	grant    string
	output   string
	sha      string
	id       string
	original AttemptReceipt
	model    *fakeModelReviewer
	gateway  *completionTestGateway
	snapshot PullRequestSnapshot
}

// newUnsealedReconsideration reproduces the defective pre-fix run: the child's
// admission lost the complete input, so its sampling checkpoint rolled back and
// only the joined verdict and the wrapper's stdout document survived.
func newUnsealedReconsideration(t *testing.T) *controllerCompletionFixture {
	t.Helper()
	dir, child := controllerChildFixture(t, "reconsider")
	f := &controllerCompletionFixture{dir: dir, grant: filepath.Join(dir, "reconsider.json")}
	f.snapshot = PullRequestSnapshot{Repository: "owner/repo", Number: 9, BaseRef: "main", BaseSHA: "base", HeadSHA: "head", Title: "repair", Description: "contract", Diff: "+fixed"}
	f.gateway = &completionTestGateway{snapshot: f.snapshot}
	code := 0
	joined := time.Unix(1700000000, 0).UTC()
	f.model = &fakeModelReviewer{result: approvedFindings(), audit: CLIAudit{Backend: "grok", RequestID: "request", SessionID: "session", RequestedModel: "grok-4.7", ReasoningEffort: "medium", ResolvedModel: "grok-4.7-build", Usage: TokenUsage{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 2, TotalTokens: 15}, CostUSD: 0.5, Duration: 2 * time.Second}}
	f.model.afterReview = func() {
		f.model.audit.Terminal = &TerminalDiagnostics{InputSHA256: receiptHash(f.model.bundle), StartedAt: joined, FinishedAt: joined, JoinedAt: &joined, ChildStarted: true, ChildWaited: true, ExitCode: &code, StdoutBytes: 7, StdoutSHA256: receiptHash([]byte("stdout!")), StderrSHA256: receiptHash(nil), SessionEvidence: "unavailable"}
		child.frozenBundle = nil
	}
	service, err := NewLocalReviewService(f.model, f.gateway, "core", "policy")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.BindReconsideration(child); err != nil {
		t.Fatal(err)
	}
	service.SetAdmission(child)
	report, err := service.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9)
	if err == nil || f.gateway.published != 0 {
		t.Fatalf("fixture did not reproduce the unsealed child: %v", err)
	}
	f.id = child.AttemptID()
	out := NewReviewCommandOutput(report)
	out.AttemptID, out.ParentAttempt, out.InputProvenance = f.id, child.Receipt().Parent, child.Receipt().InputProvenance
	raw, _ := json.MarshalIndent(out, "", "  ")
	raw = append(raw, '\n')
	f.output = filepath.Join(dir, "provider-result.json")
	if err := os.WriteFile(f.output, raw, 0600); err != nil {
		t.Fatal(err)
	}
	f.sha = receiptHash(raw)
	if f.original, err = child.load(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	if !unsealedControllerChild(f.original, admissionSettings()) {
		t.Fatalf("fixture receipt is not the defective joined state: %#v", f.original)
	}
	return f
}

func (f *controllerCompletionFixture) admission(t *testing.T) *Admission {
	t.Helper()
	a := openTestAdmission(t, f.dir, "", admissionSettings())
	a.processAbsent = func(int) error { return nil }
	a.SetPublicationLookup(f.gateway.lookup)
	return a
}

func (f *controllerCompletionFixture) evidence() ControllerChildEvidence {
	return ControllerChildEvidence{GrantPath: f.grant, OutputPath: f.output, OutputSHA256: f.sha}
}

func TestCompleteControllerChildPublishesOriginalResultWithoutSampling(t *testing.T) {
	f := newUnsealedReconsideration(t)
	a := f.admission(t)
	result, err := a.CompleteControllerChild(context.Background(), f.id, f.evidence(), admissionBundle(), f.gateway, "token", "grok-review", "core", "policy")
	if err != nil {
		t.Fatal(err)
	}
	if f.model.calls != 1 || f.gateway.published != 1 || result.Publication.CheckRunID != 100 || result.Verdict != VerdictApproved || result.Sampled || !result.CurrentInputMatches {
		t.Fatalf("calls=%d published=%d result=%#v", f.model.calls, f.gateway.published, result)
	}
	if f.gateway.audit.AttemptID != f.id || f.gateway.audit.BundleSHA256 != f.original.Identity.BundleSHA256 || f.gateway.audit.RequestID != "request" {
		t.Fatalf("publication not bound to original attempt: %#v", f.gateway.audit)
	}
	stored, _ := a.load(context.Background(), f.id)
	if !completionValuesEqual(stored, f.original) {
		t.Fatal("original receipt edited")
	}
	var state string
	var payload string
	if err := a.db.QueryRow("SELECT state,payload FROM review_completion_seals WHERE attempt=?", f.id).Scan(&state, &payload); err != nil || state != "published" {
		t.Fatalf("seal state=%s err=%v", state, err)
	}
	var seal completionSeal
	if err := decodeStrictJSON([]byte(payload), &seal); err != nil || string(seal.Bundle) != string(admissionBundle()) || seal.Result.Summary != approvedFindings().Summary {
		t.Fatal("seal lost complete original input or typed output")
	}
	var flights int
	_ = a.db.QueryRow("SELECT count(*) FROM review_flights").Scan(&flights)
	if flights != 0 {
		t.Fatal("completion flight retained")
	}
	replay, err := f.admission(t).CompleteControllerChild(context.Background(), f.id, f.evidence(), admissionBundle(), f.gateway, "token", "grok-review", "core", "policy")
	if err != nil || replay.Publication.CheckRunID != 100 || f.gateway.published != 1 || f.model.calls != 1 {
		t.Fatalf("replay republished or failed: %v %#v", err, replay)
	}
}

func TestCompleteControllerChildRejectsUnauthenticatedEvidence(t *testing.T) {
	cases := map[string]func(*testing.T, *controllerCompletionFixture) ([]byte, ControllerChildEvidence, string){
		"output-hash": func(_ *testing.T, f *controllerCompletionFixture) ([]byte, ControllerChildEvidence, string) {
			e := f.evidence()
			e.OutputSHA256 = receiptHash([]byte("other"))
			return admissionBundle(), e, "grok-review"
		},
		"rewritten-verdict": func(t *testing.T, f *controllerCompletionFixture) ([]byte, ControllerChildEvidence, string) {
			raw, _ := os.ReadFile(f.output)
			var out ReviewCommandOutput
			_ = json.Unmarshal(raw, &out)
			out.Verdict = VerdictBlocking
			out.Findings = []Finding{}
			raw, _ = json.Marshal(out)
			if err := os.WriteFile(f.output, raw, 0600); err != nil {
				t.Fatal(err)
			}
			e := f.evidence()
			e.OutputSHA256 = receiptHash(raw)
			return admissionBundle(), e, "grok-review"
		},
		"other-grant": func(t *testing.T, f *controllerCompletionFixture) ([]byte, ControllerChildEvidence, string) {
			raw, _ := json.Marshal(ReconsiderGrant{Schema: "coordinator.review-reconsider.v1", Parent: f.original.Parent, ReviewID: "child", Authorization: "controller record", Evidence: "replacement dispute"})
			path := filepath.Join(f.dir, "other.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			e := f.evidence()
			e.GrantPath = path
			return admissionBundle(), e, "grok-review"
		},
		"changed-input": func(_ *testing.T, f *controllerCompletionFixture) ([]byte, ControllerChildEvidence, string) {
			var b FrozenReviewBundle
			_ = json.Unmarshal(admissionBundle(), &b)
			b.Diff = "+other"
			raw, _ := json.Marshal(b)
			return raw, f.evidence(), "grok-review"
		},
		"other-check": func(_ *testing.T, f *controllerCompletionFixture) ([]byte, ControllerChildEvidence, string) {
			return admissionBundle(), f.evidence(), "grok-review-shadow"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newUnsealedReconsideration(t)
			bundle, evidence, check := mutate(t, f)
			a := f.admission(t)
			if _, err := a.CompleteControllerChild(context.Background(), f.id, evidence, bundle, f.gateway, "token", check, "core", "policy"); err == nil {
				t.Fatal("unauthenticated completion accepted")
			}
			var seals int
			_ = a.db.QueryRow("SELECT count(*) FROM review_completion_seals WHERE attempt=?", f.id).Scan(&seals)
			if f.gateway.published != 0 || f.model.calls != 1 || seals != 0 {
				t.Fatalf("rejected evidence published=%d calls=%d seals=%d", f.gateway.published, f.model.calls, seals)
			}
		})
	}
}

func TestCompleteControllerChildHoldsStaleInputAndUncertainWrite(t *testing.T) {
	f := newUnsealedReconsideration(t)
	f.gateway.snapshot.Diff = "+moved"
	a := f.admission(t)
	if _, err := a.CompleteControllerChild(context.Background(), f.id, f.evidence(), admissionBundle(), f.gateway, "token", "grok-review", "core", "policy"); err == nil || f.gateway.published != 0 {
		t.Fatal("stale live input published")
	}
	f.gateway.snapshot = f.snapshot
	f.gateway.err = errors.New("response lost")
	if _, err := a.CompleteControllerChild(context.Background(), f.id, f.evidence(), admissionBundle(), f.gateway, "token", "grok-review", "core", "policy"); err == nil || f.gateway.published != 1 {
		t.Fatal("lost write not reported uncertain")
	}
	// The write may have landed; without exact-attempt readback no retry is made.
	f.gateway.err = nil
	if _, err := f.admission(t).CompleteControllerChild(context.Background(), f.id, f.evidence(), admissionBundle(), f.gateway, "token", "grok-review", "core", "policy"); err == nil || f.gateway.published != 1 {
		t.Fatal("uncertain publication retried")
	}
	f.gateway.publication = Publication{CheckRunID: 100, Conclusion: "success"}
	result, err := f.admission(t).CompleteControllerChild(context.Background(), f.id, f.evidence(), admissionBundle(), f.gateway, "token", "grok-review", "core", "policy")
	if err != nil || result.Publication.CheckRunID != 100 || f.gateway.published != 1 {
		t.Fatalf("readback did not join exact publication: %v", err)
	}
}

func TestCompleteControllerChildRejectsSealedOrdinaryAttempt(t *testing.T) {
	_, child := controllerChildFixture(t, "reconsider")
	gateway := &completionTestGateway{snapshot: PullRequestSnapshot{Repository: "owner/repo", Number: 9, BaseRef: "main", BaseSHA: "base", HeadSHA: "head", Title: "repair", Description: "contract", Diff: "+fixed"}}
	service, _ := NewLocalReviewService(&fakeModelReviewer{result: approvedFindings()}, gateway, "core", "policy")
	_ = service.BindReconsideration(child)
	service.SetAdmission(child)
	if _, err := service.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9); err != nil {
		t.Fatal(err)
	}
	child.SetPublicationLookup(gateway.lookup)
	if _, err := child.CompleteControllerChild(context.Background(), child.AttemptID(), ControllerChildEvidence{}, admissionBundle(), gateway, "token", "grok-review", "core", "policy"); err == nil || gateway.published != 1 {
		t.Fatal("published child accepted controller completion")
	}
}
