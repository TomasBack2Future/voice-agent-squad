package terminalevents

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
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
	State            string
	HeadSHA          string
	BaseTipSHA       string
	ChecksPassed     bool
	AdvisoryFailures []AdvisoryReview
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
func (r MergeRelease) verify(ctx context.Context, read PRReader) (PRFacts, error) {
	var empty PRFacts
	if read == nil {
		return empty, &Rejection{Condition: "merge-release-unverifiable", Repair: "live pull request state is unavailable; no release was recorded"}
	}
	f, err := read(ctx, r.PRRepo, r.PRNumber)
	if err != nil {
		return empty, &Rejection{Condition: "merge-release-unverifiable", Repair: "live pull request read failed; no release was recorded"}
	}
	switch {
	case f.State != "OPEN":
		return empty, &Rejection{Condition: "merge-release-stale", Repair: "pull request is not open"}
	case f.HeadSHA != r.HeadSHA:
		return empty, &Rejection{Condition: "merge-release-stale", Repair: "pull request head changed; re-review and issue a new release for the new head"}
	case f.BaseTipSHA != r.BaseSHA:
		return empty, &Rejection{Condition: "merge-release-stale", Repair: "base branch advanced; revalidate against the new base and issue a new release"}
	case !f.ChecksPassed:
		return empty, &Rejection{Condition: "merge-release-unmet", Repair: "required checks have not passed on the exact head"}
	}
	return f, nil
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

// AdvisoryReview identifies the actual failed publication, not just a check name.
type AdvisoryReview struct {
	CheckRunID int64  `json:"check_run_id"`
	AppID      int64  `json:"app_id"`
	AppSlug    string `json:"app_slug"`
	ExternalID string `json:"external_id"`
	Mode       string `json:"mode"`
}

// The Dispatcher authors this record in the existing canonical evidence message.
// Evidence is an accountable human/owner attestation, not a machine proof of a finding.
type reviewDisputeEvidence struct {
	Schema     string `json:"schema"`
	Repository string `json:"repository"`
	PR         int64  `json:"pr"`
	HeadSHA    string `json:"head_sha"`
	BaseSHA    string `json:"base_sha"`
	Disputes   []struct {
		AdvisoryReview
		Verdict  string `json:"verdict"`
		Finding  string `json:"finding"`
		Evidence string `json:"evidence"`
		Reason   string `json:"reason"`
	} `json:"disputes"`
}

func (r MergeRelease) verifyDisputes(body string, failures []AdvisoryReview) error {
	if len(failures) == 0 {
		return nil
	}
	reject := &Rejection{Condition: "merge-review-dispute-required", Repair: "merge evidence must contain a verified dispute for every failed advisory review on this exact repo/PR/head/base and Check/App publication"}
	var e reviewDisputeEvidence
	if json.Unmarshal([]byte(body), &e) != nil || e.Schema != "squad.merge-review-disputes.v1" || e.Repository != r.PRRepo || e.PR != r.PRNumber || e.HeadSHA != r.HeadSHA || e.BaseSHA != r.BaseSHA || len(e.Disputes) != len(failures) {
		return reject
	}
	seen := map[int64]bool{}
	for _, d := range e.Disputes {
		if seen[d.CheckRunID] || d.Mode != "shadow" || d.Verdict != "disproved" || strings.TrimSpace(d.Finding) == "" || strings.TrimSpace(d.Evidence) == "" || strings.TrimSpace(d.Reason) == "" {
			return reject
		}
		found := false
		for _, f := range failures {
			if d.AdvisoryReview == f {
				found = true
				break
			}
		}
		if !found {
			return reject
		}
		seen[d.CheckRunID] = true
	}
	return nil
}
