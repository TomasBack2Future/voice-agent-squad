package releaseevidence

import (
	"strings"
	"testing"
)

const ref = "studio-prod-v0.7.52-9f8139a1"

func complete() Document {
	var d Document
	d.Schema, d.ReleaseRef = Schema, ref
	d.Candidate.ApplicationSHA = strings.Repeat("9", 40)
	d.Candidate.Version = "v0.7.52"
	d.Candidate.ManifestSHA256 = strings.Repeat("a", 64)
	d.Execution.Result, d.Execution.Ref = "succeeded", "run/1"
	d.Acceptance = []Check{{Name: "api-version", Result: "passed", Ref: "evidence/api.json"}}
	d.Observation = Check{Result: "passed", Ref: "evidence/obs.json"}
	d.Cleanup.Result, d.Cleanup.Ref = "complete", "evidence/cleanup.json"
	d.Custody.Disposition, d.Custody.Ref = "released", "evidence/env.json"
	return d
}

func TestEvaluateStates(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name   string
		mutate func(*Document)
		state  string
		unmet  string
	}{
		{"complete", func(*Document) {}, StateComplete, ""},
		{"acceptance-pending", func(d *Document) { d.Acceptance[0].Result = "pending" }, StatePartial, "acceptance api-version not finished"},
		{"optional-pending-ok", func(d *Document) {
			d.Acceptance = append(d.Acceptance, Check{Name: "x", Required: &no, Result: "pending"})
		}, StateComplete, ""},
		{"no-acceptance", func(d *Document) { d.Acceptance = nil }, StatePartial, "at least one check"},
		{"observation-pending", func(d *Document) { d.Observation.Result = "pending" }, StatePartial, "observation not finished"},
		{"execution-pending", func(d *Document) { d.Execution.Result = "pending" }, StatePartial, "execution not finished"},
		{"execution-failed", func(d *Document) { d.Execution.Result = "failed" }, StateBlocked, "execution failed"},
		{"acceptance-failed", func(d *Document) { d.Acceptance[0].Result = "failed" }, StateBlocked, "failed"},
		{"cleanup-failed", func(d *Document) { d.Cleanup.Result = "failed" }, StateBlocked, "cleanup failed"},
		{"cleanup-pending", func(d *Document) { d.Cleanup.Result = "pending" }, StatePartial, "cleanup not finished"},
		{"custody-retained", func(d *Document) { d.Custody.Disposition, d.Custody.Retained = "retained", []string{"ENV-002"} }, StateCustodyRetained, "ENV-002"},
		{"retained-without-names", func(d *Document) { d.Custody.Disposition = "retained" }, StateInvalid, "retained"},
		{"wrong-ref", func(d *Document) { d.ReleaseRef = "other" }, StateInvalid, "release_ref"},
		{"short-sha", func(d *Document) { d.Candidate.ApplicationSHA = "9f8139a1" }, StateInvalid, "40-hex"},
		{"no-manifest", func(d *Document) { d.Candidate.ManifestSHA256 = "" }, StateInvalid, "manifest_sha256"},
		{"no-execution-ref", func(d *Document) { d.Execution.Ref = "" }, StatePartial, "execution.ref"},
		{"role-is-not-evidence", func(d *Document) { d.Execution.Result = "deployer-exited" }, StateInvalid, "execution.result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := complete()
			tc.mutate(&d)
			v := Evaluate(d, ref)
			if v.State != tc.state || v.Satisfied != (tc.state == StateComplete) {
				t.Fatalf("state=%s satisfied=%v unmet=%v", v.State, v.Satisfied, v.Unmet)
			}
			if !strings.Contains(strings.Join(v.Unmet, "|"), tc.unmet) {
				t.Fatalf("unmet %v lacks %q", v.Unmet, tc.unmet)
			}
		})
	}
}

func TestBlockedOutranksRetainedAndPartial(t *testing.T) {
	d := complete()
	d.Cleanup.Result = "failed"
	d.Custody.Disposition, d.Custody.Retained = "retained", []string{"ENV-002"}
	d.Observation.Result = "pending"
	if v := Evaluate(d, ref); v.State != StateBlocked {
		t.Fatalf("state=%s", v.State)
	}
}

func TestParseRejectsUnknownFieldsAndOversize(t *testing.T) {
	if _, err := Parse([]byte(`{"schema":"x","role":"Deployer"}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := Parse(make([]byte, MaxBytes+1)); err == nil {
		t.Fatal("oversize accepted")
	}
}

func TestRefs(t *testing.T) {
	if r, ok := RefFromTitle("[release:studio-prod-v0.7.52-9f8139a1] Studio production release"); !ok || r != ref {
		t.Fatalf("%q %v", r, ok)
	}
	if _, ok := RefFromTitle("fix bug [release:x]"); ok {
		t.Fatal("non-prefix accepted")
	}
	if r, ok := RefFromSource("release:" + ref); !ok || r != ref {
		t.Fatal("source")
	}
	if _, ok := RefFromSource("github:o/r#1"); ok {
		t.Fatal("issue source accepted as release")
	}
}

// The 2026-10-31 Studio Y4 release (TASK-064): the facts recorded in its
// checkpoint satisfy the contract, while the same release as the stale README
// describes it (execution not begun, nothing accepted) does not.
func TestHistoricalY4ReleaseIsDecidable(t *testing.T) {
	var d Document
	d.Schema, d.ReleaseRef = Schema, ref
	d.Candidate.ApplicationSHA = "9f8139a198d0613a81de6a2083a93271931d77db"
	d.Candidate.Version = "v0.7.52"
	d.Candidate.ManifestSHA256 = "ab6ad0ef8491caa295ba57ab07e10044cc684977960cb8a369d5792ec08ef865"
	d.Execution.Result, d.Execution.Ref = "succeeded", "workflow run 41201775004 (up-v0752-20261031-r2)"
	d.Acceptance = []Check{
		{Name: "simulation-run", Result: "passed", Ref: "stage2/run-41201775004/production-execution-acceptance.log"},
		{Name: "evaluation-run", Result: "passed", Ref: "stage2/run-41201775004/production-execution-acceptance.log"},
		{Name: "public-version", Result: "passed", Ref: "stage2/run-41201775004/candidate-route-proof.json"},
	}
	d.Observation = Check{Result: "passed", Ref: "stage2/observation-samples.jsonl"}
	d.Cleanup.Result, d.Cleanup.Ref = "complete", "release-checkpoint.json#closure"
	d.Custody.Disposition, d.Custody.Ref = "released", "release-checkpoint.json#closure.env_002"
	if v := Evaluate(d, ref); !v.Satisfied {
		t.Fatalf("recorded Y4 facts not decidable as complete: %+v", v)
	}
	stale := d
	stale.Execution.Result, stale.Acceptance, stale.Observation.Result = "pending", nil, "pending"
	if v := Evaluate(stale, ref); v.Satisfied || v.State != StatePartial {
		t.Fatalf("preparation-stage claims satisfied the contract: %+v", v)
	}
}

// Same-image promotion: production releases the digests staging verified. A
// rebuild of the same SHA (new digest) or a component whose staging rollout
// was rolled back or never deployed keeps the release from completing.
func TestComponentDigestsMustBeTheStagingVerifiedImages(t *testing.T) {
	api, worker := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	rebuilt := "sha256:" + strings.Repeat("c", 64)
	base := func() Document {
		d := complete()
		d.Candidate.Components = []Component{
			{Name: "go-runtime", Digest: api, StagingDigest: api, StagingRollout: "verified", Ref: "staging/run-1#runtime"},
			{Name: "execution-worker", Digest: worker, StagingDigest: worker, StagingRollout: "verified", Ref: "staging/run-1#worker"},
		}
		return d
	}
	if v := Evaluate(base(), complete().ReleaseRef); v.State != StateComplete {
		t.Fatalf("same-digest promotion: %+v", v)
	}
	for name, tc := range map[string]struct {
		mutate func(*Document)
		state  string
		want   string
	}{
		"same SHA, different build": {func(d *Document) { d.Candidate.Components[0].Digest = rebuilt }, StatePartial, "differs from the staging-verified digest"},
		"staging partial rollback":  {func(d *Document) { d.Candidate.Components[1].StagingRollout = "rolled-back" }, StatePartial, "staging rollout rolled-back"},
		"component never deployed":  {func(d *Document) { d.Candidate.Components[1].StagingRollout = "not-deployed" }, StatePartial, "not-deployed"},
		"malformed digest":          {func(d *Document) { d.Candidate.Components[0].StagingDigest = "latest" }, StateInvalid, "sha256:<64 hex>"},
		"duplicate component":       {func(d *Document) { d.Candidate.Components[1].Name = "go-runtime" }, StateInvalid, "unique simple name"},
		"unknown rollout":           {func(d *Document) { d.Candidate.Components[0].StagingRollout = "probably" }, StateInvalid, "staging_rollout must be"},
	} {
		t.Run(name, func(t *testing.T) {
			d := base()
			tc.mutate(&d)
			v := Evaluate(d, d.ReleaseRef)
			if v.State != tc.state || !strings.Contains(strings.Join(v.Unmet, "|"), tc.want) {
				t.Fatalf("%+v", v)
			}
		})
	}
	// Documents without components keep their previous verdicts.
	if v := Evaluate(complete(), complete().ReleaseRef); v.State != StateComplete {
		t.Fatalf("component-less evidence changed: %+v", v)
	}
}
