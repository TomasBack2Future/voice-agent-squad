package grokreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// This fixture preserves the field layout of the original admission artifact,
// with repository, content and operation data replaced by isolated test values.
func historicalContentFixture(t *testing.T, p *ProspectiveReadmission) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/historical-content-admission.json")
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	if err := json.Unmarshal(raw, &content); err != nil {
		t.Fatal(err)
	}
	p.ContentEvidencePath = filepath.Join(t.TempDir(), "original-admission.json")
	p.ContentEvidenceSHA256 = receiptHash(raw)
	if err := os.WriteFile(p.ContentEvidencePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return content
}

func TestProspectiveHistoricalContentAliases(t *testing.T) {
	a, p, verify := prospectiveFixtureMode(t, "shadow")
	historicalContentFixture(t, &p)
	before, _ := os.ReadFile(p.ContentEvidencePath)
	if err := a.importProspective(p, p.Owner, verify); err != nil {
		t.Fatal(err)
	}
	if err := a.verifyProspectiveBundle(admissionBundle(), p.Original.Attempt.Identity); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p.ContentEvidencePath)
	if string(before) != string(after) {
		t.Fatal("historical evidence rewritten")
	}
	if a.prospective.Original.Attempt.Identity.BundleSHA256 != "" {
		t.Fatal("unknown original input fabricated")
	}
}

func TestProspectiveHistoricalAliasNegatives(t *testing.T) {
	for _, change := range []string{"equal", "mixed", "diff-conflict", "body-conflict", "empty", "canonical-empty", "malformed", "short", "null", "number", "missing", "wrong-hash", "wrong-repository", "wrong-pr", "wrong-base", "wrong-head", "wrong-mode", "missing-tuple", "pending", "denied", "owner", "native", "raw-sha", "unsupported-field", "duplicate"} {
		t.Run(change, func(t *testing.T) {
			a, p, verify := prospectiveFixtureMode(t, "shadow")
			content := historicalContentFixture(t, &p)
			switch change {
			case "equal":
				content["diff_sha256"] = p.DiffSHA256
				content["pr_body_sha256"] = p.BodySHA256
			case "mixed":
				delete(content, "complete_diff_sha256")
				content["diff_sha256"] = p.DiffSHA256
			case "diff-conflict":
				content["diff_sha256"] = receiptHash([]byte("other"))
			case "body-conflict":
				content["pr_body_sha256"] = receiptHash([]byte("other"))
			case "empty":
				content["complete_diff_sha256"] = ""
			case "canonical-empty":
				content["diff_sha256"] = ""
			case "short":
				content["complete_diff_sha256"] = "ab"
			case "null":
				content["complete_diff_sha256"] = nil
			case "number":
				content["complete_diff_sha256"] = 12
			case "missing":
				delete(content, "complete_diff_sha256")
			case "malformed":
				content["body_sha256"] = strings.Repeat("z", 64)
				p.BodySHA256 = strings.Repeat("z", 64)
			case "wrong-hash":
				content["body_sha256"] = receiptHash([]byte("other"))
			case "wrong-repository":
				content["repository"] = "foreign/repo"
			case "wrong-pr":
				content["pr"] = 10
			case "wrong-base":
				content["base_sha"] = "foreign"
			case "wrong-head":
				content["head_sha"] = "foreign"
			case "wrong-mode":
				content["mode"] = "required"
			case "missing-tuple":
				delete(content, "repository")
			case "pending", "denied":
				p.Disclosure.Disposition = change
			case "owner":
				p.Owner.Actor = "foreign"
			case "native":
				p.Original.NativeJoin.NativeSession = "foreign"
			case "unsupported-field":
				content["unrecognized_artifact"] = true
			}
			raw, _ := json.Marshal(content)
			if change == "duplicate" {
				raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"body_sha256":"` + p.BodySHA256 + `"}`)
			}
			if err := os.WriteFile(p.ContentEvidencePath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			p.ContentEvidenceSHA256 = receiptHash(raw)
			if change == "raw-sha" {
				p.ContentEvidenceSHA256 = receiptHash([]byte("changed"))
			}
			err := a.importProspective(p, ReviewOwner{Actor: "original-worker", Native: "owning-native"}, verify)
			if change == "equal" || change == "mixed" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("unqualified historical alias admitted")
			}
		})
	}
}

func prospectiveFixture(t *testing.T) (*Admission, ProspectiveReadmission, func(NativeJoinProof, AttemptReceipt) error) {
	return prospectiveFixtureMode(t, "required")
}

func prospectiveFixtureMode(t *testing.T, mode string) (*Admission, ProspectiveReadmission, func(NativeJoinProof, AttemptReceipt) error) {
	t.Helper()
	root, proof, original := nativeProofFixtureMode(t, mode)
	original.Identity.BundleSHA256 = ""
	status := ReviewStatus{SchemaVersion: ReviewStatusSchemaVersion, Attempt: original.ID, Repository: original.Identity.Repository, PullRequest: original.Identity.PR, BaseRef: original.Identity.BaseRef, BaseSHA: original.Identity.BaseSHA, HeadSHA: original.Identity.HeadSHA, Mode: original.Settings.Mode, State: ReviewStateError, Verdict: VerdictError, FailureStage: "sampling", FailureKind: CLIFailureTimeout, StartedAt: 1, CompletedAt: 1}
	write := func(name string, value any) (string, string) {
		raw, _ := json.Marshal(value)
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return path, receiptHash(raw)
	}
	statusPath, statusHash := write("status.json", status)
	var bundle FrozenReviewBundle
	_ = json.Unmarshal(admissionBundle(), &bundle)
	diffHash, bodyHash := receiptHash([]byte(bundle.Diff)), receiptHash([]byte(bundle.Description))
	evidencePath, evidenceHash := write("original-content.json", map[string]string{"diff_sha256": diffHash, "pr_body_sha256": bodyHash})
	owner := ReviewOwner{Actor: "original-worker", Native: proof.NativeSession}
	p := ProspectiveReadmission{SchemaVersion: "squad.review-readmission.prospective.v1", Original: LegacyRecoveryReceipt{SchemaVersion: "squad.review-recovery.legacy.v1", Attempt: original, StatusPath: statusPath, StatusSHA256: statusHash, NativeJoin: &proof}, DiffSHA256: diffHash, BodySHA256: bodyHash, ContentEvidencePath: evidencePath, ContentEvidenceSHA256: evidenceHash, Owner: owner, Disclosure: ReviewDisclosure{SchemaVersion: ReviewDisclosureSchema, Reference: "human:explicit-prospective-source-scope", Disposition: "granted", Operation: "prospective_legacy_readmission", Provider: "grok", Content: "source_diff_and_review_contract", Owner: owner, Identity: original.Identity, Mode: original.Settings.Mode}}
	return openTestAdmission(t, t.TempDir(), "", original.Settings), p, func(proof NativeJoinProof, attempt AttemptReceipt) error {
		return verifyNativeJoinAt(root, proof, attempt)
	}
}

func TestShadowLegacyAndProspectiveNativeJoinPreserveMode(t *testing.T) {
	a, p, verify := prospectiveFixtureMode(t, "shadow")
	if err := a.importProspective(p, p.Owner, verify); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if a.receipt.Settings.Mode != "shadow" {
		t.Fatal("prospective mode changed")
	}
	if err := a.Finish(context.Background(), timeoutReport()); err != nil {
		t.Fatal(err)
	}
	// The original-input lane also supports a genuinely retained shadow input.
	b, proofReceipt, verifyLegacy := prospectiveFixtureMode(t, "shadow")
	identity, _ := identityFor(admissionBundle())
	proofReceipt.Original.Attempt.Identity = identity
	input, _ := json.Marshal(LegacyInputReceipt{SchemaVersion: "squad.review-input.v1", Identity: identity, Settings: proofReceipt.Original.Attempt.Settings, RecordedAt: 1})
	path := filepath.Join(t.TempDir(), "real-input.json")
	if err := os.WriteFile(path, input, 0600); err != nil {
		t.Fatal(err)
	}
	proofReceipt.Original.InputReceiptPath = path
	proofReceipt.Original.InputReceiptSHA256 = receiptHash(input)
	if err := b.verifyLegacyReceipt(proofReceipt.Original, true, verifyLegacy); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
}

func TestProspectiveSingleFlightConcurrentAndHistoricalSlotUpgrade(t *testing.T) {
	a, p, verify := prospectiveFixture(t)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		candidate := openTestAdmission(t, a.dir, "", admissionSettings())
		if err := candidate.importProspective(p, p.Owner, verify); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if candidate.Start(context.Background(), admissionBundle()) == nil {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("admitted %d samplers", admitted.Load())
	}
	// Model an old pre-root admission store without deleting its one-use history.
	if _, err := a.db.Exec("DELETE FROM review_recovery_roots"); err != nil {
		t.Fatal(err)
	}
	upgraded := openTestAdmission(t, a.dir, "", admissionSettings())
	var count int
	if err := upgraded.db.QueryRow("SELECT count(*) FROM review_recovery_roots").Scan(&count); err != nil || count != 1 {
		t.Fatalf("lost historical consumed slot: %d %v", count, err)
	}
}

func TestProspectiveReadmissionIsNewInputAndOneUse(t *testing.T) {
	a, p, verify := prospectiveFixture(t)
	if err := a.importProspective(p, p.Owner, verify); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if a.receipt.Parent != p.Original.Attempt.ID {
		t.Fatal("lost original attempt")
	}
	if a.receipt.InputProvenance != "prospective-legacy-new-input" || a.receipt.InputRecordedAt <= 0 || a.receipt.Identity.BundleSHA256 == "" || a.legacy.Identity.BundleSHA256 != "" || a.receipt.AuthorizationSHA256 == "" {
		t.Fatal("new input equated with unknown legacy input")
	}
	if err := a.Finish(context.Background(), timeoutReport()); err != nil {
		t.Fatal(err)
	}
	b := openTestAdmission(t, a.dir, "", p.Original.Attempt.Settings)
	if err := b.importProspective(p, p.Owner, verify); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("replayed prospective sample")
	}
}

func TestProspectiveRejectsCustodyDisclosureAndLegacyFabrication(t *testing.T) {
	for _, change := range []string{"owner", "native", "pending", "denied", "foreign-pr", "foreign-head", "foreign-mode", "old-operation", "old-input", "hash", "content", "publication", "verdict", "unjoined"} {
		t.Run(change, func(t *testing.T) {
			a, p, verify := prospectiveFixture(t)
			switch change {
			case "owner":
				p.Owner.Actor = "foreign"
			case "native":
				p.Owner.Native = "foreign"
			case "pending", "denied":
				p.Disclosure.Disposition = change
			case "foreign-pr":
				p.Disclosure.Identity.PR++
			case "foreign-head":
				p.Disclosure.Identity.HeadSHA = "foreign"
			case "foreign-mode":
				p.Disclosure.Mode = "shadow"
			case "old-operation":
				p.Disclosure.Operation = "managed_review"
			case "old-input":
				p.Original.Attempt.Identity.BundleSHA256 = "invented"
			case "hash":
				p.ContentEvidenceSHA256 = "changed"
			case "content":
				p.BodySHA256 = receiptHash([]byte("changed"))
			case "publication":
				p.Original.Attempt.FailureStage = "publishing"
			case "verdict":
				p.Original.Attempt.Verdict = VerdictBlocking
			case "unjoined":
				p.Original.Attempt.Joined = false
			}
			if err := a.importProspective(p, ReviewOwner{Actor: "original-worker", Native: "owning-native"}, verify); err == nil {
				t.Fatal("unqualified prospective admitted")
			}
		})
	}
}

func TestProspectiveStaleBundleSettingsAndCrossLaneReplay(t *testing.T) {
	for _, change := range []string{"head", "base", "body", "diff", "effort", "mode"} {
		t.Run(change, func(t *testing.T) {
			a, p, verify := prospectiveFixture(t)
			if err := a.importProspective(p, p.Owner, verify); err != nil {
				t.Fatal(err)
			}
			var b FrozenReviewBundle
			_ = json.Unmarshal(admissionBundle(), &b)
			switch change {
			case "head":
				b.HeadSHA = "changed"
			case "base":
				b.BaseSHA = "changed"
			case "body":
				b.Description = "changed"
			case "diff":
				b.Diff = "changed"
			case "effort":
				a.settings.Effort = "high"
			case "mode":
				a.settings.Mode = "shadow"
			}
			raw, _ := json.Marshal(b)
			if err := a.Start(context.Background(), raw); err == nil {
				t.Fatal("changed prospective input admitted")
			}
		})
	}
	a, p, verify := prospectiveFixture(t)
	if err := a.importProspective(p, p.Owner, verify); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	// An interrupted slot remains occupied, even when another receipt is offered.
	b := openTestAdmission(t, a.dir, "", admissionSettings())
	if err := b.importProspective(p, p.Owner, verify); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("unjoined attempt bypassed")
	}
	if err := a.Finish(context.Background(), timeoutReport()); err != nil {
		t.Fatal(err)
	}
	var bundle FrozenReviewBundle
	_ = json.Unmarshal(admissionBundle(), &bundle)
	bundle.PolicyHash = "new-policy"
	changed, _ := json.Marshal(bundle)
	for _, settings := range []ReviewSettings{admissionSettings(), func() ReviewSettings { s := admissionSettings(); s.Mode = "shadow"; return s }()} {
		if err := openTestAdmission(t, a.dir, "", settings).Start(context.Background(), changed); err == nil {
			t.Fatal("normal/context/mode bypass after consumed root")
		}
	}
	if err := openTestAdmission(t, a.dir, p.Original.Attempt.ID, admissionSettings()).Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("legacy replay used missing old complete input")
	}
	// Another original attempt on this same tuple cannot obtain a second slot.
	p.Original.Attempt.ID = "another-original"
	c := openTestAdmission(t, a.dir, "", admissionSettings())
	c.from = p.Original.Attempt.ID
	c.legacy = &p.Original.Attempt
	c.prospective = &p
	if err := c.Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("another parent bypassed same tuple root")
	}
}

func TestProspectiveRejectsStoredOtherModeValidTuple(t *testing.T) {
	a, p, verify := prospectiveFixture(t)
	other := openTestAdmission(t, a.dir, "", func() ReviewSettings { s := admissionSettings(); s.Mode = "shadow"; return s }())
	if err := other.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if err := other.Finish(context.Background(), ReviewReport{Result: approvedFindings(), Publication: Publication{CheckRunID: 2, Conclusion: "success"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.importProspective(p, p.Owner, verify); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("existing valid current tuple bypassed")
	}
}

func TestProspectiveServiceFreezesBeforeSamplerAndKeepsShadowGate(t *testing.T) {
	for _, mode := range []string{"required", "shadow"} {
		for _, eligible := range []bool{true, false} {
			t.Run(mode+fmt.Sprint(eligible), func(t *testing.T) {
				a, p, verify := prospectiveFixtureMode(t, mode)
				if err := a.importProspective(p, p.Owner, verify); err != nil {
					t.Fatal(err)
				}
				if !eligible {
					a.check = func(context.Context, AttemptReceipt) error { return errors.New("current gate unresolved") }
				}
				var frozen FrozenReviewBundle
				_ = json.Unmarshal(admissionBundle(), &frozen)
				snapshot := PullRequestSnapshot{Repository: frozen.Repository, Number: frozen.PullRequest, BaseRef: frozen.BaseRef, BaseSHA: frozen.BaseSHA, HeadSHA: frozen.HeadSHA, Title: frozen.Title, Description: frozen.Description, Diff: frozen.Diff}
				model := &fakeModelReviewer{err: errors.New("joined timeout"), audit: CLIAudit{FailureKind: CLIFailureTimeout}, afterReview: func() {
					recorded, err := a.load(context.Background(), a.AttemptID())
					if err != nil || recorded.InputProvenance != "prospective-legacy-new-input" || recorded.Joined || recorded.InputRecordedAt <= 0 {
						t.Fatalf("sampler ran before durable input/custody: %#v %v", recorded, err)
					}
				}}
				github := &fakePullRequestGateway{snapshot: snapshot, current: snapshot, publication: Publication{CheckRunID: 2, Conclusion: "failure"}}
				service, err := NewLocalReviewService(model, github, frozen.CoreHash, frozen.PolicyHash)
				if err != nil {
					t.Fatal(err)
				}
				service.SetAdmission(a)
				name := "grok-review"
				if mode == "shadow" {
					name = "grok-review-shadow"
				}
				_, err = service.ReviewPullRequest(context.Background(), "fake-token", name, snapshot.Repository, snapshot.Number)
				if err == nil {
					t.Fatal("timeout or gate failure lost")
				}
				if !eligible {
					if model.calls != 0 || github.published != 0 {
						t.Fatal("rejected gate sampled/published")
					}
					return
				}
				if model.calls != 1 || github.publishedName != name {
					t.Fatal("sample count or original mode changed")
				}
				child, err := a.load(context.Background(), a.AttemptID())
				if err != nil || !child.Joined || child.Parent != p.Original.Attempt.ID || child.UsageKnown || child.CostKnown {
					t.Fatalf("terminal history: %#v %v", child, err)
				}
				retry := openTestAdmission(t, a.dir, "", p.Original.Attempt.Settings)
				if err = retry.importProspective(p, p.Owner, verify); err != nil {
					t.Fatal(err)
				}
				service.SetAdmission(retry)
				_, _ = service.ReviewPullRequest(context.Background(), "fake-token", name, snapshot.Repository, snapshot.Number)
				if model.calls != 1 || github.published != 1 {
					t.Fatal("new terminal failure resampled")
				}
			})
		}
	}
}
