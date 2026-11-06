//go:build !windows

// Package procgroup runs a command tree inside one owned process group and
// guarantees that cleanup reaches only that group.
//
// The group's ID is the PID of a small anchor shell that lives until Close.
// While the anchor (or its zombie) exists, the kernel cannot reuse that PID,
// so signalling the negative group ID can never reach another owner's
// processes. The anchor reads its stdin, which only the owning process holds:
// when the owner closes it, or exits for any reason including SIGKILL, the
// read returns EOF and the anchor kills its own group. Descendants that leave
// the group on purpose (setsid/setpgid) are outside this guarantee.
package procgroup

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// anchorScript ignores job-control signals so only EOF or SIGKILL ends it, and
// then kills every member of its own process group, itself included.
const anchorScript = `trap '' INT TERM HUP QUIT; read _; kill -KILL 0`

// Group is one owned process group pinned by a live anchor.
type Group struct {
	mu     sync.Mutex
	anchor *exec.Cmd
	stdin  io.WriteCloser
	pgid   int
	closed bool
}

// New starts the anchor. The caller must Close the group.
func New() (*Group, error) {
	anchor := exec.Command("/bin/sh", "-c", anchorScript)
	anchor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := anchor.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err = anchor.Start(); err != nil {
		return nil, fmt.Errorf("start process-group anchor: %w", err)
	}
	return &Group{anchor: anchor, stdin: stdin, pgid: anchor.Process.Pid}, nil
}

// ID is the owned process-group ID (the anchor PID).
func (g *Group) ID() int { return g.pgid }

// Attach places cmd in the group before it starts. For a command created with
// exec.CommandContext, cancellation kills the whole group instead of only the
// direct child. WaitDelay bounds how long a descendant holding the output pipes
// can delay Wait.
func (g *Group) Attach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = g.pgid
	if cmd.Cancel != nil { // only CommandContext installs a Cancel
		cmd.Cancel = func() error { return g.Kill() }
	}
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 2 * time.Second
	}
}

// Kill sends SIGKILL to every member of the group. It is safe while the group
// is open: the anchor pins the group ID. After Close it does nothing.
func (g *Group) Kill() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	return g.killLocked()
}

func (g *Group) killLocked() error {
	err := syscall.Kill(-g.pgid, syscall.SIGKILL)
	// ESRCH: no member left. Darwin reports EPERM when only the unreaped
	// anchor zombie remains; every live member is ours, so nothing is left.
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM) {
		return nil
	}
	return err
}

// Close kills every remaining member, including descendants left behind by a
// child that already exited, then reaps the anchor. It is idempotent.
func (g *Group) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	_ = g.stdin.Close()
	err := g.killLocked()
	_ = g.anchor.Wait() // the anchor always ends by SIGKILL; its status is not an error
	return err
}

// Members lists PIDs currently in the group other than the anchor. It reads
// the system process table and is intended for diagnostics and tests.
func (g *Group) Members() ([]int, error) {
	out, err := exec.Command("/bin/ps", "-A", "-o", "pid=", "-o", "pgid=").Output()
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, line := range splitLines(string(out)) {
		var pid, pgid int
		if _, err := fmt.Sscan(line, &pid, &pgid); err == nil && pgid == g.pgid && pid != g.pgid {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
