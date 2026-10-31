package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/terminalevents"
)

// ghPRFacts reads the exact pull request state through the user's gh login. It
// is used only when a Dispatcher issues a release, never at the Worker's tool boundary.
func ghPRFacts(ctx context.Context, repo string, number int64) (terminalevents.PRFacts, error) {
	var f terminalevents.PRFacts
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "pr", "view", fmt.Sprint(number), "-R", repo, "--json", "state,headRefOid,baseRefName,statusCheckRollup").Output()
	if err != nil {
		return f, err
	}
	var v struct {
		State        string `json:"state"`
		HeadRefOid   string `json:"headRefOid"`
		BaseRefName  string `json:"baseRefName"`
		StatusRollup []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			State      string `json:"state"`
		} `json:"statusCheckRollup"`
	}
	if err = json.Unmarshal(out, &v); err != nil {
		return f, err
	}
	tip, err := exec.CommandContext(ctx, "gh", "api", "repos/"+repo+"/git/ref/heads/"+v.BaseRefName, "--jq", ".object.sha").Output()
	if err != nil {
		return f, err
	}
	f.State, f.HeadSHA, f.BaseTipSHA = v.State, v.HeadRefOid, strings.TrimSpace(string(tip))
	checks := make([]checkResult, len(v.StatusRollup))
	for i, c := range v.StatusRollup {
		checks[i] = checkResult(c)
	}
	f.ChecksPassed = checksPassed(checks)
	return f, nil
}

// checkResult is one CheckRun (status/conclusion) or StatusContext (state).
type checkResult struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

// checksPassed is true only when at least one check exists and every one has
// completed successfully (skipped/neutral allowed). Pending or failing blocks.
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
