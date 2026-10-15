package grokreview

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOnlyExplicitDiffSizeFailureFallsBack(t *testing.T) {
	for _, raw := range []string{`{}`, `{"status":"403","errors":[{"field":"diff","code":"too_large"}]}`, `{"status":"406","errors":[{"field":"body","code":"too_large"}]}`, `{"status":"406","errors":[{"field":"diff","code":"too_large"}]}trailing`} {
		if oversizedDiffResponse([]byte(raw)) {
			t.Fatalf("accepted unrelated failure %s", raw)
		}
	}
	if !oversizedDiffResponse([]byte(`{"status":"406","errors":[{"field":"diff","code":"too_large"}]}`)) {
		t.Fatal("explicit limit rejected")
	}
	metadata := []byte(`{"state":"open","number":17,"title":"fix","body":"details","base":{"ref":"main","sha":"base"},"head":{"sha":"head"}}`)
	for _, changed := range []bool{false, true} {
		last := metadata
		if changed {
			last = []byte(strings.ReplaceAll(string(metadata), `"details"`, `"changed"`))
		}
		fake := &fakeCommandExecutor{outputs: [][]byte{metadata, nil, last}, errors: []error{nil, &diffTooLarge{}, nil}}
		g := testGitHubCLI(fake)
		calls := 0
		g.completeDiff = func(_ context.Context, s PullRequestSnapshot, token string) ([]byte, error) {
			calls++
			if s.HeadSHA != "head" || token != "fixture-token" {
				t.Fatal("identity lost")
			}
			return []byte(strings.Repeat("+line\n", 21001)), nil
		}
		g.maxOutputBytes = 8 << 20
		got, err := g.FetchPullRequest(context.Background(), "owner/repo", 17, "fixture-token")
		if calls != 1 || changed != (err != nil) {
			t.Fatalf("changed=%v calls=%d err=%v", changed, calls, err)
		}
		if !changed && strings.Count(got.Diff, "\n") != 21001 {
			t.Fatal("diff truncated")
		}
	}
}

func TestExactGitSnapshotPreservesCompletePatchAndBounds(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	git, err = filepath.Abs(git)
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", source, "-c", "core.hooksPath=" + os.DevNull}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("fixture git: %v %s", e, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "--template=", "-q")
	run("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "base")
	base := run("rev-parse", "HEAD")
	for i := 0; i < 105; i++ {
		if err := os.WriteFile(filepath.Join(source, fmt.Sprintf("file-%03d", i)), []byte("complete\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "large"), []byte(strings.Repeat("full-line\n", 21001)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "binary"), []byte{0, 1, 2, 3, 4}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("large", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "executable"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "head")
	head := run("rev-parse", "HEAD")
	// A fixture-only Git shim redirects fetch to this isolated local repository.
	// All remaining Git commands, graph validation and patch generation are real.
	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nroot=\nprev=\nfor arg in \"$@\"; do\n if [ \"$prev\" = -C ]; then root=$arg; fi\n if [ \"$arg\" = fetch ]; then exec '%s' -C \"$root\" -c protocol.file.allow=always fetch --no-tags --no-recurse-submodules '%s' '%s' '%s'; fi\n prev=$arg\ndone\nexec '%s' \"$@\"\n", git, source, base, head, git)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	g := &GitHubCLI{timeout: 10 * time.Second, maxOutputBytes: 8 << 20}
	s := PullRequestSnapshot{Repository: "owner/repo", BaseSHA: base, HeadSHA: head}
	diff, err := g.exactGitDiff(context.Background(), s, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"file-104", "new file mode 120000", "new file mode 100755", "GIT binary patch"} {
		if !strings.Contains(string(diff), part) {
			t.Fatalf("missing %s", part)
		}
	}
	if strings.Count(string(diff), "+full-line\n") != 21001 {
		t.Fatal("large patch incomplete")
	}
	second, err := g.exactGitDiff(context.Background(), s, "fixture-token")
	if err != nil || string(second) != string(diff) {
		t.Fatal("snapshot not deterministic", err)
	}
	g.maxOutputBytes = 128
	if _, err = g.exactGitDiff(context.Background(), s, "fixture-token"); err == nil {
		t.Fatal("oversize accepted")
	}
	g.maxOutputBytes = 8 << 20
	s.HeadSHA = strings.Repeat("a", 40)
	if _, err = g.exactGitDiff(context.Background(), s, "fixture-token"); err == nil {
		t.Fatal("missing exact commit accepted")
	}
	s.Repository = "owner/repo?injection"
	if _, err = g.exactGitDiff(context.Background(), s, "fixture-token"); err == nil {
		t.Fatal("unsafe repository accepted")
	}
}
