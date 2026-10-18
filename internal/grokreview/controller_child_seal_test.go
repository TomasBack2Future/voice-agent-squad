package grokreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// controllerChildFixture seeds an original attempt that qualifies for the
// requested controller child and returns an admission that imported the grant.
func controllerChildFixture(t *testing.T, kind string) (string, *Admission) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	parent := openTestAdmission(t, dir, "", admissionSettings())
	if err := parent.Start(ctx, admissionBundle()); err != nil {
		t.Fatal(err)
	}
	child := openTestAdmission(t, dir, "", admissionSettings())
	child.processAbsent = func(int) error { return nil }
	switch kind {
	case "reconsider":
		if err := parent.Finish(ctx, ReviewReport{Result: FindingsResult{Verdict: VerdictBlocking}, Publication: Publication{CheckRunID: 1, Conclusion: "failure"}}); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(ReconsiderGrant{Schema: "coordinator.review-reconsider.v1", Parent: parent.AttemptID(), ReviewID: "child", Authorization: "controller record", Evidence: "disproof"})
		path := filepath.Join(dir, "reconsider.json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := child.ImportReconsider(path); err != nil {
			t.Fatal(err)
		}
	case "restart":
		if err := parent.Finish(ctx, ReviewReport{Result: FindingsResult{Verdict: VerdictError}, Audit: CLIAudit{FailureKind: CLIFailureCanceled}, FailureStage: "sampling"}); err != nil {
			t.Fatal(err)
		}
		if err := child.ImportRestart(restartGrantFile(t, dir, parent.AttemptID(), 0600)); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown child %s", kind)
	}
	return dir, child
}

// A controller-admitted child must checkpoint, seal and publish its single
// sample exactly like an ordinary admission: the complete original input is
// retained on admission so the seal binds the actual reviewed bytes.
func TestControllerChildSealsAndPublishesSingleSample(t *testing.T) {
	for _, kind := range []string{"reconsider", "restart"} {
		t.Run(kind, func(t *testing.T) {
			_, child := controllerChildFixture(t, kind)
			snapshot := PullRequestSnapshot{Repository: "owner/repo", Number: 9, BaseRef: "main", BaseSHA: "base", HeadSHA: "head", Title: "repair", Description: "contract", Diff: "+fixed"}
			gateway := &completionTestGateway{snapshot: snapshot}
			model := &fakeModelReviewer{result: approvedFindings()}
			service, err := NewLocalReviewService(model, gateway, "core", "policy")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "reconsider" {
				if err := service.BindReconsideration(child); err != nil {
					t.Fatal(err)
				}
			}
			service.SetAdmission(child)
			report, err := service.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9)
			if err != nil {
				t.Fatalf("controller child did not complete: %v", err)
			}
			if model.calls != 1 || gateway.published != 1 || report.Publication.CheckRunID != 100 {
				t.Fatalf("calls=%d published=%d publication=%#v", model.calls, gateway.published, report.Publication)
			}
			stored, err := child.load(context.Background(), child.AttemptID())
			if err != nil {
				t.Fatal(err)
			}
			if !stored.Joined || !stored.SamplingCompleted || stored.Verdict != VerdictApproved || stored.Publication.CheckRunID != 100 {
				t.Fatalf("joined receipt lost result/publication: %#v", stored)
			}
			var payload, state string
			if err := child.db.QueryRow("SELECT payload,state FROM review_completion_seals WHERE attempt=?", child.AttemptID()).Scan(&payload, &state); err != nil {
				t.Fatalf("no completion seal: %v", err)
			}
			var seal completionSeal
			if err := decodeStrictJSON([]byte(payload), &seal); err != nil {
				t.Fatal(err)
			}
			if state != "published" || string(seal.Bundle) != string(admissionBundle()) || seal.Original.Parent == "" || seal.Result.Verdict != VerdictApproved {
				t.Fatalf("seal does not bind original complete input: state=%s parent=%q", state, seal.Original.Parent)
			}
		})
	}
}
