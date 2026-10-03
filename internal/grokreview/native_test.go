package grokreview

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeReviewerRejectsUnknownBackend(t *testing.T) {
	_, err := NewCLIRunner(CLIConfig{Backend: "unknown", Binary: "/bin/false", HomeDir: t.TempDir(), Model: "model", Timeout: time.Second, MaxOutputBytes: 4096, Core: "core", Policy: "policy"})
	if err == nil {
		t.Fatal("unknown backend admitted")
	}
}

func TestNativeReviewerParsersRequireTerminalAndRejectTools(t *testing.T) {
	verdict := `{"schema_version":"squad.review.findings.v2","verdict":"approved","summary":"No defects.","findings":[]}`
	fixtures := map[string]string{
		"claude": `{"type":"result","subtype":"success","is_error":false,"session_id":"native","structured_output":` + verdict + `,"modelUsage":{"selected":{}}}`,
		"codex":  "{\"type\":\"thread.started\",\"thread_id\":\"native\"}\n" + `{"type":"item.completed","item":{"type":"agent_message","text":` + quoteTest(verdict) + `}}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":3}}`,
		"muse":   `{"stream":{"kind":"session","id":"native"},"payload_type":"run.model.configured","payload":{"model_id":"selected"}}` + "\n" + `{"stream":{"kind":"session","id":"native"},"payload_type":"run.terminal.completed","payload":{"terminal":"completed","text":` + quoteTest(verdict) + `}}`,
	}
	for backend, raw := range fixtures {
		t.Run(backend, func(t *testing.T) {
			got, audit, err := parseNativeOutput(backend, []byte(raw))
			if err != nil || got.Verdict != VerdictApproved || audit.SessionID != "native" {
				t.Fatalf("%+v %+v %v", got, audit, err)
			}
			if _, _, err = parseNativeOutput(backend, []byte(verdict)); err == nil {
				t.Fatal("bare verdict accepted without native completion")
			}
			if _, _, err = parseNativeOutput(backend, []byte(raw+"\n"+`{"type":"item.completed","item":{"type":"command_execution"}}`)); err == nil {
				t.Fatal("tool event accepted")
			}
		})
	}
}

func quoteTest(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

func TestNativeReviewRunsIsolatedAndRecordsJoin(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := `#!/bin/sh
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"native","structured_output":{"schema_version":"squad.review.findings.v2","verdict":"approved","summary":"No defects.","findings":[]},"modelUsage":{"selected":{}}}'
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := NewCLIRunner(CLIConfig{Backend: "claude", Binary: bin, HomeDir: dir, Model: "selected", Core: "core", Policy: "policy", Timeout: time.Second, MaxOutputBytes: 8192})
	if err != nil {
		t.Fatal(err)
	}
	result, audit, err := runner.Review(context.Background(), []byte(`{"diff":"test"}`))
	if err != nil || result.Verdict != VerdictApproved || audit.Backend != "claude" || audit.Terminal == nil || !audit.Terminal.ChildWaited {
		t.Fatalf("%+v %+v %v", result, audit, err)
	}
}

// Opt in explicitly; this uses a synthetic snapshot and never a business session.
func TestNativeReviewerQualification(t *testing.T) {
	binary := os.Getenv("SQUAD_NATIVE_REVIEW_BINARY")
	if binary == "" {
		t.Skip("native reviewer not selected")
	}
	backend, model := os.Getenv("SQUAD_NATIVE_REVIEW_BACKEND"), os.Getenv("SQUAD_NATIVE_REVIEW_MODEL")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewCLIRunner(CLIConfig{Backend: backend, ProviderEnvironment: ProviderEnvironment(backend), Binary: binary, HomeDir: home, Model: model, ReasoningEffort: "low", Core: "Review the supplied patch only. No tools. Return approved with no findings when there is no defect.", Policy: "Return only squad.review.findings.v2 JSON.", Timeout: 90 * time.Second, MaxOutputBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	prepared, err := runner.prepare(ctx, []byte(`{"schema_version":"squad.local-review.request.v1","repository":"fixture/example","pull_request":1,"base_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","head_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","diff":"diff --git a/readme.txt b/readme.txt\n--- a/readme.txt\n+++ b/readme.txt\n@@ -1 +1 @@\n-helo\n+hello\n"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.cleanup()
	var stdout, stderr bytes.Buffer
	prepared.command.Stdout = &stdout
	prepared.command.Stderr = &stderr
	err = prepared.command.Run()
	if evidence := os.Getenv("SQUAD_NATIVE_REVIEW_EVIDENCE"); evidence != "" {
		if err := os.WriteFile(evidence+".stdout", stdout.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(evidence+".stderr", stderr.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err != nil {
		t.Fatalf("native invocation: %v (private diagnostic retained when selected)", err)
	}
	result, audit, err := parseNativeOutput(backend, stdout.Bytes())
	audit.Terminal = &TerminalDiagnostics{ChildWaited: true}

	t.Logf("backend=%s verdict=%s native=%t joined=%t failure=%s", backend, result.Verdict, audit.SessionID != "", audit.Terminal != nil && audit.Terminal.ChildWaited, audit.FailureKind)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictApproved {
		t.Fatal("synthetic spelling patch did not approve")
	}
}

func TestBackendCannotBypassConsumedReview(t *testing.T) {
	dir := t.TempDir()
	seedTimeout(t, dir)
	for _, backend := range []string{"claude", "codex", "muse"} {
		settings := admissionSettings()
		settings.Backend = backend
		settings.Model = "other-model"
		if err := openTestAdmission(t, dir, "", settings).Start(context.Background(), admissionBundle()); err == nil {
			t.Fatal("changing backend erased consumption")
		}
	}
}

func TestProviderEnvironmentNeverLeaksForeignCredentials(t *testing.T) {
	t.Setenv("GH_TOKEN", "github-secret")
	t.Setenv("META_API_KEY", "muse-secret")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "claude-secret")
	for _, backend := range []string{"grok", "codex", "claude", "muse"} {
		env := ProviderEnvironment(backend)
		if _, ok := env["GH_TOKEN"]; ok {
			t.Fatal("GitHub token inherited")
		}
		if backend != "muse" && env["META_API_KEY"] != "" {
			t.Fatal("foreign token inherited")
		}
		if backend != "claude" && env["ANTHROPIC_AUTH_TOKEN"] != "" {
			t.Fatal("foreign token inherited")
		}
	}
}

func TestNativeSchemaSatisfiesStrictRequiredProperties(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(nativeFindingsSchema()), &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	if props["schema_version"].(map[string]any)["type"] != "string" {
		t.Fatal("const lacks type")
	}
	finding := props["findings"].(map[string]any)["items"].(map[string]any)
	if len(finding["required"].([]any)) != len(finding["properties"].(map[string]any)) {
		t.Fatal("native schema has optional properties")
	}
}

func TestNativeReviewTimeoutAndOutputLimitCannotApprove(t *testing.T) {
	for _, test := range []struct {
		name, script string
		timeout      time.Duration
		limit        int
		kind         CLIFailureKind
	}{
		{"timeout", "#!/bin/sh\nexec sleep 1\n", 10 * time.Millisecond, 1024, CLIFailureTimeout},
		{"overflow", "#!/bin/sh\nprintf '%02000d' 0\n", time.Second, 64, CLIFailureOutputTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "client")
			if err := os.WriteFile(bin, []byte(test.script), 0700); err != nil {
				t.Fatal(err)
			}
			runner, err := NewCLIRunner(CLIConfig{Backend: "claude", Binary: bin, HomeDir: dir, Model: "selected", Core: "core", Policy: "policy", Timeout: test.timeout, MaxOutputBytes: test.limit})
			if err != nil {
				t.Fatal(err)
			}
			result, audit, err := runner.Review(context.Background(), []byte(`{"diff":"test"}`))
			if err == nil || result.Verdict == VerdictApproved || audit.FailureKind != test.kind || audit.Terminal == nil || !audit.Terminal.ChildWaited {
				t.Fatalf("%+v %+v %v", result, audit, err)
			}
		})
	}
}
