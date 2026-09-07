package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zsiec/squad/internal/attest"
)

const riskMetadataItemFmt = `---
id: %s
title: a sufficiently long title for ready
type: bug
priority: %s
area: core
status: open
estimate: 1h
risk: %s
evidence_required: %s
created: 2026-04-25
updated: 2026-04-25
---

## Acceptance criteria
- [ ] x
`

func setupRiskMetadataEnv(t *testing.T, id, priority, risk, evidence string) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	if err := os.WriteFile(filepath.Join(env.ItemsDir, id+"-x.md"),
		[]byte(fmt.Sprintf(riskMetadataItemFmt, id, priority, risk, evidence)),
		0o644); err != nil {
		t.Fatal(err)
	}
	gitFixtureCommit(t, env.Root)
	gitConfigUser(t, env.Root)
	if _, err := Claim(context.Background(), ClaimArgs{
		DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID,
		ItemID: id, ItemsDir: env.ItemsDir, DoneDir: env.DoneDir,
	}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	return env
}

func TestDone_RiskMetadataDoesNotImposeReviewerQuota(t *testing.T) {
	tests := []struct {
		name, id, priority, risk string
	}{
		{name: "P0", id: "BUG-700", priority: "P0", risk: "low"},
		{name: "high risk", id: "BUG-701", priority: "P2", risk: "high"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupRiskMetadataEnv(t, tt.id, tt.priority, tt.risk, "[]")
			if _, err := Done(context.Background(), DoneArgs{
				DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID,
				ItemID: tt.id, Summary: "ship",
				ItemsDir: env.ItemsDir, DoneDir: env.DoneDir,
				RepoRoot: env.Root,
			}); err != nil {
				t.Fatalf("Done with priority=%s risk=%s: %v", tt.priority, tt.risk, err)
			}
		})
	}
}

func TestDone_HighRiskStillHonorsExplicitReviewEvidence(t *testing.T) {
	const id = "BUG-702"
	env := setupRiskMetadataEnv(t, id, "P0", "high", "[review]")

	_, err := Done(context.Background(), DoneArgs{
		DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID,
		ItemID: id, Summary: "ship",
		ItemsDir: env.ItemsDir, DoneDir: env.DoneDir,
		RepoRoot: env.Root,
	})
	if err == nil {
		t.Fatal("explicit review evidence should still be required")
	}

	ledger := attest.New(env.DB, env.RepoID, nil)
	if _, err := ledger.Insert(context.Background(), attest.Record{
		ItemID: id, Kind: attest.KindReview,
		Command: "review by agent-reviewer", ExitCode: 0,
		OutputHash: "clean-review", AgentID: env.AgentID,
	}); err != nil {
		t.Fatalf("seed review attestation: %v", err)
	}

	if _, err := Done(context.Background(), DoneArgs{
		DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID,
		ItemID: id, Summary: "ship",
		ItemsDir: env.ItemsDir, DoneDir: env.DoneDir,
		RepoRoot: env.Root,
	}); err != nil {
		t.Fatalf("Done after explicit review evidence: %v", err)
	}
}
