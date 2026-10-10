package grokreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func abandonedSettings(mode string) ReviewSettings {
	s := admissionSettings()
	s.Mode = mode
	return s
}

// startSampling admits an attempt, records a reviewer child and writes the
// sampling status a monitor would see, as a wrapper killed mid-sampling leaves it.
func startSampling(t *testing.T, dir, statusDir string, settings ReviewSettings) *Admission {
	t.Helper()
	a := openTestAdmission(t, dir, "", settings)
	if err := a.Start(context.Background(), admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if err := a.ReviewerLaunching(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.ReviewerStarted(context.Background(), 4242); err != nil {
		t.Fatal(err)
	}
	w, err := NewReviewStatusWriter(statusDir, settings.Mode, time.Duration(settings.TimeoutMS)*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.BindAttempt(a.AttemptID()); err != nil {
		t.Fatal(err)
	}
	snapshot := PullRequestSnapshot{Repository: "owner/repo", Number: 9, BaseRef: "main", BaseSHA: "base", HeadSHA: "head"}
	if err = w.Observe(ReviewObservation{State: ReviewStateSampling, Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	return a
}

func readStatus(t *testing.T, dir, id string) ReviewStatus {
	t.Helper()
	var s ReviewStatus
	if _, err := readBoundedJSON(filepath.Join(dir, reviewStatusFilename("owner/repo", 9, "head", id)), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReconcileJoinsTerminatedAttemptWithoutVerdictInBothModes(t *testing.T) {
	for _, mode := range []string{"shadow", "required"} {
		t.Run(mode, func(t *testing.T) {
			dir, statusDir := t.TempDir(), t.TempDir()
			settings := abandonedSettings(mode)
			orig := startSampling(t, dir, statusDir, settings)
			id := orig.AttemptID()

			resumed := openTestAdmission(t, dir, "", settings)
			if err := resumed.Reconcile(context.Background(), id, "owner/repo", 9); err == nil {
				t.Fatal("live wrapper joined")
			}
			resumed.processAbsent = func(int) error { return nil }
			if err := resumed.SetJoinReason(JoinReasonSupersededInput); err != nil {
				t.Fatal(err)
			}
			if err := resumed.Reconcile(context.Background(), id, "owner/repo", 9); err != nil {
				t.Fatal(err)
			}
			r, err := resumed.load(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if !r.Joined || r.Verdict != VerdictError || r.FailureKind != CLIFailureCanceled || r.JoinReason != JoinReasonSupersededInput || r.Publication.CheckRunID != 0 || r.CostKnown || r.UsageKnown {
				t.Fatalf("join must record no verdict, publication or usage: %+v", r)
			}
			if err = resumed.Reconcile(context.Background(), id, "owner/repo", 9); err != nil {
				t.Fatal("not idempotent", err)
			}
			if err = openTestAdmission(t, dir, "", settings).Start(context.Background(), admissionBundle()); err == nil {
				t.Fatal("consumed input refunded")
			}
			var b FrozenReviewBundle
			_ = json.Unmarshal(admissionBundle(), &b)
			b.HeadSHA = "corrected-head"
			raw, _ := json.Marshal(b)
			if err = resumed.Start(context.Background(), raw); err != nil {
				t.Fatal("flight not released", err)
			}
		})
	}
}

func TestReconcileRefusesLaunchingGapEvenWithReason(t *testing.T) {
	for _, mode := range []string{"shadow", "required"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			a := openTestAdmission(t, dir, "", abandonedSettings(mode))
			if err := a.Start(context.Background(), admissionBundle()); err != nil {
				t.Fatal(err)
			}
			if err := a.ReviewerLaunching(context.Background()); err != nil {
				t.Fatal(err)
			}
			r := openTestAdmission(t, dir, "", abandonedSettings(mode))
			r.processAbsent = func(int) error { return nil }
			_ = r.SetJoinReason(JoinReasonSupersededInput)
			if err := r.Reconcile(context.Background(), a.AttemptID(), "owner/repo", 9); err == nil {
				t.Fatal("unrecorded child accepted")
			}
		})
	}
}

func TestJoinReasonValidationAndSettingsMismatch(t *testing.T) {
	a := openTestAdmission(t, t.TempDir(), "", abandonedSettings("shadow"))
	if err := a.SetJoinReason("because"); err == nil {
		t.Fatal("arbitrary reason accepted")
	}
	dir := t.TempDir()
	orig := startSampling(t, dir, t.TempDir(), abandonedSettings("shadow"))
	other := openTestAdmission(t, dir, "", abandonedSettings("required"))
	other.processAbsent = func(int) error { return nil }
	if err := other.Reconcile(context.Background(), orig.AttemptID(), "owner/repo", 9); err == nil {
		t.Fatal("mode switch joined")
	}
}

func TestTerminalizeStatusClosesOnlyMatchingNonTerminalFile(t *testing.T) {
	for _, mode := range []string{"shadow", "required"} {
		t.Run(mode, func(t *testing.T) {
			dir, statusDir := t.TempDir(), t.TempDir()
			settings := abandonedSettings(mode)
			orig := startSampling(t, dir, statusDir, settings)
			id := orig.AttemptID()
			a := openTestAdmission(t, dir, "", settings)
			a.processAbsent = func(int) error { return nil }
			if done, err := a.TerminalizeStatus(context.Background(), statusDir, id); err != nil || done {
				t.Fatal("unjoined attempt's status must stay untouched", done, err)
			}
			if readStatus(t, statusDir, id).State != ReviewStateSampling {
				t.Fatal("status changed before join")
			}
			_ = a.SetJoinReason(JoinReasonSupersededInput)
			if err := a.Reconcile(context.Background(), id, "owner/repo", 9); err != nil {
				t.Fatal(err)
			}
			done, err := a.TerminalizeStatus(context.Background(), statusDir, id)
			if err != nil || !done {
				t.Fatal("status not terminalized", done, err)
			}
			s := readStatus(t, statusDir, id)
			if s.State != ReviewStateError || s.Verdict != VerdictError || s.FailureKind != CLIFailureCanceled || s.CompletedAt == 0 || s.Attempt != id {
				t.Fatalf("unexpected terminal status %+v", s)
			}
			if done, err = a.TerminalizeStatus(context.Background(), statusDir, id); err != nil || done {
				t.Fatal("terminal status rewritten", done, err)
			}
			empty := t.TempDir()
			if done, err = a.TerminalizeStatus(context.Background(), empty, id); err != nil || done {
				t.Fatal("missing status must be a no-op", done, err)
			}
		})
	}
}

func TestTerminalizeStatusRejectsMismatchedOrExposedFile(t *testing.T) {
	dir, statusDir := t.TempDir(), t.TempDir()
	settings := abandonedSettings("shadow")
	orig := startSampling(t, dir, statusDir, settings)
	id := orig.AttemptID()
	a := openTestAdmission(t, dir, "", settings)
	a.processAbsent = func(int) error { return nil }
	if err := a.Reconcile(context.Background(), id, "owner/repo", 9); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(statusDir, reviewStatusFilename("owner/repo", 9, "head", id))
	s := readStatus(t, statusDir, id)
	s.Mode = "required"
	if err := writeStatusAtomically(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.TerminalizeStatus(context.Background(), statusDir, id); err == nil {
		t.Fatal("foreign-mode status rewritten")
	}
	s.Mode = "shadow"
	if err := writeStatusAtomically(path, s); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.TerminalizeStatus(context.Background(), statusDir, id); err == nil {
		t.Fatal("non-private status rewritten")
	}
}
