//go:build windows

package ownedexec

import (
	"os/exec"
	"time"
)

// Windows retains CommandContext's direct-child cancellation. Descendant
// process supervision is not qualified on this platform.
func Configure(cmd *exec.Cmd) { cmd.WaitDelay = 2 * time.Second }
