package main

import (
	"context"
	"errors"
	"testing"

	"github.com/zsiec/squad/internal/claims"
)

func TestRelease_PureReleasesHeldClaim(t *testing.T) {
	env := newTestEnv(t)
	writeMinimalItem(t, env.ItemsDir, "BUG-400")
	if _, err := Claim(context.Background(), ClaimArgs{
		DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID,
		ItemID: "BUG-400", ItemsDir: env.ItemsDir, DoneDir: env.DoneDir,
	}); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	res, err := Release(context.Background(), ReleaseArgs{
		DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID,
		ItemID: "BUG-400", Outcome: "released",
	})
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if res == nil || res.ItemID != "BUG-400" || res.Outcome != "released" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestRelease_PureRejectsUnclaimed(t *testing.T) {
	env := newTestEnv(t)
	_, err := Release(context.Background(), ReleaseArgs{
		DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID,
		ItemID: "GHOST-1",
	})
	if !errors.Is(err, claims.ErrNotClaimed) {
		t.Fatalf("err=%v want ErrNotClaimed", err)
	}
}

func TestRelease_PureRejectsNotYours(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.DB.Exec(`
		INSERT INTO claims (repo_id, item_id, agent_id, claimed_at, last_touch, intent, long)
		VALUES (?, ?, 'agent-other', 1000, 1000, '', 0)
	`, env.RepoID, "BUG-401"); err != nil {
		t.Fatal(err)
	}
	_, err := Release(context.Background(), ReleaseArgs{
		DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID,
		ItemID: "BUG-401",
	})
	if !errors.Is(err, claims.ErrNotYours) {
		t.Fatalf("err=%v want ErrNotYours", err)
	}
}

func TestRelease_NotifiesWaitersOnlyAfterSuccess(t *testing.T) {
	env := newTestEnv(t)
	writeMinimalItem(t, env.ItemsDir, "BUG-402")
	if _, err := Claim(context.Background(), ClaimArgs{
		DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID,
		ItemID: "BUG-402", ItemsDir: env.ItemsDir, DoneDir: env.DoneDir,
	}); err != nil {
		t.Fatal(err)
	}

	var notified []string
	notify := func(_ context.Context, itemID string) {
		notified = append(notified, itemID)
	}
	if _, err := Release(context.Background(), ReleaseArgs{
		DB: env.DB, RepoID: env.RepoID, AgentID: "agent-other",
		ItemID: "BUG-402", NotifyWaiters: notify,
	}); !errors.Is(err, claims.ErrNotYours) {
		t.Fatalf("wrong-holder release err=%v want ErrNotYours", err)
	}
	if len(notified) != 0 {
		t.Fatalf("failed release notified waiters: %v", notified)
	}

	if _, err := Release(context.Background(), ReleaseArgs{
		DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID,
		ItemID: "BUG-402", NotifyWaiters: notify,
	}); err != nil {
		t.Fatal(err)
	}
	if len(notified) != 1 || notified[0] != "BUG-402" {
		t.Fatalf("notifications=%v want [BUG-402]", notified)
	}
}
