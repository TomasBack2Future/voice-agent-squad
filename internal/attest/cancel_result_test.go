package attest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/store"
)

func TestCancelledRunRetainsFailedResult(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger := New(db, "repo", nil)
	if _, err := ledger.Run(context.Background(), RunOpts{ItemID: "TASK", Kind: KindTest, AgentID: "worker", Command: "true", RepoRoot: root, AttDir: filepath.Join(root, "evidence")}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = ledger.Run(ctx, RunOpts{ItemID: "TASK", Kind: KindTest, AgentID: "worker", Argv: []string{"sleep", "10"}, RepoRoot: root, AttDir: filepath.Join(root, "evidence")})
	records, err := ledger.ListForItem(context.Background(), "TASK")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("cancelled owned operation lost its terminal result: got %d records", len(records))
	}
	if records[0].ExitCode == 0 && records[1].ExitCode == 0 {
		t.Fatal("cancelled operation recorded as success")
	}
	if _, err := os.Stat(records[0].OutputPath); err != nil {
		t.Fatal(err)
	}
}
