package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zsiec/squad/internal/releaseevidence"
)

const releaseRef = "studio-prod-v0.7.52-9f8139a1"

const releaseItem = `---
id: TASK-064
title: '[release:studio-prod-v0.7.52-9f8139a1] Studio production release'
type: task
priority: P0
area: release
status: open
created: 2026-10-31
updated: 2026-10-31
evidence_required: [test]
---

## Acceptance criteria
- [ ] release proven
`

func releaseDoc(mut func(*releaseevidence.Document)) releaseevidence.Document {
	var d releaseevidence.Document
	d.Schema, d.ReleaseRef = releaseevidence.Schema, releaseRef
	d.Candidate.ApplicationSHA = strings.Repeat("9", 40)
	d.Candidate.Version = "v0.7.52"
	d.Candidate.ManifestSHA256 = strings.Repeat("a", 64)
	d.Execution.Result, d.Execution.Ref = "succeeded", "run/1"
	d.Acceptance = []releaseevidence.Check{{Name: "api-version", Result: "passed", Ref: "evidence/api.json"}}
	d.Observation = releaseevidence.Check{Result: "passed", Ref: "evidence/obs.json"}
	d.Cleanup.Result, d.Cleanup.Ref = "complete", "evidence/cleanup.json"
	d.Custody.Disposition, d.Custody.Ref = "released", "evidence/env.json"
	if mut != nil {
		mut(&d)
	}
	return d
}

func recordRelease(t *testing.T, env *testEnv, item string, d releaseevidence.Document) *AttestResult {
	t.Helper()
	f := filepath.Join(t.TempDir(), "evidence.json")
	b, _ := json.Marshal(d)
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Attest(context.Background(), AttestArgs{DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID, ItemID: item, Kind: "release", EvidenceFile: f, AttDir: filepath.Join(env.Root, ".squad", "attestations"), RepoRoot: env.Root})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func claimed(t *testing.T, env *testEnv, id, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(env.ItemsDir, id+".md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Claim(context.Background(), ClaimArgs{DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID, ItemID: id, ItemsDir: env.ItemsDir, DoneDir: env.DoneDir}); err != nil {
		t.Fatal(err)
	}
}

func doneNoForce(env *testEnv, id string) error {
	_, err := Done(context.Background(), DoneArgs{DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID, ItemID: id, ItemsDir: env.ItemsDir, DoneDir: env.DoneDir, RepoRoot: env.Root})
	return err
}

func TestReleaseItemClosesOnlyWithCompleteReleaseEvidence(t *testing.T) {
	env := newTestEnv(t)
	claimed(t, env, "TASK-064", releaseItem)

	// A generic test attestation or the Deployer role does not prove a release.
	if _, err := Attest(context.Background(), AttestArgs{DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID, ItemID: "TASK-064", Kind: "test", Command: "true", AttDir: filepath.Join(env.Root, ".squad", "attestations")}); err != nil {
		t.Fatal(err)
	}
	if err := doneNoForce(env, "TASK-064"); err == nil || !strings.Contains(err.Error(), "release") {
		t.Fatalf("test attestation closed a release: %v", err)
	}

	// Each incomplete state is recorded non-satisfying with its unmet conditions.
	for name, tc := range map[string]struct {
		mut   func(*releaseevidence.Document)
		state string
	}{
		"acceptance-incomplete": {func(d *releaseevidence.Document) { d.Acceptance[0].Result = "pending" }, "partial"},
		"cleanup-failed":        {func(d *releaseevidence.Document) { d.Cleanup.Result = "failed" }, "blocked"},
		"custody-retained": {func(d *releaseevidence.Document) {
			d.Custody.Disposition, d.Custody.Retained = "retained", []string{"ENV-002"}
		}, "custody-retained"},
	} {
		res := recordRelease(t, env, "TASK-064", releaseDoc(tc.mut))
		if res.ExitCode == 0 || res.Release == nil || res.Release.State != tc.state || len(res.Release.Unmet) == 0 {
			t.Fatalf("%s: %+v %+v", name, res, res.Release)
		}
		if err := doneNoForce(env, "TASK-064"); err == nil {
			t.Fatalf("%s closed the release", name)
		}
	}

	res := recordRelease(t, env, "TASK-064", releaseDoc(nil))
	if res.ExitCode != 0 || !res.Release.Satisfied {
		t.Fatalf("%+v", res)
	}
	// Re-recording identical evidence converges on the same row.
	if again := recordRelease(t, env, "TASK-064", releaseDoc(nil)); again.ID != res.ID {
		t.Fatalf("duplicate attestation %d != %d", again.ID, res.ID)
	}
	if err := doneNoForce(env, "TASK-064"); err != nil {
		t.Fatalf("complete release evidence rejected: %v", err)
	}
}

func TestReleaseEvidenceIsBoundToTheItemsReleaseRef(t *testing.T) {
	env := newTestEnv(t)
	claimed(t, env, "TASK-064", releaseItem)
	res := recordRelease(t, env, "TASK-064", releaseDoc(func(d *releaseevidence.Document) { d.ReleaseRef = "studio-prod-v0.7.51-a12eaedf" }))
	if res.ExitCode == 0 || res.Release.State != "invalid" {
		t.Fatalf("other release's evidence accepted: %+v", res.Release)
	}
}

func TestEngineeringItemKeepsTestRequirement(t *testing.T) {
	env := newTestEnv(t)
	body := strings.NewReplacer("area: release", "area: core", "[release:studio-prod-v0.7.52-9f8139a1] Studio production release", "Engineering fix").Replace(releaseItem)
	claimed(t, env, "TASK-065", strings.ReplaceAll(body, "TASK-064", "TASK-065"))
	// A release verdict cannot even be recorded on a non-release item...
	f := filepath.Join(t.TempDir(), "e.json")
	b, _ := json.Marshal(releaseDoc(nil))
	_ = os.WriteFile(f, b, 0o600)
	if _, err := Attest(context.Background(), AttestArgs{DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID, ItemID: "TASK-065", Kind: "release", EvidenceFile: f, AttDir: filepath.Join(env.Root, ".squad", "attestations"), RepoRoot: env.Root}); err == nil {
		t.Fatal("release verdict recorded on an engineering item")
	}
	// ...and the engineering item still demands a real test.
	if err := doneNoForce(env, "TASK-065"); err == nil || !strings.Contains(err.Error(), "test") {
		t.Fatalf("engineering evidence requirement degraded: %v", err)
	}
	if _, err := Attest(context.Background(), AttestArgs{DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID, ItemID: "TASK-065", Kind: "test", Command: "true", AttDir: filepath.Join(env.Root, ".squad", "attestations")}); err != nil {
		t.Fatal(err)
	}
	if err := doneNoForce(env, "TASK-065"); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseKindRejectsCommandAndOtherKindsRejectEvidenceFile(t *testing.T) {
	env := newTestEnv(t)
	claimed(t, env, "TASK-064", releaseItem)
	attDir := filepath.Join(env.Root, ".squad", "attestations")
	if _, err := Attest(context.Background(), AttestArgs{DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID, ItemID: "TASK-064", Kind: "release", Command: "true", AttDir: attDir, RepoRoot: env.Root}); err == nil {
		t.Fatal("kind=release accepted a command")
	}
	if _, err := Attest(context.Background(), AttestArgs{DB: env.DB, RepoID: env.RepoID, AgentID: env.AgentID, ItemID: "TASK-064", Kind: "test", Command: "true", EvidenceFile: "x", AttDir: attDir}); err == nil {
		t.Fatal("kind=test accepted evidence_file")
	}
}
