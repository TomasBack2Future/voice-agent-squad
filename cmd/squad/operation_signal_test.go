package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

// External cancellation of the wrapper must reach the operation's context so the
// existing group kill and result save run, instead of the process dying silently.
func TestOperationWrapperTurnsTerminationIntoContextCancellation(t *testing.T) {
	ctx, stop := operationSignalContext(context.Background())
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM did not cancel the operation context")
	}
}
