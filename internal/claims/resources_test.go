package claims

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func policy() []ResourceDefinition {
	return []ResourceDefinition{
		{"ENV-001", "staging", "*", "studio"}, {"ENV-002", "production", "*", "studio"},
		{"ENV-003", "staging", "importer", "importer"}, {"ENV-004", "production", "importer", "importer"},
		{"ENV-005", "staging", "feedback", "feedback"},
	}
}
func configured(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	s, db := newTestStore(t)
	if err := s.DefineResources(context.Background(), policy()); err != nil {
		t.Fatal(err)
	}
	return s, db
}
func mustClaim(t *testing.T, s *Store, item, agent, scope string) {
	t.Helper()
	if err := s.Claim(context.Background(), item, agent, "test", nil, true, ClaimWithScope(scope)); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyScopeAndIndependentServices(t *testing.T) {
	ctx := context.Background()
	s, db := configured(t)
	mustClaim(t, s, "ENV-002", "old", "")
	var conflict *ResourceConflictError
	if err := s.Claim(ctx, "ENV-004", "importer", "", nil, true); !errors.As(err, &conflict) {
		t.Fatalf("legacy must block: %v", err)
	}
	if err := s.Release(ctx, "ENV-002", "old", ""); err != nil {
		t.Fatal(err)
	}
	var scope string
	if err := db.QueryRow(`SELECT resource_scope FROM claim_history WHERE item_id='ENV-002'`).Scan(&scope); err != nil || scope != "*" {
		t.Fatalf("audit %s %v", scope, err)
	}
	mustClaim(t, s, "ENV-002", "studio", "studio")
	mustClaim(t, s, "ENV-004", "importer", "")
	mustClaim(t, s, "ENV-001", "staging", "")
	if err := s.Claim(ctx, "ENV-002", "studio", "", nil, true); !errors.Is(err, ErrAlreadyHeld) {
		t.Fatalf("self wait: %v", err)
	}
}

func TestLegacyBinaryInsertCannotBypassResourceConflict(t *testing.T) {
	s, db := configured(t)
	mustClaim(t, s, "ENV-004", "importer", "")
	insert := `INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch) VALUES('repo-test',?,?,1,1)`
	if _, err := db.Exec(insert, "ENV-002", "old"); err == nil {
		t.Fatal("legacy INSERT bypassed new claim")
	}
	if err := s.Release(context.Background(), "ENV-004", "importer", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, "ENV-002", "old"); err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(context.Background(), "ENV-004", "new", "", nil, true); err == nil {
		t.Fatal("new claim bypassed legacy INSERT")
	}
	var scope string
	_ = db.QueryRow(`SELECT resource_scope FROM claims WHERE item_id='ENV-002'`).Scan(&scope)
	if scope != "*" {
		t.Fatalf("legacy snapshot %q", scope)
	}
}

func TestResourcePolicyChangesRejectActiveClaimsAndWaiters(t *testing.T) {
	ctx := context.Background()
	s, _ := configured(t)
	mustClaim(t, s, "ENV-002", "a", "studio")
	if err := s.DefineResources(ctx, policy()); err == nil {
		t.Fatal("changed active policy")
	}
	if err := s.Release(ctx, "ENV-002", "a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Wait(ctx, "wait", "a", "ENV-002", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.DefineResources(ctx, policy()); err == nil {
		t.Fatal("changed policy with waiter")
	}
	if err := s.EndWait(ctx, "wait"); err != nil {
		t.Fatal(err)
	}
	if err := s.DefineResources(ctx, policy()); err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(ctx, "ENV-002", "a", "", nil, true, ClaimWithScope("importer")); err == nil {
		t.Fatal("invalid service scope admitted")
	}
	if err := s.Claim(ctx, "ENV-UNKNOWN", "a", "", nil, true, ClaimWithScope("studio")); err == nil {
		t.Fatal("unregistered scoped resource admitted")
	}
}

func TestDeadlockRejectsWaitAtomicallyAndPreservesOwnership(t *testing.T) {
	ctx := context.Background()
	s, db := configured(t)
	mustClaim(t, s, "ENV-001", "a", "")
	mustClaim(t, s, "ENV-002", "b", "")
	if _, err := s.Wait(ctx, "a-wait", "a", "ENV-004", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	_, err := s.Wait(ctx, "b-wait", "b", "ENV-005", "", time.Minute)
	var deadlock *DeadlockError
	if !errors.As(err, &deadlock) || len(deadlock.Cycle) != 2 {
		t.Fatalf("want two-edge cycle: %v", err)
	}
	var waits, held int
	_ = db.QueryRow(`SELECT count(*) FROM claim_waits`).Scan(&waits)
	_ = db.QueryRow(`SELECT count(*) FROM claims`).Scan(&held)
	if waits != 1 || held != 2 {
		t.Fatalf("waits=%d claims=%d", waits, held)
	}
	if _, err := s.Wait(ctx, "self", "a", "ENV-001", "", time.Minute); !errors.Is(err, ErrAlreadyHeld) {
		t.Fatalf("self wait: %v", err)
	}
}

func TestWaitExpiryAndCancellationDoNotReleaseClaims(t *testing.T) {
	ctx := context.Background()
	s, db := configured(t)
	mustClaim(t, s, "ENV-001", "a", "")
	mustClaim(t, s, "ENV-002", "b", "")
	if _, err := s.Wait(ctx, "a", "a", "ENV-002", "", time.Second); err != nil {
		t.Fatal(err)
	}
	initial := s.now()
	s.now = func() time.Time { return initial.Add(2 * time.Second) }
	if _, err := s.Wait(ctx, "b", "b", "ENV-001", "", time.Minute); err != nil {
		t.Fatalf("expired waiter formed false cycle: %v", err)
	}
	if err := s.EndWait(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM claims`).Scan(&n)
	if n != 2 {
		t.Fatal("expiry released ownership")
	}
}

func TestResourceClaimsRaceAcrossDifferentIDs(t *testing.T) {
	s, db := configured(t)
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, item := range []string{"ENV-002", "ENV-004"} {
		wg.Add(1)
		go func(item string) { defer wg.Done(); <-start; results <- s.Claim(ctx, item, item, "", nil, true) }(item)
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	var count int
	_ = db.QueryRow(`SELECT count(*) FROM claims`).Scan(&count)
	if success != 1 || count != 1 {
		t.Fatalf("race success=%d claims=%d", success, count)
	}
}

func TestResourceRecoveryPreservesScopeAndFencesOldHolder(t *testing.T) {
	s, db := configured(t)
	ctx := context.Background()
	mustClaim(t, s, "ENV-002", "agent-old", "studio")
	req := recoveryRequest()
	req.ItemID = "ENV-002"
	if _, err := s.Recover(ctx, req); err != nil {
		t.Fatal(err)
	}
	var scope string
	_ = db.QueryRow(`SELECT resource_scope FROM claims WHERE item_id='ENV-002'`).Scan(&scope)
	if scope != "studio" {
		t.Fatalf("scope lost: %q", scope)
	}
	_ = db.QueryRow(`SELECT resource_scope FROM claim_history WHERE item_id='ENV-002'`).Scan(&scope)
	if scope != "studio" {
		t.Fatal("recovery audit lost scope")
	}
	if err := s.Release(ctx, "ENV-002", "agent-old", ""); !errors.Is(err, ErrNotYours) {
		t.Fatalf("stale release: %v", err)
	}
	mustClaim(t, s, "ENV-004", "importer", "")
}

func TestPolicyRegistrationDoesNotReinterpretExistingClaims(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()
	mustClaim(t, s, "ENV-002", "old", "")
	if err := s.DefineResources(ctx, policy()); err == nil {
		t.Fatal("active legacy claim reinterpreted")
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM resource_definitions`).Scan(&n)
	if n != 0 {
		t.Fatal("failed policy installation partially committed")
	}
	var scope string
	_ = db.QueryRow(`SELECT resource_scope FROM claims WHERE item_id='ENV-002'`).Scan(&scope)
	if scope != "" {
		t.Fatal("legacy claim changed")
	}
}

func TestDeadlockDetectsThreeAgentsAndIgnoresOtherLedger(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()
	for _, p := range [][2]string{{"ENV-001", "a"}, {"ENV-002", "b"}, {"ENV-003", "c"}} {
		mustClaim(t, s, p[0], p[1], "")
	}
	if _, err := s.Wait(ctx, "a", "a", "ENV-002", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Wait(ctx, "b", "b", "ENV-003", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	other := New(db, "other", s.now)
	if _, err := other.Wait(ctx, "other", "c", "ENV-001", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	_, err := s.Wait(ctx, "c", "c", "ENV-001", "", time.Minute)
	var cycle *DeadlockError
	if !errors.As(err, &cycle) || len(cycle.Cycle) != 3 {
		t.Fatalf("missing 3-agent cycle: %v", err)
	}
}

func TestLegacyBinaryReleaseRecordsScope(t *testing.T) {
	s, db := configured(t)
	mustClaim(t, s, "ENV-002", "old", "")
	if _, err := db.Exec(`INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES('repo-test','ENV-002','old',1,2,'released')`); err != nil {
		t.Fatal(err)
	}
	var scope string
	_ = db.QueryRow(`SELECT resource_scope FROM claim_history WHERE item_id='ENV-002'`).Scan(&scope)
	if scope != "*" {
		t.Fatalf("old release lost scope %q", scope)
	}
}

func TestResourceAdmissionDistinguishesMissingPolicyFromContention(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	done := t.TempDir()
	if _, err := s.CheckResource(ctx, "ENV-003", "", dir, done, true); err == nil {
		t.Fatal("missing item admitted")
	}
	if err := os.WriteFile(filepath.Join(dir, "ENV-003.md"), []byte("---\nid: ENV-003\ntitle: importer\ntype: env\nstatus: open\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckResource(ctx, "ENV-003", "", dir, done, true); err == nil {
		t.Fatal("missing policy admitted")
	}
	if err := s.DefineResources(ctx, policy()); err != nil {
		t.Fatal(err)
	}
	mustClaim(t, s, "ENV-003", "holder", "")
	result, err := s.CheckResource(ctx, "ENV-003", "", dir, done, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result["blockers"].([]Blocker)) != 1 {
		t.Fatal("holder not reported")
	}
	if _, err := s.CheckResource(ctx, "ENV-003", "studio", dir, done, true); err == nil {
		t.Fatal("wrong scope admitted")
	}
}
