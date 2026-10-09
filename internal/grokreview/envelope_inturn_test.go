package grokreview

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mutatedEnvelope(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(approvedEnvelope(t), &envelope); err != nil {
		t.Fatal(err)
	}
	mutate(envelope)
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A denied tool request lets Grok continue inside the same turn: one turn,
// two model calls.
func TestParseCLIEnvelopeAcceptsInTurnContinuationAfterDeniedTool(t *testing.T) {
	raw := mutatedEnvelope(t, func(e map[string]any) {
		e["modelUsage"] = map[string]any{"grok-review-model": map[string]any{"modelCalls": 2}}
	})
	result, audit, err := ParseCLIEnvelope(raw)
	if err != nil || result.Verdict != VerdictApproved || audit.NumTurns != 1 {
		t.Fatalf("result=%#v audit=%#v err=%v", result, audit, err)
	}
}

// The call limit is a total per-attempt budget, not a per-turn limit.
func TestParseCLIEnvelopeEnforcesTotalModelCallBudget(t *testing.T) {
	withCalls := func(calls int) []byte {
		return mutatedEnvelope(t, func(e map[string]any) {
			e["modelUsage"] = map[string]any{"grok-review-model": map[string]any{"modelCalls": calls}}
		})
	}
	if MaxReviewerTotalModelCalls != 6 {
		t.Fatalf("budget = %d, want 6", MaxReviewerTotalModelCalls)
	}
	if _, audit, err := ParseCLIEnvelope(withCalls(6)); err != nil || audit.NumTurns != 1 {
		t.Fatalf("one turn with 6 calls: audit=%#v err=%v", audit, err)
	}
	assertEnvelopeRule(t, withCalls(7), EnvelopeRuleModelCallsUnbounded)
}

func assertEnvelopeRule(t *testing.T, raw []byte, want EnvelopeRule) {
	t.Helper()
	_, _, err := ParseCLIEnvelope(raw)
	var ruleErr *EnvelopeRuleError
	if !errors.As(err, &ruleErr) || ruleErr.Rule != want {
		t.Fatalf("rule = %#v, want %q (err=%v)", ruleErr, want, err)
	}
	if strings.Contains(err.Error(), "No blocking findings") {
		t.Fatalf("model text leaked: %v", err)
	}
}

// Every rejection rule has its own identifier so a failed review can be
// attributed without reading model output.
func TestParseCLIEnvelopeAttributesEachRejectionRule(t *testing.T) {
	const structuredB = `{"schema_version":"squad.review.findings.v2","verdict":"blocking","summary":"different","findings":[]}`
	cases := []struct {
		name   string
		rule   EnvelopeRule
		mutate func(map[string]any)
	}{
		{"stop_reason", EnvelopeRuleStopReason, func(e map[string]any) { e["stopReason"] = "max_turns" }},
		{"identity", EnvelopeRuleIdentity, func(e map[string]any) { e["sessionId"] = "" }},
		{"turns_out_of_range", EnvelopeRuleTurnsOutOfRange, func(e map[string]any) { e["num_turns"] = MaxReviewerTurns + 1 }},
		{"model_count", EnvelopeRuleModelCount, func(e map[string]any) {
			e["modelUsage"] = map[string]any{"a": map[string]any{"modelCalls": 1}, "b": map[string]any{"modelCalls": 1}}
		}},
		{"model_calls_below_turns", EnvelopeRuleModelCallsBelowTurns, func(e map[string]any) {
			e["num_turns"] = 2
			e["modelUsage"] = map[string]any{"grok-review-model": map[string]any{"modelCalls": 1}}
		}},
		{"structured_output_missing", EnvelopeRuleStructuredOutputMissing, func(e map[string]any) { delete(e, "structuredOutput") }},
		{"structured_output_invalid", EnvelopeRuleStructuredOutputInvalid, func(e map[string]any) {
			e["structuredOutput"] = map[string]any{"authority": "forged"}
		}},
		{"findings_invalid", EnvelopeRuleFindingsInvalid, func(e map[string]any) {
			e["structuredOutput"] = map[string]any{"schema_version": FindingsSchemaVersion, "verdict": "error", "summary": "x", "findings": []any{}}
		}},
		{"text_not_json", EnvelopeRuleTextNotJSON, func(e map[string]any) { e["text"] = "two assistant messages concatenated {" }},
		{"text_mismatch", EnvelopeRuleTextMismatch, func(e map[string]any) { e["text"] = structuredB }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertEnvelopeRule(t, mutatedEnvelope(t, tc.mutate), tc.rule)
		})
	}
	t.Run("envelope_unparseable", func(t *testing.T) {
		assertEnvelopeRule(t, []byte(`{"text":`), EnvelopeRuleEnvelopeUnparseable)
	})
}

func TestCLIRunnerRecordsViolatedRuleWithoutOutput(t *testing.T) {
	raw := mutatedEnvelope(t, func(e map[string]any) { e["text"] = "private-output {" })
	binary := filepath.Join(t.TempDir(), "fake-grok")
	script := "#!/bin/sh\ncat <<'JSON'\n" + string(raw) + "\nJSON\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config := testCLIConfig(t)
	config.Binary = binary
	runner, err := NewCLIRunner(config)
	if err != nil {
		t.Fatal(err)
	}
	_, audit, err := runner.Review(context.Background(), []byte("input"))
	if err == nil || audit.FailureKind != CLIFailureInvalidOutput || audit.FailureRule != EnvelopeRuleTextNotJSON {
		t.Fatalf("audit=%#v err=%v", audit, err)
	}
	if strings.Contains(err.Error(), "private-output") {
		t.Fatalf("raw output leaked: %v", err)
	}
}

func TestReviewStatusWriterRecordsFailureRule(t *testing.T) {
	dir := t.TempDir()
	writer, err := NewReviewStatusWriter(dir, "shadow", 8*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	err = writer.Observe(ReviewObservation{
		State:        ReviewStateError,
		FailureStage: "validating",
		Snapshot:     PullRequestSnapshot{Repository: "owner/repo", Number: 9},
		Audit:        CLIAudit{FailureKind: CLIFailureInvalidOutput, FailureRule: EnvelopeRuleModelCallsUnbounded},
	})
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(paths) != 1 {
		t.Fatalf("status files = %v", paths)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil || !strings.Contains(string(raw), `"failure_rule": "model_calls_unbounded"`) && !strings.Contains(string(raw), `"failure_rule":"model_calls_unbounded"`) {
		t.Fatalf("status=%s err=%v", raw, err)
	}
}

func TestLocalReviewServicePublishesViolatedRuleInCheckSummary(t *testing.T) {
	snapshot := PullRequestSnapshot{Repository: "owner/repo", Number: 9, BaseRef: "main", BaseSHA: "base", HeadSHA: "head", Title: "fix", Diff: "+fixed"}
	model := &fakeModelReviewer{
		err:   errors.New("private-error-marker"),
		audit: CLIAudit{FailureKind: CLIFailureInvalidOutput, FailureRule: EnvelopeRuleTextMismatch},
	}
	github := &fakePullRequestGateway{snapshot: snapshot, current: snapshot}
	observer := &recordingReviewObserver{}
	service, err := NewLocalReviewService(model, github, "core", "policy", observer)
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9)
	if err == nil || report.FailureStage != "validating" {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	summary := github.publishedInput.Summary
	if !strings.Contains(summary, "text_mismatch") || strings.Contains(summary, "private-error-marker") {
		t.Fatalf("summary = %q", summary)
	}
	last := observer.observations[len(observer.observations)-1]
	if last.Audit.FailureRule != EnvelopeRuleTextMismatch {
		t.Fatalf("observation=%#v", last)
	}
}
