package grokreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func restartGrantFile(t *testing.T, dir, parent string, mode os.FileMode) string {
	t.Helper()
	raw, _ := json.Marshal(RestartGrant{Schema: "coordinator.review-restart.v1", Parent: parent, ReviewID: "coordinator-child", Authorization: "controller-unverdicted-restart", Evidence: "joined empty output"})
	path := filepath.Join(dir, "restart.json")
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRestartUnverdictedAttemptOncePreservesOriginal(t *testing.T) {
	for _, report := range map[string]ReviewReport{
		"canceled-unpublished": {Result: FindingsResult{Verdict: VerdictError}, Audit: CLIAudit{FailureKind: CLIFailureCanceled}, FailureStage: "sampling"},
		"timeout-failed-check": {Result: FindingsResult{Verdict: VerdictError}, Audit: CLIAudit{FailureKind: CLIFailureTimeout}, FailureStage: "sampling", Publication: Publication{CheckRunID: 1, Conclusion: "failure"}},
	} {
		ctx := context.Background()
		dir := t.TempDir()
		parent := openTestAdmission(t, dir, "", admissionSettings())
		if err := parent.Start(ctx, admissionBundle()); err != nil {
			t.Fatal(err)
		}
		if err := parent.Finish(ctx, report); err != nil {
			t.Fatal(err)
		}
		path := restartGrantFile(t, dir, parent.AttemptID(), 0600)
		child := openTestAdmission(t, dir, "", admissionSettings())
		child.processAbsent = func(int) error { return nil }
		if err := child.ImportRestart(path); err != nil {
			t.Fatal(err)
		}
		if err := child.Start(ctx, admissionBundle()); err != nil {
			t.Fatal(err)
		}
		if child.receipt.Parent != parent.AttemptID() || child.receipt.InputProvenance != "controller-unverdicted-restart" || child.receipt.AuthorizationReference != "coordinator-child" {
			t.Fatal("lost restart lineage")
		}
		if err := child.Finish(ctx, ReviewReport{Result: FindingsResult{Verdict: VerdictApproved}, Publication: Publication{CheckRunID: 2, Conclusion: "success"}}); err != nil {
			t.Fatal(err)
		}
		again := openTestAdmission(t, dir, "", admissionSettings())
		again.processAbsent = func(int) error { return nil }
		if err := again.ImportRestart(path); err != nil {
			t.Fatal(err)
		}
		if err := again.Start(ctx, admissionBundle()); err == nil {
			t.Fatal("second restart allowed")
		}
		retained, _ := parent.load(ctx, parent.AttemptID())
		if retained.Verdict != VerdictError || retained.Parent != "" {
			t.Fatal("original attempt altered")
		}
	}
}

func TestRestartRejectsVerdictsChangedInputAndPublicGrant(t *testing.T) {
	for name, report := range map[string]ReviewReport{
		"blocking": {Result: FindingsResult{Verdict: VerdictBlocking}, Publication: Publication{CheckRunID: 1, Conclusion: "failure"}},
		"approved": {Result: FindingsResult{Verdict: VerdictApproved}, Publication: Publication{CheckRunID: 1, Conclusion: "success"}},
		"publish":  {Result: FindingsResult{Verdict: VerdictError}, Audit: CLIAudit{FailureKind: CLIFailureCanceled}, FailureStage: "publishing"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			parent := openTestAdmission(t, dir, "", admissionSettings())
			if err := parent.Start(ctx, admissionBundle()); err != nil {
				t.Fatal(err)
			}
			if err := parent.Finish(ctx, report); err != nil {
				t.Fatal(err)
			}
			child := openTestAdmission(t, dir, "", admissionSettings())
			child.processAbsent = func(int) error { return nil }
			if err := child.ImportRestart(restartGrantFile(t, dir, parent.AttemptID(), 0600)); err != nil {
				t.Fatal(err)
			}
			if err := child.Start(ctx, admissionBundle()); err == nil {
				t.Fatal("restart of an attempt that is not unverdicted sampling allowed")
			}
		})
	}
	ctx := context.Background()
	dir := t.TempDir()
	parent := openTestAdmission(t, dir, "", admissionSettings())
	if err := parent.Start(ctx, admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if err := parent.Finish(ctx, ReviewReport{Result: FindingsResult{Verdict: VerdictError}, Audit: CLIAudit{FailureKind: CLIFailureCanceled}, FailureStage: "sampling"}); err != nil {
		t.Fatal(err)
	}
	child := openTestAdmission(t, dir, "", admissionSettings())
	child.processAbsent = func(int) error { return nil }
	path := restartGrantFile(t, dir, parent.AttemptID(), 0644)
	if err := child.ImportRestart(path); err == nil {
		t.Fatal("public grant allowed")
	}
	_ = os.Chmod(path, 0600)
	if err := child.ImportRestart(path); err != nil {
		t.Fatal(err)
	}
	var bundle FrozenReviewBundle
	_ = json.Unmarshal(admissionBundle(), &bundle)
	bundle.HeadSHA = "different"
	changed, _ := json.Marshal(bundle)
	if err := child.Start(ctx, changed); err == nil {
		t.Fatal("changed input allowed")
	}
}
