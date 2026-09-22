package store

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestRevocationMigrationPreservesExistingEvidence(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	ctx := context.Background()
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(defaultMigrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "017_") {
			continue
		}
		b, err := fs.ReadFile(defaultMigrationsFS, "migrations/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		old["migrations/"+e.Name()] = &fstest.MapFile{Data: b}
	}
	if err = Migrate(ctx, db, old); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO attestations(item_id,kind,command,exit_code,output_hash,output_path,created_at,agent_id,repo_id) VALUES('TASK','manual','false; true',0,'hash','artifact',1,'worker','repo')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = Migrate(ctx, db, defaultMigrationsFS); err != nil {
			t.Fatal(err)
		}
	}
	var command string
	var exit, count int
	if err = db.QueryRow("SELECT command,exit_code FROM attestations WHERE id=1").Scan(&command, &exit); err != nil || command != "false; true" || exit != 0 {
		t.Fatalf("history changed %s %d %v", command, exit, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM attestation_revocations").Scan(&count); err != nil || count != 0 {
		t.Fatalf("unexpected corrections %d %v", count, err)
	}
}
