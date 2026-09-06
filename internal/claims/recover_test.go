package claims

import (
	"context"
	"errors"
	"testing"
)

func recoveryRequest() RecoveryRequest {
	return RecoveryRequest{
		ItemID: "ENV-001", ExpectedHolder: "agent-old", RecoveringAgent: "agent-new",
		HolderSession: "thread-stopped", Reason: "holder task was stopped",
		Evidence:             "Codex task status stopped; deployment workflow terminal; staging revision verified",
		ConfirmHolderStopped: true,
	}
}

func TestRecover_AtomicallyTransfersAndFencesClaim(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()
	if err := s.Claim(ctx, "ENV-001", "agent-old", "deploy", []string{"staging"}, true); err != nil {
		t.Fatal(err)
	}
	res, err := s.Recover(ctx, recoveryRequest())
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if res.FromAgent != "agent-old" || res.ToAgent != "agent-new" || res.PreviousGeneration != 1 || res.Generation != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
	var holder, state, previous, reason string
	var generation int64
	if err := db.QueryRow(`SELECT agent_id, state, generation, previous_agent_id, recovery_reason
		FROM claims WHERE repo_id='repo-test' AND item_id='ENV-001'`).
		Scan(&holder, &state, &generation, &previous, &reason); err != nil {
		t.Fatal(err)
	}
	if holder != "agent-new" || state != "recovering" || generation != 2 || previous != "agent-old" || reason == "" {
		t.Fatalf("claim not fenced: holder=%s state=%s generation=%d previous=%s reason=%q", holder, state, generation, previous, reason)
	}
	var recoveryRows, historyRows, activeOldTouches int
	_ = db.QueryRow(`SELECT count(*) FROM claim_recoveries WHERE item_id='ENV-001' AND generation=2`).Scan(&recoveryRows)
	_ = db.QueryRow(`SELECT count(*) FROM claim_history WHERE item_id='ENV-001' AND agent_id='agent-old' AND outcome='recovered'`).Scan(&historyRows)
	_ = db.QueryRow(`SELECT count(*) FROM touches WHERE item_id='ENV-001' AND agent_id='agent-old' AND released_at IS NULL`).Scan(&activeOldTouches)
	if recoveryRows != 1 || historyRows != 1 || activeOldTouches != 0 {
		t.Fatalf("audit/touch mismatch recovery=%d history=%d oldTouches=%d", recoveryRows, historyRows, activeOldTouches)
	}
	if err := s.Release(ctx, "ENV-001", "agent-old", "late release"); !errors.Is(err, ErrNotYours) {
		t.Fatalf("old holder must be fenced; got %v", err)
	}
}

func TestRecover_RequiresExplicitEvidence(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	_ = s.Claim(ctx, "ENV-001", "agent-old", "", nil, true)
	req := recoveryRequest()
	req.ConfirmHolderStopped = false
	if _, err := s.Recover(ctx, req); !errors.Is(err, ErrRecoveryConfirmationRequired) {
		t.Fatalf("want ErrRecoveryConfirmationRequired, got %v", err)
	}
	req.ConfirmHolderStopped = true
	req.Evidence = ""
	if _, err := s.Recover(ctx, req); !errors.Is(err, ErrRecoveryEvidenceRequired) {
		t.Fatalf("want ErrRecoveryEvidenceRequired, got %v", err)
	}
}

func TestRecover_RejectsChangedHolder(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	_ = s.Claim(ctx, "ENV-001", "agent-someone-else", "", nil, true)
	if _, err := s.Recover(ctx, recoveryRequest()); !errors.Is(err, ErrHolderChanged) {
		t.Fatalf("want ErrHolderChanged, got %v", err)
	}
}

func TestRecover_RejectsRecentRegisteredHeartbeat(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()
	_ = s.Claim(ctx, "ENV-001", "agent-old", "", nil, true)
	now := s.nowUnix()
	if _, err := db.Exec(`INSERT INTO agents
		(id, repo_id, display_name, started_at, last_tick_at, status)
		VALUES ('agent-old','repo-test','old',?,?, 'active')`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recover(ctx, recoveryRequest()); !errors.Is(err, ErrHolderRecentlyActive) {
		t.Fatalf("want ErrHolderRecentlyActive, got %v", err)
	}
}
