package grokreview

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func noVerdictReport() ReviewReport {
	return ReviewReport{Result: FindingsResult{Verdict: VerdictError}, Audit: CLIAudit{FailureKind: CLIFailureTimeout}, FailureStage: "sampling"}
}

func restartChild(t *testing.T, dir string, parent *Admission) *Admission {
	t.Helper()
	child := openTestAdmission(t, dir, "", admissionSettings())
	child.processAbsent = func(int) error { return nil }
	if err := child.ImportRestart(restartGrantFile(t, dir, parent.AttemptID(), 0600)); err != nil {
		t.Fatal(err)
	}
	return child
}

func TestRepeatedNoVerdictRestartLineage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	parent := openTestAdmission(t, dir, "", admissionSettings())
	if err := parent.Start(ctx, admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if err := parent.Finish(ctx, noVerdictReport()); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 4; n++ {
		child := restartChild(t, dir, parent)
		if err := child.Start(ctx, admissionBundle()); err != nil {
			t.Fatalf("restart %d: %v", n, err)
		}
		if child.receipt.Parent != parent.AttemptID() {
			t.Fatal("lost immediate parent")
		}
		if err := restartChild(t, dir, child).Start(ctx, admissionBundle()); err == nil {
			t.Fatal("active child restarted")
		}
		if err := child.Finish(ctx, noVerdictReport()); err != nil {
			t.Fatal(err)
		}
		if err := restartChild(t, dir, parent).Start(ctx, admissionBundle()); err == nil {
			t.Fatal("parent replay admitted")
		}
		retained, err := parent.load(ctx, parent.AttemptID())
		if err != nil || retained.Verdict != VerdictError || !retained.Joined {
			t.Fatal("parent receipt changed")
		}
		parent = child
	}
	final := restartChild(t, dir, parent)
	if err := final.Start(ctx, admissionBundle()); err != nil {
		t.Fatal(err)
	}
	if err := final.Finish(ctx, ReviewReport{Result: FindingsResult{Verdict: VerdictApproved}, Publication: Publication{CheckRunID: 9, Conclusion: "success"}}); err != nil {
		t.Fatal(err)
	}
	if err := restartChild(t, dir, final).Start(ctx, admissionBundle()); err == nil {
		t.Fatal("valid verdict resampled")
	}
}

func TestRestartChildRetainsLineageAndProcessFences(t *testing.T) {
	for _, scenario := range []string{"live-process", "changed-settings", "missing-parent", "cycle", "foreign-lineage", "ancestor-verdict", "ancestor-input"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			dir, parent := controllerChildFixture(t, "restart")
			if err := parent.Start(ctx, admissionBundle()); err != nil {
				t.Fatal(err)
			}
			if err := parent.Finish(ctx, noVerdictReport()); err != nil {
				t.Fatal(err)
			}
			child := restartChild(t, dir, parent)
			r := parent.receipt
			switch scenario {
			case "live-process":
				child.processAbsent = func(int) error { return fmt.Errorf("still live") }
			case "changed-settings":
				child.settings.Model = "other-model"
			case "missing-parent":
				r.Parent = "missing"
			case "cycle":
				r.Parent = r.ID
			case "foreign-lineage":
				r.InputProvenance = "controller-reconsideration"
			case "ancestor-verdict", "ancestor-input":
				var err error
				r, err = parent.load(ctx, r.Parent)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "ancestor-verdict" {
					r.Verdict = VerdictBlocking
				} else {
					r.Identity.HeadSHA = "other-head"
				}
			}
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parent.db.Exec("UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), r.ID); err != nil {
				t.Fatal(err)
			}
			if err := child.Start(ctx, admissionBundle()); err == nil {
				t.Fatal("invalid restart lineage admitted")
			}
		})
	}
}
