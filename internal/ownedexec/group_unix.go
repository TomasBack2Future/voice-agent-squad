//go:build !windows

// Package ownedexec limits cancellation to a subprocess group created by this
// invocation. It never selects processes by port, executable name or user input.
package ownedexec

import (
	"os/exec"
	"syscall"
	"time"
)

func Configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
