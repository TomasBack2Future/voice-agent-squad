package attest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zsiec/squad/internal/store"
)

func TestRunWorkDirSeparatesExecutionFromLedger(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "worker's $(false) checkout")
	if err := os.Mkdir(work, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "expected"), []byte("actual worker tests"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger := New(db, "coordination", nil)
	opts := RunOpts{ItemID: "TASK-1", Kind: KindTest, AgentID: "worker", Command: "cat expected", RepoRoot: root, WorkDir: work, AttDir: filepath.Join(root, "evidence")}
	rec, err := ledger.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(rec.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ExitCode != 0 || !strings.Contains(string(body), "actual worker tests") || filepath.Dir(rec.OutputPath) != opts.AttDir {
		t.Fatalf("wrong execution/evidence: %+v %s", rec, body)
	}
	// The persisted command must be replayable even with quotes/metacharacters in cwd.
	replay, err := exec.Command("sh", "-c", rec.Command).Output()
	if err != nil || string(replay) != "actual worker tests" {
		t.Fatalf("replay: %s %v", replay, err)
	}
	opts.WorkDir = root
	opts.Command = "printf same"
	first, err := ledger.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.WorkDir = work
	second, err := ledger.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.OutputHash == second.OutputHash {
		t.Fatal("identical output from different directories must not deduplicate")
	}
}

func TestRunWorkDirRejectsInvalidDirectoryBeforeCommand(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger := New(db, "coordination", nil)
	for _, dir := range []string{"relative", filepath.Join(root, "missing"), file} {
		_, err := ledger.Run(context.Background(), RunOpts{ItemID: "TASK-1", Kind: KindTest, AgentID: "worker", Command: "touch should-not-run", RepoRoot: root, WorkDir: dir, AttDir: filepath.Join(root, "evidence")})
		if err == nil {
			t.Fatalf("accepted %s", dir)
		}
	}
	records, err := ledger.ListForItem(context.Background(), "TASK-1")
	if err != nil || len(records) != 0 {
		t.Fatalf("unexpected records: %v %v", records, err)
	}
	if _, err := os.Stat(filepath.Join(root, "should-not-run")); !os.IsNotExist(err) {
		t.Fatal("command ran")
	}
}
