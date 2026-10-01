package grokreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
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
	Snapshot     PullRequestSnapshot
	Result       FindingsResult
	Audit        CLIAudit
	Publication  Publication
	FailureStage string
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
	State        ReviewState
	Snapshot     PullRequestSnapshot
	Result       FindingsResult
	Audit        CLIAudit
	Publication  Publication
	FailureStage string
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
	admission  ReviewAdmission
}

// ReviewAdmission is mandatory custody, unlike best-effort status observation.
type ReviewAdmission interface {
	Start(context.Context, []byte) error
	Finish(context.Context, ReviewReport) error
}

func (s *LocalReviewService) SetAdmission(a ReviewAdmission) { s.admission = a }

func NewLocalReviewService(model ModelReviewer, github PullRequestGateway, coreHash, policyHash string, observers ...ReviewObserver) (*LocalReviewService, error) {
	if model == nil || github == nil {
		return nil, fmt.Errorf("model reviewer and input gateway are required")
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
	return s.review(ctx, token, checkName, repository, number, false)
}

// ReviewWorktree produces local evidence only; no remote PR/approval is created.
func (s *LocalReviewService) ReviewWorktree(ctx context.Context, repository string) (ReviewReport, error) {
	if _, ok := s.github.(*LocalGitGateway); !ok {
		return ReviewReport{}, fmt.Errorf("local review requires a local git gateway")
	}
	return s.review(ctx, "", "local-review", repository, 0, true)
}

func (s *LocalReviewService) review(ctx context.Context, token, checkName, repository string, number int, local bool) (report ReviewReport, returnErr error) {
	s.observe(ReviewObservation{
		State:    ReviewStateFreezing,
		Snapshot: PullRequestSnapshot{Repository: repository, Number: number},
	})
	snapshot, err := s.github.FetchPullRequest(ctx, repository, number, token)
	if err != nil {
		s.observe(ReviewObservation{State: ReviewStateError, FailureStage: "freezing", Snapshot: PullRequestSnapshot{Repository: repository, Number: number}})
		return ReviewReport{FailureStage: "freezing"}, err
	}
	var snapshotErr error
	if local {
		snapshotErr = validateLocalSnapshot(snapshot)
	} else {
		snapshotErr = validateSnapshot(snapshot)
	}
	if err := snapshotErr; err != nil {
		s.observe(ReviewObservation{State: ReviewStateError, FailureStage: "freezing", Snapshot: snapshot})
		return ReviewReport{Snapshot: snapshot, FailureStage: "freezing"}, err
	}
	bundle, err := json.Marshal(FrozenReviewBundle{
		SchemaVersion: FrozenReviewSchemaVersion,
		Repository:    snapshot.Repository, PullRequest: snapshot.Number,
		BaseRef: snapshot.BaseRef, BaseSHA: snapshot.BaseSHA, HeadSHA: snapshot.HeadSHA,
		Title: snapshot.Title, Description: snapshot.Description, Diff: snapshot.Diff,
		CoreHash: s.coreHash, PolicyHash: s.policyHash,
	})
	if err != nil {
		s.observe(ReviewObservation{State: ReviewStateError, FailureStage: "freezing", Snapshot: snapshot})
		return ReviewReport{Snapshot: snapshot, FailureStage: "freezing"}, fmt.Errorf("encode frozen review bundle: %w", err)
	}

	if s.admission != nil {
		if err := s.admission.Start(ctx, bundle); err != nil {
			return ReviewReport{Snapshot: snapshot, FailureStage: "admission"}, err
		}
		defer func() {
			finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			returnErr = errors.Join(returnErr, s.admission.Finish(finishCtx, report))
		}()
	}
	s.observe(ReviewObservation{State: ReviewStateSampling, Snapshot: snapshot})
	result, audit, reviewErr := s.model.Review(ctx, bundle)
	failureStage := ""
	if reviewErr != nil {
		failureStage = "sampling"
		if audit.FailureKind == CLIFailureInvalidOutput {
			failureStage = "validating"
		}
	}
	s.observe(ReviewObservation{State: ReviewStateValidating, Snapshot: snapshot, Audit: audit})
	if reviewErr == nil {
		if err := ValidateModelFindings(result); err != nil {
			reviewErr = err
			failureStage = "validating"
		}
	}
	if reviewErr != nil {
		result = FindingsResult{
			SchemaVersion: FindingsSchemaVersion,
			Verdict:       VerdictError,
			Summary:       "Grok review did not complete; no valid review conclusion. Diagnose the operational failure before considering another attempt.",
			Findings:      []Finding{},
		}
		if audit.FailureKind == CLIFailureTimeout {
			result.Summary = "Grok review timed out; no valid review conclusion. Do not treat timeout as approval or blindly resample this head."
		}
	}

	var current PullRequestSnapshot
	if s.admission != nil {
		current, err = s.github.FetchPullRequest(ctx, repository, number, token)
	} else {
		current, err = s.github.FetchPullRequestIdentity(ctx, repository, number, token)
	}
	if err != nil {
		s.observe(ReviewObservation{State: ReviewStateError, FailureStage: "identity", Snapshot: snapshot, Result: result, Audit: audit})
		return ReviewReport{Snapshot: snapshot, Result: result, Audit: audit, FailureStage: "identity"}, err
	}
	if !samePullRequestTuple(snapshot, current) || (s.admission != nil && (snapshot.Title != current.Title || snapshot.Description != current.Description || snapshot.Diff != current.Diff)) {
		s.observe(ReviewObservation{State: ReviewStateStale, FailureStage: "identity", Snapshot: snapshot, Result: result, Audit: audit})
		return ReviewReport{Snapshot: snapshot, Result: result, Audit: audit, FailureStage: "identity"}, fmt.Errorf("pull request base or head changed during review; result was not published")
	}
	s.observe(ReviewObservation{State: ReviewStatePublishing, Snapshot: snapshot, Result: result, Audit: audit})
	publication, err := s.github.PublishReview(ctx, token, checkName, snapshot, result, audit)
	report = ReviewReport{Snapshot: snapshot, Result: result, Audit: audit, Publication: publication}
	if err != nil {
		report.FailureStage = "publishing"
		s.observe(ReviewObservation{State: ReviewStateError, FailureStage: "publishing", Snapshot: snapshot, Result: result, Audit: audit, Publication: publication})
		return report, err
	}
	if reviewErr != nil {
		report.FailureStage = failureStage
		s.observe(ReviewObservation{State: ReviewStateError, FailureStage: failureStage, Snapshot: snapshot, Result: result, Audit: audit, Publication: publication})
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
