package grokreview

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func localTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func localFixture(t *testing.T) (*LocalGitGateway, string) {
	t.Helper()
	dir := t.TempDir()
	localTestGit(t, dir, "init", "-b", "work")
	localTestGit(t, dir, "config", "user.name", "Test")
	localTestGit(t, dir, "config", "user.email", "test@example.invalid")
	write := func(text string) {
		if err := os.WriteFile(filepath.Join(dir, "code.txt"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("old\n")
	localTestGit(t, dir, "add", ".")
	localTestGit(t, dir, "commit", "-m", "base")
	base := localTestGit(t, dir, "rev-parse", "HEAD")
	write("new\n")
	localTestGit(t, dir, "commit", "-am", "head")
	localTestGit(t, dir, "remote", "add", "origin", "ssh://git@git.example.test/ipt/interceptor.git")
	contract := filepath.Join(t.TempDir(), "contract.md")
	if err := os.WriteFile(contract, []byte("Issue contract and acceptance"), 0600); err != nil {
		t.Fatal(err)
	}
	g, err := NewLocalGitGateway(dir, "git.example.test", "bitbucket-server", base, contract, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	return g, contract
}
func TestLocalGitReviewHasCompleteInputAndNoRemotePublication(t *testing.T) {
	g, _ := localFixture(t)
	model := &fakeModelReviewer{result: approvedFindings()}
	service, err := NewLocalReviewService(model, g, "core", "policy")
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.ReviewWorktree(context.Background(), "ipt/interceptor")
	if err != nil {
		t.Fatal(err)
	}
	if report.Snapshot.Number != 0 || report.Publication != (Publication{}) || model.calls != 1 {
		t.Fatal("unexpected PR/publication or multiple model calls")
	}
	var bundle FrozenReviewBundle
	if err = json.Unmarshal(model.bundle, &bundle); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bundle.Diff, "-old") || !strings.Contains(bundle.Diff, "+new") || bundle.Description == "" || bundle.HeadSHA == "" {
		t.Fatal("incomplete frozen input")
	}
	if _, err = g.PublishReview(context.Background(), "", "grok-review", report.Snapshot, report.Result, CLIAudit{}); err == nil {
		t.Fatal("local review published a remote check")
	}
}
func TestLocalGitReviewRejectsChangedContractDirtyHeadAndOversize(t *testing.T) {
	g, contract := localFixture(t)
	snapshot, err := g.FetchPullRequest(context.Background(), "ipt/interceptor", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(contract, []byte("new requirement"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = g.PublishReview(context.Background(), "", "local-review", snapshot, approvedFindings(), CLIAudit{}); err == nil {
		t.Fatal("accepted changed contract")
	}
	g.maxBytes = 10
	if _, err = g.git(context.Background(), "diff", g.baseSHA+"...HEAD"); err == nil {
		t.Fatal("accepted truncated diff")
	}
	g.maxBytes = 1024 * 1024
	if err = os.WriteFile(filepath.Join(g.worktree, "untracked"), []byte("omission"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = g.FetchPullRequest(context.Background(), "ipt/interceptor", 0, ""); err == nil {
		t.Fatal("accepted dirty tree")
	}
}
func TestLocalGitReviewRejectsHostSpoofAndPRNumber(t *testing.T) {
	g, _ := localFixture(t)
	for _, remote := range []string{"git@github.com:ipt/interceptor.git", "ssh://git@git.example.test.evil/ipt/interceptor.git", "https://token:secret@git.example.test/scm/ipt/interceptor.git", "/tmp/ipt/interceptor"} {
		if g.matchesRemote(remote, "ipt/interceptor") {
			t.Fatalf("accepted %s", remote)
		}
	}
	for _, remote := range []string{"ssh://git@git.example.test/ipt/interceptor.git", "git@git.example.test:ipt/interceptor.git", "https://git.example.test/scm/ipt/interceptor.git"} {
		if !g.matchesRemote(remote, "ipt/interceptor") {
			t.Fatalf("rejected %s", remote)
		}
	}
	if _, err := g.FetchPullRequest(context.Background(), "ipt/interceptor", 9, ""); err == nil {
		t.Fatal("accepted synthetic PR")
	}
	localTestGit(t, g.worktree, "remote", "set-url", "origin", "git@github.com:ipt/interceptor.git")
	if _, err := g.FetchPullRequest(context.Background(), "ipt/interceptor", 0, ""); err == nil {
		t.Fatal("accepted foreign host")
	}
}

func TestLocalGitHTTPSLayoutDoesNotAliasGitHub(t *testing.T) {
	g, _ := localFixture(t)
	g.host = "github.com"
	g.cloneLayout = "plain"
	if !g.matchesRemote("https://github.com/scm/widget.git", "scm/widget") {
		t.Fatal("rejected real scm owner")
	}
	for _, remote := range []string{"https://github.com/scm/acme/widget.git", "https://git@github.com/scm/acme/widget.git"} {
		if g.matchesRemote(remote, "acme/widget") {
			t.Fatal("accepted scm alias")
		}
	}
	if _, err := NewLocalGitGateway(g.worktree, "github.com", "bitbucket-server", g.baseSHA, g.descriptionFile, 1024); err == nil {
		t.Fatal("accepted invalid GitHub layout")
	}
	g.host = "git.example.test"
	if g.matchesRemote("https://git.example.test/scm/acme/widget.git", "acme/widget") {
		t.Fatal("implicit Bitbucket layout")
	}
	g.cloneLayout = "bitbucket-server"
	if !g.matchesRemote("https://git.example.test/scm/acme/widget.git", "acme/widget") {
		t.Fatal("rejected explicit Bitbucket layout")
	}
}

func TestLocalGitRejectsEncodedOriginAliases(t *testing.T) {
	g, _ := localFixture(t)
	for _, layout := range []string{"plain", "bitbucket-server"} {
		g.cloneLayout = layout
		prefix := "https://git.example.test/"
		if layout == "bitbucket-server" {
			prefix += "scm/"
		}
		for _, path := range []string{"ipt%2Finterceptor.git", "ipt/interceptor%2egit", "%69pt/interceptor.git"} {
			if g.matchesRemote(prefix+path, "ipt/interceptor") {
				t.Fatalf("accepted encoded origin %s", path)
			}
		}
	}
}

func TestLocalGitIgnoresReplaceRefsAndInheritedGitEnvironment(t *testing.T) {
	g, _ := localFixture(t)
	head := localTestGit(t, g.worktree, "rev-parse", "HEAD")
	localTestGit(t, g.worktree, "checkout", "--detach", g.baseSHA)
	if err := os.WriteFile(filepath.Join(g.worktree, "code.txt"), []byte("replacement-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	localTestGit(t, g.worktree, "commit", "-am", "replacement tree")
	tree := localTestGit(t, g.worktree, "rev-parse", "HEAD^{tree}")
	replacement := localTestGit(t, g.worktree, "commit-tree", tree, "-m", "root replacement")
	localTestGit(t, g.worktree, "checkout", "--detach", head)
	localTestGit(t, g.worktree, "replace", g.baseSHA, replacement)
	altered := localTestGit(t, g.worktree, "diff", g.baseSHA+"...HEAD")
	if !strings.Contains(altered, "-replacement-only") {
		t.Fatal("fixture did not demonstrate replace-ref substitution")
	}
	snapshot, err := g.FetchPullRequest(context.Background(), "ipt/interceptor", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.HeadSHA != head || !strings.Contains(snapshot.Diff, "-old") || strings.Contains(snapshot.Diff, "replacement-only") {
		t.Fatal("review diff follows replace refs")
	}
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "foreign.git"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "index"))
	t.Setenv("GIT_OBJECT_DIRECTORY", t.TempDir())
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.bare")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	again, err := g.FetchPullRequest(context.Background(), "ipt/interceptor", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if again.Diff != snapshot.Diff || again.HeadSHA != snapshot.HeadSHA {
		t.Fatal("inherited Git variables changed the reviewed input")
	}
}
