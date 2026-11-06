//go:build !windows

package grokreview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The #1965 incident left 20 orphaned CPU burners after a review run ended.
// A reviewer stub that leaves a background descendant must not leak it, on a
// normal exit or on a timeout.
func reviewerStub(t *testing.T, tail string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "descendant.pid")
	script := "#!/bin/sh\nsleep 300 &\necho $! > " + pidFile + "\n" + tail
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, pidFile
}

func descendantGone(t *testing.T, pidFile string) (int, bool) {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return pid, true
		}
	}
	return pid, false
}

func TestReviewerDescendantsDoNotOutliveANormalReview(t *testing.T) {
	bin, pidFile := reviewerStub(t, `printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"native","structured_output":{"schema_version":"squad.review.findings.v2","verdict":"approved","summary":"No defects.","findings":[]},"modelUsage":{"selected":{}}}'
`)
	runner, err := NewCLIRunner(CLIConfig{Backend: "claude", Binary: bin, HomeDir: filepath.Dir(bin), Model: "selected", Core: "core", Policy: "policy", Timeout: 30 * time.Second, MaxOutputBytes: 8192})
	if err != nil {
		t.Fatal(err)
	}
	result, audit, err := runner.Review(context.Background(), []byte(`{"diff":"test"}`))
	if err != nil || result.Verdict != VerdictApproved {
		t.Fatalf("%+v %v", result, err)
	}
	if pid, ok := descendantGone(t, pidFile); !ok {
		t.Fatalf("reviewer descendant %d outlived the review", pid)
	}
	if audit.Terminal == nil || audit.Terminal.DescendantsReaped != 1 {
		t.Fatalf("reaped descendants not recorded: %+v", audit.Terminal)
	}
}

func TestReviewerTimeoutEndsTheWholeTree(t *testing.T) {
	bin, pidFile := reviewerStub(t, "exec sleep 300\n")
	runner, err := NewCLIRunner(CLIConfig{Backend: "claude", Binary: bin, HomeDir: filepath.Dir(bin), Model: "selected", Core: "core", Policy: "policy", Timeout: 2 * time.Second, MaxOutputBytes: 8192})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, audit, err := runner.Review(context.Background(), []byte(`{"diff":"test"}`))
	if err == nil || audit.FailureKind != CLIFailureTimeout {
		t.Fatalf("timeout not reported: %+v %v", audit, err)
	}
	if time.Since(started) > 15*time.Second {
		t.Fatalf("timeout not bounded: %s", time.Since(started))
	}
	if pid, ok := descendantGone(t, pidFile); !ok {
		t.Fatalf("descendant %d survived the reviewer timeout", pid)
	}
}
