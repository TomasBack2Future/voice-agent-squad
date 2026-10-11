package dispatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func legacyGitFixture(t *testing.T) (string, func(...string) string) {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=" + os.DevNull}, args...)...)
		cmd.Dir = workspace
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GIT_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	git("remote", "add", "origin", "https://example.invalid/fixture.git")
	legacyWriteFixture(t, workspace, "file.txt", "aaa\n", 0600)
	git("add", ".")
	git("commit", "-qm", "fixture")
	return workspace, git
}

func legacyWriteFixture(t *testing.T, workspace, name, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func legacyDigestFixture(t *testing.T, workspace string) string {
	t.Helper()
	digest, err := legacyWorkspaceDigest(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestLegacyWorkspaceDigestBindsStagedBlobWithIdenticalDirtyWorktree(t *testing.T) {
	workspace, git := legacyGitFixture(t)
	legacyWriteFixture(t, workspace, "file.txt", "bbb\n", 0600)
	git("add", "file.txt")
	legacyWriteFixture(t, workspace, "file.txt", "ccc\n", 0600)
	status := git("status", "--porcelain=v1", "-z")
	before := legacyDigestFixture(t, workspace)
	legacyWriteFixture(t, workspace, "file.txt", "ddd\n", 0600)
	git("add", "file.txt")
	legacyWriteFixture(t, workspace, "file.txt", "ccc\n", 0600)
	if after := git("status", "--porcelain=v1", "-z"); after != status || !strings.HasPrefix(after, "MM file.txt") {
		t.Fatal("fixture must retain identical MM status and worktree bytes")
	}
	if before == legacyDigestFixture(t, workspace) {
		t.Fatal("staged blob drift escaped source custody digest")
	}
}

func TestLegacyWorkspaceDigestRetainsTrackedAndUntrackedBytes(t *testing.T) {
	workspace, _ := legacyGitFixture(t)
	legacyWriteFixture(t, workspace, "file.txt", "bbb\n", 0600)
	before := legacyDigestFixture(t, workspace)
	legacyWriteFixture(t, workspace, "file.txt", "ccc\n", 0600)
	after := legacyDigestFixture(t, workspace)
	if before == after {
		t.Fatal("tracked dirty bytes escaped source custody")
	}
	legacyWriteFixture(t, workspace, "new.txt", "new\n", 0600)
	if after == legacyDigestFixture(t, workspace) {
		t.Fatal("untracked bytes escaped source custody")
	}
}

func TestLegacyWorkspaceDigestDoesNotRunRepositoryHelpers(t *testing.T) {
	for _, helper := range []string{"fsmonitor", "clean-filter"} {
		t.Run(helper, func(t *testing.T) {
			workspace, git := legacyGitFixture(t)
			marker := filepath.Join(t.TempDir(), "executed")
			t.Setenv("SQUAD_LEGACY_GIT_MARKER", marker)
			script := filepath.Join(t.TempDir(), "helper")
			body := "#!/bin/sh\nprintf x >> \"$SQUAD_LEGACY_GIT_MARKER\"\n"
			if helper == "fsmonitor" {
				body += "printf 'token\\0'\n"
			} else {
				body += "cat\n"
				legacyWriteFixture(t, workspace, ".gitattributes", "*.txt filter=fixture\n", 0600)
				git("add", ".gitattributes")
				git("commit", "-qm", "attributes")
			}
			if err := os.WriteFile(script, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			if helper == "fsmonitor" {
				git("config", "core.fsmonitor", script)
			} else {
				git("config", "filter.fixture.clean", script)
				legacyWriteFixture(t, workspace, "file.txt", "bbb\n", 0600)
				stamp := time.Now().Add(2 * time.Second)
				if err := os.Chtimes(filepath.Join(workspace, "file.txt"), stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			legacyDigestFixture(t, workspace)
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("read-only source digest executed repository %s helper", helper)
			}
		})
	}
}

func TestLegacyWorkspaceDigestIgnoresInheritedIndexOverride(t *testing.T) {
	workspace, git := legacyGitFixture(t)
	before := legacyDigestFixture(t, workspace)
	index := filepath.Join(t.TempDir(), "alternate-index")
	data, err := os.ReadFile(filepath.Join(workspace, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, data, 0600); err != nil {
		t.Fatal(err)
	}
	legacyWriteFixture(t, workspace, "file.txt", "bbb\n", 0600)
	git("add", "file.txt")
	legacyWriteFixture(t, workspace, "file.txt", "aaa\n", 0600)
	actual := legacyDigestFixture(t, workspace)
	t.Setenv("GIT_INDEX_FILE", index)
	if got := legacyDigestFixture(t, workspace); got != actual || got == before {
		t.Fatal("inherited alternate index substituted the retained repository index")
	}
}

func TestLegacyWorkspaceDigestRetainsExecutableMode(t *testing.T) {
	workspace, _ := legacyGitFixture(t)
	before := legacyDigestFixture(t, workspace)
	if err := os.Chmod(filepath.Join(workspace, "file.txt"), 0700); err != nil {
		t.Fatal(err)
	}
	if before == legacyDigestFixture(t, workspace) {
		t.Fatal("worktree executable mode drift escaped source custody")
	}
}
