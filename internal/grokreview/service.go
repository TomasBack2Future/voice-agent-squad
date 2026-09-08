package grokreview

import (
	"context"
	"encoding/json"
	"fmt"
)

const FrozenReviewSchemaVersion = "squad.local-review.request.v1"

type FrozenReviewBundle struct {
	SchemaVersion string `json:"schema_version"`
	Repository    string `json:"repository"`
	PullRequest   int    `json:"pull_request"`
	BaseRef       string `json:"base_ref"`
	BaseSHA       string `json:"base_sha"`
	HeadSHA       string `json:"head_sha"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	Diff          string `json:"diff"`
	CoreHash      string `json:"reviewer_core_sha256"`
	PolicyHash    string `json:"review_policy_sha256"`
}

type ReviewReport struct {
	Snapshot    PullRequestSnapshot
	Result      FindingsResult
	Audit       CLIAudit
	Publication Publication
}

type ModelReviewer interface {
	Review(context.Context, []byte) (FindingsResult, CLIAudit, error)
}

type PullRequestGateway interface {
	FetchPullRequest(context.Context, string, int, string) (PullRequestSnapshot, error)
	FetchPullRequestIdentity(context.Context, string, int, string) (PullRequestSnapshot, error)
	PublishReview(context.Context, string, string, PullRequestSnapshot, FindingsResult, CLIAudit) (Publication, error)
}

type LocalReviewService struct {
	model      ModelReviewer
	github     PullRequestGateway
	coreHash   string
	policyHash string
}

func NewLocalReviewService(model ModelReviewer, github PullRequestGateway, coreHash, policyHash string) (*LocalReviewService, error) {
	if model == nil || github == nil {
		return nil, fmt.Errorf("model reviewer and GitHub gateway are required")
	}
	if coreHash == "" || policyHash == "" {
		return nil, fmt.Errorf("reviewer core and policy hashes are required")
	}
	return &LocalReviewService{model: model, github: github, coreHash: coreHash, policyHash: policyHash}, nil
}

func (s *LocalReviewService) ReviewPullRequest(ctx context.Context, token, checkName, repository string, number int) (ReviewReport, error) {
	snapshot, err := s.github.FetchPullRequest(ctx, repository, number, token)
	if err != nil {
		return ReviewReport{}, err
	}
	if err := validateSnapshot(snapshot); err != nil {
		return ReviewReport{}, err
	}
	bundle, err := json.Marshal(FrozenReviewBundle{
		SchemaVersion: FrozenReviewSchemaVersion,
		Repository:    snapshot.Repository, PullRequest: snapshot.Number,
		BaseRef: snapshot.BaseRef, BaseSHA: snapshot.BaseSHA, HeadSHA: snapshot.HeadSHA,
		Title: snapshot.Title, Description: snapshot.Description, Diff: snapshot.Diff,
		CoreHash: s.coreHash, PolicyHash: s.policyHash,
	})
	if err != nil {
		return ReviewReport{}, fmt.Errorf("encode frozen review bundle: %w", err)
	}

	result, audit, reviewErr := s.model.Review(ctx, bundle)
	if reviewErr == nil {
		if err := ValidateFindings(result); err != nil {
			reviewErr = err
		}
	}
	if reviewErr != nil {
		result = FindingsResult{
			SchemaVersion: FindingsSchemaVersion,
			Verdict:       VerdictError,
			Summary:       "Grok review could not complete. Retry after resolving the local reviewer failure.",
			Findings:      []Finding{},
		}
	}

	current, err := s.github.FetchPullRequestIdentity(ctx, repository, number, token)
	if err != nil {
		return ReviewReport{Snapshot: snapshot, Result: result, Audit: audit}, err
	}
	if !samePullRequestTuple(snapshot, current) {
		return ReviewReport{Snapshot: snapshot, Result: result, Audit: audit}, fmt.Errorf("pull request base or head changed during review; result was not published")
	}
	publication, err := s.github.PublishReview(ctx, token, checkName, snapshot, result, audit)
	report := ReviewReport{Snapshot: snapshot, Result: result, Audit: audit, Publication: publication}
	if err != nil {
		return report, err
	}
	if reviewErr != nil {
		return report, fmt.Errorf("local Grok review failed: %w", reviewErr)
	}
	return report, nil
}

func samePullRequestTuple(left, right PullRequestSnapshot) bool {
	return left.Repository == right.Repository && left.Number == right.Number &&
		left.BaseRef == right.BaseRef && left.BaseSHA == right.BaseSHA && left.HeadSHA == right.HeadSHA
}
