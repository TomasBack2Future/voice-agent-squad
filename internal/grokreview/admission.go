package grokreview

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/zsiec/squad/internal/store"
)

// ReviewSettings contains the selected settings, never credentials or prompts.
type ReviewSettings struct {
	Mode              string `json:"mode"`
	Model             string `json:"model"`
	Effort            string `json:"effort"`
	TimeoutMS         int64  `json:"timeout_ms"`
	MaxGitHubOutput   int    `json:"max_github_output"`
	MaxReviewerOutput int    `json:"max_reviewer_output"`
	AppID             int64  `json:"app_id"`
	InstallationID    int64  `json:"installation_id"`
}

type ReviewIdentity struct {
	Repository   string `json:"repository"`
	PR           int    `json:"pr"`
	BaseRef      string `json:"base_ref"`
	BaseSHA      string `json:"base_sha"`
	HeadSHA      string `json:"head_sha"`
	BundleSHA256 string `json:"bundle_sha256"`
}

// AttemptReceipt is authoritative only in the private admission store. No diff,
// prompt, findings prose, credentials or raw model output is persisted here.
type AttemptReceipt struct {
	InputProvenance        string         `json:"input_provenance,omitempty"`
	InputRecordedAt        int64          `json:"input_recorded_at,omitempty"`
	AuthorizationSHA256    string         `json:"authorization_sha256,omitempty"`
	AuthorizationReference string         `json:"authorization_reference,omitempty"`
	OwnerActor             string         `json:"owner_actor,omitempty"`
	OwnerNative            string         `json:"owner_native,omitempty"`
	LaunchStage            string         `json:"launch_stage,omitempty"`
	SamplingFailureStage   string         `json:"sampling_failure_stage,omitempty"`
	SamplingCompleted      bool           `json:"sampling_completed"`
	WrapperPID             int            `json:"wrapper_pid,omitempty"`
	ReviewerPID            int            `json:"reviewer_pid,omitempty"`
	CostKnown              bool           `json:"cost_known"`
	RequestID              string         `json:"request_id,omitempty"`
	SessionID              string         `json:"session_id,omitempty"`
	ResolvedModel          string         `json:"resolved_model,omitempty"`
	DurationMS             int64          `json:"duration_ms"`
	ID                     string         `json:"id"`
	Parent                 string         `json:"parent,omitempty"`
	Identity               ReviewIdentity `json:"identity"`
	Settings               ReviewSettings `json:"settings"`
	Joined                 bool           `json:"joined"`
	Verdict                Verdict        `json:"verdict"`
	FailureStage           string         `json:"failure_stage"`
	FailureKind            CLIFailureKind `json:"failure_kind"`
	Publication            Publication    `json:"publication"`
	Usage                  TokenUsage     `json:"usage"`
	UsageKnown             bool           `json:"usage_known"`
	CostUSD                float64        `json:"cost_usd"`
	CompletedAt            int64          `json:"completed_at"`
}

type RecoveryCheck func(context.Context, AttemptReceipt) error

type Admission struct {
	db                *sql.DB
	dir               string
	processAbsent     func(int) error
	settings          ReviewSettings
	from              string
	check             RecoveryCheck
	publicationLookup func(context.Context, AttemptReceipt) (Publication, error)
	receipt           AttemptReceipt
	legacy            *AttemptReceipt
	prospective       *ProspectiveReadmission
}

// OpenAdmission never opens or migrates the Squad work ledger. All invocations
// of a configured reviewer must use the same private admission directory.
func OpenAdmission(dir string, settings ReviewSettings, from string, check RecoveryCheck) (*Admission, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("admission directory must be absolute")
	}
	if err := ensureStatusDirectory(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "admission.db")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("admission database must be a private regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS review_attempts(id TEXT PRIMARY KEY, identity TEXT NOT NULL, receipt TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS review_flights(repository TEXT NOT NULL, pr INTEGER NOT NULL, attempt TEXT NOT NULL UNIQUE, PRIMARY KEY(repository,pr));
 CREATE TABLE IF NOT EXISTS review_recoveries(parent TEXT PRIMARY KEY, child TEXT NOT NULL UNIQUE, identity TEXT NOT NULL UNIQUE);
 CREATE TABLE IF NOT EXISTS review_recovery_roots(tuple TEXT PRIMARY KEY, parent TEXT NOT NULL UNIQUE, child TEXT NOT NULL UNIQUE);`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := backfillRecoveryRoots(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	id, err := newReviewAttempt()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Admission{db: db, dir: dir, processAbsent: absentProcess, settings: settings, from: from, check: check, receipt: AttemptReceipt{ID: id}}, nil
}
func (a *Admission) Close() error            { return a.db.Close() }
func (a *Admission) AttemptID() string       { return a.receipt.ID }
func (a *Admission) Receipt() AttemptReceipt { return a.receipt }

func identityFor(bundle []byte) (ReviewIdentity, error) {
	var b FrozenReviewBundle
	if err := json.Unmarshal(bundle, &b); err != nil {
		return ReviewIdentity{}, err
	}
	return ReviewIdentity{b.Repository, b.PullRequest, b.BaseRef, b.BaseSHA, b.HeadSHA, fmt.Sprintf("%x", sha256.Sum256(bundle))}, nil
}
func (a *Admission) load(ctx context.Context, id string) (AttemptReceipt, error) {
	var raw string
	err := a.db.QueryRowContext(ctx, "SELECT receipt FROM review_attempts WHERE id=?", id).Scan(&raw)
	if err != nil {
		return AttemptReceipt{}, fmt.Errorf("original attempt unavailable: %w", err)
	}
	var r AttemptReceipt
	err = json.Unmarshal([]byte(raw), &r)
	return r, err
}
func recoverable(r AttemptReceipt) bool {
	return r.Joined && r.Parent == "" && r.Verdict == VerdictError && r.FailureStage == "sampling" && r.FailureKind == CLIFailureTimeout && r.CompletedAt > 0 && r.Publication.CheckRunID > 0 && r.Publication.Conclusion == "failure"
}

func (a *Admission) Start(ctx context.Context, bundle []byte) error {
	identity, err := identityFor(bundle)
	if err != nil {
		return err
	}
	id := a.receipt.ID
	candidate := AttemptReceipt{ID: id, Identity: identity, Settings: a.settings, WrapperPID: os.Getpid(), LaunchStage: "admitted"}
	var prior AttemptReceipt
	if a.from != "" {
		if a.legacy != nil {
			prior = *a.legacy
		} else {
			prior, err = a.load(ctx, a.from)
			if err != nil {
				return err
			}
		}
		if !recoverable(prior) {
			return fmt.Errorf("recovery requires a joined sampling timeout with no valid verdict and a failed published Check")
		}
		if prior.Settings != a.settings || (a.settings.Mode != "required" && a.settings.Mode != "shadow") {
			return fmt.Errorf("recovery input or settings changed")
		}
		if a.prospective != nil {
			if err := a.verifyProspectiveBundle(bundle, identity); err != nil {
				return err
			}
			candidate.InputProvenance = "prospective-legacy-new-input"
			candidate.InputRecordedAt = time.Now().Unix()
			authority, err := json.Marshal(a.prospective)
			if err != nil {
				return err
			}
			candidate.AuthorizationSHA256 = receiptHash(authority)
			candidate.AuthorizationReference = a.prospective.Disclosure.Reference
			candidate.OwnerActor = a.prospective.Owner.Actor
			candidate.OwnerNative = a.prospective.Owner.Native
		} else if prior.Identity != identity {
			return fmt.Errorf("recovery input or settings changed")
		}
		if a.check == nil {
			return fmt.Errorf("current original-mode Check verification unavailable")
		}
		if err = a.check(ctx, prior); err != nil {
			return err
		}
		candidate.Parent = prior.ID
	}
	if a.from == "" && a.check != nil {
		if err = a.check(ctx, AttemptReceipt{Identity: identity, Settings: a.settings}); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	identityRaw, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	err = store.WithTxRetry(ctx, a.db, func(tx *sql.Tx) error {
		var active string
		err := tx.QueryRowContext(ctx, "SELECT attempt FROM review_flights WHERE repository=? AND pr=?", identity.Repository, identity.PR).Scan(&active)
		if err == nil {
			return fmt.Errorf("review invocation %s has not joined", active)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if candidate.Parent != "" {
			var verdicts int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM review_attempts WHERE
 json_extract(identity,'$.repository')=? AND json_extract(identity,'$.pr')=? AND
 json_extract(identity,'$.base_ref')=? AND json_extract(identity,'$.base_sha')=? AND json_extract(identity,'$.head_sha')=? AND
 json_extract(receipt,'$.verdict') IN ('approved','blocking')`, identity.Repository, identity.PR, identity.BaseRef, identity.BaseSHA, identity.HeadSHA).Scan(&verdicts); err != nil {
				return err
			}
			if verdicts != 0 {
				return fmt.Errorf("current tuple already has a valid managed verdict")
			}
			if a.legacy != nil {
				legacyRaw, err := json.Marshal(prior)
				if err != nil {
					return err
				}
				priorIdentity, err := json.Marshal(prior.Identity)
				if err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO review_attempts(id,identity,receipt) VALUES(?,?,?)", prior.ID, string(priorIdentity), string(legacyRaw)); err != nil {
					return err
				}
			}
			// Re-read custody in the same transaction; imported receipts cannot replace
			// an existing interrupted/valid attempt with a hand-written timeout.
			var parentRaw string
			if err := tx.QueryRowContext(ctx, "SELECT receipt FROM review_attempts WHERE id=?", prior.ID).Scan(&parentRaw); err != nil {
				return err
			}
			var stored AttemptReceipt
			if err := json.Unmarshal([]byte(parentRaw), &stored); err != nil {
				return err
			}
			if stored != prior {
				return fmt.Errorf("original custody changed")
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO review_recovery_roots(tuple,parent,child) VALUES(?,?,?)", tupleKey(prior.Identity), prior.ID, id); err != nil {
				return fmt.Errorf("original attempt/tuple one-use slot already reserved: %w", err)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO review_recoveries(parent,child,identity) VALUES(?,?,?)", prior.ID, id, string(identityRaw)); err != nil {
				return fmt.Errorf("one-use recovery already reserved: %w", err)
			}
		} else {
			var root string
			if err := tx.QueryRowContext(ctx, "SELECT parent FROM review_recovery_roots WHERE tuple=?", tupleKey(identity)).Scan(&root); err == nil {
				return fmt.Errorf("original tuple recovery slot already consumed; no ordinary retry or mode switch")
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM review_attempts WHERE identity=? AND json_extract(receipt, '$.settings.mode')=?", string(identityRaw), a.settings.Mode).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("this exact input was already sampled; diagnose its receipt, do not resample")
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO review_attempts(id,identity,receipt) VALUES(?,?,?)", id, string(identityRaw), string(raw)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO review_flights(repository,pr,attempt) VALUES(?,?,?)", identity.Repository, identity.PR, id)
		return err
	})
	if err == nil {
		a.receipt = candidate
	}
	return err
}

func (a *Admission) Finish(ctx context.Context, report ReviewReport) error {
	r := a.receipt
	if r.ID == "" {
		return fmt.Errorf("no admitted review to join")
	}
	r = receiptForReport(r, report)
	r.Joined = true
	r.CompletedAt = time.Now().Unix()
	if err := a.writeJoinJournal(r); err != nil {
		return err
	}
	return a.finishReceipt(ctx, r)
}

func (a *Admission) finishReceipt(ctx context.Context, r AttemptReceipt) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	err = store.WithTxRetry(ctx, a.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM review_flights WHERE repository=? AND pr=? AND attempt=?", r.Identity.Repository, r.Identity.PR, r.ID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("review join fence rejected")
		}
		_, err = tx.ExecContext(ctx, "UPDATE review_attempts SET receipt=? WHERE id=?", string(raw), r.ID)
		return err
	})
	if err == nil {
		a.receipt = r
	}
	return err
}

func receiptForReport(r AttemptReceipt, report ReviewReport) AttemptReceipt {
	r.Verdict = report.Result.Verdict
	r.FailureStage = report.FailureStage
	r.FailureKind = report.Audit.FailureKind
	r.Publication = report.Publication
	r.Usage = report.Audit.Usage
	r.UsageKnown = report.Audit.Usage.TotalTokens > 0
	r.CostUSD = report.Audit.CostUSD
	r.CostKnown = report.Audit.CostUSD > 0
	r.RequestID = report.Audit.RequestID
	r.SessionID = report.Audit.SessionID
	r.ResolvedModel = report.Audit.ResolvedModel
	r.DurationMS = report.Audit.Duration.Milliseconds()

	return r
}

// Checkpoint records the synchronously joined sampling outcome BEFORE publishing.
// The publication response is separately checkpointed; the exact attempt-bound
// remote Check can reconcile a crash between remote commit and response storage.
func (a *Admission) Checkpoint(ctx context.Context, report ReviewReport) error {
	r := receiptForReport(a.receipt, report)
	if !a.receipt.SamplingCompleted {
		r.SamplingFailureStage = report.FailureStage
	}
	r.SamplingCompleted = true
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
		return fmt.Errorf("sampling checkpoint custody rejected")
	}
	a.receipt = r
	return nil
}
func (a *Admission) SetPublicationLookup(lookup func(context.Context, AttemptReceipt) (Publication, error)) {
	a.publicationLookup = lookup
}
