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
