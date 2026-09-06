package claims

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestForceRelease_RequiresReason(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	_ = s.Claim(ctx, "BUG-040", "agent-a", "", nil, false)

	_, err := s.ForceRelease(ctx, "BUG-040", "agent-admin", "")
	if !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("want ErrReasonRequired, got %v", err)
	}
}

func TestForceRelease_RemovesClaimRecordsHistoryWithReason(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()
	_ = s.Claim(ctx, "BUG-041", "agent-a", "", nil, false)

	prior, err := s.ForceRelease(ctx, "BUG-041", "agent-admin", "agent-a went silent")
	if err != nil {
		t.Fatalf("force-release: %v", err)
	}
	if prior != "agent-a" {
		t.Fatalf("prior holder = %q, want agent-a", prior)
	}

	var live int
	_ = db.QueryRow(`SELECT COUNT(*) FROM claims WHERE item_id='BUG-041'`).Scan(&live)
	if live != 0 {
		t.Fatalf("claim still active")
	}
	var outcome string
	_ = db.QueryRow(`SELECT outcome FROM claim_history WHERE item_id='BUG-041'`).Scan(&outcome)
	if outcome != "force_released" {
		t.Fatalf("outcome=%q want force_released", outcome)
	}

	var body, mentions string
	_ = db.QueryRow(`SELECT body, mentions FROM messages WHERE kind='system' AND thread='global' ORDER BY id DESC LIMIT 1`).Scan(&body, &mentions)
	if !strings.Contains(body, "force-released") || !strings.Contains(body, "agent-a went silent") {
		t.Fatalf("audit message missing reason: %q", body)
	}
	if !strings.Contains(mentions, "agent-a") {
		t.Fatalf("audit message missing prior-holder mention: %q", mentions)
	}
}

func TestForceRelease_RejectsUnclaimedItem(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if _, err := s.ForceRelease(ctx, "NEVER-EXISTED", "agent-admin", "no reason"); !errors.Is(err, ErrNotClaimed) {
		t.Fatalf("want ErrNotClaimed, got %v", err)
	}
}

func TestForceRelease_RejectsProtectedEnvironment(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	_ = s.Claim(ctx, "ENV-001", "agent-a", "deploy", nil, true)
	if _, err := s.ForceRelease(ctx, "ENV-001", "agent-admin", "task stopped"); !errors.Is(err, ErrProtectedResource) {
		t.Fatalf("want ErrProtectedResource, got %v", err)
	}
}
