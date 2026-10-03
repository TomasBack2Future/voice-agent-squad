package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zsiec/squad/internal/dispatch"
)

func TestWorkerJoinRejectsOmittedOperationsAndLiveHandles(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "docker")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nif [ \"$3\" = info ]; then printf 'daemon\\n'; else printf '%s\\n' '[{\"Id\":\""+strings.Repeat("a", 64)+"\",\"Image\":\"sha256:"+strings.Repeat("b", 64)+"\",\"Config\":{\"Labels\":{\"squad.execution\":\"pin\"}},\"State\":{\"Status\":\"exited\",\"Pid\":0,\"Running\":false}}]'; fi\n"), 0700); err != nil {
		t.Fatal(err)
	}
	child := exec.Command("true")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	b := dispatch.WorkerExecutionBinding{ID: "pin", Native: "native", ClientPID: child.Process.Pid, ToolRuntime: dispatch.WorkerToolRuntime{Executable: executable, Context: "local", DaemonID: "daemon", Image: "sha256:" + strings.Repeat("b", 64)}}
	exit := 0
	r := workerJoinReceipt{Binding: b, NativePID: b.ClientPID, NativeExit: &exit, Joined: true, Operations: []workerJoinOperation{{State: "completed", Kind: "container", Container: strings.Repeat("a", 64)}}}
	receipt := filepath.Join(root, "join.json")
	binding := filepath.Join(root, "binding.json")
	write := func(path string, v any) {
		t.Helper()
		raw, _ := json.Marshal(v)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "operations.json"), r.Operations)
	write(receipt, r)
	if err := verifyWorkerJoin(context.Background(), b, binding, receipt); err != nil {
		t.Fatal(err)
	}
	r.Operations = nil
	write(receipt, r)
	if err := verifyWorkerJoin(context.Background(), b, binding, receipt); err == nil {
		t.Fatal("omitted operation accepted")
	}
	r.Operations = []workerJoinOperation{{State: "running", Kind: "container", Container: strings.Repeat("a", 64)}}
	write(receipt, r)
	write(filepath.Join(root, "operations.json"), r.Operations)
	if err := verifyWorkerJoin(context.Background(), b, binding, receipt); err == nil {
		t.Fatal("unresolved operation accepted")
	}
	r.Operations = nil
	write(filepath.Join(root, "operations.json"), r.Operations)
	pid := os.Getpid()
	r.BridgePID = &pid
	write(receipt, r)
	if err := verifyWorkerJoin(context.Background(), b, binding, receipt); err == nil {
		t.Fatal("live bridge accepted")
	}
	r.BridgePID = nil
	r.Binding.ID = "wrong"
	write(receipt, r)
	if err := verifyWorkerJoin(context.Background(), b, binding, receipt); err == nil {
		t.Fatal("wrong binding accepted")
	}
}
