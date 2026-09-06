package hygiene

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/store"
)

type emptyItems struct{}

func (emptyItems) List(context.Context) ([]ItemRef, error)     { return nil, nil }
func (emptyItems) Broken(context.Context) ([]BrokenRef, error) { return nil, nil }

type fakeItems struct {
	refs   []ItemRef
	broken []BrokenRef
}

func (f fakeItems) List(context.Context) ([]ItemRef, error)     { return f.refs, nil }
func (f fakeItems) Broken(context.Context) ([]BrokenRef, error) { return f.broken, nil }

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "global.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func registerAgent(t *testing.T, db *sql.DB, repoID, id string, lastTickAt int64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO agents (id, repo_id, display_name, worktree, pid, started_at, last_tick_at, status)
		VALUES (?, ?, ?, '/tmp/wt', 1, ?, ?, 'active')
	`, id, repoID, id, lastTickAt, lastTickAt); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
}

func insertClaim(t *testing.T, db *sql.DB, repoID, itemID, agentID string, lastTouch int64, long int) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO claims (repo_id, item_id, agent_id, claimed_at, last_touch, intent, long)
		VALUES (?, ?, ?, ?, ?, '', ?)
	`, repoID, itemID, agentID, lastTouch, lastTouch, long); err != nil {
		t.Fatalf("insert claim: %v", err)
	}
}

func joinFindings(fs []Finding) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = f.Message
	}
	return strings.Join(parts, "\n")
}

func TestSweep_FlagsStaleClaim(t *testing.T) {
	db := newDB(t)
	t0 := int64(1_000_000)
	registerAgent(t, db, "repo-test", "agent-a", t0)
	insertClaim(t, db, "repo-test", "BUG-X", "agent-a", t0, 0) // last_touch = t0

	// Now is t0 + 75 minutes — claim is stale (>60m default).
	now := time.Unix(t0+75*60, 0)
	sw := NewWithClock(db, "repo-test", emptyItems{}, func() time.Time { return now })
	findings, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joinFindings(findings), "BUG-X") {
		t.Fatalf("no stale claim flagged: %s", joinFindings(findings))
	}
}

func TestSweep_FlagsGhostAgent(t *testing.T) {
	db := newDB(t)
	t0 := int64(1_000_000)
	registerAgent(t, db, "repo-test", "agent-zombie", t0)

	now := time.Unix(t0+25*3600, 0)
	sw := NewWithClock(db, "repo-test", emptyItems{}, func() time.Time { return now })
	findings, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joinFindings(findings), "agent-zombie") {
		t.Fatalf("ghost agent not flagged: %v", findings)
	}
}

func TestSweep_FlagsOrphanTouch(t *testing.T) {
	db := newDB(t)
	t0 := int64(1_000_000)
	registerAgent(t, db, "repo-test", "agent-a", t0)
	if _, err := db.Exec(`
		INSERT INTO touches (repo_id, agent_id, item_id, path, started_at)
		VALUES (?, ?, ?, ?, ?)
	`, "repo-test", "agent-a", "GONE-9", "ghost.go", t0); err != nil {
		t.Fatal(err)
	}

	sw := NewWithClock(db, "repo-test", emptyItems{}, func() time.Time { return time.Unix(t0, 0) })
	findings, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joinFindings(findings), "ghost.go") {
		t.Fatalf("orphan touch not flagged: %v", findings)
	}
}

func TestSweep_FlagsPhantomClaim(t *testing.T) {
	db := newDB(t)
	t0 := int64(1_000_000)
	registerAgent(t, db, "repo-test", "agent-a", t0)
	if _, err := db.Exec(`
		INSERT INTO claims (repo_id, item_id, agent_id, claimed_at, last_touch, intent, long)
		VALUES (?, ?, ?, ?, ?, ?, 0)
	`, "repo-test", "GONE-1", "agent-a", t0, t0, "claim against deleted item"); err != nil {
		t.Fatal(err)
	}

	sw := NewWithClock(db, "repo-test", emptyItems{}, func() time.Time { return time.Unix(t0, 0) })
	findings, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var phantom *Finding
	for i := range findings {
		if findings[i].Code == "phantom_claim" {
			phantom = &findings[i]
			break
		}
	}
	if phantom == nil {
		t.Fatalf("phantom_claim not flagged: %v", findings)
	}
	if !strings.Contains(phantom.Message, "GONE-1") {
		t.Fatalf("phantom finding missing item id: %v", phantom)
	}
	if !strings.Contains(phantom.Message, "agent-a") {
		t.Fatalf("phantom finding missing holder: %v", phantom)
	}
}

func TestSweep_CleanIntegrityProducesNoFinding(t *testing.T) {
	db := newDB(t)
	sw := NewWithClock(db, "repo-test", emptyItems{}, time.Now)
	findings, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Code == "integrity_check" {
			t.Fatalf("clean DB should not produce integrity finding: %s", f.Message)
		}
	}
}

func TestSweep_FlagsDoneItemMarkedInProgress(t *testing.T) {
	db := newDB(t)
	items := fakeItems{refs: []ItemRef{
		{ID: "BUG-77", Path: ".squad/done/BUG-77.md", Status: "in_progress"},
	}}
	sw := NewWithClock(db, "repo-test", items, time.Now)
	findings, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joinFindings(findings), "BUG-77") {
		t.Fatalf("done-but-in-progress not flagged: %v", findings)
	}
}

func TestSweep_FlagsBrokenReference(t *testing.T) {
	db := newDB(t)
	items := fakeItems{refs: []ItemRef{
		{ID: "FEAT-1", Path: ".squad/items/FEAT-1.md", Status: "ready",
			References: []string{"/nonexistent/file.go:42"}},
	}}
	sw := NewWithClock(db, "repo-test", items, time.Now)
	findings, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joinFindings(findings), "/nonexistent/file.go") {
		t.Fatalf("broken ref not flagged: %v", findings)
	}
}

func TestSweep_FlagsDuplicateID(t *testing.T) {
	db := newDB(t)
	items := fakeItems{refs: []ItemRef{
		{ID: "FEAT-1", Path: ".squad/items/a.md", Status: "open"},
		{ID: "FEAT-1", Path: ".squad/items/b.md", Status: "open"},
	}}
	sw := NewWithClock(db, "repo-test", items, time.Now)
	findings, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := joinFindings(findings)
	if !strings.Contains(got, "duplicate item id FEAT-1") {
		t.Fatalf("duplicate_id not flagged: %v", findings)
	}
}

func TestSweep_FlagsBlockedBySelf(t *testing.T) {
	db := newDB(t)
	items := fakeItems{refs: []ItemRef{
		{ID: "FEAT-1", Path: ".squad/items/a.md", Status: "open", BlockedBy: []string{"FEAT-1"}},
	}}
	sw := NewWithClock(db, "repo-test", items, time.Now)
	findings, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := joinFindings(findings)
	if !strings.Contains(got, "blocked-by itself") {
		t.Fatalf("blocked_by_self not flagged: %v", findings)
	}
}

func TestSweep_FlagsBlockedByUnknown(t *testing.T) {
	db := newDB(t)
	items := fakeItems{refs: []ItemRef{
		{ID: "FEAT-1", Path: ".squad/items/a.md", Status: "open", BlockedBy: []string{"GHOST-999"}},
	}}
	sw := NewWithClock(db, "repo-test", items, time.Now)
	findings, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := joinFindings(findings)
	if !strings.Contains(got, "blocked-by GHOST-999") {
		t.Fatalf("blocked_by_unknown not flagged: %v", findings)
	}
}

func TestSweep_FlagsMalformedItem(t *testing.T) {
	db := newDB(t)
	items := fakeItems{
		broken: []BrokenRef{{Path: ".squad/items/crlf.md", Error: "no frontmatter"}},
	}
	sw := NewWithClock(db, "repo-test", items, time.Now)
	findings, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := joinFindings(findings)
	if !strings.Contains(got, "could not parse .squad/items/crlf.md") {
		t.Fatalf("malformed_item not flagged: %v", findings)
	}
}

func TestSweep_EvidenceRequiredSurvivesAdapter(t *testing.T) {
	db := newDB(t)
	fake := fakeItems{refs: []ItemRef{
		{
			ID: "FEAT-001", Path: "/x/.squad/items/FEAT-001.md",
			Status: "in_progress", EvidenceRequired: []string{"test", "review"},
		},
	}}
	sw := New(db, "repo-test", fake)
	if _, err := sw.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got := fake.refs[0].EvidenceRequired
	if len(got) != 2 || got[0] != "test" || got[1] != "review" {
		t.Fatalf("EvidenceRequired = %v", got)
	}
}

func TestSweep_EvidenceMissingForDoneItems(t *testing.T) {
	db := newDB(t)
	repoID := "repo-test"

	if _, err := db.Exec(`
		INSERT INTO attestations (item_id, kind, command, exit_code, output_hash, output_path, created_at, agent_id, repo_id)
		VALUES (?, 'test', 'go test', 0, 'h1', '/tmp/h1.txt', 100, 'a', ?)
	`, "FEAT-001", repoID); err != nil {
		t.Fatal(err)
	}

	fake := fakeItems{refs: []ItemRef{{
		ID:               "FEAT-001",
		Path:             "/tmp/.squad/done/FEAT-001.md",
		Status:           "done",
		EvidenceRequired: []string{"test", "review"},
	}}}
	sw := New(db, repoID, fake)
	findings, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var got *Finding
	for i := range findings {
		if findings[i].Code == "evidence_missing" {
			got = &findings[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("expected evidence_missing finding, got %+v", findings)
	}
	if !strings.Contains(got.Message, "FEAT-001") || !strings.Contains(got.Message, "review") {
		t.Fatalf("finding message = %q", got.Message)
	}
}

func TestSweep_NoEvidenceMissingForActiveItems(t *testing.T) {
	db := newDB(t)
	fake := fakeItems{refs: []ItemRef{{
		ID:               "FEAT-001",
		Path:             "/tmp/.squad/items/FEAT-001.md",
		Status:           "in_progress",
		EvidenceRequired: []string{"test", "review"},
	}}}
	sw := New(db, "repo-test", fake)
	findings, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Code == "evidence_missing" {
			t.Fatalf("active item should not flag evidence_missing: %v", f)
		}
	}
}

func TestReclaimStale_ShortClaim(t *testing.T) {
	db := newDB(t)
	t0 := int64(1_000_000)
	registerAgent(t, db, "repo-test", "agent-a", t0)
	insertClaim(t, db, "repo-test", "BUG-200", "agent-a", t0, 0)

	now := time.Unix(t0+61*60, 0)
	sw := NewWithClock(db, "repo-test", emptyItems{}, func() time.Time { return now })
	got, err := sw.ReclaimStale(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "BUG-200" {
		t.Fatalf("got=%v want [BUG-200]", got)
	}
	var c int
	_ = db.QueryRow(`SELECT COUNT(*) FROM claims WHERE item_id='BUG-200'`).Scan(&c)
	if c != 0 {
		t.Fatalf("claim still present")
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM claim_history WHERE item_id='BUG-200' AND outcome='reclaimed'`).Scan(&c)
	if c != 1 {
		t.Fatalf("history row missing")
	}
	// Regression: the auto-reclaim must surface on chat so the SSE pump
	// can notify any connected dashboard. Without this the reclaim is
	// silent and the dashboard shows the stale agent as still claiming
	// until a manual refresh.
	var msgCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM messages WHERE thread='global' AND kind='release' AND agent_id='agent-a'`).Scan(&msgCount)
	if msgCount != 1 {
		t.Fatalf("expected 1 release message on global, got %d", msgCount)
	}
}

func TestReclaimStale_LongClaimWaitsTwoHours(t *testing.T) {
	db := newDB(t)
	t0 := int64(1_000_000)
	registerAgent(t, db, "repo-test", "agent-a", t0)
	insertClaim(t, db, "repo-test", "BUG-201", "agent-a", t0, 1) // long=1

	clock := time.Unix(t0+31*60, 0)
	sw := NewWithClock(db, "repo-test", emptyItems{}, func() time.Time { return clock })
	got, _ := sw.ReclaimStale(context.Background())
	if len(got) != 0 {
		t.Fatalf("long claim reclaimed early: %v", got)
	}

	clock = time.Unix(t0+121*60, 0)
	got, _ = sw.ReclaimStale(context.Background())
	if len(got) != 1 {
		t.Fatalf("long claim not reclaimed after 2h: %v", got)
	}
}

func TestReclaimStale_NeverDeletesProtectedEnvironmentClaim(t *testing.T) {
	db := newDB(t)
	t0 := int64(1_000_000)
	registerAgent(t, db, "repo-test", "agent-a", t0)
	insertClaim(t, db, "repo-test", "ENV-001", "agent-a", t0, 1)

	now := time.Unix(t0+48*3600, 0)
	sw := NewWithClock(db, "repo-test", emptyItems{}, func() time.Time { return now })
	got, err := sw.ReclaimStale(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("protected ENV claim auto-reclaimed: %v", got)
	}
	var holder string
	if err := db.QueryRow(`SELECT agent_id FROM claims WHERE repo_id='repo-test' AND item_id='ENV-001'`).Scan(&holder); err != nil {
		t.Fatalf("ENV claim disappeared: %v", err)
	}
}

func TestSweep_ProtectedEnvironmentSuggestsRecoveryNotForceRelease(t *testing.T) {
	db := newDB(t)
	t0 := int64(1_000_000)
	registerAgent(t, db, "repo-test", "agent-a", t0)
	insertClaim(t, db, "repo-test", "ENV-001", "agent-a", t0, 1)
	now := time.Unix(t0+3*3600, 0)
	findings, err := NewWithClock(db, "repo-test", emptyItems{}, func() time.Time { return now }).Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Code == "stale_claim" && strings.Contains(f.Message, "ENV-001") {
			if !strings.Contains(f.Fix, "squad recover ENV-001") || strings.Contains(f.Fix, "force-release") {
				t.Fatalf("unsafe protected-resource fix: %q", f.Fix)
			}
			return
		}
	}
	t.Fatalf("missing protected ENV stale finding: %+v", findings)
}

func TestMarkStaleAgents_FlipsStatus(t *testing.T) {
	db := newDB(t)
	t0 := int64(1_000_000)
	registerAgent(t, db, "repo-test", "agent-a", t0)
	insertClaim(t, db, "repo-test", "BUG-300", "agent-a", t0, 0)

	now := time.Unix(t0+25*3600, 0)
	sw := NewWithClock(db, "repo-test", emptyItems{}, func() time.Time { return now })
	if err := sw.MarkStaleAgents(context.Background()); err != nil {
		t.Fatal(err)
	}

	var status string
	_ = db.QueryRow(`SELECT status FROM agents WHERE id='agent-a'`).Scan(&status)
	if status != "stale" {
		t.Fatalf("status=%q want 'stale'", status)
	}
	var c int
	_ = db.QueryRow(`SELECT COUNT(*) FROM claims WHERE item_id='BUG-300'`).Scan(&c)
	if c != 1 {
		t.Fatalf("claim was auto-released; should require force-release")
	}
}

func TestStripLineSuffix(t *testing.T) {
	cases := map[string]string{
		"path/foo.go:42":  "path/foo.go",
		"path/foo.go":     "path/foo.go",
		"path/foo.go:abc": "path/foo.go:abc",
		"path/foo.go:":    "path/foo.go:",
	}
	for in, want := range cases {
		if got := stripLineSuffix(in); got != want {
			t.Errorf("stripLineSuffix(%q)=%q want %q", in, got, want)
		}
	}
}
