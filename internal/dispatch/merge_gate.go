package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// mergeLike recognises merge-class commands wherever they appear in a Bash
// command, including inside shell -c or interpreter strings. Recognition only
// decides that authority is required; it never grants any.
var mergeLike = regexp.MustCompile(`(?i)\bgh\s+pr\s+merge\b|\bgh\s+api\b[^\n]*(?:/merge\b|mergePullRequest|auto_merge|enableAutoMerge)|mergePullRequest|enablePullRequestAutoMerge|\bgit\s+push\b[^\n]*(?:\bmain\b|\bmaster\b)`)

const mergePlainRejects = ";&|<>$`()\n\r\\\"'*?{}!#~="

var (
	mergeSHA  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	mergeRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

func isMergeClass(tool NativeHookTool) bool {
	if strings.HasPrefix(tool.Name, "mcp__") && strings.Contains(strings.ToLower(tool.Name), "merge") {
		return true
	}
	return tool.Name == "Bash" && mergeLike.MatchString(tool.Command)
}

// plainMerge parses the single supported form
// `gh pr merge N --repo OWNER/REPO (--squash|--merge|--rebase) --match-head-commit SHA [--delete-branch]`.
func plainMerge(command string) (repo string, number string, head string, ok bool) {
	if len(command) > 1024 || strings.ContainsAny(command, mergePlainRejects) {
		return
	}
	w := strings.Fields(command)
	if len(w) < 8 || filepath.Base(w[0]) != "gh" || w[1] != "pr" || w[2] != "merge" {
		return
	}
	number = w[3]
	strategies := 0
	for i := 4; i < len(w); i++ {
		switch w[i] {
		case "--repo", "-R":
			if i+1 >= len(w) || repo != "" {
				return "", "", "", false
			}
			repo = w[i+1]
			i++
		case "--match-head-commit":
			if i+1 >= len(w) || head != "" {
				return "", "", "", false
			}
			head = w[i+1]
			i++
		case "--squash", "--merge", "--rebase":
			strategies++
		case "--delete-branch":
		default:
			return "", "", "", false
		}
	}
	if strategies != 1 || !mergeRepo.MatchString(repo) || !mergeSHA.MatchString(head) {
		return "", "", "", false
	}
	return repo, number, head, true
}

// mergeAuthorization enforces the structured merge release for an assignment
// whose decision is release-required. Standing assignments are unchanged.
func mergeAuthorization(ctx context.Context, tx *sql.Tx, repoID, key string, generation int64, item, policy, action string, revision, now int64, tool NativeHookTool) error {
	if policy != "release-required" || !isMergeClass(tool) {
		return nil
	}
	deny := func(why string) error {
		return fmt.Errorf("merge-release-required: %s; ask the Dispatcher for a merge release naming this exact PR head (squad terminal-events decision-get shows the current one)", why)
	}
	if action != "proceed" || revision < 1 {
		return deny("the current decision does not permit work")
	}
	repo, number, head, ok := plainMerge(tool.Command)
	if !ok {
		return deny("only `gh pr merge N --repo OWNER/REPO --squash|--merge|--rebase --match-head-commit SHA` is accepted, with no other merge route")
	}
	var r struct {
		repo, head string
		number     int64
		expires    int64
	}
	err := tx.QueryRowContext(ctx, `SELECT pr_repo,pr_number,head_sha,expires_at FROM dispatch_merge_releases WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=? AND decision_revision=? AND expires_at>? AND pr_repo=?`, repoID, key, generation, item, revision, now, repo).Scan(&r.repo, &r.number, &r.head, &r.expires)
	if errors.Is(err, sql.ErrNoRows) {
		return deny("no current release for this repository under the effective decision revision")
	}
	if err != nil {
		return err
	}
	if fmt.Sprint(r.number) != number || r.head != head {
		return deny("the command does not match the released PR number and head")
	}
	return nil
}
