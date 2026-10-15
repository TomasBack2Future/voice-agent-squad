package grokreview

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Only the explicit GitHub diff-size response enables the Git fallback. Other
// API, authentication and process failures retain their ordinary failure path.
type diffTooLarge struct{}

func (*diffTooLarge) Error() string { return "GitHub complete diff exceeds API line limit" }

func oversizedDiffResponse(raw []byte) bool {
	var response struct {
		Status string `json:"status"`
		Errors []struct {
			Field string `json:"field"`
			Code  string `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Status != "406" {
		return false
	}
	for _, failure := range response.Errors {
		if failure.Field == "diff" && failure.Code == "too_large" {
			return true
		}
	}
	return false
}

func isOversizedDiff(err error) bool {
	var size *diffTooLarge
	return errors.As(err, &size)
}

// exactGitDiff owns a disposable bare object store. Authentication is supplied
// only to its fetch process, never persisted to config, URLs or command lines.
// No checkout, hook, filter, textconv or external diff is executed.
func (g *GitHubCLI) exactGitDiff(ctx context.Context, snapshot PullRequestSnapshot, token string) ([]byte, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(snapshot.Repository) ||
		!regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(snapshot.BaseSHA) ||
		!regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(snapshot.HeadSHA) || token == "" {
		return nil, fmt.Errorf("exact GitHub snapshot identity required")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git executable unavailable for complete snapshot")
	}
	git, err = filepath.Abs(git)
	if err != nil {
		return nil, fmt.Errorf("absolute Git executable required")
	}
	root, err := os.MkdirTemp("", "review-github-objects-*")
	if err != nil {
		return nil, fmt.Errorf("create owned Git object store")
	}
	defer func() { _ = os.RemoveAll(root) }()
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	baseEnv := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_GRAFT_FILE=" + os.DevNull}
	run := func(auth bool, args ...string) ([]byte, error) {
		fixed := []string{"--no-replace-objects", "-c", "core.hooksPath=" + os.DevNull,
			"-c", "credential.helper=", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always",
			"-c", "http.followRedirects=false", "-c", "core.abbrev=40", "-c", "diff.algorithm=myers", "-C", root}
		command := exec.CommandContext(ctx, git, append(fixed, args...)...)
		ownGitProcessGroup(command)
		command.Env = append([]string(nil), baseEnv...)
		if auth {
			header := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
			command.Env = append(command.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic "+header)
		}
		stdout := &cappedBuffer{limit: g.maxOutputBytes}
		stderr := &cappedBuffer{limit: 4096}
		command.Stdout, command.Stderr = stdout, stderr
		if err := command.Run(); err != nil || stdout.overflow {
			return nil, fmt.Errorf("complete Git snapshot failed or exceeded output bound; output withheld")
		}
		return append([]byte(nil), stdout.Bytes()...), nil
	}
	if _, err = run(false, "init", "--bare", "--template="); err != nil {
		return nil, err
	}
	if _, err = run(true, "fetch", "--no-tags", "--no-recurse-submodules", "https://github.com/"+snapshot.Repository+".git", snapshot.BaseSHA, snapshot.HeadSHA); err != nil {
		return nil, err
	}
	for _, sha := range []string{snapshot.BaseSHA, snapshot.HeadSHA} {
		actual, err := run(false, "rev-parse", "--verify", sha+"^{commit}")
		if err != nil || strings.TrimSpace(string(actual)) != sha {
			return nil, fmt.Errorf("exact Git commit unavailable")
		}
	}
	shallow, err := run(false, "rev-parse", "--is-shallow-repository")
	if err != nil || strings.TrimSpace(string(shallow)) != "false" {
		return nil, fmt.Errorf("complete Git history required")
	}
	ancestor, err := run(false, "merge-base", "--all", snapshot.BaseSHA, snapshot.HeadSHA)
	mergeBase := strings.TrimSpace(string(ancestor))
	if err != nil || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(mergeBase) {
		return nil, fmt.Errorf("one complete merge-base required")
	}
	diff, err := run(false, "diff", "--no-ext-diff", "--no-textconv", "--binary", "--full-index",
		"--find-renames=50%", "--ignore-submodules=none", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", mergeBase, snapshot.HeadSHA, "--")
	if err != nil || len(bytes.TrimSpace(diff)) == 0 {
		return nil, fmt.Errorf("complete nonempty Git diff required")
	}
	return diff, nil
}
