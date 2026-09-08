package grokreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func approvedEnvelope(t *testing.T) []byte {
	t.Helper()
	structured := FindingsResult{
		SchemaVersion: FindingsSchemaVersion,
		Verdict:       VerdictApproved,
		Summary:       "No blocking findings.",
		Findings:      []Finding{},
	}
	structuredJSON, err := json.Marshal(structured)
	if err != nil {
		t.Fatal(err)
	}
	envelope := map[string]any{
		"text":       string(structuredJSON),
		"stopReason": "end_turn",
		"sessionId":  "session-1",
		"requestId":  "request-1",
		"thought":    "discard this and never expose it in audit output",
		"usage": map[string]any{
			"input_tokens":            10,
			"cache_read_input_tokens": 2,
			"output_tokens":           4,
			"reasoning_tokens":        1,
			"total_tokens":            17,
		},
		"num_turns":      1,
		"total_cost_usd": 0.01,
		"modelUsage": map[string]any{
			"grok-review-model": map[string]any{"modelCalls": 1},
		},
		"structuredOutput": structured,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testCLIConfig(t *testing.T) CLIConfig {
	t.Helper()
	return CLIConfig{
		Binary:          "/opt/reviewer/bin/grok",
		HomeDir:         filepath.Join(t.TempDir(), "home"),
		Model:           "grok-review-model",
		ReasoningEffort: "high",
		Timeout:         time.Minute,
		MaxOutputBytes:  1 << 20,
		SandboxProfile:  "reviewer-readonly",
		Core:            "trusted core",
		Policy:          "trusted policy",
	}
}

func TestCLICommandIsOneShotNoToolAndEnvironmentIsAllowlisted(t *testing.T) {
	config := testCLIConfig(t)
	config.SafeEnvironment = map[string]string{
		"SSL_CERT_FILE": "/etc/ssl/cert.pem",
		"LANG":          "C.UTF-8",
	}
	runner, err := NewCLIRunner(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "must-not-leak")
	t.Setenv("GITHUB_TOKEN", "must-not-leak")
	t.Setenv("XAI_API_KEY", "must-not-leak")

	frozenBundle := []byte(`{"bundle":"data"}`)
	prepared, err := runner.prepare(context.Background(), frozenBundle)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.cleanup()

	wantArgs := []string{
		config.Binary,
		"--prompt-file", prepared.promptPath,
		"--json-schema", FindingsJSONSchema,
		"--output-format", "json",
		"--no-subagents",
		"--disable-web-search",
		"--permission-mode", "dontAsk",
		"--no-plan",
		"--sandbox", config.SandboxProfile,
		"--tools", "",
		"--max-turns", "1",
		"--model", config.Model,
		"--reasoning-effort", config.ReasoningEffort,
		"--cwd", prepared.workDir,
		"--system-prompt-override=trusted core\n\ntrusted policy",
		"--verbatim",
	}
	if !reflect.DeepEqual(prepared.command.Args, wantArgs) {
		t.Fatalf("args:\n got %#v\nwant %#v", prepared.command.Args, wantArgs)
	}
	wantEnv := []string{
		"HOME=" + config.HomeDir,
		"LANG=C.UTF-8",
		"SSL_CERT_FILE=/etc/ssl/cert.pem",
		"TMPDIR=" + prepared.workDir,
	}
	if !reflect.DeepEqual(prepared.command.Env, wantEnv) {
		t.Fatalf("env:\n got %#v\nwant %#v", prepared.command.Env, wantEnv)
	}
	for _, entry := range prepared.command.Env {
		if strings.HasPrefix(entry, "GITHUB_") || strings.HasPrefix(entry, "XAI_") {
			t.Fatalf("secret environment leaked: %q", entry)
		}
	}
	info, err := os.Stat(prepared.promptPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("prompt mode = %o", info.Mode().Perm())
	}
	if filepath.Ext(prepared.promptPath) != ".txt" {
		t.Fatalf("prompt path = %q; .json makes Grok parse it as an ACP envelope", prepared.promptPath)
	}
	prompt, err := os.ReadFile(prepared.promptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prompt, frozenBundle) {
		t.Fatalf("prompt content = %q", prompt)
	}
}

func TestPreparePreservesLargeJSONBundleAsTextPrompt(t *testing.T) {
	config := testCLIConfig(t)
	runner, err := NewCLIRunner(config)
	if err != nil {
		t.Fatal(err)
	}
	bundle := []byte(`{"diff":"` + strings.Repeat("x", 83_077) + `"}`)
	prepared, err := runner.prepare(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.cleanup()
	if filepath.Ext(prepared.promptPath) != ".txt" {
		t.Fatalf("prompt path = %q", prepared.promptPath)
	}
	prompt, err := os.ReadFile(prepared.promptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prompt, bundle) {
		t.Fatalf("large prompt changed: got %d bytes, want %d", len(prompt), len(bundle))
	}
}

func TestCLIRunnerParsesStructuredOutputAndAudit(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-grok")
	script := "#!/bin/sh\nprintf '%s' '" + strings.ReplaceAll(string(approvedEnvelope(t)), "'", "'\\''") + "'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config := testCLIConfig(t)
	config.Binary = binary
	config.SandboxProfile = ""
	runner, err := NewCLIRunner(config)
	if err != nil {
		t.Fatal(err)
	}

	result, audit, err := runner.Review(context.Background(), []byte(`{"bundle":"data"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictApproved || result.Summary != "No blocking findings." {
		t.Fatalf("result = %#v", result)
	}
	if audit.RequestID != "request-1" || audit.SessionID != "session-1" ||
		audit.RequestedModel != config.Model || audit.ResolvedModel != config.Model {
		t.Fatalf("audit = %#v", audit)
	}
	if audit.Usage.TotalTokens != 17 || audit.NumTurns != 1 {
		t.Fatalf("audit usage = %#v", audit)
	}
}

func TestCLIRunnerClassifiesFailureWithoutExposingStderr(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-grok")
	script := "#!/bin/sh\nprintf '%s\\n' 'Error: /tmp/private-review-bundle.json: JSON object must have a \"type\" field (private-source-marker)' >&2\nexit 2\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config := testCLIConfig(t)
	config.Binary = binary
	config.SandboxProfile = ""
	runner, err := NewCLIRunner(config)
	if err != nil {
		t.Fatal(err)
	}

	_, audit, err := runner.Review(context.Background(), []byte(`{"bundle":"data"}`))
	if err == nil {
		t.Fatal("expected Grok CLI failure")
	}
	if audit.FailureKind != CLIFailurePromptFileFormat || audit.StderrSHA256 == "" {
		t.Fatalf("audit = %#v", audit)
	}
	if !strings.Contains(err.Error(), "kind=prompt_file_format") || !strings.Contains(err.Error(), "stderr_sha256=") {
		t.Fatalf("error lacks safe diagnostics: %v", err)
	}
	for _, forbidden := range []string{"private-source-marker", "/tmp/private-review-bundle.json", "JSON object must have"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error exposed stderr %q: %v", forbidden, err)
		}
	}
}

func TestClassifyCLIFailurePrioritizesInvalidArgumentsOverOAuthHelp(t *testing.T) {
	stderr := []byte("error: unexpected argument '--old-flag'\nUsage: grok [OPTIONS]\n  --oauth  Log in with OAuth\n")
	if got := classifyCLIFailure(stderr); got != CLIFailureInvalidArguments {
		t.Fatalf("failure kind = %q, want %q", got, CLIFailureInvalidArguments)
	}
}

func TestClassifyCLIFailureRecognizesLocalSessionPermissions(t *testing.T) {
	stderr := []byte(`Couldn't create session: Permission denied: {"code":"FS_PERMISSION_DENIED"}`)
	if got := classifyCLIFailure(stderr); got != CLIFailureLocalPermissions {
		t.Fatalf("failure kind = %q, want %q", got, CLIFailureLocalPermissions)
	}
}

func TestCLIRunnerDoctorChecksWritableStateCLIContractAndModel(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(filepath.Join(home, ".grok", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "fake-grok")
	script := `#!/bin/sh
set -eu
case "$1" in
  version)
    printf '%s\n' 'grok 1.0.13 (test) [stable]'
    ;;
  --help)
    printf '%s\n' '--prompt-file --json-schema --output-format --no-subagents --disable-web-search --permission-mode --no-plan --tools --max-turns --model --reasoning-effort --cwd --system-prompt-override --verbatim'
    ;;
  models)
    if [ "${GITHUB_TOKEN+x}" = x ] || [ "${GITHUB_APP_PRIVATE_KEY+x}" = x ]; then
      printf '%s\n' 'secret leaked' >&2
      exit 9
    fi
    printf '%s\n' 'You are logged in with grok.com.' 'Default model: grok-4.6' 'Available models:' '  * grok-4.6 (default)' '  - grok-4.5'
    ;;
  --prompt-file)
    found_override=false
    for argument in "$@"; do
      case "$argument" in
        --system-prompt-override=*) found_override=true ;;
        --system-prompt-override) printf '%s\n' 'separate system prompt argument' >&2; exit 7 ;;
      esac
    done
    if [ "$found_override" != true ]; then
      printf '%s\n' 'missing system prompt override' >&2
      exit 7
    fi
    printf '%s\n' 'Error: Failed to read doctor-missing-review-bundle.txt: No such file' >&2
    exit 2
    ;;
  *)
    exit 8
    ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config := testCLIConfig(t)
	config.Binary = binary
	config.HomeDir = home
	config.Model = "grok-4.6"
	config.SandboxProfile = ""
	runner, err := NewCLIRunner(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_TOKEN", "must-not-leak")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "must-not-leak")

	health, err := runner.Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.Version != "grok 1.0.13 (test) [stable]" || health.Model != "grok-4.6" {
		t.Fatalf("health = %#v", health)
	}
}

func TestCLIRunnerDoctorRejectsUnavailableConfiguredModel(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(filepath.Join(home, ".grok", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "fake-grok")
	script := `#!/bin/sh
case "$1" in
  version) printf '%s\n' 'grok 1.0.13' ;;
  --help) printf '%s\n' '--prompt-file --json-schema --output-format --no-subagents --disable-web-search --permission-mode --no-plan --tools --max-turns --model --reasoning-effort --cwd --system-prompt-override --verbatim' ;;
  models) printf '%s\n' 'Available models:' '  - grok-4.5' ;;
  --prompt-file) printf '%s\n' 'Error: Failed to read doctor-missing-review-bundle.txt: No such file' >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config := testCLIConfig(t)
	config.Binary = binary
	config.HomeDir = home
	config.Model = "grok-4.6"
	config.SandboxProfile = ""
	runner, err := NewCLIRunner(config)
	if err != nil {
		t.Fatal(err)
	}

	_, err = runner.Doctor(context.Background())
	if err == nil || !strings.Contains(err.Error(), `configured model "grok-4.6" is unavailable`) {
		t.Fatalf("error = %v", err)
	}
}

func TestCLIRunnerDoctorExplainsSandboxedStateDirectory(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".grok"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := testCLIConfig(t)
	config.HomeDir = home
	runner, err := NewCLIRunner(config)
	if err != nil {
		t.Fatal(err)
	}

	_, err = runner.Doctor(context.Background())
	if err == nil || !strings.Contains(err.Error(), "outside the Codex sandbox") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseCLIEnvelopeRejectsTextThatDiffersFromStructuredOutput(t *testing.T) {
	var envelope map[string]any
	if err := json.Unmarshal(approvedEnvelope(t), &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["text"] = `{"schema_version":"squad.review.findings.v2","verdict":"blocking","summary":"different","findings":[]}`
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseCLIEnvelope(raw); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseCLIEnvelopeRejectsUnknownFindingFields(t *testing.T) {
	structured := `{"schema_version":"squad.review.findings.v2","verdict":"approved","summary":"ok","findings":[],"authority":"forged"}`
	envelope := `{"text":` + strconvQuote(structured) + `,"stopReason":"end_turn","sessionId":"s","requestId":"r","usage":{"total_tokens":1},"num_turns":1,"modelUsage":{"m":{"modelCalls":1}},"structuredOutput":` + structured + `}`
	if _, _, err := ParseCLIEnvelope([]byte(envelope)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseCLIEnvelopeRejectsModelOperationalVerdict(t *testing.T) {
	structured := `{"schema_version":"squad.review.findings.v2","verdict":"error","summary":"inspect later","findings":[]}`
	envelope := `{"text":` + strconvQuote(structured) + `,"stopReason":"end_turn","sessionId":"s","requestId":"r","usage":{"total_tokens":1},"num_turns":1,"modelUsage":{"m":{"modelCalls":1}},"structuredOutput":` + structured + `}`
	if _, _, err := ParseCLIEnvelope([]byte(envelope)); err == nil || !strings.Contains(err.Error(), "reserved operational verdict") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateFindingsRejectsApprovedWithBlockingFinding(t *testing.T) {
	result := FindingsResult{
		SchemaVersion: FindingsSchemaVersion,
		Verdict:       VerdictApproved,
		Summary:       "looks fine",
		Findings: []Finding{{
			Category: "correctness", Severity: "high", Blocking: true,
			Path: "main.go", Line: 10, Title: "bad", Body: "broken",
		}},
	}
	if err := ValidateFindings(result); err == nil || !strings.Contains(err.Error(), "approved") {
		t.Fatalf("error = %v", err)
	}
}

func strconvQuote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
