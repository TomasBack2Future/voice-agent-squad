package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type githubRun struct {
	ID         int64  `json:"id"`
	Attempt    int64  `json:"run_attempt"`
	SHA        string `json:"head_sha"`
	Path       string `json:"path"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// ghOutput runs the service's own gh with whatever HOME/PATH/GH_CONFIG_DIR the
// process actually has. Failures are reduced to a sanitized Class.
func ghOutput(ctx context.Context, path string, notFound Class) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", "api", path)
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		if strings.Contains(strings.ToLower(stderr.String()), "http 404") {
			// Reachable and authenticated, but the object is absent or invisible.
			return nil, &Error{notFound}
		}
		return nil, classifyGH(ctx, err, stderr.Bytes())
	}
	return raw, nil
}

func readGitHubRun(ctx context.Context, path string) (githubRun, error) {
	var run githubRun
	raw, err := ghOutput(ctx, path, ClassRunMismatch)
	if err != nil {
		return run, err
	}
	if err := json.Unmarshal(raw, &run); err != nil {
		return run, &Error{ClassGitHubUnavailable}
	}
	return run, nil
}
func (r githubRun) matches(b Binding, id int64) bool {
	return r.ID == id && r.SHA == b.WorkflowSHA && strings.Split(r.Path, "@")[0] == b.WorkflowPath && r.Event == "workflow_dispatch" && r.Repository.FullName == b.Repository
}

// VerifyGitHubRun uses the authority host's gh credentials, never runner-supplied
// run JSON. Admission requires latest attempt 1. Reconciliation verifies both the
// pinned attempt and latest terminal state: an accidental, rejected UI rerun must
// not strand a pin forever, but an active newer attempt must prevent unpinning.
func VerifyGitHubRun(ctx context.Context, b Binding, runID, attempt int64, terminal bool) error {
	if !repository.MatchString(b.Repository) || runID < 1 || attempt != 1 {
		return &Error{ClassRunMismatch}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	path := fmt.Sprintf("repos/%s/actions/runs/%d", b.Repository, runID)
	run, err := readGitHubRun(ctx, path)
	if err != nil {
		return err
	}
	if !run.matches(b, runID) {
		return &Error{ClassRunMismatch}
	}
	if !terminal {
		if run.Attempt != attempt || run.Status != "in_progress" {
			return &Error{ClassRunMismatch}
		}
		return nil
	}
	if run.Status != "completed" || run.Attempt < attempt {
		return &Error{ClassRunMismatch}
	}
	if run.Attempt != attempt {
		run, err = readGitHubRun(ctx, fmt.Sprintf("%s/attempts/%d", path, attempt))
		if err != nil {
			return err
		}
	}
	if !run.matches(b, runID) || run.Attempt != attempt || run.Status != "completed" {
		return &Error{ClassRunMismatch}
	}
	return nil
}
