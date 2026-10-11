package dispatch

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestLegacyKernelArgumentsExcludeEnvironmentAndRetainArgumentBoundaries(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native kernel observer is deliberately unsupported on this host")
	}
	args, err := legacyProcessArguments(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != len(os.Args) {
		t.Fatal("kernel argument count differs; do not interpret trailing environment as arguments")
	}
	for i, arg := range args {
		if arg != os.Args[i] {
			t.Fatal("kernel argument boundary differs; process arguments are not reconstructed from a display string")
		}
	}
}

func TestLegacyProcessSnapshotExcludesOwnHelperAndRetainsLiveChild(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native process snapshot is deliberately unsupported on this host")
	}
	if os.Getenv("SQUAD_LEGACY_SNAPSHOT_TEST_CHILD") != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, binary, "-test.run=^TestLegacyProcessSnapshotExcludesOwnHelperAndRetainsLiveChild$")
		child.Env = append(os.Environ(), "SQUAD_LEGACY_SNAPSHOT_TEST_CHILD=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated actual-host snapshot regression: %v\n%s", err, output)
		}
		return
	}
	live := exec.Command("/bin/sleep", "30")
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = live.Process.Kill()
		_ = live.Wait()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	parents, err := legacyProcessParents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if parents[live.Process.Pid] != os.Getpid() {
		t.Fatal("snapshot lost the actual live child; unknown tools must remain visible")
	}
	for pid, parent := range parents {
		if parent == os.Getpid() && pid != live.Process.Pid {
			t.Fatalf("snapshot retained its reaped helper as an owned child: pid=%d", pid)
		}
	}
}
