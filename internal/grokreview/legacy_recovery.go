package grokreview

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// LegacyRecoveryReceipt supplies evidence missing from older status files.
// The owning Worker creates it after joining the original wrapper and child.
// It is not an approval, and cannot alter inputs, settings, or review gates.
type LegacyRecoveryReceipt struct {
	InputReceiptPath   string           `json:"input_receipt_path"`
	InputReceiptSHA256 string           `json:"input_receipt_sha256"`
	SchemaVersion      string           `json:"schema_version"`
	Attempt            AttemptReceipt   `json:"attempt"`
	StatusPath         string           `json:"status_path"`
	StatusSHA256       string           `json:"status_sha256"`
	NativeJoin         *NativeJoinProof `json:"native_join,omitempty"`
	WrapperPID         int              `json:"wrapper_pid"`
	ReviewerPID        int              `json:"reviewer_pid"`
	JoinReceiptPath    string           `json:"join_receipt_path"`
	JoinReceiptSHA256  string           `json:"join_receipt_sha256"`
}

// LegacyInputReceipt is the original pre-sampling frozen input evidence.
// A later reconstruction or a hash of only the diff/body is insufficient.
type LegacyInputReceipt struct {
	SchemaVersion string         `json:"schema_version"`
	Identity      ReviewIdentity `json:"identity"`
	Settings      ReviewSettings `json:"settings"`
	RecordedAt    int64          `json:"recorded_at"`
}

type LegacyJoinReceipt struct {
	SchemaVersion string `json:"schema_version"`
	Attempt       string `json:"attempt"`
	WrapperPID    int    `json:"wrapper_pid"`
	ReviewerPID   int    `json:"reviewer_pid"`
	Joined        bool   `json:"joined"`
	ExitCode      int    `json:"exit_code"`
}

func readBoundedJSON(path string, out any) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("receipt path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, fmt.Errorf("receipt must be a bounded regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		return nil, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("receipt contains trailing JSON")
	}
	return raw, nil
}
func receiptHash(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }
func absentProcess(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("joined process identity missing")
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer func() { _ = p.Release() }()
	if err = p.Signal(syscall.Signal(0)); err != os.ErrProcessDone && err != syscall.ESRCH {
		return fmt.Errorf("old invocation process is live or its absence is unverified")
	}
	return nil
}

// ImportLegacy verifies local exit evidence plus original safe status. Remote
// current-head Check and exact inputs/settings are still checked by Start.
func (a *Admission) ImportLegacy(path string) error {
	var legacy LegacyRecoveryReceipt
	if _, err := readBoundedJSON(path, &legacy); err != nil {
		return err
	}
	r := legacy.Attempt
	if legacy.SchemaVersion != "squad.review-recovery.legacy.v1" || !recoverable(r) || r.Settings.Mode != "required" {
		return fmt.Errorf("legacy receipt is not a recoverable required timeout")
	}
	var status ReviewStatus
	raw, err := readBoundedJSON(legacy.StatusPath, &status)
	if err != nil {
		return err
	}
	if receiptHash(raw) != legacy.StatusSHA256 || status.SchemaVersion != ReviewStatusSchemaVersion || status.Attempt != r.ID || status.Repository != r.Identity.Repository || status.PullRequest != r.Identity.PR || status.BaseRef != r.Identity.BaseRef || status.BaseSHA != r.Identity.BaseSHA || status.HeadSHA != r.Identity.HeadSHA || status.Mode != r.Settings.Mode || status.State != ReviewStateError || status.Verdict != VerdictError || status.FindingCount != 0 || status.FailureStage != "sampling" || status.FailureKind != CLIFailureTimeout || status.CompletedAt != r.CompletedAt {
		return fmt.Errorf("legacy terminal timeout status does not match custody")
	}
	var input LegacyInputReceipt
	inputRaw, err := readBoundedJSON(legacy.InputReceiptPath, &input)
	if err != nil {
		return fmt.Errorf("original complete frozen input provenance unavailable: %w", err)
	}
	if receiptHash(inputRaw) != legacy.InputReceiptSHA256 || input.SchemaVersion != "squad.review-input.v1" || input.Identity != r.Identity || input.Settings != r.Settings || input.RecordedAt <= 0 || input.RecordedAt > status.StartedAt || len(input.Identity.BundleSHA256) != 64 {
		return fmt.Errorf("original pre-sampling frozen input evidence mismatched")
	}
	if legacy.NativeJoin != nil {
		if legacy.WrapperPID != 0 || legacy.ReviewerPID != 0 || legacy.JoinReceiptPath != "" {
			return fmt.Errorf("ambiguous legacy join provenance")
		}
		if err = verifyNativeJoin(*legacy.NativeJoin, r); err != nil {
			return err
		}
	} else {
		var join LegacyJoinReceipt
		raw, err = readBoundedJSON(legacy.JoinReceiptPath, &join)
		if err != nil {
			return err
		}
		if receiptHash(raw) != legacy.JoinReceiptSHA256 || join.SchemaVersion != "squad.review-join.v1" || join.Attempt != r.ID || !join.Joined || join.ExitCode != 1 || join.WrapperPID != legacy.WrapperPID || join.ReviewerPID != legacy.ReviewerPID {
			return fmt.Errorf("legacy invocation join evidence missing or mismatched")
		}
		if err = absentProcess(legacy.WrapperPID); err != nil {
			return err
		}
		if err = absentProcess(legacy.ReviewerPID); err != nil {
			return err
		}
	}
	// A second status in the original directory can evidence a live invocation or
	// valid verdict from an old wrapper that did not use this admission store.
	entries, err := os.ReadDir(filepath.Dir(legacy.StatusPath))
	if err != nil {
		return err
	}
	if len(entries) > 1000 {
		return fmt.Errorf("legacy status inventory exceeds bound")
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var other ReviewStatus
		if _, err = readBoundedJSON(filepath.Join(filepath.Dir(legacy.StatusPath), entry.Name()), &other); err != nil {
			return fmt.Errorf("unverified legacy status inventory: %w", err)
		}
		if other.Repository == r.Identity.Repository && other.PullRequest == r.Identity.PR && other.Attempt != r.ID {
			if other.CompletedAt == 0 {
				return fmt.Errorf("legacy review custody has an unresolved invocation")
			}
			sameTuple := other.HeadSHA == r.Identity.HeadSHA && other.BaseSHA == r.Identity.BaseSHA && other.BaseRef == r.Identity.BaseRef
			if sameTuple && other.Mode == r.Settings.Mode && (other.Verdict == VerdictApproved || other.Verdict == VerdictBlocking) {
				return fmt.Errorf("legacy review custody has a valid exact-tuple verdict")
			}
		}
	}
	a.from = r.ID
	a.legacy = &r
	return nil
}

func (a *Admission) LegacyAttemptID() string {
	if a.legacy == nil {
		return ""
	}
	return a.legacy.ID
}
