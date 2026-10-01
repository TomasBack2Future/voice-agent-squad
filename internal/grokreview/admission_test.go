package grokreview

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func admissionSettings() ReviewSettings {
	return ReviewSettings{Mode: "required", Model: "grok-4.7", Effort: "medium", TimeoutMS: 1200000, MaxGitHubOutput: 8 << 20, MaxReviewerOutput: 1 << 20, AppID: 1, InstallationID: 2}
}
func admissionBundle() []byte {
	raw, _ := json.Marshal(FrozenReviewBundle{SchemaVersion: FrozenReviewSchemaVersion, Repository: "owner/repo", PullRequest: 9, BaseRef: "main", BaseSHA: "base", HeadSHA: "head", Title: "repair", Description: "contract", Diff: "+fixed", CoreHash: "core", PolicyHash: "policy"})
	return raw
}
func openTestAdmission(t *testing.T, dir, from string, settings ReviewSettings) *Admission {
	t.Helper()
	a, err := OpenAdmission(dir, settings, from, func(context.Context, AttemptReceipt) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}
func timeoutReport() ReviewReport {
	return ReviewReport{Result: FindingsResult{Verdict: VerdictError}, Audit: CLIAudit{FailureKind: CLIFailureTimeout}, FailureStage: "sampling", Publication: Publication{CheckRunID: 1, Conclusion: "failure"}}
}
func seedTimeout(t *testing.T, dir string) string {
	t.Helper()
	a := openTestAdmission(t, dir, "", admissionSettings())
	if err := a.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if err := a.Finish(context.Background(), timeoutReport()); err != nil {
		t.Fatal(err)
	}
	return a.AttemptID()
}
func TestAdmissionRecoveryOneUseAndUnknownUsage(t *testing.T) {
	dir := t.TempDir()
	id := seedTimeout(t, dir)
	a := openTestAdmission(t, dir, id, admissionSettings())
	if err := a.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if err := a.Finish(context.Background(), timeoutReport()); err != nil {
		t.Fatal(err)
	}
	r, err := a.load(context.Background(), a.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	if r.Parent != id || r.UsageKnown {
		t.Fatalf("lost parent or invented usage: %#v", r)
	}
	if err := openTestAdmission(t, dir, id, admissionSettings()).Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("replayed recovery sampled")
	}
	if err := openTestAdmission(t, dir, a.AttemptID(), admissionSettings()).Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("second timeout chained recovery")
	}
	if err := openTestAdmission(t, dir, "", admissionSettings()).Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("normal command bypassed one use")
	}
}
func TestAdmissionRejectsChangedInputAndSettings(t *testing.T) {
	dir := t.TempDir()
	id := seedTimeout(t, dir)
	for _, field := range []string{"BaseRef", "BaseSHA", "HeadSHA", "Title", "Description", "Diff", "CoreHash", "PolicyHash"} {
		t.Run(field, func(t *testing.T) {
			var b FrozenReviewBundle
			_ = json.Unmarshal(admissionBundle(), &b)
			switch field {
			case "BaseRef":
				b.BaseRef = "changed"
			case "BaseSHA":
				b.BaseSHA = "changed"
			case "HeadSHA":
				b.HeadSHA = "changed"
			case "Title":
				b.Title = "changed"
			case "Description":
				b.Description = "changed"
			case "Diff":
				b.Diff = "changed"
			case "CoreHash":
				b.CoreHash = "changed"
			case "PolicyHash":
				b.PolicyHash = "changed"
			}
			raw, _ := json.Marshal(b)
			if err := openTestAdmission(t, dir, id, admissionSettings()).Start(context.Background(), raw); err == nil {
				t.Fatal("changed frozen input accepted")
			}
		})
	}
	settings := admissionSettings()
	settings.Effort = "high"
	if err := openTestAdmission(t, dir, id, settings).Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("changed effort accepted")
	}
	settings = admissionSettings()
	settings.TimeoutMS++
	if err := openTestAdmission(t, dir, id, settings).Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("changed timeout accepted")
	}
	settings = admissionSettings()
	settings.Model = "other"
	if err := openTestAdmission(t, dir, id, settings).Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("changed model accepted")
	}
}
func TestAdmissionRejectsValidVerdictPublicationFailureAndUnjoined(t *testing.T) {
	for _, state := range []string{"approved", "blocking", "publication", "unjoined", "canceled"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			a := openTestAdmission(t, dir, "", admissionSettings())
			if err := a.Start(context.Background(), admissionBundle()); err != nil {
				t.Fatal(err)
			}
			report := timeoutReport()
			switch state {
			case "approved":
				report.Result.Verdict = VerdictApproved
			case "blocking":
				report.Result.Verdict = VerdictBlocking
			case "publication":
				report.FailureStage = "publishing"
			case "canceled":
				report.Audit.FailureKind = CLIFailureCanceled
			}
			if state != "unjoined" {
				if err := a.Finish(context.Background(), report); err != nil {
					t.Fatal(err)
				}
			}
			if err := openTestAdmission(t, dir, a.AttemptID(), admissionSettings()).Start(context.Background(), admissionBundle()); err == nil {
				t.Fatal("ineligible attempt recovered")
			}
		})
	}
}
func TestAdmissionSingleFlightRaceAndInterruptedRecovery(t *testing.T) {
	dir := t.TempDir()
	id := seedTimeout(t, dir)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		a := openTestAdmission(t, dir, id, admissionSettings())
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.Start(context.Background(), admissionBundle()) == nil {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("samplers=%d", winners.Load())
	}
	// Simulated process exit after reservation: closing all connections does not
	// release the flight or refund the slot, including against a changed head.
	a := openTestAdmission(t, dir, id, admissionSettings())
	if err := a.Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("interrupted recovery replayed")
	}
	var b FrozenReviewBundle
	_ = json.Unmarshal(admissionBundle(), &b)
	b.HeadSHA = "newhead"
	raw, _ := json.Marshal(b)
	if err := openTestAdmission(t, dir, "", admissionSettings()).Start(context.Background(), raw); err == nil {
		t.Fatal("new head overlapped unjoined invocation")
	}
}
func TestAdmissionRemoteGateRejectsBeforeSampler(t *testing.T) {
	dir := t.TempDir()
	id := seedTimeout(t, dir)
	a := openTestAdmission(t, dir, id, admissionSettings())
	a.check = func(context.Context, AttemptReceipt) error { return errors.New("current Check has valid verdict") }
	model := &fakeModelReviewer{result: approvedFindings()}
	var b FrozenReviewBundle
	_ = json.Unmarshal(admissionBundle(), &b)
	snap := PullRequestSnapshot{Repository: b.Repository, Number: b.PullRequest, BaseRef: b.BaseRef, BaseSHA: b.BaseSHA, HeadSHA: b.HeadSHA, Title: b.Title, Description: b.Description, Diff: b.Diff}
	gateway := &fakePullRequestGateway{snapshot: snap, current: snap}
	svc, err := NewLocalReviewService(model, gateway, b.CoreHash, b.PolicyHash)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAdmission(a)
	if _, err = svc.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9); err == nil {
		t.Fatal("rejected gate proceeded")
	}
	if model.calls != 0 || gateway.published != 0 {
		t.Fatal("gate rejection sampled or published")
	}
}
func TestAdmissionServiceJoinsTimeoutAndPublishesRealGate(t *testing.T) {
	dir := t.TempDir()
	a := openTestAdmission(t, dir, "", admissionSettings())
	model := &fakeModelReviewer{audit: CLIAudit{FailureKind: CLIFailureTimeout}, err: context.DeadlineExceeded}
	var b FrozenReviewBundle
	_ = json.Unmarshal(admissionBundle(), &b)
	snap := PullRequestSnapshot{Repository: b.Repository, Number: b.PullRequest, BaseRef: b.BaseRef, BaseSHA: b.BaseSHA, HeadSHA: b.HeadSHA, Title: b.Title, Description: b.Description, Diff: b.Diff}
	gateway := &fakePullRequestGateway{snapshot: snap, current: snap, publication: Publication{CheckRunID: 1, Conclusion: "failure"}}
	svc, err := NewLocalReviewService(model, gateway, b.CoreHash, b.PolicyHash)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAdmission(a)
	if _, err = svc.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9); err == nil {
		t.Fatal("timeout approved")
	}
	r, err := a.load(context.Background(), a.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	if !recoverable(r) || model.calls != 1 || gateway.published != 1 {
		t.Fatalf("missing joined timeout: %#v", r)
	}
	recovery := openTestAdmission(t, dir, r.ID, admissionSettings())
	model.err = nil
	model.result = approvedFindings()
	gateway.publication = Publication{CheckRunID: 2, Conclusion: "success"}
	svc.SetAdmission(recovery)
	if _, err = svc.ReviewPullRequest(context.Background(), "token", "grok-review", "owner/repo", 9); err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 || gateway.published != 2 || gateway.publishedName != "grok-review" {
		t.Fatal("recovery bypassed managed review or publication")
	}
}
func TestLegacyImportRequiresJoinedAbsentProcessesAndExactStatus(t *testing.T) {
	dir := t.TempDir()
	statusDir := t.TempDir()
	a := openTestAdmission(t, dir, "", admissionSettings())
	identity, err := identityFor(admissionBundle())
	if err != nil {
		t.Fatal(err)
	}
	r := AttemptReceipt{ID: "0123456789abcdef", Identity: identity, Settings: admissionSettings(), Joined: true, Verdict: VerdictError, FailureStage: "sampling", FailureKind: CLIFailureTimeout, CompletedAt: 1, Publication: Publication{CheckRunID: 1, Conclusion: "failure"}}
	status := ReviewStatus{SchemaVersion: ReviewStatusSchemaVersion, Attempt: r.ID, Repository: identity.Repository, PullRequest: identity.PR, BaseRef: identity.BaseRef, BaseSHA: identity.BaseSHA, HeadSHA: identity.HeadSHA, Mode: "required", State: ReviewStateError, Verdict: VerdictError, FailureStage: "sampling", FailureKind: CLIFailureTimeout, CompletedAt: 1, StartedAt: 1}
	statusRaw, _ := json.Marshal(status)
	statusPath := filepath.Join(statusDir, "status.json")
	if err = os.WriteFile(statusPath, statusRaw, 0600); err != nil {
		t.Fatal(err)
	}
	// Definitely absent PIDs are obtained by joining short-lived test processes
	// elsewhere; live current PID must always fail despite asserted joined=true.
	join := LegacyJoinReceipt{SchemaVersion: "squad.review-join.v1", Attempt: r.ID, WrapperPID: os.Getpid(), ReviewerPID: os.Getpid(), Joined: true, ExitCode: 1}
	joinRaw, _ := json.Marshal(join)
	joinPath := filepath.Join(t.TempDir(), "join.json")
	_ = os.WriteFile(joinPath, joinRaw, 0600)
	inputRaw, _ := json.Marshal(LegacyInputReceipt{SchemaVersion: "squad.review-input.v1", Identity: r.Identity, Settings: r.Settings, RecordedAt: 1})
	inputPath := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(inputPath, inputRaw, 0600); err != nil {
		t.Fatal(err)
	}
	legacy := LegacyRecoveryReceipt{InputReceiptPath: inputPath, InputReceiptSHA256: receiptHash(inputRaw), SchemaVersion: "squad.review-recovery.legacy.v1", Attempt: r, StatusPath: statusPath, StatusSHA256: receiptHash(statusRaw), WrapperPID: os.Getpid(), ReviewerPID: os.Getpid(), JoinReceiptPath: joinPath, JoinReceiptSHA256: receiptHash(joinRaw)}
	raw, _ := json.Marshal(legacy)
	path := filepath.Join(t.TempDir(), "legacy.json")
	_ = os.WriteFile(path, raw, 0600)
	if err = a.ImportLegacy(path); err == nil {
		t.Fatal("live process imported as joined")
	}
	legacy.InputReceiptPath = ""
	raw, _ = json.Marshal(legacy)
	_ = os.WriteFile(path, raw, 0600)
	if err = a.ImportLegacy(path); err == nil || !strings.Contains(err.Error(), "frozen input provenance unavailable") {
		t.Fatalf("missing input proof incorrectly accepted: %v", err)
	}
	legacy.InputReceiptPath = inputPath
	legacy.StatusSHA256 = "changed"
	raw, _ = json.Marshal(legacy)
	_ = os.WriteFile(path, raw, 0600)
	if err = a.ImportLegacy(path); err == nil {
		t.Fatal("changed legacy evidence imported")
	}
}

func TestLegacyImportPreservesValidDifferentHeadHistory(t *testing.T) {
	_, _, r := nativeProofFixture(t)
	// The fixture history must live under the same host-owned root used by native
	// verification. This test uses a PID receipt instead, with truly joined child
	// processes, and the reported two-head inventory identities.
	r.Identity.HeadSHA = "ab0c57e9bb2a11174b9d5db1449e503f7b2b7e03"
	statusDir := t.TempDir()
	status := ReviewStatus{SchemaVersion: ReviewStatusSchemaVersion, Attempt: r.ID, Repository: r.Identity.Repository, PullRequest: r.Identity.PR, BaseRef: r.Identity.BaseRef, BaseSHA: r.Identity.BaseSHA, HeadSHA: r.Identity.HeadSHA, Mode: "required", State: ReviewStateError, Verdict: VerdictError, FailureStage: "sampling", FailureKind: CLIFailureTimeout, CompletedAt: r.CompletedAt, StartedAt: 1}
	write := func(path string, v any) string {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return receiptHash(raw)
	}
	statusPath := filepath.Join(statusDir, "failed.json")
	statusHash := write(statusPath, status)
	old := status
	old.Attempt = "valid-old"
	old.HeadSHA = "590e8122e5e839b22f27b8a4090c80bb52eb2214"
	old.State = ReviewStateApproved
	old.Verdict = VerdictApproved
	write(filepath.Join(statusDir, "old-valid.json"), old)
	// Obtain real absent process identities; never guess a dead PID.
	child := exec.Command(os.Args[0], "-test.run=^$")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	join := LegacyJoinReceipt{SchemaVersion: "squad.review-join.v1", Attempt: r.ID, WrapperPID: pid, ReviewerPID: pid, Joined: true, ExitCode: 1}
	joinPath := filepath.Join(t.TempDir(), "join.json")
	joinHash := write(joinPath, join)
	inputRaw, _ := json.Marshal(LegacyInputReceipt{SchemaVersion: "squad.review-input.v1", Identity: r.Identity, Settings: r.Settings, RecordedAt: 1})
	inputPath := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(inputPath, inputRaw, 0600); err != nil {
		t.Fatal(err)
	}
	legacy := LegacyRecoveryReceipt{InputReceiptPath: inputPath, InputReceiptSHA256: receiptHash(inputRaw), SchemaVersion: "squad.review-recovery.legacy.v1", Attempt: r, StatusPath: statusPath, StatusSHA256: statusHash, WrapperPID: pid, ReviewerPID: pid, JoinReceiptPath: joinPath, JoinReceiptSHA256: joinHash}
	path := filepath.Join(t.TempDir(), "legacy.json")
	write(path, legacy)
	a := openTestAdmission(t, t.TempDir(), "", admissionSettings())
	if err := a.ImportLegacy(path); err != nil {
		t.Fatalf("old valid head incorrectly blocks failed current head: %v", err)
	}
	old.CompletedAt = 0
	write(filepath.Join(statusDir, "old-valid.json"), old)
	if err := a.ImportLegacy(path); err == nil {
		t.Fatal("active different-head invocation ignored")
	}
	old.CompletedAt = 1
	old.HeadSHA = status.HeadSHA
	write(filepath.Join(statusDir, "old-valid.json"), old)
	if err := a.ImportLegacy(path); err == nil {
		t.Fatal("valid current-head verdict ignored")
	}
}
