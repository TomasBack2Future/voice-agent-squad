package dispatch

import (
	"os"
	"runtime"
	"testing"
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
