package grokreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func TestAdmissionShadowRecoveryPreservesModeAndOneUse(t *testing.T) {
	dir := t.TempDir()
	settings := admissionSettings()
	settings.Mode = "shadow"
	parent := openTestAdmission(t, dir, "", settings)
	if err := parent.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if err := parent.Finish(context.Background(), timeoutReport()); err != nil {
		t.Fatal(err)
	}
	child := openTestAdmission(t, dir, parent.AttemptID(), settings)
	if err := child.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if child.receipt.Settings.Mode != "shadow" {
		t.Fatal("original mode changed")
	}
	if err := child.Finish(context.Background(), timeoutReport()); err != nil {
		t.Fatal(err)
	}
	if err := openTestAdmission(t, dir, parent.AttemptID(), settings).Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("shadow slot replayed")
	}
	if err := openTestAdmission(t, dir, parent.AttemptID(), admissionSettings()).Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("mode switch admitted")
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
	id := seedDiagnosticTimeout(t, dir)
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
	old.Mode = "shadow"
	write(filepath.Join(statusDir, "old-valid.json"), old)
	if err := a.ImportLegacy(path); err != nil {
		t.Fatal("valid shadow poisoned required timeout recovery", err)
	}
	old.Mode = status.Mode
	write(filepath.Join(statusDir, "old-valid.json"), old)
	if err := a.ImportLegacy(path); err == nil {
		t.Fatal("valid current-head verdict ignored")
	}
}

func TestAdmissionReconcileAfterJoinWriteFailurePreservesSlot(t *testing.T) {
	dir := t.TempDir()
	parent := seedTimeout(t, dir)
	a := openTestAdmission(t, dir, parent, admissionSettings())
	if err := a.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	id := a.AttemptID()
	if err := a.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Finish(context.Background(), timeoutReport()); err == nil {
		t.Fatal("closed DB join succeeded")
	}
	resumed := openTestAdmission(t, dir, "", admissionSettings())
	if err := resumed.Reconcile(context.Background(), id, "owner/repo", 9); err == nil {
		t.Fatal("live original wrapper bypassed")
	}
	resumed.processAbsent = func(int) error { return nil } // Isolated fixture simulates verified original process exit.
	if err := resumed.Reconcile(context.Background(), id, "owner/repo", 9); err != nil {
		t.Fatal(err)
	}
	if err := resumed.Reconcile(context.Background(), id, "owner/repo", 9); err != nil {
		t.Fatal("idempotent join", err)
	}
	if err := openTestAdmission(t, dir, parent, admissionSettings()).Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("recovery slot refunded")
	}
	var b FrozenReviewBundle
	_ = json.Unmarshal(admissionBundle(), &b)
	b.HeadSHA = "corrected-head"
	raw, _ := json.Marshal(b)
	if err := resumed.Start(context.Background(), raw); err != nil {
		t.Fatal("joined flight still wedges corrected head", err)
	}
}

func TestAdmissionReconcileRequiresTerminalProofAndExactCustody(t *testing.T) {
	dir := t.TempDir()
	a := openTestAdmission(t, dir, "", admissionSettings())
	if err := a.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	id := a.AttemptID()
	if err := a.Reconcile(context.Background(), id, "owner/repo", 9); err == nil {
		t.Fatal("unjoined interrupted flight expired without proof")
	}
	if err := a.Finish(context.Background(), timeoutReport()); err != nil {
		t.Fatal(err)
	}
	a.processAbsent = func(int) error { return nil }
	if err := a.Reconcile(context.Background(), id, "foreign/repo", 9); err == nil {
		t.Fatal("wrong repo joined")
	}
	if err := a.Reconcile(context.Background(), id, "owner/repo", 8); err == nil {
		t.Fatal("wrong PR joined")
	}
	var journal joinJournal
	path := filepath.Join(dir, "joins", id+".json")
	if _, err := readBoundedJSON(path, &journal); err != nil {
		t.Fatal(err)
	}
	journal.Receipt.Identity.HeadSHA = "changed"
	raw, _ := json.Marshal(journal)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.Reconcile(context.Background(), id, "owner/repo", 9); err == nil {
		t.Fatal("changed terminal input joined")
	}
}

func TestAdmissionInterruptedProcessJoinDoesNotResampleOrRefund(t *testing.T) {
	dir := t.TempDir()
	parent := seedTimeout(t, dir)
	a := openTestAdmission(t, dir, parent, admissionSettings())
	if err := a.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if err := a.ReviewerStarted(context.Background(), os.Getpid()); err != nil {
		t.Fatal(err)
	}
	id := a.AttemptID()
	resumed := openTestAdmission(t, dir, "", admissionSettings())
	if err := resumed.Reconcile(context.Background(), id, "owner/repo", 9); err == nil {
		t.Fatal("live original processes joined")
	}
	resumed.processAbsent = func(int) error { return nil } // Actual PID fixture; simulate both verified exits.
	if err := resumed.Reconcile(context.Background(), id, "owner/repo", 9); err != nil {
		t.Fatal(err)
	}
	receipt, err := resumed.load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Joined || receipt.Verdict != VerdictError || receipt.FailureKind != CLIFailureCanceled || receipt.Publication.CheckRunID != 0 {
		t.Fatal("invented verdict or publication", receipt)
	}
	if err := openTestAdmission(t, dir, parent, admissionSettings()).Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("interrupted one-use slot refunded")
	}
	var b FrozenReviewBundle
	_ = json.Unmarshal(admissionBundle(), &b)
	b.HeadSHA = "fixed-head"
	raw, _ := json.Marshal(b)
	if err := resumed.Start(context.Background(), raw); err != nil {
		t.Fatal("joined interruption wedges next head", err)
	}
}

func TestAdmissionInterruptedBeforeLaunchAndSpawnGap(t *testing.T) {
	for _, launching := range []bool{false, true} {
		t.Run(fmt.Sprint(launching), func(t *testing.T) {
			dir := t.TempDir()
			a := openTestAdmission(t, dir, "", admissionSettings())
			if err := a.Start(context.Background(), admissionBundle()); err != nil {
				t.Fatal(err)
			}
			if launching {
				if err := a.ReviewerLaunching(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			resumed := openTestAdmission(t, dir, "", admissionSettings())
			resumed.processAbsent = func(int) error { return nil } // Verified original wrapper exit fixture.
			err := resumed.Reconcile(context.Background(), a.AttemptID(), "owner/repo", 9)
			if launching {
				if err == nil {
					t.Fatal("unknown spawned child provenance accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = resumed.Reconcile(context.Background(), a.AttemptID(), "owner/repo", 9); err != nil {
				t.Fatal("joined replay", err)
			}
			if err = resumed.Start(context.Background(), admissionBundle()); err == nil {
				t.Fatal("same input sampled again")
			}
			var b FrozenReviewBundle
			_ = json.Unmarshal(admissionBundle(), &b)
			b.HeadSHA = "corrected-head"
			raw, _ := json.Marshal(b)
			if err = resumed.Start(context.Background(), raw); err != nil {
				t.Fatal("prelaunch interruption wedges corrected head", err)
			}
		})
	}
}

func TestAdmissionShadowDoesNotConsumeRequiredSample(t *testing.T) {
	dir := t.TempDir()
	shadow := admissionSettings()
	shadow.Mode = "shadow"
	a := openTestAdmission(t, dir, "", shadow)
	if err := a.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if err := openTestAdmission(t, dir, "", admissionSettings()).Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("overlapping modes admitted")
	}
	report := timeoutReport()
	report.Result.Verdict = VerdictApproved
	if err := a.Finish(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	required := openTestAdmission(t, dir, "", admissionSettings())
	if err := required.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal("shadow blocked required gate", err)
	}
	if err := required.Finish(context.Background(), timeoutReport()); err != nil {
		t.Fatal(err)
	}
	if err := openTestAdmission(t, dir, "", admissionSettings()).Start(context.Background(), admissionBundle()); err == nil {
		t.Fatal("required sample repeated")
	}
}

func TestAdmissionPublishedTimeoutWithoutJoinJournal(t *testing.T) {
	for _, postPublication := range []bool{false, true} {
		t.Run(fmt.Sprint(postPublication), func(t *testing.T) {
			dir := t.TempDir()
			a := openTestAdmission(t, dir, "", admissionSettings())
			if err := a.Start(context.Background(), admissionBundle()); err != nil {
				t.Fatal(err)
			}
			if err := a.ReviewerStarted(context.Background(), os.Getpid()); err != nil {
				t.Fatal(err)
			}
			report := timeoutReport()
			if checkpoint, ok := any(a).(interface {
				Checkpoint(context.Context, ReviewReport) error
			}); ok {
				sampling := report
				sampling.Publication = Publication{}
				if err := checkpoint.Checkpoint(context.Background(), sampling); err != nil {
					t.Fatal(err)
				}
				if postPublication {
					if err := checkpoint.Checkpoint(context.Background(), report); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Actual remote Check has committed, but the process dies before recording
			// its response, or Finish cannot create the terminal journal.
			if err := os.WriteFile(filepath.Join(dir, "joins"), []byte("not a directory"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.Finish(context.Background(), report); err == nil {
				t.Fatal("journal failure not reproduced")
			}
			if err := os.Remove(filepath.Join(dir, "joins")); err != nil {
				t.Fatal(err)
			}
			resumed := openTestAdmission(t, dir, "", admissionSettings())
			resumed.processAbsent = func(int) error { return nil }
			if lookup, ok := any(resumed).(interface {
				SetPublicationLookup(func(context.Context, AttemptReceipt) (Publication, error))
			}); ok {
				lookup.SetPublicationLookup(func(_ context.Context, r AttemptReceipt) (Publication, error) {
					if r.FailureKind != CLIFailureTimeout {
						t.Fatal("timeout outcome lost")
					}
					return report.Publication, nil
				})
			}
			if err := resumed.Reconcile(context.Background(), a.AttemptID(), "owner/repo", 9); err != nil {
				t.Fatal(err)
			}
			r, err := resumed.load(context.Background(), a.AttemptID())
			if err != nil {
				t.Fatal(err)
			}
			if !recoverable(r) {
				t.Fatal("published timeout rewritten to unrecoverable cancellation", r)
			}
			if err := openTestAdmission(t, dir, r.ID, admissionSettings()).Start(context.Background(), admissionBundle()); err != nil {
				t.Fatal("proven timeout recovery blocked", err)
			}
		})
	}
}

func TestReconcileLostPublicationResponsePreservesJoinedTimeout(t *testing.T) {
	dir := t.TempDir()
	a := openTestAdmission(t, dir, "", admissionSettings())
	if err := a.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	sampling := timeoutReport()
	sampling.Publication = Publication{}
	if err := a.Checkpoint(context.Background(), sampling); err != nil {
		t.Fatal(err)
	}
	lost := sampling
	lost.FailureStage = "publishing"
	if err := a.Finish(context.Background(), lost); err != nil {
		t.Fatal(err)
	}
	resumed := openTestAdmission(t, dir, "", admissionSettings())
	resumed.processAbsent = func(int) error { return nil }
	lookup := func(_ context.Context, r AttemptReceipt) (Publication, error) {
		if r.SamplingFailureStage != "sampling" || r.FailureKind != CLIFailureTimeout {
			t.Fatal("original sampling evidence lost")
		}
		return timeoutReport().Publication, nil
	}
	resumed.SetPublicationLookup(lookup)
	if err := resumed.Reconcile(context.Background(), a.AttemptID(), "owner/repo", 9); err != nil {
		t.Fatal(err)
	}
	// Refined actual publication and immutable original journal coexist.
	if err := resumed.Reconcile(context.Background(), a.AttemptID(), "owner/repo", 9); err != nil {
		t.Fatal("replay", err)
	}
	r, err := resumed.load(context.Background(), a.AttemptID())
	if err != nil {
		t.Fatal(err)
	}
	if !recoverable(r) {
		t.Fatal("actual failed timeout Check not recovered", r)
	}
	if err := openTestAdmission(t, dir, r.ID, admissionSettings()).Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
}

// Seed the actual receipt type, including every optional pointer-bearing subtree.
// Persist and decode it independently, as normal timeout recovery does.
func seedDiagnosticTimeout(t *testing.T, dir string) string {
	t.Helper()
	id := seedTimeout(t, dir)
	a := openTestAdmission(t, dir, id, admissionSettings())
	r, err := a.load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)
	joined := started.Add(20 * time.Minute)
	exit := -1
	r.Terminal = &TerminalDiagnostics{
		InputSHA256: r.Identity.BundleSHA256, StartedAt: started, FinishedAt: joined, JoinedAt: &joined,
		ChildStarted: true, ChildWaited: true, ExitCode: &exit, DeadlineProducer: "reviewer_timeout",
		StdoutBytes: 13, StderrBytes: 7, StdoutSHA256: strings.Repeat("a", 64), StderrSHA256: strings.Repeat("b", 64),
		OutputTruncated: true, SessionEvidence: "qualified_local",
		Session: &TerminalSession{SessionID: "00000000-0000-4000-8000-000000000001", LocalRequestID: "00000000-0000-4000-8000-000000000002", EventsSHA256: strings.Repeat("c", 64), FirstTokenAt: &started, FirstReasoningAt: &started, LastReasoningAt: &joined, ReasoningNotifications: 10},
	}
	r.RendererProvenance = &PatchRendererProvenance{Rule: "strict-full-patch", OriginalSHA256: strings.Repeat("d", 64), CurrentSHA256: strings.Repeat("e", 64), EvidenceSHA256: strings.Repeat("f", 64)}
	r.InputProvenance = "prospective-legacy-new-input"
	r.InputRecordedAt = started.Unix()
	r.AuthorizationSHA256 = strings.Repeat("1", 64)
	r.AuthorizationReference = "local-authorized-source"
	r.OwnerActor, r.OwnerNative = "original-owner", "original-native"
	r.LaunchStage, r.SamplingFailureStage = "sampling-completed", "sampling"
	r.SamplingCompleted = true
	r.ReviewerPID = 999999
	r.RequestID, r.SessionID, r.ResolvedModel = "local-request", "local-session", r.Settings.Model
	r.DurationMS = r.Settings.TimeoutMS
	r.Publication.CommentID, r.Publication.CommentURL, r.Publication.CheckURL = 2, "https://example.test/comment/2", "https://example.test/check/1"
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAdmissionRecoveryDecodedDiagnosticReceipt(t *testing.T) {
	for _, field := range []string{"terminal", "renderer", "both"} {
		t.Run(field, func(t *testing.T) {
			dir := t.TempDir()
			id := seedDiagnosticTimeout(t, dir)
			a := openTestAdmission(t, dir, id, admissionSettings())
			prior, err := a.load(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if field == "terminal" {
				prior.RendererProvenance = nil
			}
			if field == "renderer" {
				prior.Terminal = nil
			}
			raw, err := json.Marshal(prior)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.db.Exec("UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), id); err != nil {
				t.Fatal(err)
			}
			a.check = func(_ context.Context, loaded AttemptReceipt) error {
				if !reflect.DeepEqual(loaded, prior) {
					t.Fatal("decoded receipt values changed")
				}
				if loaded == prior {
					t.Fatal("fixture did not reproduce independent pointer decoding")
				}
				return nil
			}
			if err := a.Start(context.Background(), admissionBundle()); err != nil {
				t.Fatalf("same-value diagnostic receipt rejected: %v", err)
			}
			after, err := a.load(context.Background(), id)
			if err != nil || !reflect.DeepEqual(after, prior) {
				t.Fatal("original receipt rewritten", err)
			}
			if a.Receipt().Parent != id {
				t.Fatal("lost original root")
			}
			if err := a.Finish(context.Background(), timeoutReport()); err != nil {
				t.Fatal(err)
			}
			if err := openTestAdmission(t, dir, id, admissionSettings()).Start(context.Background(), admissionBundle()); err == nil {
				t.Fatal("diagnostic receipt recovery slot refunded")
			}
		})
	}
}

func TestAdmissionRecoveryRejectsTransactionReceiptChanges(t *testing.T) {
	changes := map[string]func(*AttemptReceipt){
		"terminal-hash":      func(r *AttemptReceipt) { r.Terminal.InputSHA256 = "changed" },
		"terminal-exit":      func(r *AttemptReceipt) { *r.Terminal.ExitCode = 0 },
		"terminal-join-time": func(r *AttemptReceipt) { *r.Terminal.JoinedAt = r.Terminal.JoinedAt.Add(time.Second) },
		"terminal-session":   func(r *AttemptReceipt) { r.Terminal.Session.LocalRequestID = "changed" },
		"terminal-reasoning": func(r *AttemptReceipt) { r.Terminal.Session.ReasoningNotifications++ },
		"terminal-nil":       func(r *AttemptReceipt) { r.Terminal = nil },
		"renderer-rule":      func(r *AttemptReceipt) { r.RendererProvenance.Rule = "changed" },
		"renderer-evidence":  func(r *AttemptReceipt) { r.RendererProvenance.EvidenceSHA256 = "changed" },
		"renderer-nil":       func(r *AttemptReceipt) { r.RendererProvenance = nil },
		"owner":              func(r *AttemptReceipt) { r.OwnerActor = "changed" },
		"native":             func(r *AttemptReceipt) { r.OwnerNative = "changed" },
		"tuple":              func(r *AttemptReceipt) { r.Identity.HeadSHA = "changed" },
		"settings":           func(r *AttemptReceipt) { r.Settings.TimeoutMS++ },
		"join":               func(r *AttemptReceipt) { r.Joined = false },
		"verdict":            func(r *AttemptReceipt) { r.Verdict = VerdictApproved },
		"publication":        func(r *AttemptReceipt) { r.Publication.CheckRunID++ },
		"authorization":      func(r *AttemptReceipt) { r.AuthorizationSHA256 = "changed" },
		"usage":              func(r *AttemptReceipt) { r.UsageKnown = true; r.Usage.TotalTokens = 1 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			id := seedDiagnosticTimeout(t, dir)
			a := openTestAdmission(t, dir, id, admissionSettings())
			a.check = func(ctx context.Context, _ AttemptReceipt) error {
				changed, err := a.load(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				change(&changed)
				raw, err := json.Marshal(changed)
				if err != nil {
					t.Fatal(err)
				}
				_, err = a.db.ExecContext(ctx, "UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), id)
				return err
			}
			err := a.Start(context.Background(), admissionBundle())
			want := "original custody changed"
			if name == "verdict" {
				want = "current tuple already has a valid managed verdict"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("changed receipt admitted: %v", err)
			}
			for _, table := range []string{"review_flights", "review_recoveries", "review_recovery_roots"} {
				var count int
				if err := a.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("rejected custody reserved %s: %d", table, count)
				}
			}
			var count int
			if err := a.db.QueryRow("SELECT count(*) FROM review_attempts").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatal("rejected custody persisted a child")
			}
		})
	}
}
