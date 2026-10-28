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

func TestWorkerJoinDefiniteCreateRefusalVerifiesOwnedNameAbsence(t *testing.T) {
	root := t.TempDir()
	engine := filepath.Join(root, "docker")
	child := exec.Command("true")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	b := dispatch.WorkerExecutionBinding{ID: "pin", Native: "native", ClientPID: child.Process.Pid, ToolRuntime: dispatch.WorkerToolRuntime{Executable: engine, Context: "local", DaemonID: "daemon", Image: "sha256:" + strings.Repeat("b", 64)}}
	operation := map[string]any{"id": "c6ccac1e-4340-4a55-8f9d-7c4d35247910", "name": "squad-muse-c6ccac1e-4340-4a55-8f9d-7c4d35247910", "kind": "container", "state": "joined", "not_started": true, "create_refusal": "invalid-bind-mount"}
	write := func(path string, value any) {
		t.Helper()
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save := func() {
		write(filepath.Join(root, "operations.json"), []any{operation})
		write(filepath.Join(root, "join.json"), map[string]any{"binding": b, "native_pid": b.ClientPID, "native_exit": 0, "bridge_live": false, "operations": []any{operation}, "joined": true})
	}
	engineOutput := func(output string) {
		t.Helper()
		body := "#!/bin/sh\nif [ \"$3\" = info ]; then printf 'daemon\\n'; elif [ \"$3\" = container ] && [ \"$4\" = ls ]; then printf '%s' '" + output + "'; else exit 9; fi\n"
		if err := os.WriteFile(engine, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	save()
	engineOutput("")
	if err := verifyWorkerJoin(context.Background(), b, filepath.Join(root, "binding.json"), filepath.Join(root, "join.json")); err != nil {
		t.Fatal("explicit rejected create with proven empty owned name must join:", err)
	}
	engineOutput("existing-owned-container")
	if err := verifyWorkerJoin(context.Background(), b, filepath.Join(root, "binding.json"), filepath.Join(root, "join.json")); err == nil {
		t.Fatal("existing owned container treated as not started")
	}
	engineOutput("")
	operation["create_refusal"] = "unknown"
	save()
	if err := verifyWorkerJoin(context.Background(), b, filepath.Join(root, "binding.json"), filepath.Join(root, "join.json")); err == nil {
		t.Fatal("unknown create failure treated as not started")
	}
	operation["create_refusal"] = "invalid-bind-mount"
	operation["pid"] = os.Getpid()
	save()
	if err := verifyWorkerJoin(context.Background(), b, filepath.Join(root, "binding.json"), filepath.Join(root, "join.json")); err == nil {
		t.Fatal("live child in a not-started receipt")
	}
}
