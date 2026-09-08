package grokreview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeModelReviewer struct {
	result FindingsResult
	audit  CLIAudit
	err    error
	bundle []byte
	calls  int
}

func (f *fakeModelReviewer) Review(_ context.Context, bundle []byte) (FindingsResult, CLIAudit, error) {
	f.calls++
	f.bundle = append([]byte(nil), bundle...)
	return f.result, f.audit, f.err
}

type fakePullRequestGateway struct {
	snapshot       PullRequestSnapshot
	current        PullRequestSnapshot
	publication    Publication
	fetchErr       error
	identityErr    error
	publishErr     error
	published      int
	publishedToken string
	publishedName  string
	publishedInput FindingsResult
}

type recordingReviewObserver struct {
	observations []ReviewObservation
	err          error
}

func (o *recordingReviewObserver) Observe(observation ReviewObservation) error {
	o.observations = append(o.observations, observation)
	return o.err
}

func (f *fakePullRequestGateway) FetchPullRequest(context.Context, string, int, string) (PullRequestSnapshot, error) {
	return f.snapshot, f.fetchErr
}

func (f *fakePullRequestGateway) FetchPullRequestIdentity(context.Context, string, int, string) (PullRequestSnapshot, error) {
	return f.current, f.identityErr
}

func (f *fakePullRequestGateway) PublishReview(_ context.Context, token, name string, _ PullRequestSnapshot, result FindingsResult, _ CLIAudit) (Publication, error) {
	f.published++
	f.publishedToken = token
	f.publishedName = name
	f.publishedInput = result
	return f.publication, f.publishErr
}

func approvedFindings() FindingsResult {
	return FindingsResult{
		SchemaVersion: FindingsSchemaVersion,
		Verdict:       VerdictApproved,
		Summary:       "No blocking findings.",
		Findings:      []Finding{},
	}
}

func TestLocalReviewServiceBindsReviewAndPublicationToSameTuple(t *testing.T) {
	snapshot := PullRequestSnapshot{
		Repository: "owner/repo", Number: 9, BaseRef: "main", BaseSHA: "base",
		HeadSHA: "head", Title: "fix", Description: "details", Diff: "+fixed",
	}
	model := &fakeModelReviewer{result: approvedFindings(), audit: CLIAudit{RequestID: "request"}}
	github := &fakePullRequestGateway{
		snapshot: snapshot, current: snapshot,
		publication: Publication{CommentID: 1, CheckRunID: 2, Conclusion: "success"},
	}
	observer := &recordingReviewObserver{}
	service, err := NewLocalReviewService(model, github, "core-hash", "policy-hash", observer)
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.ReviewPullRequest(context.Background(), "token", "grok-review-shadow", "owner/repo", 9)
	if err != nil {
		t.Fatal(err)
	}
	if report.Snapshot.HeadSHA != "head" || report.Publication.CheckRunID != 2 {
		t.Fatalf("report = %#v", report)
	}
	if model.calls != 1 || github.published != 1 || github.publishedName != "grok-review-shadow" {
		t.Fatalf("model calls=%d publish calls=%d name=%q", model.calls, github.published, github.publishedName)
	}
	wantStates := []ReviewState{
		ReviewStateFreezing, ReviewStateSampling, ReviewStateValidating,
		ReviewStatePublishing, ReviewStateApproved,
	}
	if len(observer.observations) != len(wantStates) {
		t.Fatalf("observations = %#v", observer.observations)
	}
	for index, want := range wantStates {
		if observer.observations[index].State != want {
			t.Fatalf("observation %d state = %q, want %q", index, observer.observations[index].State, want)
		}
	}
	var bundle FrozenReviewBundle
	if err := json.Unmarshal(model.bundle, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.HeadSHA != "head" || bundle.CoreHash != "core-hash" || bundle.PolicyHash != "policy-hash" || bundle.Diff != "+fixed" {
		t.Fatalf("bundle = %#v", bundle)
	}
}

func TestLocalReviewServiceIgnoresObserverFailure(t *testing.T) {
	snapshot := PullRequestSnapshot{
		Repository: "owner/repo", Number: 9, BaseRef: "main", BaseSHA: "base",
		HeadSHA: "head", Title: "fix", Diff: "+fixed",
	}
	model := &fakeModelReviewer{result: approvedFindings()}
	github := &fakePullRequestGateway{
		snapshot: snapshot, current: snapshot,
		publication: Publication{CommentID: 1, CheckRunID: 2, Conclusion: "success"},
	}
	observer := &recordingReviewObserver{err: errors.New("status directory unavailable")}
	service, err := NewLocalReviewService(model, github, "core", "policy", observer)
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result.Verdict != VerdictApproved || github.published != 1 {
		t.Fatalf("report=%#v published=%d", report, github.published)
	}
}

func TestLocalReviewServiceRefusesPublicationWhenHeadChanges(t *testing.T) {
	snapshot := PullRequestSnapshot{
		Repository: "owner/repo", Number: 9, BaseRef: "main", BaseSHA: "base",
		HeadSHA: "old", Title: "fix", Diff: "+fixed",
	}
	current := snapshot
	current.HeadSHA = "new"
	model := &fakeModelReviewer{result: approvedFindings()}
	github := &fakePullRequestGateway{snapshot: snapshot, current: current}
	service, err := NewLocalReviewService(model, github, "core", "policy")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9)
	if err == nil || !strings.Contains(err.Error(), "changed during review") {
		t.Fatalf("error = %v", err)
	}
	if github.published != 0 {
		t.Fatalf("publish calls = %d", github.published)
	}
}

func TestLocalReviewServicePublishesFailClosedResultForModelError(t *testing.T) {
	snapshot := PullRequestSnapshot{
		Repository: "owner/repo", Number: 9, BaseRef: "main", BaseSHA: "base",
		HeadSHA: "head", Title: "fix", Diff: "+fixed",
	}
	model := &fakeModelReviewer{err: errors.New("provider unavailable")}
	github := &fakePullRequestGateway{
		snapshot: snapshot, current: snapshot,
		publication: Publication{CommentID: 1, CheckRunID: 2, Conclusion: "failure"},
	}
	service, err := NewLocalReviewService(model, github, "core", "policy")
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9)
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("error = %v", err)
	}
	if report.Publication.CheckRunID != 2 || github.publishedInput.Verdict != VerdictError {
		t.Fatalf("report=%#v published=%#v", report, github.publishedInput)
	}
	if strings.Contains(github.publishedInput.Summary, "provider unavailable") {
		t.Fatalf("internal error leaked into publication: %q", github.publishedInput.Summary)
	}
}
