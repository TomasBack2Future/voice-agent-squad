package grokreview

// LocalGitGateway freezes a worktree for review when PRs are human-operated.
// It never contacts a hosting API, pushes a branch or publishes a remote result.
import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type LocalGitGateway struct {
	worktree        string
	host            string
	cloneLayout     string
	baseSHA         string
	descriptionFile string
	maxBytes        int
}

func NewLocalGitGateway(worktree, host, cloneLayout, baseSHA, descriptionFile string, maxBytes int) (*LocalGitGateway, error) {
	if !filepath.IsAbs(worktree) || !filepath.IsAbs(descriptionFile) || maxBytes <= 0 {
		return nil, fmt.Errorf("local review requires absolute worktree/contract paths and a positive input limit")
	}
	if !regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*$`).MatchString(host) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(baseSHA) {
		return nil, fmt.Errorf("local review requires an explicit host and 40-character base SHA")
	}
	if (cloneLayout != "plain" && cloneLayout != "bitbucket-server") || (host == "github.com" && cloneLayout != "plain") {
		return nil, fmt.Errorf("unsupported host/clone layout")
	}
	return &LocalGitGateway{worktree: worktree, host: host, cloneLayout: cloneLayout, baseSHA: baseSHA, descriptionFile: descriptionFile, maxBytes: maxBytes}, nil
}

func (g *LocalGitGateway) git(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", g.worktree}, args...)...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("open git output")
	}
	if err = cmd.Start(); err != nil {
		return "", fmt.Errorf("start git inspection")
	}
	data, readErr := io.ReadAll(io.LimitReader(pipe, int64(g.maxBytes)+1))
	if readErr != nil || len(data) > g.maxBytes {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", fmt.Errorf("complete git input exceeds review limit")
	}
	if err = cmd.Wait(); err != nil {
		return "", fmt.Errorf("git inspection failed; verify exact local commits")
	}
	return string(data), nil
}

func (g *LocalGitGateway) matchesRemote(remote, repository string) bool {
	var host, path string
	remote = strings.TrimSpace(remote)
	if strings.HasPrefix(remote, "git@") && !strings.Contains(remote, "://") {
		pieces := strings.SplitN(strings.TrimPrefix(remote, "git@"), ":", 2)
		if len(pieces) != 2 {
			return false
		}
		host, path = pieces[0], pieces[1]
	} else {
		u, err := url.Parse(remote)
		if err != nil || (u.Scheme != "ssh" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
			return false
		}
		if u.User != nil {
			if _, password := u.User.Password(); password || u.User.Username() != "git" {
				return false
			}
		}
		host, path = u.Hostname(), strings.TrimPrefix(u.Path, "/")
		if u.Scheme == "https" && g.cloneLayout == "bitbucket-server" {
			if !strings.HasPrefix(path, "scm/") {
				return false
			}
			path = strings.TrimPrefix(path, "scm/")
		}
	}
	return host == g.host && strings.TrimSuffix(path, ".git") == repository
}

func (g *LocalGitGateway) FetchPullRequest(ctx context.Context, repository string, number int, _ string) (PullRequestSnapshot, error) {
	if number != 0 || !regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_-][A-Za-z0-9_.-]*$`).MatchString(repository) {
		return PullRequestSnapshot{}, fmt.Errorf("local review requires namespace/name and no PR number")
	}
	root, err := g.git(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return PullRequestSnapshot{}, err
	}
	expected, err := filepath.EvalSymlinks(g.worktree)
	if err != nil || filepath.Clean(strings.TrimSpace(root)) != expected {
		return PullRequestSnapshot{}, fmt.Errorf("local review path must be the git root")
	}
	remote, err := g.git(ctx, "remote", "get-url", "origin")
	if err != nil || !g.matchesRemote(remote, repository) {
		return PullRequestSnapshot{}, fmt.Errorf("local origin does not match the assigned host/repository")
	}
	dirty, err := g.git(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil || dirty != "" {
		return PullRequestSnapshot{}, fmt.Errorf("local review worktree must be clean")
	}
	head, err := g.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return PullRequestSnapshot{}, err
	}
	head = strings.TrimSpace(head)
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(head) {
		return PullRequestSnapshot{}, fmt.Errorf("invalid local head")
	}
	diff, err := g.git(ctx, "diff", "--no-ext-diff", "--no-textconv", "--binary", "--ignore-submodules=none", "--src-prefix=a/", "--dst-prefix=b/", g.baseSHA+"..."+head, "--")
	if err != nil {
		return PullRequestSnapshot{}, err
	}
	if diff == "" {
		return PullRequestSnapshot{}, fmt.Errorf("empty local diff")
	}
	f, err := os.Open(g.descriptionFile)
	if err != nil {
		return PullRequestSnapshot{}, fmt.Errorf("local contract file unavailable")
	}
	defer f.Close()
	description, err := io.ReadAll(io.LimitReader(f, int64(g.maxBytes)+1))
	if err != nil || len(description) > g.maxBytes || strings.TrimSpace(string(description)) == "" {
		return PullRequestSnapshot{}, fmt.Errorf("local contract is empty, unavailable or exceeds limit")
	}
	return PullRequestSnapshot{Repository: repository, Number: 0, BaseRef: g.baseSHA, BaseSHA: g.baseSHA, HeadSHA: head,
		Title: "Local worktree review (human PR handoff)", Description: string(description), Diff: diff}, nil
}

func (g *LocalGitGateway) FetchPullRequestIdentity(ctx context.Context, repository string, number int, token string) (PullRequestSnapshot, error) {
	return g.FetchPullRequest(ctx, repository, number, token)
}

func (g *LocalGitGateway) PublishReview(ctx context.Context, _ string, checkName string, snapshot PullRequestSnapshot, _ FindingsResult, _ CLIAudit) (Publication, error) {
	if checkName != "local-review" {
		return Publication{}, fmt.Errorf("local review cannot publish a hosting check")
	}
	current, err := g.FetchPullRequest(ctx, snapshot.Repository, 0, "")
	if err != nil {
		return Publication{}, err
	}
	if !samePullRequestTuple(snapshot, current) || snapshot.Description != current.Description || snapshot.Diff != current.Diff {
		return Publication{}, fmt.Errorf("local review input changed; result is not valid")
	}
	return Publication{}, nil // Evidence is returned to the caller and local status writer only.
}

func validateLocalSnapshot(snapshot PullRequestSnapshot) error {
	if err := validateRepository(snapshot.Repository); err != nil {
		return err
	}
	if snapshot.Number != 0 || snapshot.BaseRef == "" || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(snapshot.BaseSHA) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(snapshot.HeadSHA) || snapshot.Diff == "" || snapshot.Description == "" {
		return fmt.Errorf("local review snapshot is incomplete or contains a PR number")
	}
	return nil
}
