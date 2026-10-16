package grokreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReconsiderBlockingOncePreservesOriginal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	parent := openTestAdmission(t, dir, "", admissionSettings())
	if err := parent.Start(ctx, admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if err := parent.Finish(ctx, ReviewReport{Result: FindingsResult{Verdict: VerdictBlocking}, Publication: Publication{CheckRunID: 1, Conclusion: "failure"}}); err != nil {
		t.Fatal(err)
	}
	original, err := parent.load(ctx, parent.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	grant := ReconsiderGrant{Schema: "coordinator.review-reconsider.v1", Parent: parent.AttemptID(), ReviewID: "independent-child", Authorization: "controller decision with dispute", Evidence: "exact original findings disproved by tests"}
	raw, _ := json.Marshal(grant)
	path := filepath.Join(dir, "grant.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	child := openTestAdmission(t, dir, "", admissionSettings())
	child.processAbsent = func(int) error { return nil }
	if err = child.ImportReconsider(path); err != nil {
		t.Fatal(err)
	}
	if err = child.Start(ctx, admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if child.receipt.Parent != parent.AttemptID() || child.receipt.AuthorizationReference != grant.ReviewID {
		t.Fatal("lost lineage")
	}
	if err = child.Finish(ctx, ReviewReport{Result: FindingsResult{Verdict: VerdictApproved}, Publication: Publication{CheckRunID: 2, Conclusion: "success"}}); err != nil {
		t.Fatal(err)
	}
	again := openTestAdmission(t, dir, "", admissionSettings())
	again.processAbsent = func(int) error { return nil }
	if err = again.ImportReconsider(path); err != nil {
		t.Fatal(err)
	}
	if err = again.Start(ctx, admissionBundle()); err == nil {
		t.Fatal("second reconsideration allowed")
	}
	retained, _ := parent.load(ctx, parent.AttemptID())
	if retained.Verdict != original.Verdict || retained.Parent != "" {
		t.Fatal("original verdict altered")
	}
}
func TestReconsiderPrivateGrantAndExactInput(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	parent := openTestAdmission(t, dir, "", admissionSettings())
	if err := parent.Start(ctx, admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if err := parent.Finish(ctx, ReviewReport{Result: FindingsResult{Verdict: VerdictBlocking}, Publication: Publication{CheckRunID: 1, Conclusion: "failure"}}); err != nil {
		t.Fatal(err)
	}
	grant := ReconsiderGrant{Schema: "coordinator.review-reconsider.v1", Parent: parent.AttemptID(), ReviewID: "child", Authorization: "record", Evidence: "proof"}
	raw, _ := json.Marshal(grant)
	path := filepath.Join(dir, "grant.json")
	_ = os.WriteFile(path, raw, 0644)
	child := openTestAdmission(t, dir, "", admissionSettings())
	child.processAbsent = func(int) error { return nil }
	if err := child.ImportReconsider(path); err == nil {
		t.Fatal("public grant allowed")
	}
	_ = os.Chmod(path, 0600)
	if err := child.ImportReconsider(path); err != nil {
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

func TestReconsiderCheckBindsOriginalBlockingPublication(t *testing.T) {
	for _, kind := range []string{"exact", "wrong-attempt", "active", "other-live", "missing"} {
		t.Run(kind, func(t *testing.T) {
			identity, _ := identityFor(admissionBundle())
			prior := AttemptReceipt{ID: "0123456789abcdef", Identity: identity, Settings: admissionSettings(), SamplingCompleted: true, Verdict: VerdictBlocking, Publication: Publication{CheckRunID: 3, Conclusion: "failure"}}
			c := map[string]any{"id": 3, "name": "grok-review", "head_sha": identity.HeadSHA, "external_id": publicationExternalID(identity.Repository, identity.PR, identity.HeadSHA, prior.ID, identity.BundleSHA256), "status": "completed", "conclusion": "failure", "app": map[string]any{"id": 1}, "output": map[string]any{"title": "Grok review blocking"}}
			if kind == "wrong-attempt" {
				c["external_id"] = "other"
			}
			if kind == "active" {
				c["status"] = "in_progress"
			}
			checks := []any{c}
			if kind == "missing" {
				checks = nil
			}
			if kind == "other-live" {
				checks = append(checks, map[string]any{"id": 4, "name": "grok-review", "head_sha": identity.HeadSHA, "status": "in_progress", "app": map[string]any{"id": 1}})
			}
			g, err := NewGitHubCLI("/fake/gh", 1, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			g.execute = func(context.Context, []string, []byte, []string) ([]byte, error) {
				return json.Marshal(map[string]any{"total_count": len(checks), "check_runs": checks})
			}
			err = g.VerifyReconsiderCheck(context.Background(), "fixture", 1, prior)
			if (err == nil) != (kind == "exact") {
				t.Fatalf("%s: %v", kind, err)
			}
			if kind == "exact" && g.VerifyRecoveryCheck(context.Background(), "fixture", 1, prior) == nil {
				t.Fatal("ordinary timeout recovery accepted blocking")
			}
		})
	}
}

func TestReconsiderModelUsesImmutableAdmittedEvidence(t *testing.T) {
	dir := t.TempDir()
	a := openTestAdmission(t, dir, "", admissionSettings())
	grant := ReconsiderGrant{Schema: "coordinator.review-reconsider.v1", Parent: "parent", ReviewID: "child", Authorization: "controller record", Evidence: "original evidence"}
	raw, _ := json.Marshal(grant)
	path := filepath.Join(dir, "grant.json")
	_ = os.WriteFile(path, raw, 0600)
	if err := a.ImportReconsider(path); err != nil {
		t.Fatal(err)
	}
	model := &fakeModelReviewer{}
	service := &LocalReviewService{model: model}
	if err := service.BindReconsideration(a); err != nil {
		t.Fatal(err)
	}
	grant.Evidence = "replacement after admission"
	raw, _ = json.Marshal(grant)
	_ = os.WriteFile(path, raw, 0600)
	wrapped := service.model.(ReconsiderReviewer)
	if wrapped.Grant.Evidence != "original evidence" {
		t.Fatal("admitted evidence replaced")
	}
	if _, _, err := wrapped.Review(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
}
