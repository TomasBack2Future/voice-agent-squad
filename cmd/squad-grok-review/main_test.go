package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/grokreview"
)

func TestParseConfigDiscoversLocalReviewerConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "grok-review.json")
	privateKeyPath := filepath.Join(dir, "reviewer.pem")
	if err := os.WriteFile(configPath, []byte(`{
  "app_id": 4862345,
  "installation_id": 159920856,
  "app_private_key": "`+privateKeyPath+`",
  "grok_home": "`+dir+`"
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SQUAD_GROK_REVIEW_CONFIG", configPath)

	var output bytes.Buffer
	config, err := parseConfig([]string{"--repo", "owner/repo", "--pr", "17"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if config.appID != 4862345 || config.installationID != 159920856 || config.appPrivateKey != privateKeyPath {
		t.Fatalf("config = %#v", config)
	}
	if config.grokHome != dir || config.configPath != configPath {
		t.Fatalf("config = %#v", config)
	}
	if config.reasoningEffort != "medium" {
		t.Fatalf("default reasoning effort = %q, want medium", config.reasoningEffort)
	}
}

func TestParseConfigReasoningEffortPrecedenceAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name, local, flag, want string
		invalid                 bool
	}{
		{name: "local", local: "high", want: "high"},
		{name: "explicit", local: "xhigh", flag: "medium", want: "medium"},
		{name: "invalid local", local: "unbounded", invalid: true},
		{name: "invalid flag", local: "medium", flag: "unbounded", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "review.json")
			if err := os.WriteFile(path, []byte(`{"app_id":1,"installation_id":2,"app_private_key":"/key","reasoning_effort":"`+tc.local+`"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"doctor", "--config", path}
			if tc.flag != "" {
				args = append(args, "--reasoning-effort", tc.flag)
			}
			var output bytes.Buffer
			config, err := parseConfig(args, &output)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid effort accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if config.reasoningEffort != tc.want {
				t.Fatalf("effort = %q, want %q", config.reasoningEffort, tc.want)
			}
		})
	}
}

func TestParseConfigExplicitFlagsOverrideLocalConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "grok-review.json")
	if err := os.WriteFile(configPath, []byte(`{
  "app_id": 1,
  "installation_id": 2,
  "app_private_key": "/config/key.pem"
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	config, err := parseConfig([]string{
		"--repo", "owner/repo", "--pr", "17", "--config", configPath,
		"--app-id", "3", "--installation-id", "4", "--app-private-key", "/flag/key.pem",
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if config.appID != 3 || config.installationID != 4 || config.appPrivateKey != "/flag/key.pem" {
		t.Fatalf("config = %#v", config)
	}
}

func TestParseConfigDoctorDoesNotRequirePullRequest(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "grok-review.json")
	if err := os.WriteFile(configPath, []byte(`{
  "app_id": 4862345,
  "installation_id": 159920856,
  "app_private_key": "/secure/reviewer.pem"
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	config, err := parseConfig([]string{"doctor", "--config", configPath}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !config.doctor || config.repository != "" || config.pullRequest != 0 {
		t.Fatalf("config = %#v", config)
	}
}

func TestParseConfigReportsMissingDiscoveredIdentity(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing.json")
	t.Setenv("SQUAD_GROK_REVIEW_CONFIG", configPath)

	var output bytes.Buffer
	_, err := parseConfig([]string{"--repo", "owner/repo", "--pr", "17"}, &output)
	if err == nil || !strings.Contains(err.Error(), configPath) || !strings.Contains(err.Error(), "reviewer config") {
		t.Fatalf("error = %v", err)
	}
}

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
