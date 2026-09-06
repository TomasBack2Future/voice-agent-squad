package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"
)

func readMigration(t *testing.T, name string) *fstest.MapFile {
	t.Helper()
	body, err := fs.ReadFile(defaultMigrationsFS, "migrations/"+name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return &fstest.MapFile{Data: body}
}

func openEmptyDBNoMigrate(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrate_AppliesNewMigrationsInOrder(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	fsys := fstest.MapFS{
		"migrations/001_initial.sql": &fstest.MapFile{Data: []byte("CREATE TABLE foo (x INTEGER)")},
		"migrations/002_add_bar.sql": &fstest.MapFile{Data: []byte("ALTER TABLE foo ADD COLUMN bar TEXT")},
	}
	if err := Migrate(context.Background(), db, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var maxV int
	if err := db.QueryRow(`SELECT max(version) FROM migration_versions`).Scan(&maxV); err != nil {
		t.Fatalf("query: %v", err)
	}
	if maxV != 2 {
		t.Fatalf("want max version 2, got %d", maxV)
	}
}

func TestMigrate_SkipsAlreadyApplied(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	fsys := fstest.MapFS{
		"migrations/001_initial.sql": &fstest.MapFile{Data: []byte("CREATE TABLE foo (x INTEGER)")},
	}
	if err := Migrate(context.Background(), db, fsys); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := Migrate(context.Background(), db, fsys); err != nil {
		t.Fatalf("second migrate (idempotent): %v", err)
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM migration_versions`).Scan(&rows); err != nil {
		t.Fatalf("query: %v", err)
	}
	if rows != 1 {
		t.Fatalf("want 1 row, got %d", rows)
	}
}

func TestMigrate_FailsCleanlyOnBadMigration(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	fsys := fstest.MapFS{
		"migrations/001_initial.sql": &fstest.MapFile{Data: []byte("CREATE TABLE foo (x INTEGER)")},
		"migrations/002_broken.sql":  &fstest.MapFile{Data: []byte("this is not valid sql")},
	}
	err := Migrate(context.Background(), db, fsys)
	if err == nil {
		t.Fatalf("want error from broken migration")
	}
	var maxV int
	_ = db.QueryRow(`SELECT COALESCE(max(version), 0) FROM migration_versions`).Scan(&maxV)
	if maxV != 1 {
		t.Fatalf("want migration 001 applied (version 1), got %d", maxV)
	}
}

func TestMigrate_SortsByVersionNotByName(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	fsys := fstest.MapFS{
		// Mixed-width numeric prefixes: lexical order would be
		// "10_z.sql" < "2_b.sql" (because '1' < '2' at position 0), running
		// 10 before 2. Migration 10 references column b that 2 adds, so a
		// lexical-only sort would crash here. The row-count assertion below
		// confirms 2 ran before 10.
		"migrations/10_z.sql": &fstest.MapFile{Data: []byte("INSERT INTO foo (a, b) VALUES ('hi', 'there')")},
		"migrations/2_b.sql":  &fstest.MapFile{Data: []byte("ALTER TABLE foo ADD COLUMN b TEXT")},
		"migrations/1_a.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE foo (a TEXT)")},
	}
	if err := Migrate(context.Background(), db, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM foo WHERE a='hi' AND b='there'`).Scan(&n); err != nil {
		t.Fatalf("query foo: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 row inserted by 10 after 2 added column b, got %d", n)
	}
}

func TestMigrate_RejectsDuplicateVersionNumbers(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	fsys := fstest.MapFS{
		"migrations/001_a.sql": &fstest.MapFile{Data: []byte("CREATE TABLE foo (x INT)")},
		"migrations/01_b.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE bar (x INT)")},
	}
	err := Migrate(context.Background(), db, fsys)
	if err == nil {
		t.Fatalf("want duplicate-version error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "duplicate version 1") {
		t.Fatalf("want error mentioning %q, got %q", "duplicate version 1", msg)
	}
}

func TestMigrate_BootstrapsLegacyDB(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE migration_versions`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("re-migrate must succeed: %v", err)
	}
	var maxV int
	if err := db.QueryRow(`SELECT max(version) FROM migration_versions`).Scan(&maxV); err != nil {
		t.Fatalf("query: %v", err)
	}
	if maxV < 3 {
		t.Fatalf("want at least version 3 seeded, got %d", maxV)
	}
}

func TestMigrate_AppliesIntakeProvenance(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var have int
	if err := db.QueryRow(
		`SELECT count(*) FROM pragma_table_info('items') WHERE name='captured_by'`,
	).Scan(&have); err != nil {
		t.Fatalf("query: %v", err)
	}
	if have != 1 {
		t.Fatalf("want captured_by column; got %d hits", have)
	}
	var maxV int
	if err := db.QueryRow(`SELECT max(version) FROM migration_versions`).Scan(&maxV); err != nil {
		t.Fatalf("max: %v", err)
	}
	if maxV < 4 {
		t.Fatalf("want at least version 4 applied; got %d", maxV)
	}
}

func TestMigrate_AppliesAgentEvents(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	wantCols := map[string]string{
		"id":          "INTEGER",
		"repo_id":     "TEXT",
		"agent_id":    "TEXT",
		"session_id":  "TEXT",
		"ts":          "INTEGER",
		"event_kind":  "TEXT",
		"tool":        "TEXT",
		"target":      "TEXT",
		"exit_code":   "INTEGER",
		"duration_ms": "INTEGER",
	}
	rows, err := db.Query(`SELECT name, type FROM pragma_table_info('agent_events')`)
	if err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	defer rows.Close()
	gotCols := map[string]string{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			t.Fatalf("scan: %v", err)
		}
		gotCols[name] = typ
	}
	if len(gotCols) == 0 {
		t.Fatalf("agent_events table not created")
	}
	for name, typ := range wantCols {
		if got, ok := gotCols[name]; !ok {
			t.Errorf("missing column %s", name)
		} else if !strings.EqualFold(got, typ) {
			t.Errorf("column %s: want type %s, got %s", name, typ, got)
		}
	}

	var strict int
	if err := db.QueryRow(
		`SELECT count(*) FROM pragma_table_list WHERE name='agent_events' AND "strict"=1`,
	).Scan(&strict); err != nil {
		t.Fatalf("pragma_table_list: %v", err)
	}
	if strict != 1 {
		t.Fatalf("agent_events must be STRICT; got strict=%d", strict)
	}

	wantIdx := map[string][]string{
		"idx_agent_events_agent_ts": {"repo_id", "agent_id", "ts"},
		"idx_agent_events_repo_ts":  {"repo_id", "ts"},
	}
	for idxName, wantCols := range wantIdx {
		idxRows, err := db.Query(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, idxName)
		if err != nil {
			t.Fatalf("pragma_index_info(%s): %v", idxName, err)
		}
		var gotCols []string
		for idxRows.Next() {
			var name string
			if err := idxRows.Scan(&name); err != nil {
				idxRows.Close()
				t.Fatalf("scan idx col: %v", err)
			}
			gotCols = append(gotCols, name)
		}
		idxRows.Close()
		if len(gotCols) == 0 {
			t.Errorf("index %s missing or empty", idxName)
			continue
		}
		if !slicesEqual(gotCols, wantCols) {
			t.Errorf("index %s: want columns %v, got %v", idxName, wantCols, gotCols)
		}
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMigrate_BootstrapsLegacyDBWithoutIntakeColumns(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	legacy := fstest.MapFS{
		"migrations/001_initial.sql":         readMigration(t, "001_initial.sql"),
		"migrations/002_items_extras.sql":    readMigration(t, "002_items_extras.sql"),
		"migrations/003_subagent_events.sql": readMigration(t, "003_subagent_events.sql"),
	}
	if err := Migrate(context.Background(), db, legacy); err != nil {
		t.Fatalf("legacy migrate: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE migration_versions`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("re-migrate must succeed: %v", err)
	}
	var maxV int
	if err := db.QueryRow(`SELECT max(version) FROM migration_versions`).Scan(&maxV); err != nil {
		t.Fatalf("max: %v", err)
	}
	if maxV != 15 {
		t.Fatalf("want version 15 after bootstrap; got %d", maxV)
	}
}

// TestMigrate_BootstrapPreservesWorktreeAndSeedsAllVersions covers the
// failure mode where bootstrapLegacyVersions seeds only v1-v4 and v9 on
// a fully-migrated DB whose migration_versions row got dropped. The
// missing v5/v7 markers cause migration 5 (claims RENAME-and-recreate)
// to re-run, dropping live worktree values, and migration 7 to re-add
// the column with default ”. Asserts the column survives and that
// bootstrap leaves migration_versions with all 9 rows.
func TestMigrate_BootstrapPreservesWorktreeAndSeedsAllVersions(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	const wantWT = "/tmp/wt-bootstrap-probe"
	if _, err := db.Exec(
		`INSERT INTO claims (item_id, repo_id, agent_id, claimed_at, last_touch, intent, long, worktree)
		 VALUES ('TEST-001', 'repo-1', 'agent-x', 1, 1, '', 0, ?)`, wantWT,
	); err != nil {
		t.Fatalf("seed claim with worktree: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE migration_versions`); err != nil {
		t.Fatalf("drop migration_versions: %v", err)
	}
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("bootstrap re-migrate: %v", err)
	}
	var got string
	if err := db.QueryRow(
		`SELECT worktree FROM claims WHERE repo_id='repo-1' AND item_id='TEST-001'`,
	).Scan(&got); err != nil {
		t.Fatalf("select worktree: %v", err)
	}
	if got != wantWT {
		t.Errorf("worktree = %q, want %q (migration 5 re-ran and clobbered the column)", got, wantWT)
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM migration_versions`).Scan(&rows); err != nil {
		t.Fatalf("count migration_versions: %v", err)
	}
	if rows != 15 {
		t.Errorf("migration_versions row count = %d, want 15 (bootstrap missed markers)", rows)
	}
}

func TestMigrate_AppliesItemsRequiresCapability(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rows, err := db.Query(`SELECT name, type, "notnull", dflt_value
		FROM pragma_table_info('items') WHERE name='requires_capability'`)
	if err != nil {
		t.Fatalf("pragma items: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("items.requires_capability column missing")
	}
	var name, typ string
	var notnull int
	var dflt sql.NullString
	if err := rows.Scan(&name, &typ, &notnull, &dflt); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !strings.EqualFold(typ, "TEXT") {
		t.Errorf("type=%q want TEXT", typ)
	}
	if notnull != 1 {
		t.Errorf("notnull=%d want 1", notnull)
	}
	if !dflt.Valid || dflt.String != "'[]'" {
		t.Errorf("default=%v want '[]'", dflt)
	}
	var maxV int
	if err := db.QueryRow(`SELECT max(version) FROM migration_versions`).Scan(&maxV); err != nil {
		t.Fatalf("max: %v", err)
	}
	if maxV < 10 {
		t.Errorf("want at least version 10 applied; got %d", maxV)
	}
}

// Pre-existing-column legacy bootstrap: simulate a DB whose schema already
// has items.requires_capability but whose migration_versions row got dropped
// (e.g., after a CHORE-009-style rebuild). bootstrapLegacyVersions should
// stamp v10 so migration 010 doesn't try to ALTER the column a second time.
func TestMigrate_BootstrapStampsRequiresCapabilityV10(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE migration_versions`); err != nil {
		t.Fatalf("drop migration_versions: %v", err)
	}
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("bootstrap re-migrate: %v", err)
	}
	var has int
	if err := db.QueryRow(
		`SELECT count(*) FROM migration_versions WHERE version=10`,
	).Scan(&has); err != nil {
		t.Fatalf("count v10: %v", err)
	}
	if has != 1 {
		t.Errorf("v10 marker not stamped after bootstrap; got %d rows", has)
	}
}

func TestMigrate_AppliesAgentsCapabilities(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rows, err := db.Query(`SELECT name, type, "notnull", dflt_value
		FROM pragma_table_info('agents') WHERE name='capabilities'`)
	if err != nil {
		t.Fatalf("pragma agents: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("agents.capabilities column missing")
	}
	var name, typ string
	var notnull int
	var dflt sql.NullString
	if err := rows.Scan(&name, &typ, &notnull, &dflt); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !strings.EqualFold(typ, "TEXT") {
		t.Errorf("type=%q want TEXT", typ)
	}
	if notnull != 1 {
		t.Errorf("notnull=%d want 1", notnull)
	}
	if !dflt.Valid || dflt.String != "'[]'" {
		t.Errorf("default=%v want '[]'", dflt)
	}
	var maxV int
	if err := db.QueryRow(`SELECT max(version) FROM migration_versions`).Scan(&maxV); err != nil {
		t.Fatalf("max: %v", err)
	}
	if maxV < 11 {
		t.Errorf("want at least version 11; got %d", maxV)
	}
}

func TestMigrate_BootstrapStampsAgentsCapabilitiesV11(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE migration_versions`); err != nil {
		t.Fatalf("drop migration_versions: %v", err)
	}
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("bootstrap re-migrate: %v", err)
	}
	var has int
	if err := db.QueryRow(`SELECT count(*) FROM migration_versions WHERE version=11`).Scan(&has); err != nil {
		t.Fatalf("count v11: %v", err)
	}
	if has != 1 {
		t.Errorf("v11 marker not stamped after bootstrap; got %d rows", has)
	}
}

func TestMigrate_AppliesClaimTimeboxNudges(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cols := pragmaCols(t, db, "claims")
	for _, name := range []string{"nudged_90m_at", "nudged_120m_at"} {
		if got, ok := cols[name]; !ok {
			t.Errorf("claims.%s missing", name)
		} else if !strings.EqualFold(got, "INTEGER") {
			t.Errorf("claims.%s type=%q want INTEGER", name, got)
		}
	}
	var maxV int
	if err := db.QueryRow(`SELECT max(version) FROM migration_versions`).Scan(&maxV); err != nil {
		t.Fatalf("max: %v", err)
	}
	if maxV < 15 {
		t.Errorf("want at least version 15; got %d", maxV)
	}
}

func TestMigrate_BootstrapStampsClaimTimeboxV12(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE migration_versions`); err != nil {
		t.Fatalf("drop migration_versions: %v", err)
	}
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("bootstrap re-migrate: %v", err)
	}
	var has int
	if err := db.QueryRow(`SELECT count(*) FROM migration_versions WHERE version=12`).Scan(&has); err != nil {
		t.Fatalf("count v12: %v", err)
	}
	if has != 1 {
		t.Errorf("v12 marker not stamped after bootstrap; got %d rows", has)
	}
}

func TestMigrate_AppliesIntakeInterview(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	wantSession := map[string]string{
		"id":             "TEXT",
		"repo_id":        "TEXT",
		"agent_id":       "TEXT",
		"mode":           "TEXT",
		"refine_item_id": "TEXT",
		"idea_seed":      "TEXT",
		"status":         "TEXT",
		"shape":          "TEXT",
		"bundle_json":    "TEXT",
		"created_at":     "INTEGER",
		"updated_at":     "INTEGER",
		"committed_at":   "INTEGER",
	}
	gotSession := pragmaCols(t, db, "intake_sessions")
	if len(gotSession) == 0 {
		t.Fatalf("intake_sessions table not created")
	}
	for n, typ := range wantSession {
		if got, ok := gotSession[n]; !ok {
			t.Errorf("intake_sessions: missing column %s", n)
		} else if !strings.EqualFold(got, typ) {
			t.Errorf("intake_sessions.%s: want %s, got %s", n, typ, got)
		}
	}

	wantTurn := map[string]string{
		"id":            "INTEGER",
		"session_id":    "TEXT",
		"seq":           "INTEGER",
		"role":          "TEXT",
		"content":       "TEXT",
		"fields_filled": "TEXT",
		"created_at":    "INTEGER",
	}
	gotTurn := pragmaCols(t, db, "intake_turns")
	if len(gotTurn) == 0 {
		t.Fatalf("intake_turns table not created")
	}
	for n, typ := range wantTurn {
		if got, ok := gotTurn[n]; !ok {
			t.Errorf("intake_turns: missing column %s", n)
		} else if !strings.EqualFold(got, typ) {
			t.Errorf("intake_turns.%s: want %s, got %s", n, typ, got)
		}
	}

	var hasCol int
	if err := db.QueryRow(
		`SELECT count(*) FROM pragma_table_info('items') WHERE name='intake_session_id'`,
	).Scan(&hasCol); err != nil {
		t.Fatalf("items.intake_session_id check: %v", err)
	}
	if hasCol != 1 {
		t.Fatalf("items.intake_session_id column missing")
	}

	var idxCount, idxUnique int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master
		 WHERE type='index' AND name='idx_intake_sessions_open'`,
	).Scan(&idxCount); err != nil {
		t.Fatalf("idx exists: %v", err)
	}
	if idxCount != 1 {
		t.Fatalf("idx_intake_sessions_open not created")
	}
	if err := db.QueryRow(
		`SELECT "unique" FROM pragma_index_list('intake_sessions') WHERE name='idx_intake_sessions_open'`,
	).Scan(&idxUnique); err != nil {
		t.Fatalf("idx unique: %v", err)
	}
	if idxUnique != 1 {
		t.Fatalf("idx_intake_sessions_open must be UNIQUE; got unique=%d", idxUnique)
	}

	if _, err := db.Exec(
		`INSERT INTO intake_sessions (id, repo_id, agent_id, mode, idea_seed, created_at, updated_at)
		 VALUES ('intake-1', 'r', 'a', 'new', 'seed', 0, 0)`,
	); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO intake_sessions (id, repo_id, agent_id, mode, idea_seed, created_at, updated_at)
		 VALUES ('intake-2', 'r', 'a', 'new', 'seed', 0, 0)`,
	); err == nil {
		t.Fatalf("want UNIQUE violation on second open session for same (repo, agent)")
	}

	if _, err := db.Exec(
		`UPDATE intake_sessions SET status='cancelled' WHERE id='intake-1'`,
	); err != nil {
		t.Fatalf("cancel first: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO intake_sessions (id, repo_id, agent_id, mode, idea_seed, created_at, updated_at)
		 VALUES ('intake-3', 'r', 'a', 'new', 'seed', 0, 0)`,
	); err != nil {
		t.Fatalf("second open after cancel: %v", err)
	}

	var maxV int
	if err := db.QueryRow(`SELECT max(version) FROM migration_versions`).Scan(&maxV); err != nil {
		t.Fatalf("max: %v", err)
	}
	if maxV < 9 {
		t.Fatalf("want at least version 9; got %d", maxV)
	}
}

func TestMigrate_IntakeInterviewIdempotent_From008(t *testing.T) {
	db := openEmptyDBNoMigrate(t)
	v8only := fstest.MapFS{
		"migrations/001_initial.sql":           readMigration(t, "001_initial.sql"),
		"migrations/002_items_extras.sql":      readMigration(t, "002_items_extras.sql"),
		"migrations/003_subagent_events.sql":   readMigration(t, "003_subagent_events.sql"),
		"migrations/004_intake_provenance.sql": readMigration(t, "004_intake_provenance.sql"),
		"migrations/005_claims_repo_scope.sql": readMigration(t, "005_claims_repo_scope.sql"),
		"migrations/006_commits.sql":           readMigration(t, "006_commits.sql"),
		"migrations/007_claim_worktree.sql":    readMigration(t, "007_claim_worktree.sql"),
		"migrations/008_agent_events.sql":      readMigration(t, "008_agent_events.sql"),
	}
	if err := Migrate(context.Background(), db, v8only); err != nil {
		t.Fatalf("seed at v8: %v", err)
	}
	if err := Migrate(context.Background(), db, defaultMigrationsFS); err != nil {
		t.Fatalf("upgrade to latest: %v", err)
	}
	var maxV int
	if err := db.QueryRow(`SELECT max(version) FROM migration_versions`).Scan(&maxV); err != nil {
		t.Fatalf("max: %v", err)
	}
	if maxV != 15 {
		t.Fatalf("want max version 15 after 008→015 upgrade; got %d", maxV)
	}
}

func pragmaCols(t *testing.T, db *sql.DB, table string) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT name, type FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[name] = typ
	}
	return out
}
