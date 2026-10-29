package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadOnlyDoesNotCreateOrMutateLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if db, err := OpenReadOnly(path); err == nil {
		db.Close()
		t.Fatal("created missing ledger")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("created database: %v", err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err = ro.Exec(`INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES('r',1,'a','global','say','write','[]','normal')`); err == nil {
		t.Fatal("read-only connection accepted write")
	}
}
