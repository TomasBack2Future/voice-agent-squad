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

type ReviewState string

const (
	ReviewStateFreezing   ReviewState = "freezing"
	ReviewStateSampling   ReviewState = "sampling"
	ReviewStateValidating ReviewState = "validating"
	ReviewStatePublishing ReviewState = "publishing"
	ReviewStateApproved   ReviewState = "approved"
	ReviewStateBlocking   ReviewState = "blocking"
	ReviewStateError      ReviewState = "error"
	ReviewStateStale      ReviewState = "stale"
)

// ReviewObservation is the deliberately small, sanitized lifecycle surface
// exposed to local read-only observers. It never contains the frozen diff,
// prompt, GitHub token, private key, Grok stdout/stderr, or hidden reasoning.
type ReviewObservation struct {
	State       ReviewState
	Snapshot    PullRequestSnapshot
	Result      FindingsResult
	Audit       CLIAudit
	Publication Publication
}

// ReviewObserver receives best-effort lifecycle observations. Observer errors
// must never change whether a review is accepted, rejected, or published.
type ReviewObserver interface {
	Observe(ReviewObservation) error
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
	observer   ReviewObserver
}

func NewLocalReviewService(model ModelReviewer, github PullRequestGateway, coreHash, policyHash string, observers ...ReviewObserver) (*LocalReviewService, error) {
	if model == nil || github == nil {
		return nil, fmt.Errorf("model reviewer and GitHub gateway are required")
	}
	if coreHash == "" || policyHash == "" {
		return nil, fmt.Errorf("reviewer core and policy hashes are required")
	}
	if len(observers) > 1 {
		return nil, fmt.Errorf("at most one review observer is supported")
	}
	var observer ReviewObserver
	if len(observers) == 1 {
		observer = observers[0]
	}
	return &LocalReviewService{model: model, github: github, coreHash: coreHash, policyHash: policyHash, observer: observer}, nil
}

func (s *LocalReviewService) ReviewPullRequest(ctx context.Context, token, checkName, repository string, number int) (ReviewReport, error) {
	s.observe(ReviewObservation{
		State:    ReviewStateFreezing,
		Snapshot: PullRequestSnapshot{Repository: repository, Number: number},
	})
	snapshot, err := s.github.FetchPullRequest(ctx, repository, number, token)
	if err != nil {
		s.observe(ReviewObservation{State: ReviewStateError, Snapshot: PullRequestSnapshot{Repository: repository, Number: number}})
		return ReviewReport{}, err
	}
	if err := validateSnapshot(snapshot); err != nil {
		s.observe(ReviewObservation{State: ReviewStateError, Snapshot: snapshot})
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
		s.observe(ReviewObservation{State: ReviewStateError, Snapshot: snapshot})
		return ReviewReport{}, fmt.Errorf("encode frozen review bundle: %w", err)
	}

	s.observe(ReviewObservation{State: ReviewStateSampling, Snapshot: snapshot})
	result, audit, reviewErr := s.model.Review(ctx, bundle)
	s.observe(ReviewObservation{State: ReviewStateValidating, Snapshot: snapshot, Audit: audit})
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
		s.observe(ReviewObservation{State: ReviewStateError, Snapshot: snapshot, Result: result, Audit: audit})
		return ReviewReport{Snapshot: snapshot, Result: result, Audit: audit}, err
	}
	if !samePullRequestTuple(snapshot, current) {
		s.observe(ReviewObservation{State: ReviewStateStale, Snapshot: snapshot, Result: result, Audit: audit})
		return ReviewReport{Snapshot: snapshot, Result: result, Audit: audit}, fmt.Errorf("pull request base or head changed during review; result was not published")
	}
	s.observe(ReviewObservation{State: ReviewStatePublishing, Snapshot: snapshot, Result: result, Audit: audit})
	publication, err := s.github.PublishReview(ctx, token, checkName, snapshot, result, audit)
	report := ReviewReport{Snapshot: snapshot, Result: result, Audit: audit, Publication: publication}
	if err != nil {
		s.observe(ReviewObservation{State: ReviewStateError, Snapshot: snapshot, Result: result, Audit: audit, Publication: publication})
		return report, err
	}
	if reviewErr != nil {
		s.observe(ReviewObservation{State: ReviewStateError, Snapshot: snapshot, Result: result, Audit: audit, Publication: publication})
		return report, fmt.Errorf("local Grok review failed: %w", reviewErr)
	}
	state := ReviewStateApproved
	if result.Verdict == VerdictBlocking {
		state = ReviewStateBlocking
	}
	s.observe(ReviewObservation{State: state, Snapshot: snapshot, Result: result, Audit: audit, Publication: publication})
	return report, nil
}

func (s *LocalReviewService) observe(observation ReviewObservation) {
	if s.observer != nil {
		_ = s.observer.Observe(observation)
	}
}

func samePullRequestTuple(left, right PullRequestSnapshot) bool {
	return left.Repository == right.Repository && left.Number == right.Number &&
		left.BaseRef == right.BaseRef && left.BaseSHA == right.BaseSHA && left.HeadSHA == right.HeadSHA
}
