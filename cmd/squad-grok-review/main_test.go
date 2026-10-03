package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	if config.timeout != 20*time.Minute {
		t.Fatalf("default timeout = %s, want 20m", config.timeout)
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
			Terminal:  &grokreview.TerminalDiagnostics{InputSHA256: "input-hash", ChildWaited: true, SessionEvidence: "qualified_local", Session: &grokreview.TerminalSession{LocalRequestID: "local-request", ReasoningNotifications: 2}},
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
	for _, required := range []string{"head", "approved", "request", "session", "prompt_file_format", "terminal_diagnostics", "local_request_id", "reasoning_notifications"} {
		if !strings.Contains(text, required) {
			t.Fatalf("output lacks %q: %s", required, text)
		}
	}
}

func TestLocalConfigRequiresNeitherPRNorHostingCredentials(t *testing.T) {
	t.Setenv("SQUAD_GROK_REVIEW_CONFIG", "")
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--config", configPath, "--provider", "local-git", "--repository-host", "git.example.test", "--repo", "ipt/interceptor", "--worktree", t.TempDir(), "--base-sha", strings.Repeat("a", 40), "--description-file", filepath.Join(t.TempDir(), "contract.md")}
	cfg, err := parseConfig(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.appID != 0 || cfg.pullRequest != 0 || !strings.Contains(cfg.statusDir, "local-git") {
		t.Fatal("local mode acquired remote identity")
	}
	for _, extra := range [][]string{{"--pr", "1"}, {"--mode", "required"}, {"--base-sha", "master"}} {
		if _, err = parseConfig(append(append([]string{}, args...), extra...), io.Discard); err == nil {
			t.Fatal("accepted invalid local mode")
		}
	}
}

func TestParseConfigSupportsExplicitRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(path, []byte(`{"app_id":1,"installation_id":2,"app_private_key":"/key"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err := parseConfig([]string{"recover", "--from", "0123456789abcdef", "--config", path, "--repo", "owner/repo", "--pr", "9", "--mode", "required"}, &out)
	if err != nil {
		t.Fatalf("supported recovery operation rejected: %v", err)
	}
}

func TestParseConfigRejectsRecoveryModeBypass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(path, []byte(`{"app_id":1,"installation_id":2,"app_private_key":"/key"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"recover", "--from", "original", "--repo", "owner/repo", "--pr", "9", "--mode", "unknown"},
		{"--from", "original", "--repo", "owner/repo", "--pr", "9", "--mode", "required"},
	} {
		if _, err := parseConfig(append(args, "--config", path), io.Discard); err == nil {
			t.Fatal("unsupported recovery bypass accepted")
		}
	}
}

func TestParseConfigShadowAndProspectivePreserveOriginalMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(path, []byte(`{"app_id":1,"installation_id":2,"app_private_key":"/key"}`), 0600); err != nil {
		t.Fatal(err)
	}

	for _, suffix := range []string{"recover", "reconcile", "doctor"} {
		if _, err := parseConfig([]string{"readmit", suffix, "--from", "/absolute/prospective.json", "--config", path, "--repo", "owner/repo", "--pr", "9", "--mode", "shadow"}, io.Discard); err == nil {
			t.Fatal("combined operation admitted", suffix)
		}
	}
	for _, operation := range []string{"recover", "reconcile", "readmit"} {
		from := "original"
		if operation == "readmit" {
			from = filepath.Join(t.TempDir(), "prospective.json")
		}
		configuration, err := parseConfig([]string{operation, "--from", from, "--config", path, "--repo", "owner/repo", "--pr", "9", "--mode", "shadow"}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if configuration.mode != "shadow" {
			t.Fatal("mode changed")
		}
	}
	if _, err := parseConfig([]string{"readmit", "--from", "original", "--config", path, "--repo", "owner/repo", "--pr", "9", "--mode", "shadow"}, io.Discard); err == nil {
		t.Fatal("readmit accepted attempt ID instead of explicit new-input receipt")
	}
}

func TestAuthorizationReadbackUsesOnlyExactLocalReceipt(t *testing.T) {
	t.Setenv("SQUAD_AGENT", "worker")
	t.Setenv("CODEX_THREAD_ID", "native")
	t.Setenv("SQUAD_SESSION_ID", "codex:native")
	t.Setenv("SQUAD_GROK_REVIEW_CONFIG", "/nonexistent/no-provider-config")
	scope := grokreview.ReviewDisclosure{SchemaVersion: grokreview.ReviewDisclosureSchema, Owner: grokreview.ReviewOwner{Actor: "worker", Native: "native"}, Identity: grokreview.ReviewIdentity{Repository: "owner/repo", PR: 92, BaseRef: "main", BaseSHA: "base", HeadSHA: "head"}, Mode: "required", Operation: "managed_review", Provider: "grok", Content: "source_diff_and_review_contract"}
	raw, _ := json.Marshal(scope)
	path := filepath.Join(t.TempDir(), "scope.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"authorization-readback", "--request", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("readback initialized provider: %d %s", code, stderr.String())
	}
	var result grokreview.DisclosureReadback
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Status != "unavailable" || result.Sampled || result.Published {
		t.Fatalf("invented permission: %#v %v", result, err)
	}
	t.Setenv("SQUAD_SESSION_ID", "codex:foreign")
	if code := run(context.Background(), []string{"authorization-readback", "--request", path}, &stdout, &stderr); code == 0 {
		t.Fatal("mismatched native accepted")
	}
}

func TestParseExplicitSameExecutionCompletion(t *testing.T) {
	base := []string{"--repo", "owner/repo", "--pr", "9", "--app-id", "1", "--installation-id", "2", "--app-private-key", "/key", "--grok-bin", "/never-grok", "--gh-bin", "/never-gh"}
	valid := append([]string{"complete", "--from", "0123456789abcdef", "--completion-custody", "/private/custody.json"}, base...)
	var out bytes.Buffer
	c, err := parseConfig(valid, &out)
	if err != nil || !c.completion || c.recovery || c.reconcile {
		t.Fatal("explicit complete invalid", err)
	}
	for _, prefix := range [][]string{{"complete"}, {"complete", "--from", "invalid", "--completion-custody", "/proof"}, {"complete", "--from", "0123456789abcdef", "--completion-custody", "relative"}, {"--completion-evidence", "/proof"}, {"reconcile", "--from", "0123456789abcdef", "--completion-custody", "/proof"}} {
		if _, err := parseConfig(append(prefix, base...), &out); err == nil {
			t.Fatal("incomplete/ambiguous operation accepted", prefix)
		}
	}
}

func TestCompletionNativeAndTargetRejectBeforeAnyProvider(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("SQUAD_AGENT", "worker")
	t.Setenv("SQUAD_SESSION_ID", "codex:native")
	t.Setenv("CODEX_THREAD_ID", "native")
	for _, name := range []string{"native", "target", "missing-ledger", "public-proof"} {
		t.Run(name, func(t *testing.T) {
			c := grokreview.CompletionCustody{SchemaVersion: "squad.review-completion.custody.v1", Owner: grokreview.ReviewOwner{Actor: "worker", Native: "native"}}
			c.Disclosure.Identity.Repository = "owner/repo"
			c.Disclosure.Identity.PR = 9
			if name == "native" {
				c.Owner.Native = "foreign"
			}
			if name == "target" {
				c.Disclosure.Identity.PR = 10
			}
			raw, _ := json.Marshal(c)
			path := filepath.Join(dir, name+".json")
			mode := os.FileMode(0600)
			if name == "public-proof" {
				mode = 0644
			}
			if err := os.WriteFile(path, raw, mode); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			cfg := config{repository: "owner/repo", pullRequest: 9, completionCustodyPath: path, grokBinary: "/must-not-execute", githubBinary: "/must-not-execute", admissionDir: filepath.Join(dir, "must-not-create")}
			if code := runCompletion(context.Background(), cfg, &out, &stderr); code != 1 || out.Len() != 0 || strings.Contains(stderr.String(), "must-not-execute") {
				t.Fatal("provider reached before provenance", code, stderr.String())
			}
			if _, err := os.Stat(cfg.admissionDir); !os.IsNotExist(err) {
				t.Fatal("unqualified completion opened admission store", err)
			}
		})
	}
}

func TestHistoricalPublicationReceiptCannotApproveCurrentInput(t *testing.T) {
	result := grokreview.CompletionResult{Attempt: "0123456789abcdef", Publication: grokreview.Publication{CheckRunID: 100}, Verdict: grokreview.VerdictApproved, OriginalInput: grokreview.ReviewIdentity{Repository: "owner/repo", PR: 9, HeadSHA: "original-head"}, CurrentInputMatches: false}
	var out, stderr bytes.Buffer
	if code := writeCompletionResult(result, fmt.Errorf("original publication joined; current input is stale"), &out, &stderr); code != 1 {
		t.Fatal("historical publication approved stale input", code)
	}
	var receipt grokreview.CompletionResult
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt.CurrentInputMatches || receipt.OriginalInput.HeadSHA != "original-head" || receipt.Publication.CheckRunID != 100 || receipt.Sampled {
		t.Fatal("historical publication receipt lost attribution", err)
	}
}

func TestParseHumanRestartUsesCanonicalConfiguredStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	canonical := filepath.Join(dir, "canonical")
	raw, _ := json.Marshal(localConfig{AppID: 1, InstallationID: 2, AppPrivateKey: "/key", AdmissionDir: canonical})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"restart", "--config", path, "--repo", "owner/repo", "--pr", "9", "--human-grant", filepath.Join(dir, "grant.json")}
	c, err := parseConfig(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !c.humanRestart || c.admissionDir != canonical {
		t.Fatal("restart missed canonical config")
	}
	for _, extra := range [][]string{{"--admission-dir", filepath.Join(dir, "other")}, {"--from", "old"}, {"--completion-custody", filepath.Join(dir, "custody")}} {
		if _, err := parseConfig(append(append([]string(nil), args...), extra...), io.Discard); err == nil {
			t.Fatal("conflicting restart bypass accepted", extra)
		}
	}
	if _, err := parseConfig(append([]string(nil), args[1:]...), io.Discard); err == nil {
		t.Fatal("grant on ordinary invocation accepted")
	}
	if _, err := parseConfig(args[:len(args)-2], io.Discard); err == nil {
		t.Fatal("missing grant accepted")
	}
}
