package grokreview

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const terminalTestID = "11111111-2222-3333-4444-555555555555"
const terminalTestRequest = "66666666-7777-8888-9999-aaaaaaaaaaaa"

func terminalFixture(t *testing.T) (*CLIRunner, string, []byte, time.Time, string, string) {
	t.Helper()
	config := testCLIConfig(t)
	runner, err := NewCLIRunner(config)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	work, err = filepath.EvalSymlinks(work)
	if err != nil {
		t.Fatal(err)
	}
	input := admissionBundle()
	started := time.Now().UTC().Truncate(time.Millisecond)
	index := filepath.Join(config.HomeDir, ".grok", "sessions", strings.ReplaceAll(url.QueryEscape(work), "+", "%20"))
	session := filepath.Join(index, terminalTestID)
	if err = os.MkdirAll(session, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, v any) {
		t.Helper()
		raw, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write(filepath.Join(index, "prompt_history.jsonl"), map[string]any{"prompt": string(input), "session_id": terminalTestID, "timestamp": started.Format(time.RFC3339Nano), "is_bash": false})
	write(filepath.Join(session, "summary.json"), map[string]any{"request_id": terminalTestRequest, "current_model_id": config.Model, "reasoning_effort": config.ReasoningEffort, "session_summary": "private-reasoning-must-not-be-exported"})
	events := []map[string]any{
		{"ts": started.Format(time.RFC3339Nano), "type": "turn_started", "session_id": terminalTestID, "model_id": config.Model},
		{"ts": started.Add(time.Second).Format(time.RFC3339Nano), "type": "first_token"},
		{"ts": started.Add(time.Second).Format(time.RFC3339Nano), "type": "phase_changed", "phase": "streaming_reasoning"},
		{"ts": started.Add(2 * time.Second).Format(time.RFC3339Nano), "type": "phase_changed", "phase": "streaming_reasoning"},
	}
	var raw []byte
	for _, e := range events {
		b, _ := json.Marshal(e)
		raw = append(raw, b...)
		raw = append(raw, '\n')
	}
	if err = os.WriteFile(filepath.Join(session, "events.jsonl"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return runner, work, input, started, index, session
}

func TestTerminalSessionExactInputQualifiesSafeProgress(t *testing.T) {
	runner, work, input, started, _, _ := terminalFixture(t)
	result, state := runner.terminalSession(work, input, started, started.Add(3*time.Second))
	if state != "qualified_local" || result == nil || result.SessionID != terminalTestID || result.LocalRequestID != terminalTestRequest || result.ReasoningNotifications != 2 || result.FirstTokenAt == nil || result.LastReasoningAt == nil {
		t.Fatalf("state=%s result=%#v", state, result)
	}
	raw, _ := json.Marshal(result)
	for _, secret := range []string{string(input), "private-reasoning", "prompt", "thought", "usage", "verdict"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("diagnostics exposed %s", secret)
		}
	}
}

func TestTerminalSessionRejectsUnqualifiedEvidence(t *testing.T) {
	for _, kind := range []string{"wrong-input", "bash-history", "null-history", "wrong-session", "wrong-model", "wrong-effort", "duplicate-id", "truncated", "unknown-event", "private-event-field", "wrong-event-session", "old-time", "out-of-order", "symlink-file", "symlink-directory", "ambiguous", "oversized", "too-many-events", "missing"} {
		t.Run(kind, func(t *testing.T) {
			runner, work, input, started, index, session := terminalFixture(t)
			eventPath := filepath.Join(session, "events.jsonl")
			raw, err := os.ReadFile(eventPath)
			if err != nil {
				t.Fatal(err)
			}
			write := func(p string, v []byte) {
				t.Helper()
				if err := os.WriteFile(p, v, 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "wrong-input":
				input = append(input, ' ')
			case "bash-history", "null-history":
				p := filepath.Join(index, "prompt_history.jsonl")
				b, _ := os.ReadFile(p)
				value := "true"
				if kind == "null-history" {
					value = "null"
				}
				write(p, []byte(strings.ReplaceAll(string(b), `"is_bash":false`, `"is_bash":`+value)))

			case "wrong-session":
				p := filepath.Join(index, "prompt_history.jsonl")
				b, _ := os.ReadFile(p)
				write(p, []byte(strings.ReplaceAll(string(b), terminalTestID, terminalTestRequest)))
			case "wrong-model", "wrong-effort":
				p := filepath.Join(session, "summary.json")
				b, _ := os.ReadFile(p)
				old := runner.config.Model
				if kind == "wrong-effort" {
					old = runner.config.ReasoningEffort
				}
				write(p, []byte(strings.ReplaceAll(string(b), old, "other")))
			case "duplicate-id":
				p := filepath.Join(session, "summary.json")
				b, _ := os.ReadFile(p)
				write(p, append([]byte(`{"request_id":"`+terminalTestRequest+`",`), b[1:]...))
			case "truncated":
				write(eventPath, raw[:len(raw)-5])
			case "unknown-event":
				write(eventPath, []byte(strings.ReplaceAll(string(raw), "first_token", "unknown")))
			case "private-event-field":
				write(eventPath, []byte(strings.ReplaceAll(string(raw), `"type":"first_token"`, `"thought":"private-hidden-thought","type":"first_token"`)))
			case "wrong-event-session":
				write(eventPath, []byte(strings.ReplaceAll(string(raw), terminalTestID, terminalTestRequest)))
			case "old-time":
				started = started.Add(time.Hour)
			case "out-of-order":
				lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
				lines[2], lines[3] = lines[3], lines[2]
				write(eventPath, []byte(strings.Join(lines, "\n")))
			case "symlink-file":
				target := filepath.Join(t.TempDir(), "events")
				write(target, raw)
				if err := os.Remove(eventPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, eventPath); err != nil {
					t.Fatal(err)
				}
			case "symlink-directory":
				target := filepath.Join(t.TempDir(), "session")
				if err := os.Rename(session, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, session); err != nil {
					t.Fatal(err)
				}
			case "ambiguous":
				if err := os.Mkdir(filepath.Join(index, terminalTestRequest), 0700); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				write(eventPath, make([]byte, (4<<20)+1))
			case "too-many-events":
				lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
				write(eventPath, []byte(lines[0]+"\n"+strings.Repeat(lines[1]+"\n", 20001)))
			case "missing":
				if err := os.Remove(eventPath); err != nil {
					t.Fatal(err)
				}
			}
			result, state := runner.terminalSession(work, input, started, started.Add(3*time.Second))
			if result != nil || state == "qualified_local" {
				t.Fatalf("accepted %s: %s %#v", kind, state, result)
			}
		})
	}
}

func TestTerminalDiagnosticsPersistWithoutRefundingConsumedRoot(t *testing.T) {
	dir := t.TempDir()
	parent := seedTimeout(t, dir)
	a := openTestAdmission(t, dir, parent, admissionSettings())
	if err := a.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	report := timeoutReport()
	report.Audit.Terminal = &TerminalDiagnostics{InputSHA256: a.receipt.Identity.BundleSHA256, ChildStarted: true, ChildWaited: true, DeadlineProducer: "reviewer_timeout", SessionEvidence: "qualified_local", Session: &TerminalSession{SessionID: terminalTestID, LocalRequestID: terminalTestRequest, ReasoningNotifications: 2}}
	if err := a.Checkpoint(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if err := a.Finish(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	receipt, err := a.load(context.Background(), a.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(receipt.Terminal, report.Audit.Terminal) || !receipt.Joined || receipt.UsageKnown || receipt.CostKnown || receipt.Verdict != VerdictError {
		t.Fatalf("receipt=%#v", receipt)
	}
	for _, from := range []string{parent, a.AttemptID(), ""} {
		if err := openTestAdmission(t, dir, from, admissionSettings()).Start(context.Background(), admissionBundle()); err == nil {
			t.Fatalf("diagnostics refunded root for %s", from)
		}
	}
	writer, err := NewReviewStatusWriter(filepath.Join(t.TempDir(), "status"), "required", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Observe(ReviewObservation{State: ReviewStateError, Snapshot: PullRequestSnapshot{Repository: "owner/repo", Number: 9}, Audit: report.Audit}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(writer.path)
	if err != nil {
		t.Fatal(err)
	}
	var status ReviewStatus
	if err = json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(status.Terminal, receipt.Terminal) {
		t.Fatal("status lost terminal evidence")
	}
}

func TestTerminalOutputFingerprintIncludesOverflowBytes(t *testing.T) {
	buffer := cappedBuffer{limit: 4}
	input := []byte("private-complete-output")
	if n, err := buffer.Write(input); err != nil || n != len(input) {
		t.Fatalf("write=%d %v", n, err)
	}
	sum := sha256.Sum256(input)
	if !buffer.overflow || buffer.total != int64(len(input)) || buffer.digest() != fmt.Sprintf("%x", sum) || len(buffer.Bytes()) != 4 {
		t.Fatal("truncated fingerprint misrepresented as complete")
	}
}

func TestCLIRunnerJoinedTimeoutCapturesOnlyItsRetainedSession(t *testing.T) {
	runner, _, input, _, oldIndex, session := terminalFixture(t)
	ready := filepath.Join(t.TempDir(), "ready")
	binary := filepath.Join(t.TempDir(), "fake-grok")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'private-stdout'\nprintf 'private-stderr' >&2\nprintf '%%s' \"$PWD\" > '%s'\nexec /bin/sleep 30\n", strings.ReplaceAll(ready, "'", "'\\''"))
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	runner.config.Binary = binary
	runner.config.Timeout = 3 * time.Second
	runner.SetProcessObserver(func(ctx context.Context, _ int) error {
		limit := time.NewTimer(2 * time.Second)
		defer limit.Stop()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		var work []byte
		for len(work) == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-limit.C:
				return fmt.Errorf("mock did not start")
			case <-tick.C:
				work, _ = os.ReadFile(ready)
			}
		}
		physical, err := filepath.EvalSymlinks(string(work))
		if err != nil {
			return err
		}
		index := filepath.Join(runner.config.HomeDir, ".grok", "sessions", strings.ReplaceAll(url.QueryEscape(physical), "+", "%20"))
		if err = os.Rename(oldIndex, index); err != nil {
			return err
		}
		session = filepath.Join(index, filepath.Base(session))
		now := time.Now().UTC().Format(time.RFC3339Nano)
		events := fmt.Sprintf("{\"type\":\"turn_started\",\"ts\":%q,\"session_id\":%q,\"model_id\":%q}\n{\"type\":\"first_token\",\"ts\":%q}\n{\"type\":\"phase_changed\",\"ts\":%q,\"phase\":\"streaming_reasoning\"}\n", now, terminalTestID, runner.config.Model, now, now)
		return os.WriteFile(filepath.Join(session, "events.jsonl"), []byte(events), 0600)
	})
	result, audit, err := runner.Review(context.Background(), input)
	if err == nil || result.Verdict != "" || audit.FailureKind != CLIFailureTimeout || audit.Terminal == nil || !audit.Terminal.ChildWaited || audit.Terminal.SessionEvidence != "qualified_local" || audit.Terminal.Session == nil || audit.Terminal.Session.ReasoningNotifications != 1 || audit.RequestID != "" || audit.SessionID != "" || audit.Usage.TotalTokens != 0 {
		t.Fatalf("result=%#v audit=%#v err=%v", result, audit, err)
	}
	raw, _ := json.Marshal(audit)
	for _, secret := range []string{"private-stdout", "private-stderr", "private-reasoning", string(input)} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private data leaked")
		}
	}
}
