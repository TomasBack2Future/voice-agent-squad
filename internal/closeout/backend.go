package closeout

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/zsiec/squad/internal/ownedexec"
)

// LocalBackend invokes existing gate-enforcing Done behavior and the user's
// configured gh connection; it never supplies credentials or bypass flags.
type LocalBackend struct {
	GitHubRunner                       func(context.Context, string, ...string) ([]byte, error)
	DB                                 *sql.DB
	Repo, Actor, Directory, Executable string
}
type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, fmt.Errorf("response exceeds bounded output limit")
	}
	return b.Buffer.Write(p)
}
func (b LocalBackend) gh(ctx context.Context, input string, args ...string) ([]byte, error) {
	if b.GitHubRunner != nil {
		return b.GitHubRunner(ctx, input, args...)
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	ownedexec.Configure(cmd)
	cmd.Dir = b.Directory
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	out := &boundedBuffer{limit: 1024 * 1024}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("GitHub operation failed: %w", err)
	}
	return out.Bytes(), nil
}
func (b LocalBackend) Done(ctx context.Context, item string) error {
	var n int
	err := b.DB.QueryRowContext(ctx, `SELECT count(*) FROM items i WHERE i.repo_id=? AND i.item_id=? AND i.status='done' AND EXISTS(SELECT 1 FROM claim_history c WHERE c.repo_id=i.repo_id AND c.item_id=i.item_id AND c.agent_id=? AND c.outcome='done' AND c.claimed_at>=(SELECT reserved_at FROM dispatch_reservations r WHERE r.repo_id=i.repo_id AND r.canonical_item_id=i.item_id))`, b.Repo, item, b.Actor).Scan(&n)
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	cmd := exec.CommandContext(ctx, b.Executable, "done", item, "--summary", "acceptance and cleanup complete; GitHub closeout pending")
	ownedexec.Configure(cmd)
	cmd.Dir = b.Directory
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("existing done verification/evidence gates did not complete: %w", err)
	}
	return nil
}
func (b LocalBackend) Comment(ctx context.Context, source, marker, body string) error {
	repo, number, err := ParseSource(source)
	if err != nil {
		return err
	}
	raw, err := b.gh(ctx, "", "api", "--paginate", "--slurp", "repos/"+repo+"/issues/"+number+"/comments?per_page=100")
	if err != nil {
		return err
	}
	login, err := b.gh(ctx, "", "api", "user", "--jq", ".login")
	if err != nil {
		return err
	}
	author := strings.TrimSpace(string(login))
	if author == "" {
		return fmt.Errorf("GitHub author identity unavailable")
	}
	var pages [][]struct {
		ID   int64 `json:"id"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Body string `json:"body"`
	}
	if err = json.Unmarshal(raw, &pages); err != nil {
		return fmt.Errorf("invalid comments response")
	}
	var found int64
	for _, page := range pages {
		for _, c := range page {
			if strings.HasPrefix(c.Body, marker+"\n") {
				if c.User.Login != author {
					return fmt.Errorf("closeout comment marker belongs to another GitHub author")
				}
				if found != 0 {
					return fmt.Errorf("multiple matching closeout comments; reconcile before retry")
				}
				found = c.ID
			}
		}
	}
	payload, _ := json.Marshal(map[string]string{"body": marker + "\n" + body})
	endpoint := "repos/" + repo + "/issues/" + number + "/comments"
	method := "POST"
	if found != 0 {
		endpoint = fmt.Sprintf("repos/%s/issues/comments/%d", repo, found)
		method = "PATCH"
	}
	_, err = b.gh(ctx, string(payload), "api", "--method", method, endpoint, "--input", "-")
	return err
}
func (b LocalBackend) Close(ctx context.Context, source string) error {
	repo, number, err := ParseSource(source)
	if err != nil {
		return err
	}
	raw, err := b.gh(ctx, "", "issue", "view", number, "--repo", repo, "--json", "state")
	if err != nil {
		return err
	}
	var issue struct {
		State string `json:"state"`
	}
	if err = json.Unmarshal(raw, &issue); err != nil {
		return fmt.Errorf("invalid Issue response")
	}
	if issue.State == "CLOSED" {
		return nil
	}
	if issue.State != "OPEN" {
		return fmt.Errorf("unknown Issue state")
	}
	_, err = b.gh(ctx, "", "issue", "close", number, "--repo", repo)
	return err
}
