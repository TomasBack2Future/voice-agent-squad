package grokreview

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

const (
	frozenHead = "58645f46ef2a167c2dc42abce64b106558b78459"
	frozenBase = "d36d68365a1810e979ceff3de82c89caecbfe21d"
	frozenDiff = "diff --git a/a.go b/a.go\n+fixed\n"
)

func pullMetadata(state string, merged bool, head, base string) []byte {
	return []byte(fmt.Sprintf(`{"state":%q,"merged":%t,"number":17,"title":"fix","body":"details","base":{"ref":"main","sha":%q},"head":{"sha":%q}}`, state, merged, base, head))
}

func frozenExpectation() FrozenExpectation {
	return FrozenExpectation{HeadSHA: frozenHead, BaseSHA: frozenBase, DiffSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(frozenDiff)))}
}

func TestFetchPullRequestFrozenSnapshotAdmission(t *testing.T) {
	otherSHA := strings.Repeat("e", 40)
	cases := []struct {
		name         string
		state        string
		merged       bool
		head, base   string
		diff         string
		frozen       bool
		wantErr      string
		wantGHCalls  int
		wantDiffRead bool
	}{
		{name: "open unchanged without expectation", state: "open", head: frozenHead, base: frozenBase, diff: frozenDiff, wantGHCalls: 3},
		{name: "open matching expectation", state: "open", head: frozenHead, base: frozenBase, diff: frozenDiff, frozen: true, wantGHCalls: 3},
		{name: "open with moved head refused", state: "open", head: otherSHA, base: frozenBase, diff: frozenDiff, frozen: true, wantErr: "differs from the frozen snapshot", wantGHCalls: 1},
		{name: "merged matching head base and diff accepted", state: "closed", merged: true, head: frozenHead, base: frozenBase, diff: frozenDiff, frozen: true, wantGHCalls: 3},
		{name: "merged without expectation refused", state: "closed", merged: true, head: frozenHead, base: frozenBase, diff: frozenDiff, wantErr: "metadata is incomplete", wantGHCalls: 1},
		{name: "merged with different head refused", state: "closed", merged: true, head: otherSHA, base: frozenBase, diff: frozenDiff, frozen: true, wantErr: "differs from the frozen snapshot", wantGHCalls: 1},
		{name: "merged with different base refused", state: "closed", merged: true, head: frozenHead, base: otherSHA, diff: frozenDiff, frozen: true, wantErr: "differs from the frozen snapshot", wantGHCalls: 1},
		{name: "merged with different diff refused", state: "closed", merged: true, head: frozenHead, base: frozenBase, diff: frozenDiff + "+extra\n", frozen: true, wantErr: "differs from the frozen snapshot", wantGHCalls: 3},
		{name: "closed unmerged matching refused", state: "closed", head: frozenHead, base: frozenBase, diff: frozenDiff, frozen: true, wantErr: "only when merged", wantGHCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata := pullMetadata(tc.state, tc.merged, tc.head, tc.base)
			executor := &fakeCommandExecutor{outputs: [][]byte{metadata, []byte(tc.diff), metadata}}
			client := testGitHubCLI(executor)
			if tc.frozen {
				if err := client.ExpectFrozen(frozenExpectation()); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := client.FetchPullRequest(context.Background(), "owner/repo", 17, "app-token")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.HeadSHA != frozenHead || snapshot.BaseSHA != frozenBase || snapshot.Diff != frozenDiff {
					t.Fatalf("snapshot = %#v", snapshot)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if len(executor.calls) != tc.wantGHCalls {
				t.Fatalf("GitHub reads = %d, want %d", len(executor.calls), tc.wantGHCalls)
			}
		})
	}
}

func TestFetchPullRequestRefusesMergeDuringFrozenRead(t *testing.T) {
	executor := &fakeCommandExecutor{outputs: [][]byte{
		pullMetadata("open", false, frozenHead, frozenBase), []byte(frozenDiff), pullMetadata("closed", true, frozenHead, frozenBase),
	}}
	client := testGitHubCLI(executor)
	if err := client.ExpectFrozen(frozenExpectation()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FetchPullRequest(context.Background(), "owner/repo", 17, "app-token"); err == nil || !strings.Contains(err.Error(), "changed during") {
		t.Fatalf("err = %v", err)
	}
}

func TestExpectFrozenRequiresExactDigests(t *testing.T) {
	valid := frozenExpectation()
	for _, bad := range []FrozenExpectation{
		{BaseSHA: valid.BaseSHA, DiffSHA256: valid.DiffSHA256},
		{HeadSHA: "head456", BaseSHA: valid.BaseSHA, DiffSHA256: valid.DiffSHA256},
		{HeadSHA: valid.HeadSHA, BaseSHA: strings.ToUpper(valid.BaseSHA), DiffSHA256: valid.DiffSHA256},
		{HeadSHA: valid.HeadSHA, BaseSHA: valid.BaseSHA, DiffSHA256: valid.DiffSHA256[:63]},
	} {
		if err := (&GitHubCLI{}).ExpectFrozen(bad); err == nil {
			t.Fatalf("accepted %#v", bad)
		}
	}
}
