//go:build !windows

package procgroup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func gone(t *testing.T, pid int) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(b), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no pid in %s", path)
	return 0
}

// A child that exits normally after leaving a background descendant (the
// orphaned `yes` pattern) does not leak it past Close.
func TestCloseReapsDescendantsOfANormallyExitedChild(t *testing.T) {
	g, err := New()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	cmd := exec.Command("/bin/sh", "-c", `sleep 300 & echo $! > "$1"; exit 0`, "sh", pidFile)
	g.Attach(cmd)
	if err = cmd.Run(); err != nil {
		t.Fatal(err)
	}
	orphan := readPID(t, pidFile)
	if !alive(orphan) {
		t.Fatal("fixture descendant did not start")
	}
	if members, _ := g.Members(); len(members) == 0 {
		t.Fatal("descendant not in the owned group")
	}
	if err = g.Close(); err != nil {
		t.Fatal(err)
	}
	if !gone(t, orphan) {
		t.Fatalf("descendant %d survived Close", orphan)
	}
	if err = g.Kill(); err != nil || g.Close() != nil {
		t.Fatal("Kill/Close after Close must be no-ops")
	}
}

// A timeout kills the whole tree, not only the direct child.
func TestTimeoutKillsTheWholeTree(t *testing.T) {
	g, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `sleep 300 & echo $! > "$1"; while :; do :; done`, "sh", pidFile)
	g.Attach(cmd)
	start := time.Now()
	if err = cmd.Run(); err == nil {
		t.Fatal("busy child was not stopped")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("timeout not enforced: %s", time.Since(start))
	}
	if child := cmd.ProcessState.Sys().(syscall.WaitStatus); !child.Signaled() || child.Signal() != syscall.SIGKILL {
		t.Fatalf("child not killed by group SIGKILL: %v", cmd.ProcessState)
	}
	if !gone(t, readPID(t, pidFile)) {
		t.Fatal("descendant survived the timeout")
	}
}

// Cleanup never reaches a process outside the owned group, even one started
// by the same owner with a similar command.
func TestCleanupDoesNotTouchOtherOwners(t *testing.T) {
	other := exec.Command("/bin/sleep", "300")
	other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Process.Kill(); _ = other.Wait() }()
	g, err := New()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sleep", "300")
	g.Attach(cmd)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err = g.Close(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if !alive(other.Process.Pid) {
		t.Fatal("cleanup killed a process outside the owned group")
	}
}

// TestHelperOwnerCrash is the owner process for TestOwnerCrashStillCleansUp.
func TestHelperOwnerCrash(t *testing.T) {
	pidFile := os.Getenv("PROCGROUP_HELPER_PIDFILE")
	if pidFile == "" {
		t.Skip("helper")
	}
	g, err := New()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", `sleep 300 & echo $! > "$1"; exec sleep 300`, "sh", pidFile)
	g.Attach(cmd)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Hour) // killed by the parent test; never reaches Close
}

// An owner killed with SIGKILL runs no deferred cleanup; the anchor sees EOF
// on its stdin and kills the group itself.
func TestOwnerCrashStillCleansUp(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	owner := exec.Command(os.Args[0], "-test.run=^TestHelperOwnerCrash$")
	owner.Env = append(os.Environ(), "PROCGROUP_HELPER_PIDFILE="+pidFile)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	descendant := readPID(t, pidFile)
	if !alive(descendant) {
		t.Fatal("fixture descendant did not start")
	}
	if err := owner.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	if !gone(t, descendant) {
		t.Fatalf("descendant %d survived its owner's crash", descendant)
	}
}
