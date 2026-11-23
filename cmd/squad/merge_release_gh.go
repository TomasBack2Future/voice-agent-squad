package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/terminalevents"
)

const mergeChecksQuery = `query($owner:String!,$name:String!,$number:Int!,$cursor:String){repository(owner:$owner,name:$name){pullRequest(number:$number){state headRefOid baseRefName baseRef{target{oid}} commits(last:1){nodes{commit{oid statusCheckRollup{contexts(first:100,after:$cursor){nodes{__typename ... on CheckRun{databaseId name status conclusion externalId title isRequired(pullRequestNumber:$number) checkSuite{commit{oid} app{databaseId slug}}} ... on StatusContext{context state isRequired(pullRequestNumber:$number)}} pageInfo{hasNextPage endCursor}}}}}}}}}`

type mergeCheck struct {
	Type       string `json:"__typename"`
	ID         int64  `json:"databaseId"`
	Name       string `json:"name"`
	Context    string `json:"context"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
	Required   *bool  `json:"isRequired"`
	ExternalID string `json:"externalId"`
	Title      string `json:"title"`
	Suite      struct {
		Commit struct {
			OID string `json:"oid"`
		} `json:"commit"`
		App struct {
			ID   int64  `json:"databaseId"`
			Slug string `json:"slug"`
		} `json:"app"`
	} `json:"checkSuite"`
}

type requiredMergeCheck struct {
	Context string `json:"context"`
	AppID   int64  `json:"app_id"`
}
type mergeCheckPolicy struct {
	Contexts []string             `json:"contexts"`
	Checks   []requiredMergeCheck `json:"checks"`
}
type mergePRPage struct {
	State       string `json:"state"`
	HeadSHA     string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
	BaseRef     struct {
		Target struct {
			OID string `json:"oid"`
		} `json:"target"`
	} `json:"baseRef"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				OID    string `json:"oid"`
				Rollup *struct {
					Contexts struct {
						Nodes    []mergeCheck `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

type mergeGHReader func(context.Context, ...string) ([]byte, error)

// ghPRFacts is used at release issuance, never at the Worker's tool boundary.
func ghPRFacts(ctx context.Context, repo string, number int64) (terminalevents.PRFacts, error) {
	return readMergePRFacts(ctx, repo, number, func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "gh", args...).Output()
	}, reviewerAppID)
}

func readMergePRFacts(ctx context.Context, repo string, number int64, gh mergeGHReader, appID func() (int64, error)) (terminalevents.PRFacts, error) {
	var f terminalevents.PRFacts
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || number < 1 {
		return f, fmt.Errorf("invalid pull request")
	}
	var checks []mergeCheck
	var pr mergePRPage
	cursor := ""
	for {
		args := []string{"api", "graphql", "-f", "query=" + mergeChecksQuery, "-f", "owner=" + parts[0], "-f", "name=" + parts[1], "-F", fmt.Sprintf("number=%d", number)}
		if cursor != "" {
			args = append(args, "-f", "cursor="+cursor)
		}
		raw, err := gh(ctx, args...)
		if err != nil {
			return f, err
		}
		var page struct {
			Data struct {
				Repository struct {
					PR *mergePRPage `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
			Errors []json.RawMessage `json:"errors"`
		}
		if err = json.Unmarshal(raw, &page); err != nil {
			return f, err
		}
		if len(page.Errors) > 0 || page.Data.Repository.PR == nil {
			return f, fmt.Errorf("pull request metadata unavailable")
		}
		p := *page.Data.Repository.PR
		if p.HeadSHA == "" || p.BaseRef.Target.OID == "" || p.BaseRefName == "" || len(p.Commits.Nodes) != 1 || p.Commits.Nodes[0].Commit.OID != p.HeadSHA {
			return f, fmt.Errorf("incomplete pull request identity")
		}
		if cursor != "" && (p.HeadSHA != pr.HeadSHA || p.BaseRef.Target.OID != pr.BaseRef.Target.OID || p.BaseRefName != pr.BaseRefName || p.State != pr.State) {
			return f, fmt.Errorf("pull request changed during check read")
		}
		pr = p
		rollup := p.Commits.Nodes[0].Commit.Rollup
		if rollup == nil {
			break
		}
		checks = append(checks, rollup.Contexts.Nodes...)
		if len(checks) > 1000 {
			return f, fmt.Errorf("check read exceeds bound")
		}
		info := rollup.Contexts.PageInfo
		if !info.HasNextPage {
			break
		}
		if info.EndCursor == "" || info.EndCursor == cursor {
			return f, fmt.Errorf("incomplete check pagination")
		}
		cursor = info.EndCursor
	}
	// Read both policy surfaces even when the observed checks are all green:
	// a required check that never started is absent from the rollup.
	branch := url.PathEscape(pr.BaseRefName)
	raw, err := gh(ctx, "api", "repos/"+repo+"/branches/"+branch)
	if err != nil {
		return f, err
	}
	var protection struct {
		Protected  *bool `json:"protected"`
		Protection *struct {
			Required *mergeCheckPolicy `json:"required_status_checks"`
		} `json:"protection"`
	}
	if err = json.Unmarshal(raw, &protection); err != nil {
		return f, err
	}
	if protection.Protected == nil || (*protection.Protected && protection.Protection == nil) {
		return f, fmt.Errorf("branch policy unavailable")
	}
	var required []requiredMergeCheck
	if protection.Protection != nil && protection.Protection.Required != nil {
		p := protection.Protection.Required
		required = append(required, p.Checks...)
		for _, c := range p.Contexts {
			required = append(required, requiredMergeCheck{Context: c})
		}
	}
	raw, err = gh(ctx, "api", "--paginate", "--slurp", "repos/"+repo+"/rules/branches/"+branch)
	if err != nil {
		return f, err
	}
	var rules [][]struct {
		Type       string `json:"type"`
		Parameters struct {
			Checks []struct {
				Context       string `json:"context"`
				IntegrationID int64  `json:"integration_id"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	if err = json.Unmarshal(raw, &rules); err != nil {
		return f, err
	}
	if rules == nil {
		return f, fmt.Errorf("rules policy unavailable")
	}
	for _, page := range rules {
		for _, r := range page {
			if r.Type == "required_status_checks" {
				if len(r.Parameters.Checks) == 0 {
					return f, fmt.Errorf("required check policy is incomplete")
				}
				for _, c := range r.Parameters.Checks {
					required = append(required, requiredMergeCheck{c.Context, c.IntegrationID})
				}
			}
		}
	}
	// GitHub can redact a private App from GraphQL while the Check Runs REST
	// endpoint still exposes its public publisher identity. Qualify the same run.
	for i := range checks {
		c := &checks[i]
		if c.Type != "CheckRun" || c.Suite.App.ID != 0 {
			continue
		}
		raw, err = gh(ctx, "api", fmt.Sprintf("repos/%s/check-runs/%d", repo, c.ID))
		if err != nil {
			return f, err
		}
		var run struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			HeadSHA    string `json:"head_sha"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			ExternalID string `json:"external_id"`
			App        struct {
				ID   int64  `json:"id"`
				Slug string `json:"slug"`
			} `json:"app"`
		}
		if err = json.Unmarshal(raw, &run); err != nil {
			return f, err
		}
		if run.ID != c.ID || run.Name != c.Name || run.HeadSHA != pr.HeadSHA || !strings.EqualFold(run.Status, c.Status) || !strings.EqualFold(run.Conclusion, c.Conclusion) || run.ExternalID != c.ExternalID {
			return f, fmt.Errorf("check changed during publisher read")
		}
		c.Suite.App.ID, c.Suite.App.Slug = run.App.ID, run.App.Slug
	}
	f.State, f.HeadSHA, f.BaseTipSHA = pr.State, pr.HeadSHA, pr.BaseRef.Target.OID
	f.ChecksPassed, f.AdvisoryFailures, err = mergeChecksPassed(checks, required, f.HeadSHA, appID)
	return f, err
}

func reviewerAppID() (int64, error) {
	path := strings.TrimSpace(os.Getenv("SQUAD_GROK_REVIEW_CONFIG"))
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return 0, err
		}
		path = filepath.Join(dir, "squad", "grok-review.json")
	}
	if !filepath.IsAbs(path) {
		return 0, fmt.Errorf("review App config path must be absolute")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("review App config unavailable")
	}
	var c struct {
		AppID int64 `json:"app_id"`
	}
	if json.Unmarshal(raw, &c) != nil || c.AppID < 1 {
		return 0, fmt.Errorf("review App identity unavailable")
	}
	return c.AppID, nil
}

func mergeChecksPassed(checks []mergeCheck, required []requiredMergeCheck, head string, appID func() (int64, error)) (bool, []terminalevents.AdvisoryReview, error) {
	var failures []terminalevents.AdvisoryReview
	var ordinary []checkResult
	trustedApp := int64(0)
	for _, c := range checks {
		if c.Required == nil {
			return false, nil, fmt.Errorf("check requiredness unavailable")
		}
		if c.Type == "CheckRun" {
			if c.ID < 1 || c.Name == "" || c.Suite.Commit.OID != head || c.Suite.App.ID < 1 || c.Suite.App.Slug == "" {
				return false, nil, fmt.Errorf("check identity unavailable")
			}
		} else if c.Type != "StatusContext" || c.Context == "" {
			return false, nil, fmt.Errorf("unknown check metadata")
		}
	}
	checks = currentCheckRuns(checks)
	for _, r := range required {
		found := false
		if r.Context == "" {
			return false, nil, fmt.Errorf("required check name unavailable")
		}
		for _, c := range checks {
			if matchesRequired(c, r) && checkPassed(c) {
				found = true
			}
		}
		if !found {
			return false, nil, nil
		}
	}
	for _, c := range checks {
		if strings.HasPrefix(c.Name, "grok-review") {
			if c.Name != "grok-review" && c.Name != "grok-review-shadow" {
				return false, nil, fmt.Errorf("unknown review mode")
			}
			if trustedApp == 0 {
				var err error
				trustedApp, err = appID()
				if err != nil {
					return false, nil, err
				}
			}
			if c.Suite.App.ID != trustedApp {
				return false, nil, fmt.Errorf("review App mismatch")
			}
			enforced := *c.Required || c.Name == "grok-review"
			for _, r := range required {
				if matchesRequired(c, r) {
					enforced = true
				}
			}
			if !enforced && c.Status == "COMPLETED" {
				if c.Conclusion == "SUCCESS" {
					continue
				}
				if c.Conclusion == "FAILURE" && c.Title == "Grok review blocking" && strings.HasPrefix(c.ExternalID, "local-grok-attempt:") {
					failures = append(failures, terminalevents.AdvisoryReview{CheckRunID: c.ID, AppID: c.Suite.App.ID, AppSlug: c.Suite.App.Slug, ExternalID: c.ExternalID, Mode: "shadow"})
					continue
				}
			}
		}
		ordinary = append(ordinary, checkResult{c.Status, c.Conclusion, c.State})
	}
	return checksPassed(ordinary), failures, nil
}

// currentCheckRuns keeps, for each check name of one App on this head, only its
// newest run. A commit's rollup lists every run ever created for it, so a run
// cancelled because a rerun replaced it, or a flaky failure followed by a
// passing rerun, would otherwise refuse the release for the life of that head.
// The newest run decides, as the code host's own branch protection does: a
// later failure also supersedes an earlier success. A run from another App
// never supersedes, and review runs are never superseded: a blocking review is
// disputed in the release evidence, not resampled away.
func currentCheckRuns(checks []mergeCheck) []mergeCheck {
	type key struct {
		app  int64
		name string
	}
	newest := map[key]int64{}
	superseding := func(c mergeCheck) bool {
		return c.Type == "CheckRun" && !strings.HasPrefix(c.Name, "grok-review")
	}
	for _, c := range checks {
		if superseding(c) {
			if k := (key{c.Suite.App.ID, c.Name}); c.ID > newest[k] {
				newest[k] = c.ID
			}
		}
	}
	out := make([]mergeCheck, 0, len(checks))
	for _, c := range checks {
		if superseding(c) && c.ID != newest[key{c.Suite.App.ID, c.Name}] {
			continue
		}
		out = append(out, c)
	}
	return out
}

func matchesRequired(c mergeCheck, r requiredMergeCheck) bool {
	return (c.Name == r.Context || c.Context == r.Context) && (r.AppID <= 0 || (c.Type == "CheckRun" && c.Suite.App.ID == r.AppID))
}
func checkPassed(c mergeCheck) bool {
	return checksPassed([]checkResult{{c.Status, c.Conclusion, c.State}})
}

type checkResult struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

func checksPassed(checks []checkResult) bool {
	if len(checks) == 0 {
		return false
	}
	for _, c := range checks {
		switch {
		case c.State == "SUCCESS":
		case c.Status == "COMPLETED" && (c.Conclusion == "SUCCESS" || c.Conclusion == "SKIPPED" || c.Conclusion == "NEUTRAL"):
		default:
			return false
		}
	}
	return true
}
