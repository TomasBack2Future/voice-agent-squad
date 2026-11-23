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

// A commit's rollup keeps the runs of every check suite created for it. Only
// what a cancelled workflow run left behind is dropped, and only for a later
// suite of the same workflow, App and event: a cancelled job when that suite
// succeeded as a whole, any other verdict when the same job passed in it. This
// is a merge gate: every other combination must keep refusing.
func TestMergeChecksDropOnlyWhatACancelledRunLeftBehind(t *testing.T) {
	required := []requiredMergeCheck{{Context: "CI Gate", AppID: 15368}}
	// run builds a check run of workflow 7 started by pull_request. Suite 1 was
	// cancelled; every other suite succeeded.
	run := func(name, conclusion string, id, suite int64) mergeCheck {
		c := checkFixture(name, conclusion)
		c.ID, c.Suite.ID, c.Suite.Conclusion = id, suite, "SUCCESS"
		if suite == 1 {
			c.Suite.Conclusion = "CANCELLED"
		}
		c.Suite.WorkflowRun = &mergeWorkflowRun{Event: "pull_request"}
		c.Suite.WorkflowRun.Workflow.ID = 7
		return c
	}
	with := func(c mergeCheck, edit func(*mergeCheck)) mergeCheck { edit(&c); return c }
	suiteConcluded := func(conclusion string) func(*mergeCheck) {
		return func(c *mergeCheck) { c.Suite.Conclusion = conclusion }
	}
	workflowRun := func(event string, workflow int64) func(*mergeCheck) {
		return func(c *mergeCheck) {
			c.Suite.WorkflowRun = &mergeWorkflowRun{Event: event}
			c.Suite.WorkflowRun.Workflow.ID = workflow
		}
	}
	noWorkflowRun := func(c *mergeCheck) { c.Suite.WorkflowRun = nil }
	noSuiteID := func(c *mergeCheck) { c.Suite.ID = 0 }
	otherApp := func(c *mergeCheck) { c.Suite.App.ID, c.Suite.App.Slug = 999, "other-app" }
	unfinished := func(c *mergeCheck) { c.Status, c.Conclusion = "IN_PROGRESS", "" }
	// What a cancelled run leaves behind: a failed gate (it always runs), a job
	// cancelled before any verdict, or a job that failed before the cancellation.
	cancelledGate, cancelledGo, failedGo := run("CI Gate", "FAILURE", 21, 1), run("go", "CANCELLED", 22, 1), run("go", "FAILURE", 22, 1)
	// The run that took over.
	gate, goPass := run("CI Gate", "SUCCESS", 31, 2), run("go", "SUCCESS", 32, 2)
	// requiredGate satisfies the required check by itself, so that a refusal
	// below can only come from the run that must not be dropped.
	requiredGate := run("CI Gate", "SUCCESS", 20, 1)
	for name, tc := range map[string]struct {
		checks []mergeCheck
		want   bool
	}{
		// The reported case (Studio #2288): the later run skipped one cancelled
		// job and expanded the matrix another was cancelled before expanding.
		"cancelled run replaced by the run that took over": {[]mergeCheck{cancelledGate, cancelledGo,
			run("web-release-contract", "CANCELLED", 23, 1), run("unit (${{ matrix.shard }})", "CANCELLED", 24, 1),
			gate, goPass, run("web-release-contract", "SKIPPED", 33, 2), run("unit (1)", "SUCCESS", 34, 2)}, true},
		"job that failed before the cancellation, then passed": {[]mergeCheck{cancelledGate, failedGo, gate, goPass}, true},

		// Nothing is dropped outside a completed run of a cancelled suite.
		"failed suite followed by a passing one": {[]mergeCheck{
			with(cancelledGate, suiteConcluded("FAILURE")), with(failedGo, suiteConcluded("FAILURE")), gate, goPass}, false},
		"cancelled job of a suite that ran to a failure":    {[]mergeCheck{requiredGate, with(cancelledGo, suiteConcluded("FAILURE")), goPass}, false},
		"cancelled job of a suite with no known conclusion": {[]mergeCheck{requiredGate, with(cancelledGo, suiteConcluded("")), goPass}, false},
		"unfinished run of a cancelled suite":               {[]mergeCheck{requiredGate, with(cancelledGo, unfinished), goPass}, false},

		// A cancelled job needs a later suite of the same workflow, App and
		// event that succeeded as a whole.
		"cancelled job, no later suite":                  {[]mergeCheck{requiredGate, cancelledGo}, false},
		"cancelled job, later suite failed":              {[]mergeCheck{requiredGate, cancelledGo, with(goPass, suiteConcluded("FAILURE"))}, false},
		"cancelled job, later suite cancelled":           {[]mergeCheck{requiredGate, cancelledGo, with(goPass, suiteConcluded("CANCELLED"))}, false},
		"cancelled job, later suite unfinished":          {[]mergeCheck{requiredGate, cancelledGo, with(goPass, suiteConcluded(""))}, false},
		"cancelled job, only an older suite passed":      {[]mergeCheck{gate, goPass, with(run("go", "CANCELLED", 42, 3), suiteConcluded("CANCELLED"))}, false},
		"cancelled job, later suite of another workflow": {[]mergeCheck{requiredGate, cancelledGo, with(goPass, workflowRun("pull_request", 8))}, false},
		"cancelled job, later suite of another App":      {[]mergeCheck{requiredGate, cancelledGo, with(goPass, otherApp)}, false},
		"cancelled job, later suite started by a push":   {[]mergeCheck{requiredGate, cancelledGo, with(goPass, workflowRun("push", 7))}, false},
		"cancelled job, event unknown on it":             {[]mergeCheck{requiredGate, with(cancelledGo, workflowRun("", 7)), goPass}, false},
		"cancelled job, event unknown on both":           {[]mergeCheck{requiredGate, with(cancelledGo, workflowRun("", 7)), with(goPass, workflowRun("", 7))}, false},
		"cancelled job, workflow unknown on it":          {[]mergeCheck{requiredGate, with(cancelledGo, noWorkflowRun), goPass}, false},
		"cancelled job, workflow unknown on the later":   {[]mergeCheck{requiredGate, cancelledGo, with(goPass, noWorkflowRun)}, false},
		"cancelled job, workflow unknown on both":        {[]mergeCheck{requiredGate, with(cancelledGo, noWorkflowRun), with(goPass, noWorkflowRun)}, false},
		"cancelled job, workflow id unknown on both":     {[]mergeCheck{requiredGate, with(cancelledGo, workflowRun("pull_request", 0)), with(goPass, workflowRun("pull_request", 0))}, false},
		"cancelled job, suite id unknown on it":          {[]mergeCheck{requiredGate, with(cancelledGo, noSuiteID), goPass}, false},

		// Any other verdict needs the same job to pass, as a later run of such
		// a later suite. A rerun that skips the job hides nothing.
		"failed job, later run skipped (PR made a draft)":  {[]mergeCheck{requiredGate, failedGo, run("go", "SKIPPED", 32, 2)}, false},
		"failed job, later run neutral":                    {[]mergeCheck{requiredGate, failedGo, run("go", "NEUTRAL", 32, 2)}, false},
		"failed job, later run cancelled":                  {[]mergeCheck{requiredGate, failedGo, run("go", "CANCELLED", 32, 2)}, false},
		"failed job, later run failed":                     {[]mergeCheck{requiredGate, failedGo, run("go", "FAILURE", 32, 2)}, false},
		"failed job, later run unfinished":                 {[]mergeCheck{requiredGate, failedGo, with(goPass, unfinished)}, false},
		"failed job, no later run of it":                   {[]mergeCheck{requiredGate, failedGo, run("lint", "SUCCESS", 33, 2)}, false},
		"failed job, failure after the run that took over": {[]mergeCheck{requiredGate, failedGo, goPass, run("go", "FAILURE", 42, 3)}, false},
		"failed job, only an older suite passed":           {[]mergeCheck{gate, with(run("go", "FAILURE", 32, 3), suiteConcluded("CANCELLED")), run("go", "SUCCESS", 40, 2)}, false},
		"failed job, later suite but earlier run":          {[]mergeCheck{requiredGate, with(failedGo, func(c *mergeCheck) { c.ID = 50 }), goPass}, false},
		"failed job, passing run of its own suite":         {[]mergeCheck{requiredGate, failedGo, run("go", "SUCCESS", 23, 1)}, false},
		"failed job, same name in another workflow":        {[]mergeCheck{requiredGate, failedGo, with(goPass, workflowRun("pull_request", 8))}, false},
		"failed job, same name from another App":           {[]mergeCheck{requiredGate, failedGo, with(goPass, otherApp)}, false},
		"failed job, later run started by a push":          {[]mergeCheck{requiredGate, failedGo, with(goPass, workflowRun("push", 7))}, false},
		"failed job, event unknown on both":                {[]mergeCheck{requiredGate, with(failedGo, workflowRun("", 7)), with(goPass, workflowRun("", 7))}, false},
		"failed job, workflow unknown on both":             {[]mergeCheck{requiredGate, with(failedGo, noWorkflowRun), with(goPass, noWorkflowRun)}, false},
		"failed job, suite id unknown on it":               {[]mergeCheck{requiredGate, with(failedGo, noSuiteID), goPass}, false},
	} {
		t.Run(name, func(t *testing.T) {
			got, _, err := mergeChecksPassed(tc.checks, required, mergeHead, reviewApp)
			if got != tc.want || (tc.want && err != nil) {
				t.Fatalf("passed=%v want %v error=%v", got, tc.want, err)
			}
		})
	}
	// A dropped run is still validated: one bound to another head is an error,
	// not something a later pass makes disappear.
	stale := with(cancelledGo, func(c *mergeCheck) { c.Suite.Commit.OID = mergeBase })
	if got, _, err := mergeChecksPassed([]mergeCheck{gate, stale, goPass}, required, mergeHead, reviewApp); got || err == nil {
		t.Fatalf("a dropped run of another head was accepted: passed=%v error=%v", got, err)
	}
	// A blocking review is never dropped for a later sample, even one a
	// workflow produced in a later suite after its own was cancelled: it stays
	// a failure the release evidence must dispute.
	sample := func(name, conclusion string, id, suite int64) mergeCheck {
		c := run("grok-review-shadow", conclusion, id, suite)
		c.Name = name
		return c
	}
	blocking := sample("grok-review-shadow", "FAILURE", 22, 1)
	got, failures, err := mergeChecksPassed([]mergeCheck{gate, blocking, sample("grok-review-shadow", "SUCCESS", 32, 2)}, required, mergeHead, reviewApp)
	if err != nil || !got || len(failures) != 1 || failures[0].CheckRunID != blocking.ID {
		t.Fatalf("a later review sample hid a blocking one: passed=%v failures=%+v error=%v", got, failures, err)
	}
	if got, _, _ = mergeChecksPassed([]mergeCheck{gate, sample("grok-review", "FAILURE", 22, 1), sample("grok-review", "SUCCESS", 32, 2)}, required, mergeHead, reviewApp); got {
		t.Fatal("an enforced blocking review was resampled away")
	}
	if got, _, _ = mergeChecksPassed([]mergeCheck{gate, sample("grok-review", "CANCELLED", 22, 1), goPass}, required, mergeHead, reviewApp); got {
		t.Fatal("a cancelled enforced review was dropped for a later suite")
	}
}

// The rollup of Studio #2288's head on 2026-11-22 as GitHub returned it, with
// run and suite ids shortened: suite 145 was cancelled when suite 252 started.
// It was refused; it must pass, and only through the fields the query reads.
func TestMergeChecksAcceptTheReportedRollup(t *testing.T) {
	for _, field := range []string{"checkSuite{databaseId conclusion ", "workflowRun{event workflow{databaseId}}"} {
		if !strings.Contains(mergeChecksQuery, field) {
			t.Fatalf("the rollup query does not read %q", field)
		}
	}
	node := func(id, suite int64, suiteConclusion, name, conclusion string) string {
		return fmt.Sprintf(`{"__typename":"CheckRun","databaseId":%d,"name":%q,"status":"COMPLETED","conclusion":%q,"isRequired":false,`+
			`"checkSuite":{"databaseId":%d,"conclusion":%q,"commit":{"oid":%q},"app":{"databaseId":15368,"slug":"github-actions"},`+
			`"workflowRun":{"event":"pull_request","workflow":{"databaseId":7}}}}`, id, name, conclusion, suite, suiteConclusion, mergeHead)
	}
	var nodes []string
	for i, job := range []struct{ name, cancelled, later string }{
		{"ci-fast / policy and classify", "CANCELLED", "SUCCESS"},
		{"web-release-contract", "CANCELLED", "SKIPPED"},
		{"go", "CANCELLED", "SUCCESS"},
		{"logging-contract", "CANCELLED", "SUCCESS"},
		{"CI Gate", "FAILURE", "SUCCESS"},
		{"image-candidate", "CANCELLED", "SUCCESS"},
		{"frontend-unit (${{ matrix.shard }})", "CANCELLED", "SKIPPED"},
		{"frontend-dependencies", "CANCELLED", "SKIPPED"},
		{"repository-tooling", "CANCELLED", "SUCCESS"},
		{"frontend-image", "CANCELLED", "SKIPPED"},
		{"image", "CANCELLED", "SKIPPED"},
		{"frontend-static", "CANCELLED", "SKIPPED"},
		{"frontend-integration", "CANCELLED", "SKIPPED"},
		{"frontend-candidate", "CANCELLED", "SKIPPED"},
		{"Freeze changed staging components for terminal CI consumption", "CANCELLED", "SKIPPED"},
	} {
		nodes = append(nodes, node(int64(100+i), 145, "CANCELLED", job.name, job.cancelled), node(int64(200+i), 252, "SUCCESS", job.name, job.later))
	}
	var checks []mergeCheck
	if err := json.Unmarshal([]byte("["+strings.Join(nodes, ",")+"]"), &checks); err != nil {
		t.Fatal(err)
	}
	required := []requiredMergeCheck{{Context: "CI Gate", AppID: 15368}}
	if got, _, err := mergeChecksPassed(checks, required, mergeHead, reviewApp); err != nil || !got {
		t.Fatalf("the reported rollup was refused: passed=%v error=%v", got, err)
	}
	// Without the run that took over, the same cancelled suite refuses.
	var alone []mergeCheck
	for _, c := range checks {
		if c.Suite.ID == 145 {
			alone = append(alone, c)
		}
	}
	if got, _, _ := mergeChecksPassed(alone, required, mergeHead, reviewApp); got {
		t.Fatal("a cancelled suite passed with no run to take over")
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
