package grokreview

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRecoveryCheckRequiresExactFailedAppGateAndRejectsVerdicts(t *testing.T) {
	for _, kind := range []string{"timeout", "approved", "blocking", "active", "missing", "other-app", "normal-retry", "empty-normal"} {
		t.Run(kind, func(t *testing.T) {
			identity, _ := identityFor(admissionBundle())
			r := AttemptReceipt{ID: "original", Identity: identity, Settings: admissionSettings(), Publication: Publication{CheckRunID: 3, Conclusion: "failure"}}
			c := map[string]any{"id": 3, "name": "grok-review", "head_sha": "head", "status": "completed", "conclusion": "failure", "app": map[string]any{"id": 1}, "output": map[string]any{"title": "Grok review error"}}
			switch kind {
			case "approved":
				c["conclusion"] = "success"
				c["output"] = map[string]any{"title": "Grok review approved"}
			case "blocking":
				c["output"] = map[string]any{"title": "Grok review blocking"}
			case "active":
				c["status"] = "in_progress"
			case "other-app":
				c["app"] = map[string]any{"id": 2}
			case "normal-retry", "empty-normal":
				r.ID = ""
			}
			checks := []any{c}
			if kind == "missing" || kind == "empty-normal" {
				checks = []any{}
			}
			g, err := NewGitHubCLI("/fake/gh", 1, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			g.execute = func(_ context.Context, args []string, _ []byte, _ []string) ([]byte, error) {
				if !strings.Contains(strings.Join(args, " "), "commits/head/check-runs") {
					t.Fatal("Check lookup used wrong head")
				}
				return json.Marshal(map[string]any{"total_count": len(checks), "check_runs": checks})
			}
			err = g.VerifyRecoveryCheck(context.Background(), "token", 1, r)
			pass := kind == "timeout" || kind == "empty-normal"
			if (err == nil) != pass {
				t.Fatalf("kind %s: %v", kind, err)
			}
		})
	}
}

func TestLostPublicationReadbackBindsAttemptInputAppModeAndVerdict(t *testing.T) {
	for _, kind := range []string{"exact", "other-attempt", "other-input", "other-app", "other-mode", "active", "wrong-verdict", "duplicate", "missing"} {
		t.Run(kind, func(t *testing.T) {
			identity, _ := identityFor(admissionBundle())
			r := AttemptReceipt{ID: "0123456789abcdef", Identity: identity, Settings: admissionSettings(), SamplingCompleted: true, Verdict: VerdictError, FailureKind: CLIFailureTimeout, FailureStage: "sampling"}
			c := map[string]any{"id": 3, "name": "grok-review", "head_sha": r.Identity.HeadSHA, "external_id": publicationExternalID(r.Identity.Repository, r.Identity.PR, r.Identity.HeadSHA, r.ID, r.Identity.BundleSHA256), "status": "completed", "conclusion": "failure", "app": map[string]any{"id": 1}, "output": map[string]any{"title": "Grok review error"}}
			switch kind {
			case "other-attempt":
				c["external_id"] = publicationExternalID(r.Identity.Repository, r.Identity.PR, r.Identity.HeadSHA, "fedcba9876543210", r.Identity.BundleSHA256)
			case "other-input":
				c["external_id"] = publicationExternalID(r.Identity.Repository, r.Identity.PR, r.Identity.HeadSHA, r.ID, strings.Repeat("0", 64))
			case "other-app":
				c["app"] = map[string]any{"id": 2}
			case "other-mode":
				c["name"] = "grok-review-shadow"
			case "active":
				c["status"] = "in_progress"
			case "wrong-verdict":
				c["output"] = map[string]any{"title": "Grok review blocking"}
			}
			checks := []any{c}
			if kind == "duplicate" {
				checks = append(checks, c)
			}
			if kind == "missing" {
				checks = nil
			}
			g, err := NewGitHubCLI("/fake/gh", 1, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			g.execute = func(_ context.Context, args []string, _ []byte, _ []string) ([]byte, error) {
				if !strings.Contains(strings.Join(args, " "), "commits/head/check-runs") {
					t.Fatal("wrong head readback")
				}
				return json.Marshal(map[string]any{"total_count": len(checks), "check_runs": checks})
			}
			publication, err := g.FindAttemptPublication(context.Background(), "token", 1, r)
			if kind == "active" || kind == "wrong-verdict" || kind == "duplicate" {
				if err == nil {
					t.Fatal("unqualified publication accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (publication.CheckRunID > 0) != (kind == "exact") {
				t.Fatal("another invocation consumed", kind, publication)
			}
		})
	}
}

func TestRecoveryRejectsOtherModeVerdictWithoutBlockingOrdinarySample(t *testing.T) {
	for _, mode := range []string{"required", "shadow"} {
		for _, state := range []string{"approved", "blocking", "active", "old-head"} {
			t.Run(mode+"/"+state, func(t *testing.T) {
				identity, _ := identityFor(admissionBundle())
				r := AttemptReceipt{ID: "original", Identity: identity, Settings: admissionSettings(), Publication: Publication{CheckRunID: 3, Conclusion: "failure"}}
				r.Settings.Mode = mode
				original, other := "grok-review", "grok-review-shadow"
				if mode == "shadow" {
					original, other = other, original
				}
				failed := map[string]any{"id": 3, "name": original, "head_sha": "head", "status": "completed", "conclusion": "failure", "app": map[string]any{"id": 1}, "output": map[string]any{"title": "Grok review error"}}
				competing := map[string]any{"id": 4, "name": other, "head_sha": "head", "status": "completed", "conclusion": "success", "app": map[string]any{"id": 1}, "output": map[string]any{"title": "Grok review approved"}}
				if state == "blocking" {
					competing["conclusion"] = "failure"
					competing["output"] = map[string]any{"title": "Grok review blocking"}
				}
				if state == "active" {
					competing["status"] = "in_progress"
				}
				if state == "old-head" {
					competing["head_sha"] = "old-head"
				}
				g, _ := NewGitHubCLI("/fake/gh", 1, 1<<20)
				g.execute = func(context.Context, []string, []byte, []string) ([]byte, error) {
					return json.Marshal(map[string]any{"total_count": 2, "check_runs": []any{failed, competing}})
				}
				err := g.VerifyRecoveryCheck(context.Background(), "token", 1, r)
				if (err == nil) != (state == "old-head") {
					t.Fatalf("recovery other-mode verdict %s: %v", state, err)
				}
				r.ID = ""
				g.execute = func(context.Context, []string, []byte, []string) ([]byte, error) {
					return json.Marshal(map[string]any{"total_count": 1, "check_runs": []any{competing}})
				}
				if err = g.VerifyRecoveryCheck(context.Background(), "token", 1, r); err != nil {
					t.Fatalf("ordinary sample poisoned by other mode: %v", err)
				}
			})
		}
	}
}
