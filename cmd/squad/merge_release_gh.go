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
	f.ChecksPassed = len(v.StatusRollup) > 0
	for _, c := range v.StatusRollup {
		ok := c.Conclusion == "SUCCESS" || c.Conclusion == "SKIPPED" || c.Conclusion == "NEUTRAL" || c.State == "SUCCESS"
		if !ok {
			f.ChecksPassed = false
		}
	}
	return f, nil
}
