package execution

import (
	"context"
	"encoding/json"
	"errors"
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

func readGitHubRun(ctx context.Context, path string) (githubRun, error) {
	var run githubRun
	raw, err := exec.CommandContext(ctx, "gh", "api", path).Output()
	if err != nil {
		return run, errors.New("cannot verify GitHub run")
	}
	err = json.Unmarshal(raw, &run)
	return run, err
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
		return errors.New("invalid GitHub run identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	path := fmt.Sprintf("repos/%s/actions/runs/%d", b.Repository, runID)
	run, err := readGitHubRun(ctx, path)
	if err != nil {
		return err
	}
	if !run.matches(b, runID) {
		return errors.New("GitHub run identity mismatch")
	}
	if !terminal {
		if run.Attempt != attempt || run.Status != "in_progress" {
			return errors.New("GitHub run/attempt is not active")
		}
		return nil
	}
	if run.Status != "completed" || run.Attempt < attempt {
		return errors.New("latest GitHub run is not terminal")
	}
	if run.Attempt != attempt {
		run, err = readGitHubRun(ctx, fmt.Sprintf("%s/attempts/%d", path, attempt))
		if err != nil {
			return err
		}
	}
	if !run.matches(b, runID) || run.Attempt != attempt || run.Status != "completed" {
		return errors.New("pinned GitHub attempt is not terminal")
	}
	return nil
}
