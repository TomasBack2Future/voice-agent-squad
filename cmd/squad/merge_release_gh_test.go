package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChecksPassedRequiresEveryCheckCompletedSuccessfully(t *testing.T) {
	ok := checkResult{Status: "COMPLETED", Conclusion: "SUCCESS"}
	for name, c := range map[string]struct {
		in   []checkResult
		want bool
	}{
		"empty rollup":        {nil, false},
		"all success":         {[]checkResult{ok, {State: "SUCCESS"}}, true},
		"skipped and neutral": {[]checkResult{ok, {Status: "COMPLETED", Conclusion: "SKIPPED"}, {Status: "COMPLETED", Conclusion: "NEUTRAL"}}, true},
		"pending run":         {[]checkResult{ok, {Status: "IN_PROGRESS"}}, false},
		"queued":              {[]checkResult{ok, {Status: "QUEUED"}}, false},
		"failed":              {[]checkResult{ok, {Status: "COMPLETED", Conclusion: "FAILURE"}}, false},
		"cancelled":           {[]checkResult{ok, {Status: "COMPLETED", Conclusion: "CANCELLED"}}, false},
		"pending status ctx":  {[]checkResult{ok, {State: "PENDING"}}, false},
		"error status ctx":    {[]checkResult{{State: "ERROR"}}, false},
	} {
		if got := checksPassed(c.in); got != c.want {
			t.Errorf("%s: got %v want %v", name, got, c.want)
		}
	}
}

const mergeHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const mergeBase = "cccccccccccccccccccccccccccccccccccccccc"

func checkFixture(name, conclusion string) mergeCheck {
	no := false
	c := mergeCheck{Type: "CheckRun", ID: 11, Name: name, Status: "COMPLETED", Conclusion: conclusion, Required: &no}
	c.Suite.Commit.OID = mergeHead
	c.Suite.App.ID = 15368
	c.Suite.App.Slug = "github-actions"
	if name == "grok-review-shadow" {
		c.ID = 12
		c.Suite.App.ID = 123
		c.Suite.App.Slug = "review-app"
		c.ExternalID = "local-grok-attempt:attempt:hash"
		c.Title = "Grok review blocking"
	}
	return c
}
func reviewApp() (int64, error) { return 123, nil }

func TestMergeChecksRequiredAndAdvisoryFailures(t *testing.T) {
	for _, name := range []string{"disputed shadow", "approved shadow", "failed ci", "pending ci", "missing ci", "enforced review", "required shadow", "rule required shadow", "required app mismatch", "pending shadow", "unknown mode", "wrong app", "missing app", "missing requiredness", "wrong head", "tool error", "unreadable app config", "only review", "missing check id"} {
		t.Run(name, func(t *testing.T) {
			checks := []mergeCheck{checkFixture("CI Gate", "SUCCESS"), checkFixture("grok-review-shadow", "FAILURE")}
			required := []requiredMergeCheck{{Context: "CI Gate", AppID: 15368}}
			app := reviewApp
			want := false
			wantFailures := 0
			switch name {
			case "disputed shadow":
				want = true
				wantFailures = 1
			case "approved shadow":
				checks[1].Conclusion = "SUCCESS"
				want = true
			case "failed ci":
				checks[0].Conclusion = "FAILURE"
			case "pending ci":
				checks[0].Status = "IN_PROGRESS"
				checks[0].Conclusion = ""
			case "missing ci":
				checks = checks[1:]
			case "enforced review":
				checks[1].Name = "grok-review"
			case "required shadow":
				yes := true
				checks[1].Required = &yes
			case "rule required shadow":
				required = append(required, requiredMergeCheck{Context: "grok-review-shadow", AppID: 123})
			case "required app mismatch":
				required[0].AppID = 999
			case "pending shadow":
				checks[1].Status = "IN_PROGRESS"
			case "unknown mode":
				checks[1].Name = "grok-review-unknown"
			case "wrong app":
				checks[1].Suite.App.ID = 999
			case "missing app":
				checks[1].Suite.App.ID = 0
			case "missing requiredness":
				checks[1].Required = nil
			case "wrong head":
				checks[1].Suite.Commit.OID = mergeBase
			case "tool error":
				checks[1].Title = "Grok review error"
			case "unreadable app config":
				app = func() (int64, error) { return 0, fmt.Errorf("unreadable") }
			case "only review":
				checks = checks[1:]
				required = nil
			case "missing check id":
				checks[1].ID = 0
			}
			got, failures, err := mergeChecksPassed(checks, required, mergeHead, app)
			if got != want || (want && err != nil) || (want && len(failures) != wantFailures) {
				t.Fatalf("passed=%v failures=%v error=%v", got, failures, err)
			}
		})
	}
}

func mergePage(checks []mergeCheck, more bool, cursor string) []byte {
	p := mergePRPage{State: "OPEN", HeadSHA: mergeHead, BaseRefName: "main"}
	p.BaseRef.Target.OID = mergeBase
	// Keep the fixture in the actual GraphQL envelope, including connection pages.
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
		"state": p.State, "headRefOid": p.HeadSHA, "baseRefName": p.BaseRefName, "baseRef": p.BaseRef,
		"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"oid": mergeHead, "statusCheckRollup": map[string]any{"contexts": map[string]any{"nodes": checks, "pageInfo": map[string]any{"hasNextPage": more, "endCursor": cursor}}}}}}},
	}}}})
	return raw
}

func TestGHPRFactsSeparatesFailedAdvisoryReview(t *testing.T) {
	for _, scenario := range []string{"ok", "branch failure", "rules failure", "metadata failure", "graphql error", "missing policy", "missing rules", "missing required", "rule missing required", "second page failure", "stale second page", "two pages", "missing cursor", "private app", "unreadable publisher", "changed publisher"} {
		t.Run(scenario, func(t *testing.T) {
			pages := 0
			gh := func(_ context.Context, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				switch {
				case args[1] == "graphql":
					pages++
					if scenario == "metadata failure" {
						return nil, fmt.Errorf("offline")
					}
					if scenario == "graphql error" {
						return []byte(`{"data":{},"errors":[{"message":"unreadable"}]}`), nil
					}
					checks := []mergeCheck{checkFixture("CI Gate", "SUCCESS"), checkFixture("grok-review-shadow", "FAILURE")}
					if scenario == "private app" || scenario == "unreadable publisher" || scenario == "changed publisher" {
						checks[1].Suite.App.ID = 0
						checks[1].Suite.App.Slug = ""
					}
					more := strings.Contains(scenario, "page") || scenario == "missing cursor"
					cursor := "next"
					if scenario == "missing cursor" {
						cursor = ""
					}
					if pages == 2 {
						if scenario == "second page failure" {
							return nil, fmt.Errorf("page unavailable")
						}
						raw := mergePage(checks[1:], false, "")
						if scenario == "stale second page" {
							raw = []byte(strings.ReplaceAll(string(raw), mergeHead, mergeBase))
						}
						return raw, nil
					}
					if more {
						checks = checks[:1]
					}
					return mergePage(checks, more, cursor), nil
				case strings.Contains(joined, "/branches/main") && !strings.Contains(joined, "/rules/"):
					if scenario == "branch failure" {
						return nil, fmt.Errorf("forbidden")
					}
					if scenario == "missing policy" {
						return []byte(`{}`), nil
					}
					name := "CI Gate"
					if scenario == "missing required" {
						name = "unstarted"
					}
					return []byte(fmt.Sprintf(`{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":%q,"app_id":15368}]}}}`, name)), nil
				case strings.Contains(joined, "/rules/branches/main"):
					if scenario == "rules failure" {
						return nil, fmt.Errorf("forbidden")
					}
					if scenario == "missing rules" {
						return []byte(`null`), nil
					}
					if scenario == "rule missing required" {
						return []byte(`[[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"absent","integration_id":15368}]}}]]`), nil
					}
					return []byte(`[[]]`), nil
				case strings.Contains(joined, "/check-runs/12"):
					if scenario == "unreadable publisher" {
						return nil, fmt.Errorf("metadata unavailable")
					}
					head := mergeHead
					if scenario == "changed publisher" {
						head = mergeBase
					}
					return []byte(fmt.Sprintf(`{"id":12,"name":"grok-review-shadow","head_sha":%q,"status":"completed","conclusion":"failure","external_id":"local-grok-attempt:attempt:hash","app":{"id":123,"slug":"review-app"}}`, head)), nil

				default:
					t.Fatalf("unexpected gh request: %q", args)
					return nil, nil
				}
			}
			f, err := readMergePRFacts(context.Background(), "o/r", 7, gh, reviewApp)
			want := scenario == "ok" || scenario == "two pages" || scenario == "private app"
			if want {
				if err != nil || !f.ChecksPassed || len(f.AdvisoryFailures) != 1 || f.HeadSHA != mergeHead || f.BaseTipSHA != mergeBase {
					t.Fatalf("%+v %v", f, err)
				}
			} else if err == nil && f.ChecksPassed {
				t.Fatalf("unsafe release facts: %+v", f)
			}
		})
	}
}

func TestReviewerAppConfigDoesNotFallBackOnInvalidExplicitConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	t.Setenv("SQUAD_GROK_REVIEW_CONFIG", p)
	for _, raw := range []string{`{}`, `{"app_id":0}`, `broken`, `{"app_id":123,"app_private_key":"not-read"}`} {
		if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		id, err := reviewerAppID()
		if strings.Contains(raw, `123`) {
			if err != nil || id != 123 {
				t.Fatal(id, err)
			}
		} else if err == nil {
			t.Fatal("accepted missing identity")
		}
	}
	t.Setenv("SQUAD_GROK_REVIEW_CONFIG", "relative.json")
	if _, err := reviewerAppID(); err == nil {
		t.Fatal("accepted relative config")
	}
}

func TestMergeCheckPaginationOverHundredIncludesLastFailure(t *testing.T) {
	pages := 0
	gh := func(_ context.Context, args ...string) ([]byte, error) {
		if args[1] == "graphql" {
			pages++
			if pages == 1 {
				var checks []mergeCheck
				for i := 0; i < 100; i++ {
					c := checkFixture(fmt.Sprintf("check-%d", i), "SUCCESS")
					c.ID = int64(i + 1)
					checks = append(checks, c)
				}
				return mergePage(checks, true, "next"), nil
			}
			return mergePage([]mergeCheck{checkFixture("last-ci", "FAILURE")}, false, ""), nil
		}
		if strings.Contains(strings.Join(args, " "), "/rules/") {
			return []byte(`[[]]`), nil
		}
		return []byte(`{"protected":false}`), nil
	}
	f, err := readMergePRFacts(context.Background(), "o/r", 7, gh, reviewApp)
	if err != nil || f.ChecksPassed || pages != 2 {
		t.Fatal(f, err, pages)
	}
}
