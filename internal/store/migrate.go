package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"time"
)

//go:embed migrations/*.sql
var defaultMigrationsFS embed.FS

const migrationVersionsTable = `
CREATE TABLE IF NOT EXISTS migration_versions (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at INTEGER NOT NULL
)`

var migrationFilenameRE = regexp.MustCompile(`^(\d+)_([a-zA-Z0-9_]+)\.sql$`)

type migrationFile struct {
	version int
	name    string
	sql     string
}

func Migrate(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	if _, err := db.ExecContext(ctx, migrationVersionsTable); err != nil {
		return fmt.Errorf("ensure migration_versions: %w", err)
	}
	if err := bootstrapLegacyVersions(ctx, db); err != nil {
		return fmt.Errorf("bootstrap legacy versions: %w", err)
	}
	files, err := loadMigrationFiles(fsys)
	if err != nil {
		return err
	}
	applied, err := loadApplied(ctx, db)
	if err != nil {
		return err
	}
	for _, f := range files {
		if applied[f.version] {
			continue
		}
		if err := applyMigration(ctx, db, f); err != nil {
			return err
		}
	}
	return nil
}

func loadMigrationFiles(fsys fs.FS) ([]migrationFile, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	type fileWithName struct {
		migrationFile
		filename string
	}
	var loaded []fileWithName
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFilenameRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, _ := strconv.Atoi(m[1])
		body, err := fs.ReadFile(fsys, "migrations/"+e.Name())
		if err != nil {
			return nil, err
		}
		loaded = append(loaded, fileWithName{migrationFile{v, m[2], string(body)}, e.Name()})
	}
	sort.Slice(loaded, func(i, j int) bool {
		if loaded[i].version != loaded[j].version {
			return loaded[i].version < loaded[j].version
		}
		return loaded[i].filename < loaded[j].filename
	})
	for i := 1; i < len(loaded); i++ {
		if loaded[i].version == loaded[i-1].version {
			return nil, fmt.Errorf("migrations: duplicate version %d in files %s and %s",
				loaded[i].version, loaded[i-1].filename, loaded[i].filename)
		}
	}
	files := make([]migrationFile, len(loaded))
	for i, f := range loaded {
		files[i] = f.migrationFile
	}
	return files, nil
}

func loadApplied(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	applied := map[int]bool{}
	rows, err := db.QueryContext(ctx, `SELECT version FROM migration_versions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, nil
}

func applyMigration(ctx context.Context, db *sql.DB, f migrationFile) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		// Post-Commit returns ErrTxDone — expected. Anything else means
		// the DB is in trouble during bootstrap; surface it.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			fmt.Fprintf(os.Stderr, "squad migrate: rollback after %03d_%s: %v\n", f.version, f.name, rerr)
		}
	}()
	if _, err := tx.ExecContext(ctx, f.sql); err != nil {
		return fmt.Errorf("apply migration %03d_%s: %w", f.version, f.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO migration_versions (version, name, applied_at) VALUES (?, ?, ?)`,
		f.version, f.name, time.Now().Unix()); err != nil {
		return fmt.Errorf("record migration %03d_%s: %w", f.version, f.name, err)
	}
	return tx.Commit()
}

// bootstrapLegacyVersions seeds migration_versions for DBs whose schema
// already reflects earlier migrations but has no migration_versions rows —
// either pre-Task-5 DBs created by the schema-and-alters mechanism, or DBs
// whose migration_versions table was dropped. We anchor each version on a
// column or table that migration uniquely created.
func bootstrapLegacyVersions(ctx context.Context, db *sql.DB) error {
	var seeded int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM migration_versions`).Scan(&seeded)
	if seeded > 0 {
		return nil
	}
	var hasEpicID int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('items') WHERE name='epic_id'`).Scan(&hasEpicID)
	if hasEpicID == 0 {
		return nil
	}
	type legacyRow struct {
		version int
		name    string
	}
	legacy := []legacyRow{
		{1, "initial"},
		{2, "items_extras"},
		{3, "subagent_events"},
	}
	var hasCapturedBy int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('items') WHERE name='captured_by'`).Scan(&hasCapturedBy)
	if hasCapturedBy > 0 {
		legacy = append(legacy, legacyRow{4, "intake_provenance"})
	}
	var claimsPKCols int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('claims') WHERE pk > 0`).Scan(&claimsPKCols)
	if claimsPKCols > 1 {
		legacy = append(legacy, legacyRow{5, "claims_repo_scope"})
	}
	var hasCommits int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='commits'`).Scan(&hasCommits)
	if hasCommits > 0 {
		legacy = append(legacy, legacyRow{6, "commits"})
	}
	var hasWorktree int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('claims') WHERE name='worktree'`).Scan(&hasWorktree)
	if hasWorktree > 0 {
		legacy = append(legacy, legacyRow{7, "claim_worktree"})
	}
	var hasAgentEvents int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='agent_events'`).Scan(&hasAgentEvents)
	if hasAgentEvents > 0 {
		legacy = append(legacy, legacyRow{8, "agent_events"})
	}
	var hasIntakeSessionID int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('items') WHERE name='intake_session_id'`).Scan(&hasIntakeSessionID)
	if hasIntakeSessionID > 0 {
		legacy = append(legacy, legacyRow{9, "intake_interview"})
	}
	var hasRequiresCapability int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('items') WHERE name='requires_capability'`).Scan(&hasRequiresCapability)
	if hasRequiresCapability > 0 {
		legacy = append(legacy, legacyRow{10, "items_requires_capability"})
	}
	var hasAgentsCapabilities int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('agents') WHERE name='capabilities'`).Scan(&hasAgentsCapabilities)
	if hasAgentsCapabilities > 0 {
		legacy = append(legacy, legacyRow{11, "agents_capabilities"})
	}
	var hasNudged90m int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('claims') WHERE name='nudged_90m_at'`).Scan(&hasNudged90m)
	if hasNudged90m > 0 {
		legacy = append(legacy, legacyRow{12, "claim_timebox_nudges"})
	}
	var hasClaimGeneration int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('claims') WHERE name='generation'`).Scan(&hasClaimGeneration)
	if hasClaimGeneration > 0 {
		legacy = append(legacy, legacyRow{13, "claim_recovery"})
	}
	var hasDispatchReservations int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='dispatch_reservations'`).Scan(&hasDispatchReservations)
	if hasDispatchReservations > 0 {
		legacy = append(legacy, legacyRow{14, "dispatch_reservations"})
	}
	nowTS := time.Now().Unix()
	for _, l := range legacy {
		if _, err := db.ExecContext(ctx,
			`INSERT OR IGNORE INTO migration_versions (version, name, applied_at) VALUES (?, ?, ?)`,
			l.version, "legacy-"+l.name, nowTS); err != nil {
			return err
		}
	}
	return nil
}
