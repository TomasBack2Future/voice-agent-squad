package main

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zsiec/squad/internal/notify"
)

func TestClaimWithWait_AcquiresImmediatelyWithoutRegistering(t *testing.T) {
	env := newTestEnv(t)
	writeMinimalItem(t, env.ItemsDir, "BUG-500")
	registry := notify.NewRegistry(env.DB)

	res, err := ClaimWithWait(context.Background(), ClaimWaitArgs{
		Claim: ClaimArgs{
			DB: env.DB, RepoID: env.RepoID, AgentID: "agent-waiter",
			ItemID: "BUG-500", ItemsDir: env.ItemsDir, DoneDir: env.DoneDir,
		},
		Registry: registry,
		Instance: "waiter-immediate",
		Fallback: time.Second,
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("ClaimWithWait: %v", err)
	}
	if res.AgentID != "agent-waiter" {
		t.Fatalf("agent=%q want agent-waiter", res.AgentID)
	}
	eps, err := registry.LookupRepo(context.Background(), env.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 0 {
		t.Fatalf("immediate claim registered wait endpoint: %+v", eps)
	}
}

func TestClaimCommand_ExposesWaitControls(t *testing.T) {
	cmd := newClaimCmd()
	for _, name := range []string{"wait", "wait-timeout", "wait-fallback"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("claim command missing --%s", name)
		}
	}
}

func TestClaimWithWait_RejectsInvalidControlsBeforeClaiming(t *testing.T) {
	env := newTestEnv(t)
	writeMinimalItem(t, env.ItemsDir, "BUG-504")

	_, err := ClaimWithWait(context.Background(), ClaimWaitArgs{
		Claim: ClaimArgs{
			DB: env.DB, RepoID: env.RepoID, AgentID: "agent-waiter",
			ItemID: "BUG-504", ItemsDir: env.ItemsDir, DoneDir: env.DoneDir,
		},
		Registry: notify.NewRegistry(env.DB),
		Instance: "waiter-invalid",
		Timeout:  0,
		Fallback: time.Second,
	})
	if err == nil {
		t.Fatal("zero timeout should fail")
	}
	var n int
	if err := env.DB.QueryRow(`SELECT COUNT(*) FROM claims WHERE repo_id=? AND item_id=?`,
		env.RepoID, "BUG-504").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("invalid wait controls claimed the item")
	}
}

func TestClaimWithWait_WakesOnReleaseWithoutFallback(t *testing.T) {
	env := newTestEnv(t)
	writeMinimalItem(t, env.ItemsDir, "BUG-501")
	seedClaimForWait(t, env, "BUG-501", "agent-holder")
	registry := notify.NewRegistry(env.DB)

	var waitOutput bytes.Buffer
	result := make(chan *ClaimResult, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := ClaimWithWait(context.Background(), ClaimWaitArgs{
			Claim: ClaimArgs{
				DB: env.DB, RepoID: env.RepoID, AgentID: "agent-waiter",
				ItemID: "BUG-501", ItemsDir: env.ItemsDir, DoneDir: env.DoneDir,
			},
			Registry:   registry,
			Instance:   "waiter-release",
			Fallback:   10 * time.Second,
			Timeout:    2 * time.Second,
			WaitWriter: &waitOutput,
		})
		result <- res
		errs <- err
	}()

	waitForClaimWaiter(t, registry, env.RepoID, "BUG-501", 1)
	_, err := Release(context.Background(), ReleaseArgs{
		DB: env.DB, RepoID: env.RepoID, AgentID: "agent-holder",
		ItemID: "BUG-501", Outcome: "released",
		NotifyWaiters: func(ctx context.Context, itemID string) {
			_ = notify.WakeKind(ctx, registry, env.RepoID, notify.ClaimWaitKind(itemID), 100*time.Millisecond)
		},
	})
	if err != nil {
		t.Fatalf("Release: %v", err)
	}

	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("ClaimWithWait: %v", err)
		}
		res := <-result
		if res == nil || res.AgentID != "agent-waiter" {
			t.Fatalf("unexpected result: %+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not wake after release")
	}
	if got := waitOutput.String(); got == "" {
		t.Fatal("waiting transition should be reported once")
	}
}

func TestClaimWithWait_TimesOut(t *testing.T) {
	env := newTestEnv(t)
	writeMinimalItem(t, env.ItemsDir, "BUG-502")
	seedClaimForWait(t, env, "BUG-502", "agent-holder")

	_, err := ClaimWithWait(context.Background(), ClaimWaitArgs{
		Claim: ClaimArgs{
			DB: env.DB, RepoID: env.RepoID, AgentID: "agent-waiter",
			ItemID: "BUG-502", ItemsDir: env.ItemsDir, DoneDir: env.DoneDir,
		},
		Registry: notify.NewRegistry(env.DB),
		Instance: "waiter-timeout",
		Fallback: 10 * time.Millisecond,
		Timeout:  80 * time.Millisecond,
	})
	var timeoutErr *ClaimWaitTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err=%v want *ClaimWaitTimeoutError", err)
	}
}

func TestClaimWithWait_TwoWaitersProduceOneWinnerPerRelease(t *testing.T) {
	env := newTestEnv(t)
	writeMinimalItem(t, env.ItemsDir, "BUG-503")
	seedClaimForWait(t, env, "BUG-503", "agent-holder")
	registry := notify.NewRegistry(env.DB)

	type outcome struct {
		res *ClaimResult
		err error
	}
	outcomes := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, agentID := range []string{"agent-a", "agent-b"} {
		agentID := agentID
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := ClaimWithWait(context.Background(), ClaimWaitArgs{
				Claim: ClaimArgs{
					DB: env.DB, RepoID: env.RepoID, AgentID: agentID,
					ItemID: "BUG-503", ItemsDir: env.ItemsDir, DoneDir: env.DoneDir,
				},
				Registry: registry,
				Instance: "waiter-" + agentID,
				Fallback: 10 * time.Second,
				Timeout:  3 * time.Second,
			})
			outcomes <- outcome{res: res, err: err}
		}()
	}

	waitForClaimWaiter(t, registry, env.RepoID, "BUG-503", 2)
	releaseWithWaitWake(t, env, registry, "BUG-503", "agent-holder")

	first := <-outcomes
	if first.err != nil || first.res == nil {
		t.Fatalf("first outcome: res=%+v err=%v", first.res, first.err)
	}
	select {
	case second := <-outcomes:
		t.Fatalf("two waiters acquired one release: first=%+v second=%+v", first, second)
	case <-time.After(100 * time.Millisecond):
	}

	releaseWithWaitWake(t, env, registry, "BUG-503", first.res.AgentID)
	second := <-outcomes
	if second.err != nil || second.res == nil {
		t.Fatalf("second outcome: res=%+v err=%v", second.res, second.err)
	}
	if second.res.AgentID == first.res.AgentID {
		t.Fatalf("same waiter won twice: %s", second.res.AgentID)
	}
	wg.Wait()
}

func seedClaimForWait(t *testing.T, env *testEnv, itemID, agentID string) {
	t.Helper()
	if _, err := env.DB.Exec(`
		INSERT INTO claims (repo_id, item_id, agent_id, claimed_at, last_touch, intent, long)
		VALUES (?, ?, ?, 1000, 1000, '', 0)
	`, env.RepoID, itemID, agentID); err != nil {
		t.Fatal(err)
	}
}

func waitForClaimWaiter(t *testing.T, registry *notify.Registry, repoID, itemID string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		eps, err := registry.LookupRepo(context.Background(), repoID)
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		for _, ep := range eps {
			if ep.Kind == notify.ClaimWaitKind(itemID) {
				got++
			}
		}
		if got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("claim waiters for %s did not reach %d", itemID, want)
}

func releaseWithWaitWake(t *testing.T, env *testEnv, registry *notify.Registry, itemID, agentID string) {
	t.Helper()
	_, err := Release(context.Background(), ReleaseArgs{
		DB: env.DB, RepoID: env.RepoID, AgentID: agentID, ItemID: itemID,
		NotifyWaiters: func(ctx context.Context, itemID string) {
			_ = notify.WakeKind(ctx, registry, env.RepoID, notify.ClaimWaitKind(itemID), 100*time.Millisecond)
		},
	})
	if err != nil {
		t.Fatalf("Release(%s): %v", agentID, err)
	}
}
