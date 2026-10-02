package grokreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const rendererPatch = "diff --git a/x.go b/x.go\nindex 1111111..2222222 100644\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,2 @@ func old()\n package x\n-var x = 1\n+var x = 2\n"

func TestProspectiveRendererEquivalence(t *testing.T) {
	a, p, verify := prospectiveFixture(t)
	p.DiffSHA256 = receiptHash([]byte(rendererPatch))
	raw, _ := json.Marshal(map[string]string{"diff_sha256": p.DiffSHA256, "pr_body_sha256": p.BodySHA256})
	if err := os.WriteFile(p.ContentEvidencePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	p.ContentEvidenceSHA256 = receiptHash(raw)
	current := strings.Replace(rendererPatch, "func old()", "func current()", 1)
	p.RendererEquivalence = rendererEvidenceFixture(t, p, rendererPatch, current)
	if err := a.importProspective(p, p.Owner, verify); err != nil {
		t.Fatal(err)
	}
	var bundle FrozenReviewBundle
	_ = json.Unmarshal(admissionBundle(), &bundle)
	bundle.Diff = "diff --git a/x.go b/x.go\nindex 1111111..2222222 100644\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,2 @@ func current()\n package x\n-var x = 1\n+var x = 2\n"
	frozen, _ := json.Marshal(bundle)
	if err := a.verifyProspectiveBundle(frozen, p.Original.Attempt.Identity); err != nil {
		t.Fatal(err)
	}
}

func rendererEvidenceFixture(t *testing.T, p ProspectiveReadmission, old, current string) *PatchRendererEvidence {
	t.Helper()
	dir := t.TempDir()
	before, after := filepath.Join(dir, "retained.patch"), filepath.Join(dir, "gateway.patch")
	for path, raw := range map[string]string{before: old, after: current} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return &PatchRendererEvidence{SchemaVersion: rendererSchema, Identity: p.Original.Attempt.Identity, OriginalPatchPath: before, OriginalPatchSHA256: receiptHash([]byte(old)), CurrentPatchPath: after, CurrentPatchSHA256: receiptHash([]byte(current))}
}

func rendererProspectiveFixture(t *testing.T) (*Admission, ProspectiveReadmission, func(NativeJoinProof, AttemptReceipt) error, []byte) {
	t.Helper()
	a, p, verify := prospectiveFixture(t)
	p.DiffSHA256 = receiptHash([]byte(rendererPatch))
	raw, _ := json.Marshal(map[string]string{"diff_sha256": p.DiffSHA256, "pr_body_sha256": p.BodySHA256})
	if err := os.WriteFile(p.ContentEvidencePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	p.ContentEvidenceSHA256 = receiptHash(raw)
	current := strings.Replace(rendererPatch, "func old()", "func current()", 1)
	p.RendererEquivalence = rendererEvidenceFixture(t, p, rendererPatch, current)
	var b FrozenReviewBundle
	_ = json.Unmarshal(admissionBundle(), &b)
	b.Diff = current
	frozen, _ := json.Marshal(b)
	return a, p, verify, frozen
}

func TestRendererNegativeStreams(t *testing.T) {
	changes := map[string]string{
		"source":           strings.Replace(rendererPatch, "+var x = 2", "+var x = 3", 1),
		"whitespace":       strings.Replace(rendererPatch, "+var x = 2", "+var  x = 2", 1),
		"context":          strings.Replace(rendererPatch, " package x", " package y", 1),
		"range":            strings.Replace(rendererPatch, "-1,2", "-2,2", 1),
		"count":            strings.Replace(rendererPatch, "+1,2", "+1,3", 1),
		"header":           strings.Replace(rendererPatch, "a/x.go b/x.go", "a/y.go b/y.go", 1),
		"mode":             strings.Replace(rendererPatch, "100644", "100755", 1),
		"index":            strings.Replace(rendererPatch, "1111111", "1111112", 1),
		"truncated":        rendererPatch[:len(rendererPatch)-1],
		"missing-source":   strings.TrimSuffix(rendererPatch, "+var x = 2\n"),
		"extra-source":     rendererPatch + "+extra\n",
		"trailing-junk":    rendererPatch + "junk\n",
		"malformed-hunk":   strings.Replace(rendererPatch, "@@ -1,2 +1,2 @@", "@@@ -1,2 +1,2 @@@", 1),
		"overflow":         strings.Replace(rendererPatch, "-1,2", "-9999999999999999999999,2", 1),
		"orphan-marker":    strings.Replace(rendererPatch, "@@ -1,2 +1,2 @@ func old()\n", "@@ -1,2 +1,2 @@ func old()\n\\ No newline at end of file\n", 1),
		"newline-marker":   rendererPatch + "\\ No newline at end of file\n",
		"nul":              rendererPatch + "\x00\n",
		"zero-range":       strings.Replace(rendererPatch, "-1,2", "-0,2", 1),
		"metadata-in-hunk": strings.Replace(rendererPatch, " package x", "new mode 100755", 1),
	}
	for name, changed := range changes {
		t.Run(name, func(t *testing.T) {
			if err := verifyPatchEquivalence([]byte(rendererPatch), []byte(changed)); err == nil {
				t.Fatal("changed/invalid complete patch accepted")
			}
		})
	}
	for name, invalid := range map[string]string{
		"partial-mode":   "diff --git a/x b/x\nnew mode 100755\n",
		"duplicate-mode": "diff --git a/x b/x\nold mode 100644\nnew mode 100755\nold mode 100644\n",
		"partial-rename": "diff --git a/x b/y\nrename from x\n",
		"duplicate-file": rendererPatch + rendererPatch,
		"overlap":        rendererPatch + "@@ -1,2 +1,2 @@ more\n package x\n-var x = 1\n+var x = 2\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := verifyPatchEquivalence([]byte(invalid), []byte(invalid)); err == nil {
				t.Fatal("ambiguous patch accepted")
			}
		})
	}
	// Two equally malformed streams must not qualify merely because they agree.
	for _, invalid := range []string{changes["truncated"], changes["count"], changes["extra-source"], changes["malformed-hunk"]} {
		if err := verifyPatchEquivalence([]byte(invalid), []byte(invalid)); err == nil {
			t.Fatal("matching malformed streams accepted")
		}
	}
}

func TestRendererProspectiveCustodyNegatives(t *testing.T) {
	for _, change := range []string{"missing-proof", "missing-original", "missing-current", "original-sha", "current-sha", "proof-tuple", "proof-input", "body", "tuple", "native", "grant", "source", "changed-after-import", "frozen-stream", "symlink", "oversized"} {
		t.Run(change, func(t *testing.T) {
			a, p, verify, bundle := rendererProspectiveFixture(t)
			switch change {
			case "missing-proof":
				p.RendererEquivalence = nil
			case "missing-original":
				_ = os.Remove(p.RendererEquivalence.OriginalPatchPath)
			case "missing-current":
				_ = os.Remove(p.RendererEquivalence.CurrentPatchPath)
			case "original-sha":
				p.RendererEquivalence.OriginalPatchSHA256 = receiptHash([]byte("wrong"))
			case "current-sha":
				p.RendererEquivalence.CurrentPatchSHA256 = receiptHash([]byte("wrong"))
			case "proof-tuple":
				p.RendererEquivalence.Identity.HeadSHA = "foreign"
			case "proof-input":
				p.RendererEquivalence.Identity.BundleSHA256 = "invented"
			case "body":
				p.BodySHA256 = receiptHash([]byte("changed"))
			case "tuple":
				p.Original.Attempt.Identity.HeadSHA = "foreign"
			case "native":
				p.Original.NativeJoin.NativeSession = "foreign"
			case "grant":
				p.Disclosure.Disposition = "pending"
			case "source":
				p.RendererEquivalence = rendererEvidenceFixture(t, p, rendererPatch, strings.Replace(rendererPatch, "+var x = 2", "+var x = 3", 1))
			case "symlink":
				path := p.RendererEquivalence.OriginalPatchPath
				_ = os.Rename(path, path+".real")
				if err := os.Symlink(path+".real", path); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				f, err := os.OpenFile(p.RendererEquivalence.OriginalPatchPath, os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				err = f.Truncate(maxPatchBytes + 1)
				_ = f.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			err := a.importProspective(p, p.Owner, verify)
			if err == nil {
				if change == "changed-after-import" {
					if err := os.WriteFile(p.RendererEquivalence.CurrentPatchPath, []byte("changed"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if change == "frozen-stream" {
					var b FrozenReviewBundle
					_ = json.Unmarshal(bundle, &b)
					b.Diff = rendererPatch
					bundle, _ = json.Marshal(b)
				}
				err = a.Start(context.Background(), bundle)
			}
			if err == nil {
				t.Fatal("custody mismatch admitted")
			}
			var n int
			if err := a.db.QueryRow("SELECT count(*) FROM review_recovery_roots").Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatal("failed qualification consumed root")
			}
		})
	}
}

func TestRendererProvenanceSingleFlightAndReplay(t *testing.T) {
	a, p, verify, bundle := rendererProspectiveFixture(t)
	if err := a.importProspective(p, p.Owner, verify); err != nil {
		t.Fatal(err)
	}
	b := openTestAdmission(t, a.dir, "", p.Original.Attempt.Settings)
	if err := b.importProspective(p, p.Owner, verify); err != nil {
		t.Fatal(err)
	}
	winners := make(chan *Admission, 2)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for _, candidate := range []*Admission{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if candidate.Start(context.Background(), bundle) == nil {
				wins.Add(1)
				winners <- candidate
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("single flight winners %d", wins.Load())
	}
	winner := <-winners
	r, err := winner.load(context.Background(), winner.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	if r.RendererProvenance == nil || r.RendererProvenance.OriginalSHA256 != p.DiffSHA256 || r.RendererProvenance.CurrentSHA256 != p.RendererEquivalence.CurrentPatchSHA256 || r.RendererProvenance.EvidenceSHA256 == "" {
		t.Fatal("raw hashes/provenance missing")
	}
	if err := winner.Finish(context.Background(), timeoutReport()); err != nil {
		t.Fatal(err)
	}
	replay := openTestAdmission(t, a.dir, "", p.Original.Attempt.Settings)
	if err := replay.importProspective(p, p.Owner, verify); err != nil {
		t.Fatal(err)
	}
	if err := replay.Start(context.Background(), bundle); err == nil {
		t.Fatal("renderer proof refunded consumed one-use root")
	}
}

func TestRendererMetadataAndNewlinePreservation(t *testing.T) {
	binary := "diff --git a/x b/x\nindex b8a990648f560f273ddc610ff0865ed544192332..ce0a67d13da5723bcc7457b3c3afbcfbe5cbc52a 100644\nGIT binary patch\nliteral 5\nMcmZQbNli-!00Z{{mjD0&\n\nliteral 4\nLcmZQbOiBg-0!{%Z\n\n\n"
	patches := []string{binary, "diff --git a/x b/x\nnew file mode 100644\nindex 0000000..2222222\n--- /dev/null\n+++ b/x\n@@ -0,0 +1 @@\n+created\n", "diff --git a/x b/x\nold mode 100644\nnew mode 100755\n", "diff --git a/x b/y\nsimilarity index 100%\nrename from x\nrename to y\n", rendererPatch + "\\ No newline at end of file\n"}
	for _, patch := range patches {
		if err := verifyPatchEquivalence([]byte(patch), []byte(patch)); err != nil {
			t.Fatal(err)
		}
	}
	for _, changed := range []string{strings.Replace(binary, "literal 5", "literal 6", 1), strings.Replace(binary, "McmZQb", "McmZQc", 1), strings.TrimSuffix(binary, "literal 4\nLcmZQbOiBg-0!{%Z\n\n\n")} {
		if err := verifyPatchEquivalence([]byte(binary), []byte(changed)); err == nil {
			t.Fatal("changed or truncated binary accepted")
		}
	}
	for _, invalid := range []string{strings.Replace(binary, "literal 5", "literal 6", 1), strings.Replace(binary, "McmZQb", "McmZQc", 1)} {
		if err := verifyPatchEquivalence([]byte(invalid), []byte(invalid)); err == nil {
			t.Fatal("matching malformed binary accepted")
		}
	}
	first := rendererPatch
	second := strings.ReplaceAll(rendererPatch, "x.go", "y.go")
	if err := verifyPatchEquivalence([]byte(first+second), []byte(second+first)); err == nil {
		t.Fatal("file order changed")
	}
}
