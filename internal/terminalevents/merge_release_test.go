package terminalevents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	baseA = "cccccccccccccccccccccccccccccccccccccccc"
)

func liveFacts(f PRFacts, err error) PRReader {
	return func(context.Context, string, int64) (PRFacts, error) { return f, err }
}

func mergeReleaseFixture(t *testing.T, facts PRReader) (Store, DecisionRequest) {
	t.Helper()
	s := fixture(t)
	s.Recipient = ""
	s.PRFacts = facts
	evidence := decisionMessage(t, s)
	q := DecisionRequest{Reservation: "DISPATCH-1", Generation: 1, WorkerSession: "worker-session", OutcomeID: decisionMessage(t, s), Action: "proceed", Condition: "merge release for PR 7", MergePolicy: "release-required",
		MergeRelease: &MergeRelease{PRRepo: "o/r", PRNumber: 7, HeadSHA: headA, BaseSHA: baseA, EvidenceOutcome: evidence, ExpiresAt: time.Now().Add(time.Hour).Unix()}}
	return s, q
}

func TestMergeReleaseIsRecordedOnlyAfterLiveVerificationAndBoundToItsRevision(t *testing.T) {
	ok := PRFacts{State: "OPEN", HeadSHA: headA, BaseTipSHA: baseA, ChecksPassed: true}
	s, q := mergeReleaseFixture(t, liveFacts(ok, nil))
	ctx := context.Background()
	d, err := s.Decide(ctx, "dispatcher", q)
	if err != nil || d.MergeRelease == nil || d.MergePolicy != "release-required" || d.MergeRelease.DecisionRevision != d.Revision {
		t.Fatal(d, err)
	}
	got, err := s.CurrentDecision(ctx, "DISPATCH-1", 1, "worker-session")
	if err != nil || got.MergeRelease == nil || got.MergeRelease.HeadSHA != headA {
		t.Fatal(got, err)
	}
	// A later ordinary proceed revision revokes the release but keeps the policy.
	next := q
	next.ExpectedRevision = d.Revision
	next.OutcomeID = decisionMessage(t, s)
	next.MergeRelease, next.MergePolicy = nil, ""
	d2, err := s.Decide(ctx, "dispatcher", next)
	if err != nil || d2.MergeRelease != nil || d2.MergePolicy != "release-required" {
		t.Fatal(d2, err)
	}
	if got, _ = s.CurrentDecision(ctx, "DISPATCH-1", 1, "worker-session"); got.MergeRelease != nil || got.MergePolicy != "release-required" {
		t.Fatal("release survived a new revision", got)
	}
	// Retrying the identical first request is stale, never a resurrected release.
	if _, err = s.Decide(ctx, "dispatcher", q); err == nil {
		t.Fatal("stale release request accepted")
	}
}

func TestMergeReleaseRefusesStaleUnmetOrUnverifiableStateWithoutWriting(t *testing.T) {
	cases := map[string]struct {
		facts  PRReader
		mutate func(*DecisionRequest)
		cond   string
	}{
		"head changed":       {liveFacts(PRFacts{State: "OPEN", HeadSHA: headB, BaseTipSHA: baseA, ChecksPassed: true}, nil), nil, "merge-release-stale"},
		"base advanced":      {liveFacts(PRFacts{State: "OPEN", HeadSHA: headA, BaseTipSHA: headB, ChecksPassed: true}, nil), nil, "merge-release-stale"},
		"checks not passing": {liveFacts(PRFacts{State: "OPEN", HeadSHA: headA, BaseTipSHA: baseA}, nil), nil, "merge-release-unmet"},
		"already merged":     {liveFacts(PRFacts{State: "MERGED", HeadSHA: headA, BaseTipSHA: baseA, ChecksPassed: true}, nil), nil, "merge-release-stale"},
		"host unreadable":    {liveFacts(PRFacts{}, errors.New("offline")), nil, "merge-release-unverifiable"},
		"no reader":          {nil, nil, "merge-release-unverifiable"},
		"hold with release":  {liveFacts(PRFacts{State: "OPEN", HeadSHA: headA, BaseTipSHA: baseA, ChecksPassed: true}, nil), func(q *DecisionRequest) { q.Action = "hold" }, "invalid-merge-release"},
		"short sha":          {liveFacts(PRFacts{State: "OPEN", HeadSHA: headA, BaseTipSHA: baseA, ChecksPassed: true}, nil), func(q *DecisionRequest) { q.MergeRelease.HeadSHA = "abc" }, "invalid-merge-release"},
		"expired":            {liveFacts(PRFacts{State: "OPEN", HeadSHA: headA, BaseTipSHA: baseA, ChecksPassed: true}, nil), func(q *DecisionRequest) { q.MergeRelease.ExpiresAt = time.Now().Add(-time.Minute).Unix() }, "invalid-merge-release"},
		"ttl too long":       {liveFacts(PRFacts{State: "OPEN", HeadSHA: headA, BaseTipSHA: baseA, ChecksPassed: true}, nil), func(q *DecisionRequest) { q.MergeRelease.ExpiresAt = time.Now().Add(48 * time.Hour).Unix() }, "invalid-merge-release"},
		"foreign evidence":   {liveFacts(PRFacts{State: "OPEN", HeadSHA: headA, BaseTipSHA: baseA, ChecksPassed: true}, nil), func(q *DecisionRequest) { q.MergeRelease.EvidenceOutcome = 999999 }, "invalid-merge-release"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s, q := mergeReleaseFixture(t, c.facts)
			if c.mutate != nil {
				c.mutate(&q)
			}
			_, err := s.Decide(context.Background(), "dispatcher", q)
			var r *Rejection
			if !errors.As(err, &r) || r.Condition != c.cond {
				t.Fatalf("want %s, got %v", c.cond, err)
			}
			var n int
			if e := s.DB.QueryRow(`SELECT (SELECT count(*) FROM dispatch_merge_releases)+(SELECT count(*) FROM dispatch_decisions)`).Scan(&n); e != nil || n != 0 {
				t.Fatal("a refused release left durable state", n, e)
			}
		})
	}
}

func TestInvalidMergePolicyIsRejected(t *testing.T) {
	s, q := mergeReleaseFixture(t, nil)
	q.MergeRelease, q.MergePolicy = nil, "always"
	if _, err := s.Decide(context.Background(), "dispatcher", q); err == nil || strings.Contains(err.Error(), "panic") {
		t.Fatal("policy accepted", err)
	}
}
