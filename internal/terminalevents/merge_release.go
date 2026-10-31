package terminalevents

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"
)

// MergeRelease is the Dispatcher's structured permission to merge exactly one
// pull request head. It is valid only while the decision revision it was issued
// with stays current; the evidence outcome is a pointer to the attested
// independent review and CI result, never proof by itself.
type MergeRelease struct {
	PRRepo           string `json:"pr_repo"`
	PRNumber         int64  `json:"pr_number"`
	HeadSHA          string `json:"head_sha"`
	BaseSHA          string `json:"base_sha"`
	EvidenceOutcome  int64  `json:"evidence_outcome"`
	ExpiresAt        int64  `json:"expires_at"`
	DecisionRevision int64  `json:"decision_revision,omitempty"`
}

// PRFacts is the live state read from the code host when a release is issued.
type PRFacts struct {
	State        string
	HeadSHA      string
	BaseTipSHA   string
	ChecksPassed bool
}

// PRReader reads the pull request's current facts; nil means unavailable.
type PRReader func(ctx context.Context, repo string, number int64) (PRFacts, error)

var (
	shaPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	prRepoShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

const maxReleaseTTL = 4 * time.Hour

func (r MergeRelease) validate(now time.Time) error {
	if !prRepoShape.MatchString(r.PRRepo) || r.PRNumber < 1 || !shaPattern.MatchString(r.HeadSHA) || !shaPattern.MatchString(r.BaseSHA) || r.EvidenceOutcome < 1 {
		return &Rejection{Condition: "invalid-merge-release", Repair: "pr_repo owner/name, pr_number, 40-hex head and base SHAs and an evidence outcome are required"}
	}
	if r.ExpiresAt <= now.Unix() || r.ExpiresAt > now.Add(maxReleaseTTL).Unix() {
		return &Rejection{Condition: "invalid-merge-release", Repair: fmt.Sprintf("expires_at must be in the future and within %s", maxReleaseTTL)}
	}
	return nil
}

// verify checks the live facts the Dispatcher attests: open PR, exact head, the
// base branch tip the release was prepared against, and passing required checks.
func (r MergeRelease) verify(ctx context.Context, read PRReader) error {
	if read == nil {
		return &Rejection{Condition: "merge-release-unverifiable", Repair: "live pull request state is unavailable; no release was recorded"}
	}
	f, err := read(ctx, r.PRRepo, r.PRNumber)
	if err != nil {
		return &Rejection{Condition: "merge-release-unverifiable", Repair: "live pull request read failed; no release was recorded"}
	}
	switch {
	case f.State != "OPEN":
		return &Rejection{Condition: "merge-release-stale", Repair: "pull request is not open"}
	case f.HeadSHA != r.HeadSHA:
		return &Rejection{Condition: "merge-release-stale", Repair: "pull request head changed; re-review and issue a new release for the new head"}
	case f.BaseTipSHA != r.BaseSHA:
		return &Rejection{Condition: "merge-release-stale", Repair: "base branch advanced; revalidate against the new base and issue a new release"}
	case !f.ChecksPassed:
		return &Rejection{Condition: "merge-release-unmet", Repair: "required checks have not passed on the exact head"}
	}
	return nil
}

func currentRelease(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, repo, key string, generation int64, item string, revision int64, now int64) (*MergeRelease, error) {
	var r MergeRelease
	err := q.QueryRowContext(ctx, `SELECT pr_repo,pr_number,head_sha,base_sha,evidence_outcome,expires_at,decision_revision FROM dispatch_merge_releases WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=? AND decision_revision=? AND expires_at>?`, repo, key, generation, item, revision, now).Scan(&r.PRRepo, &r.PRNumber, &r.HeadSHA, &r.BaseSHA, &r.EvidenceOutcome, &r.ExpiresAt, &r.DecisionRevision)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}
