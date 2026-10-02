package grokreview

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zsiec/squad/internal/store"
)

type joinJournal struct {
	SchemaVersion string         `json:"schema_version"`
	WrapperPID    int            `json:"wrapper_pid"`
	Receipt       AttemptReceipt `json:"receipt"`
}

// writeJoinJournal runs only after the synchronous sampler and publication have
// returned. It survives a database join failure without resampling or publishing.
func (a *Admission) writeJoinJournal(r AttemptReceipt) error {
	raw, err := json.Marshal(joinJournal{"squad.review-terminal-join.v1", os.Getpid(), r})
	if err != nil {
		return err
	}
	dir := filepath.Join(a.dir, "joins")
	if err := ensureStatusDirectory(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".join-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, r.ID+".json")); err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	return parent.Sync()
}

// Reconcile joins only the original terminal evidence. Time/lease expiry is
// never authority to release single-flight. Recovery slots and exact input
// history remain consumed; this operation never samples or publishes a Check.
func (a *Admission) Reconcile(ctx context.Context, id, repository string, pr int) error {
	if !validAttemptID(id) {
		return fmt.Errorf("invalid original attempt identity")
	}
	var r AttemptReceipt
	if a.legacy != nil && a.legacy.ID == id {
		r = *a.legacy // ImportLegacy independently verified original input and native/process join.
	} else {
		path := filepath.Join(a.dir, "joins", id+".json")
		info, err := os.Lstat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			stored, loadErr := a.load(ctx, id)
			if loadErr != nil {
				return loadErr
			}

			if stored.Joined {
				r = stored
			} else {
				if stored.WrapperPID <= 0 || (stored.ReviewerPID <= 0 && stored.LaunchStage != "admitted") {
					return fmt.Errorf("original process provenance unavailable; terminal/native join proof required")
				}
				if err = a.processAbsent(stored.WrapperPID); err != nil {
					return err
				}
				if stored.ReviewerPID > 0 {
					if err = a.processAbsent(stored.ReviewerPID); err != nil {
						return err
					}
				}
				r = stored
				r.Joined = true
				if !stored.SamplingCompleted {
					r.Verdict = VerdictError
					r.FailureStage = "sampling"
					r.FailureKind = CLIFailureCanceled
				}
				r.CompletedAt = time.Now().Unix()
			}
		} else {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				return fmt.Errorf("terminal join journal is not private")
			}
			var journal joinJournal
			if _, err = readBoundedJSON(path, &journal); err != nil {
				return err
			}
			if journal.SchemaVersion != "squad.review-terminal-join.v1" {
				return fmt.Errorf("unsupported terminal join journal")
			}
			if err = a.processAbsent(journal.WrapperPID); err != nil {
				return err
			}
			r = journal.Receipt
			if r.WrapperPID != journal.WrapperPID {
				return fmt.Errorf("terminal process provenance changed")
			}
		}
	}
	if r.ID != id || r.Identity.Repository != repository || r.Identity.PR != pr || !r.Joined || r.CompletedAt <= 0 || r.Settings != a.settings {
		return fmt.Errorf("terminal join identity/settings mismatch")
	}

	beforeReadback := r
	lookupApplied := false
	if r.SamplingCompleted && r.Publication.CheckRunID == 0 {
		if a.publicationLookup == nil {
			return fmt.Errorf("original publication outcome unavailable; qualified original terminal/Check proof required")
		}
		if r.WrapperPID > 0 {
			if err := a.processAbsent(r.WrapperPID); err != nil {
				return err
			}
		}
		publication, err := a.publicationLookup(ctx, r)
		if err != nil {
			return err
		}
		if publication.CheckRunID > 0 {
			r.Publication = publication
			lookupApplied = true
			if r.SamplingFailureStage == "sampling" && r.Verdict == VerdictError && r.FailureKind == CLIFailureTimeout {
				r.FailureStage = "sampling"
			}
		}
	}
	return store.WithTxRetry(ctx, a.db, func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRowContext(ctx, "SELECT receipt FROM review_attempts WHERE id=?", id).Scan(&raw); err != nil {
			return err
		}
		var old AttemptReceipt
		if err := json.Unmarshal([]byte(raw), &old); err != nil {
			return err
		}
		if old.Identity != r.Identity || old.Settings != r.Settings || old.Parent != r.Parent {
			return fmt.Errorf("terminal join input custody changed")
		}
		if old.Joined {
			if old == r {
				return nil
			}
			if !lookupApplied || old != beforeReadback {
				return fmt.Errorf("terminal join receipt conflicts with durable history")
			}
			encoded, err := json.Marshal(r)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "UPDATE review_attempts SET receipt=? WHERE id=?", string(encoded), id)
			return err
		}
		result, err := tx.ExecContext(ctx, "DELETE FROM review_flights WHERE repository=? AND pr=? AND attempt=?", r.Identity.Repository, r.Identity.PR, id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("terminal join flight fence rejected")
		}
		encoded, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE review_attempts SET receipt=? WHERE id=?", string(encoded), id)
		return err
	})
}

func validAttemptID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ReviewerStarted records the actual child before waiting. If this write fails,
// the runner joins its owned child and returns an operational error.
func (a *Admission) ReviewerStarted(ctx context.Context, pid int) error {
	if pid <= 0 || a.receipt.ID == "" {
		return fmt.Errorf("actual admitted reviewer process required")
	}
	r := a.receipt
	r.ReviewerPID = pid
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	result, err := a.db.ExecContext(ctx, `UPDATE review_attempts SET receipt=? WHERE id=? AND EXISTS(SELECT 1 FROM review_flights WHERE attempt=?)`, string(raw), r.ID, r.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("reviewer process custody fence rejected")
	}
	a.receipt = r
	return nil
}

// ReviewerLaunching commits before OS spawn. A dead admitted wrapper therefore
// proves no child launch; a crash in the spawn/PID-record window stays closed
// until actual original process/native join provenance is supplied.
func (a *Admission) ReviewerLaunching(ctx context.Context) error {
	r := a.receipt
	if r.ID == "" || r.LaunchStage != "admitted" {
		return fmt.Errorf("admitted launch custody required")
	}
	r.LaunchStage = "launching"
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	result, err := a.db.ExecContext(ctx, `UPDATE review_attempts SET receipt=? WHERE id=? AND EXISTS(SELECT 1 FROM review_flights WHERE attempt=?)`, string(raw), r.ID, r.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("review launch custody fence rejected")
	}
	a.receipt = r
	return nil
}
