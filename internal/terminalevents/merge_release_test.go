package terminalevents

import (
	"context"
	"encoding/json"
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

func TestFirstDecisionOfANewGenerationKeepsTheAssignmentPolicy(t *testing.T) {
	ok := PRFacts{State: "OPEN", HeadSHA: headA, BaseTipSHA: baseA, ChecksPassed: true}
	s, q := mergeReleaseFixture(t, liveFacts(ok, nil))
	if _, err := s.Decide(context.Background(), "dispatcher", q); err != nil {
		t.Fatal(err)
	}
	// A replacement Worker at generation 2 of the same assignment.
	if _, err := s.DB.Exec(`UPDATE dispatch_reservations SET generation=2`); err != nil {
		t.Fatal(err)
	}
	next := DecisionRequest{Reservation: "DISPATCH-1", Generation: 2, WorkerSession: "worker-session", OutcomeID: decisionMessage(t, s), Action: "proceed", Condition: "resume"}
	d, err := s.Decide(context.Background(), "dispatcher", next)
	if err != nil || d.MergePolicy != "release-required" || d.MergeRelease != nil {
		t.Fatal(d, err)
	}
}

func TestMergeReleaseRetryCannotSwapEvidenceWithoutANewRevision(t *testing.T) {
	ctx := context.Background()
	failed := AdvisoryReview{CheckRunID: 42, AppID: 123, AppSlug: "review-app", ExternalID: "local-grok-attempt:id:hash", Mode: "shadow"}
	facts := func(check AdvisoryReview) PRReader {
		return liveFacts(PRFacts{State: "OPEN", HeadSHA: headA, BaseTipSHA: baseA, ChecksPassed: true, AdvisoryFailures: []AdvisoryReview{check}}, nil)
	}
	s, q := mergeReleaseFixture(t, facts(failed))
	dispute := func(id int64, check AdvisoryReview) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"schema": "squad.merge-review-disputes.v1", "repository": "o/r", "pr": 7, "head_sha": headA, "base_sha": baseA, "disputes": []any{map[string]any{
			"check_run_id": check.CheckRunID, "app_id": check.AppID, "app_slug": check.AppSlug, "external_id": check.ExternalID, "mode": check.Mode,
			"verdict": "disproved", "finding": "finding reference", "evidence": "source and test evidence", "reason": "contradicted by invariant"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.DB.Exec("UPDATE messages SET body=? WHERE id=?", string(raw), id); err != nil {
			t.Fatal(err)
		}
	}
	type snapshot struct{ revision, outcome, evidence, receipts int64 }
	state := func() snapshot {
		t.Helper()
		var v snapshot
		if err := s.DB.QueryRow(`SELECT (SELECT revision FROM dispatch_decisions),(SELECT outcome_id FROM dispatch_decisions),(SELECT evidence_outcome FROM dispatch_merge_releases),(SELECT count(*) FROM terminal_event_receipts)`).Scan(&v.revision, &v.outcome, &v.evidence, &v.receipts); err != nil {
			t.Fatal(err)
		}
		return v
	}
	oldEvidence := q.MergeRelease.EvidenceOutcome
	dispute(oldEvidence, failed)
	first, err := s.Decide(ctx, "dispatcher", q)
	if err != nil || first.MergeRelease == nil || first.MergeRelease.EvidenceOutcome != oldEvidence {
		t.Fatal(first, err)
	}
	if _, err = s.Decide(ctx, "dispatcher", q); err != nil {
		t.Fatal("safe retry", err)
	}
	if _, err = s.DecideBatch(ctx, "dispatcher", []DecisionRequest{q}); err != nil {
		t.Fatal("safe batch retry", err)
	}
	before := state()

	// A new failed publication appears on the same head; new evidence covers it.
	changed := failed
	changed.CheckRunID, changed.ExternalID = 43, "local-grok-attempt:newid:hash"
	s.PRFacts = facts(changed)
	newEvidence := decisionMessage(t, s)
	dispute(newEvidence, changed)
	swapped := q
	release := *q.MergeRelease
	release.EvidenceOutcome = newEvidence
	swapped.MergeRelease = &release
	if _, err = s.Decide(ctx, "dispatcher", swapped); !errors.Is(err, ErrStaleDecision) {
		t.Fatal("retry swapped evidence under the old decision", err)
	}
	if _, err = s.DecideBatch(ctx, "dispatcher", []DecisionRequest{swapped}); !errors.Is(err, ErrStaleDecision) {
		t.Fatal("batch retry swapped evidence under the old decision", err)
	}
	if after := state(); after != before {
		t.Fatal("refused evidence swap changed durable state", before, after)
	}

	// The changed evidence is accepted only as a new decision revision.
	swapped.ExpectedRevision = first.Revision
	swapped.OutcomeID = decisionMessage(t, s)
	next, err := s.Decide(ctx, "dispatcher", swapped)
	if err != nil || next.Revision != first.Revision+1 || next.MergeRelease == nil || next.MergeRelease.EvidenceOutcome != newEvidence {
		t.Fatal(next, err)
	}
	if after := state(); after.revision != next.Revision || after.evidence != newEvidence || after.receipts <= before.receipts {
		t.Fatal("new revision did not persist and wake", before, after)
	}
}

func TestAdvisoryReviewReleaseRequiresExactDispatcherDispute(t *testing.T) {
	failed := AdvisoryReview{CheckRunID: 42, AppID: 123, AppSlug: "review-app", ExternalID: "local-grok-attempt:id:hash", Mode: "shadow"}
	for _, kind := range []string{"verified", "plain prose", "wrong repo", "wrong pr", "wrong head", "wrong base", "wrong check", "wrong app", "wrong slug", "wrong mode", "wrong publication", "unverified", "no finding", "no evidence", "no reason", "other author", "other thread", "old evidence", "missing dispute", "duplicate dispute", "batch"} {
		t.Run(kind, func(t *testing.T) {
			s, q := mergeReleaseFixture(t, liveFacts(PRFacts{State: "OPEN", HeadSHA: headA, BaseTipSHA: baseA, ChecksPassed: true, AdvisoryFailures: []AdvisoryReview{failed}}, nil))
			body := map[string]any{"schema": "squad.merge-review-disputes.v1", "repository": "o/r", "pr": 7, "head_sha": headA, "base_sha": baseA}
			dispute := map[string]any{"check_run_id": failed.CheckRunID, "app_id": failed.AppID, "app_slug": failed.AppSlug, "external_id": failed.ExternalID, "mode": "shadow", "verdict": "disproved", "finding": "original finding URL", "evidence": "regression output and source paths", "reason": "producer invariant excludes the proposed transition"}
			body["disputes"] = []any{dispute}
			switch kind {
			case "wrong repo":
				body["repository"] = "o/other"
			case "wrong pr":
				body["pr"] = 8
			case "wrong head":
				body["head_sha"] = headB
			case "wrong base":
				body["base_sha"] = headB
			case "wrong check":
				dispute["check_run_id"] = 43
			case "wrong app":
				dispute["app_id"] = 999
			case "wrong slug":
				dispute["app_slug"] = "other"
			case "wrong mode":
				dispute["mode"] = "required"
			case "wrong publication":
				dispute["external_id"] = "another attempt"
			case "unverified":
				dispute["verdict"] = "pending"
			case "no finding":
				dispute["finding"] = ""
			case "no evidence":
				dispute["evidence"] = ""
			case "no reason":
				dispute["reason"] = ""
			case "missing dispute":
				body["disputes"] = []any{}
			case "duplicate dispute":
				body["disputes"] = []any{dispute, dispute}
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "plain prose" {
				raw = []byte("review is a false positive")
			}
			if _, err = s.DB.Exec("UPDATE messages SET body=? WHERE id=?", string(raw), q.MergeRelease.EvidenceOutcome); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "other author":
				_, err = s.DB.Exec("UPDATE messages SET agent_id='worker' WHERE id=?", q.MergeRelease.EvidenceOutcome)
			case "other thread":
				_, err = s.DB.Exec("UPDATE messages SET thread='OTHER' WHERE id=?", q.MergeRelease.EvidenceOutcome)
			case "old evidence":
				_, err = s.DB.Exec("UPDATE messages SET ts=0 WHERE id=?", q.MergeRelease.EvidenceOutcome)
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "batch" {
				_, err = s.DecideBatch(context.Background(), "dispatcher", []DecisionRequest{q})
			} else {
				_, err = s.Decide(context.Background(), "dispatcher", q)
			}
			want := kind == "verified" || kind == "batch"
			if (err == nil) != want {
				t.Fatalf("want release=%v: %v", want, err)
			}
			var releases, decisions, events int
			if err = s.DB.QueryRow(`SELECT (SELECT count(*) FROM dispatch_merge_releases),(SELECT count(*) FROM dispatch_decisions),(SELECT count(*) FROM terminal_event_receipts)`).Scan(&releases, &decisions, &events); err != nil {
				t.Fatal(err)
			}
			if !want && (releases != 0 || decisions != 0 || events != 0) {
				t.Fatal("refused dispute wrote state", releases, decisions, events)
			}
			if want {
				if _, err = s.Decide(context.Background(), "dispatcher", q); err != nil {
					t.Fatal("retry", err)
				}
				// A newer failed publication on the same head cannot reuse the old dispute.
				changed := failed
				changed.CheckRunID++
				s.PRFacts = liveFacts(PRFacts{State: "OPEN", HeadSHA: headA, BaseTipSHA: baseA, ChecksPassed: true, AdvisoryFailures: []AdvisoryReview{changed}}, nil)
				if _, err = s.Decide(context.Background(), "dispatcher", q); err == nil {
					t.Fatal("retry reused stale dispute")
				}
			}
		})
	}
}
