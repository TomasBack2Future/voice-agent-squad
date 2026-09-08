package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/grokreview"
)

func TestParseConfigUsesExplicitLocalReviewerInputs(t *testing.T) {
	var output bytes.Buffer
	config, err := parseConfig([]string{
		"--repo", "owner/repo",
		"--pr", "17",
		"--mode", "shadow",
		"--app-id", "4862345",
		"--installation-id", "99",
		"--app-private-key", "/secure/app.pem",
		"--grok-bin", "/opt/bin/grok",
		"--gh-bin", "/opt/bin/gh",
		"--grok-home", "/srv/reviewer",
		"--status-dir", "/srv/status",
		"--model", "grok-4.6",
		"--timeout", "8m",
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if config.repository != "owner/repo" || config.pullRequest != 17 || config.checkName != "grok-review-shadow" || config.mode != "shadow" {
		t.Fatalf("config = %#v", config)
	}
	if config.appID != 4862345 || config.installationID != 99 || config.timeout != 8*time.Minute {
		t.Fatalf("config = %#v", config)
	}
	if config.statusDir != "/srv/status" {
		t.Fatalf("status dir = %q", config.statusDir)
	}
}

func TestParseConfigRejectsUnknownMode(t *testing.T) {
	var output bytes.Buffer
	_, err := parseConfig([]string{
		"--repo", "owner/repo", "--pr", "1", "--mode", "future",
		"--app-id", "1", "--installation-id", "2", "--app-private-key", "/key",
		"--grok-bin", "/grok", "--gh-bin", "/gh", "--grok-home", "/home",
	}, &output)
	if err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunHelpReturnsSuccess(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage of squad-grok-review") || strings.Contains(stderr.String(), "flag: help requested") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestParseConfigRejectsRelativeStatusDirectory(t *testing.T) {
	var output bytes.Buffer
	_, err := parseConfig([]string{
		"--repo", "owner/repo", "--pr", "1", "--mode", "shadow",
		"--app-id", "1", "--installation-id", "2", "--app-private-key", "/key",
		"--grok-bin", "/grok", "--gh-bin", "/gh", "--grok-home", "/home",
		"--status-dir", "relative/status",
	}, &output)
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("error = %v", err)
	}
}

func TestCommandOutputDoesNotExposeFrozenDiffOrHiddenThought(t *testing.T) {
	report := grokreview.ReviewReport{
		Snapshot: grokreview.PullRequestSnapshot{
			Repository: "owner/repo", Number: 17, BaseRef: "main", BaseSHA: "base",
			HeadSHA: "head", Title: "title", Description: "private description", Diff: "private diff",
		},
		Result: grokreview.FindingsResult{
			SchemaVersion: grokreview.FindingsSchemaVersion,
			Verdict:       grokreview.VerdictApproved,
			Summary:       "approved",
			Findings:      []grokreview.Finding{},
		},
		Audit: grokreview.CLIAudit{
			RequestID: "request", SessionID: "session", RequestedModel: "grok-4.6",
			ResolvedModel: "grok-4.6-build", Usage: grokreview.TokenUsage{TotalTokens: 22},
			FailureKind: grokreview.CLIFailurePromptFileFormat,
		},
		Publication: grokreview.Publication{CommentID: 1, CheckRunID: 2, Conclusion: "success"},
	}
	raw, err := json.Marshal(newCommandOutput(report))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"private description", "private diff", "thought"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("output exposed %q: %s", forbidden, text)
		}
	}
	for _, required := range []string{"head", "approved", "request", "session", "prompt_file_format"} {
		if !strings.Contains(text, required) {
			t.Fatalf("output lacks %q: %s", required, text)
		}
	}
}
