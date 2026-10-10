package grokreview

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
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
					r.JoinReason = a.joinReason
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
			if reflect.DeepEqual(old, r) {
				return nil
			}
			if !lookupApplied || !reflect.DeepEqual(old, beforeReadback) {
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
	if a.humanRestart != nil {
		if err := a.validateCompletionCustody(ctx, a.humanRestart.Grant.Custody, a.receipt.Identity); err != nil {
			return err
		}
	}
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

// CompletionCustody is an explicit original-owner binding, not sampling authority.
// The CLI independently reads its actual local ledger; no arbitrary ledger path
// or imported receipt can replace the original canonical attempt.
type CompletionCustody struct {
	SchemaVersion    string           `json:"schema_version"`
	Owner            ReviewOwner      `json:"owner"`
	LedgerRepo       string           `json:"ledger_repo"`
	Item             string           `json:"item"`
	Reservation      string           `json:"reservation"`
	Generation       int64            `json:"generation"`
	ClaimGeneration  int64            `json:"claim_generation"`
	DecisionRevision int64            `json:"decision_revision"`
	Disclosure       ReviewDisclosure `json:"disclosure"`
}

type completionSeal struct {
	SchemaVersion string             `json:"schema_version"`
	Original      AttemptReceipt     `json:"original"`
	Bundle        []byte             `json:"bundle"`
	Result        FindingsResult     `json:"result"`
	Audit         CLIAudit           `json:"audit"`
	Custody       *CompletionCustody `json:"custody,omitempty"`
}

// LegacyCompletionEvidence authenticates retained bytes against hashes already
// written by the original wrapper. It never imports or rewrites an attempt.
type LegacyCompletionEvidence struct {
	SchemaVersion string                     `json:"schema_version"`
	Attempt       string                     `json:"attempt"`
	BundlePath    string                     `json:"bundle_path"`
	EnvelopePath  string                     `json:"envelope_path"`
	NativeJoin    NativeJoinProof            `json:"native_join"`
	WrapperReport *WrapperCompletionEvidence `json:"wrapper_report,omitempty"`
}

type CompletionResult struct {
	Attempt             string         `json:"attempt"`
	Sampled             bool           `json:"sampled"`
	Publication         Publication    `json:"publication"`
	Verdict             Verdict        `json:"verdict"`
	SealSHA256          string         `json:"seal_sha256"`
	OriginalInput       ReviewIdentity `json:"original_input"`
	CurrentInputMatches bool           `json:"current_input_matches"`
}

func (a *Admission) BindCompletionCustody(c CompletionCustody, guard func(context.Context, CompletionCustody) error) {
	a.completionCustody = &c
	a.completionGuard = guard
}

func (a *Admission) validateCompletionCustody(ctx context.Context, c CompletionCustody, id ReviewIdentity) error {
	d := c.Disclosure
	want := ReviewDisclosure{SchemaVersion: ReviewDisclosureSchema, Owner: c.Owner, Identity: id, Mode: a.settings.Mode, Operation: "managed_review", Provider: "grok", Content: "source_diff_and_review_contract"}
	// A standing tuple grant may omit the bundle hash. The seal still binds the
	// complete original bytes and the complete granted disclosure independently.
	if d.Identity.BundleSHA256 == "" {
		want.Identity.BundleSHA256 = ""
	}
	if c.SchemaVersion != "squad.review-completion.custody.v1" || c.Owner.Actor == "" || c.Owner.Native == "" || c.LedgerRepo == "" || c.Item == "" || c.Reservation == "" || c.Generation <= 0 || c.ClaimGeneration <= 0 || c.DecisionRevision <= 0 || d.Disposition != "granted" || !disclosureMatches(d, want) || a.completionGuard == nil {
		return fmt.Errorf("authenticated original native/claim/reservation/disclosure custody required")
	}
	return a.completionGuard(ctx, c)
}

// VerifyCompletionCustody reads the actual default work ledger without opening,
// migrating or writing it. Review admission and work ownership stay separate.
func VerifyCompletionCustody(ctx context.Context, c CompletionCustody, owner ReviewOwner) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return verifyCompletionCustodyAt(ctx, filepath.Join(home, ".squad", "global.db"), c, owner)
}
func verifyCompletionCustodyAt(ctx context.Context, path string, c CompletionCustody, owner ReviewOwner) error {
	if c.Owner != owner || owner.Actor == "" || owner.Native == "" {
		return fmt.Errorf("original native owner mismatch")
	}
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return err
	}
	defer db.Close()
	var count int
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM claims c JOIN dispatch_reservations r
 ON r.repo_id=c.repo_id AND r.canonical_item_id=c.item_id JOIN dispatch_decisions d
 ON d.repo_id=r.repo_id AND d.reservation_key=r.item_id AND d.item_id=c.item_id AND d.generation=r.generation
 WHERE c.repo_id=? AND c.item_id=? AND c.agent_id=? AND c.generation=? AND c.state='held'
 AND r.item_id=? AND r.generation=? AND r.worker_thread_id=? AND r.state='dispatched'
 AND c.claimed_at>=r.reserved_at AND d.revision=? AND d.action='proceed' AND d.worker_agent=?`,
		c.LedgerRepo, c.Item, owner.Actor, c.ClaimGeneration, c.Reservation, c.Generation, owner.Native, c.DecisionRevision, owner.Actor).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("current native/claim/reservation/decision fence rejected")
	}
	return nil
}

func (a *Admission) compareReceiptTx(ctx context.Context, tx *sql.Tx, want AttemptReceipt) error {
	var raw string
	if err := tx.QueryRowContext(ctx, "SELECT receipt FROM review_attempts WHERE id=?", want.ID).Scan(&raw); err != nil {
		return err
	}
	var got AttemptReceipt
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		return err
	}
	if !completionValuesEqual(got, want) {
		return fmt.Errorf("original attempt custody changed")
	}
	return nil
}

func (a *Admission) sealReportTx(ctx context.Context, tx *sql.Tx, r AttemptReceipt, report ReviewReport) error {
	if len(a.frozenBundle) == 0 {
		return fmt.Errorf("original complete input unavailable for seal")
	}
	var frozen FrozenReviewBundle
	if err := decodeStrictJSON(a.frozenBundle, &frozen); err != nil {
		return err
	}
	if frozen.Title != report.Snapshot.Title || frozen.Description != report.Snapshot.Description || frozen.Diff != report.Snapshot.Diff || frozen.Repository != report.Snapshot.Repository || frozen.PullRequest != report.Snapshot.Number || frozen.BaseRef != report.Snapshot.BaseRef || frozen.BaseSHA != report.Snapshot.BaseSHA || frozen.HeadSHA != report.Snapshot.HeadSHA {
		return fmt.Errorf("seal full input changed")
	}
	seal := completionSeal{"squad.review-completion.seal.v1", r, append(json.RawMessage(nil), a.frozenBundle...), report.Result, report.Audit, a.completionCustody}
	raw, err := json.Marshal(seal)
	if err != nil {
		return err
	}
	if len(raw) > 32<<20 {
		return fmt.Errorf("completion seal exceeds bound")
	}
	var priorRaw, digest string
	err = tx.QueryRowContext(ctx, "SELECT payload,digest FROM review_completion_seals WHERE attempt=?", r.ID).Scan(&priorRaw, &digest)
	if err == sql.ErrNoRows {
		_, err = tx.ExecContext(ctx, "INSERT INTO review_completion_seals(attempt,digest,payload,state) VALUES(?,?,?,'sealed')", r.ID, receiptHash(raw), string(raw))
		return err
	}
	if err != nil {
		return err
	}
	var prior completionSeal
	if receiptHash([]byte(priorRaw)) != digest || decodeStrictJSON([]byte(priorRaw), &prior) != nil || !bytes.Equal(prior.Bundle, seal.Bundle) || !reflect.DeepEqual(prior.Result, seal.Result) || !completionValuesEqual(prior.Audit, seal.Audit) || !reflect.DeepEqual(prior.Custody, seal.Custody) {
		return fmt.Errorf("immutable completion seal changed")
	}
	if report.Publication.CheckRunID > 0 {
		p, err := json.Marshal(report.Publication)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE review_completion_seals SET state='published',publication=? WHERE attempt=?", string(p), r.ID)
		return err
	}
	return nil
}

// PublicationStarting precedes the remote write. An uncertain write can only
// use exact-attempt readback, never an absence-based retry or second comment.
func (a *Admission) PublicationStarting(ctx context.Context) error {
	if a.completionCustody != nil {
		if err := a.validateCompletionCustody(ctx, *a.completionCustody, a.receipt.Identity); err != nil {
			return err
		}
	}
	return store.WithTxRetry(ctx, a.db, func(tx *sql.Tx) error {
		if err := a.compareReceiptTx(ctx, tx, a.receipt); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE review_completion_seals SET state='publishing',publisher_pid=? WHERE attempt=? AND state='sealed'", os.Getpid(), a.receipt.ID)
		return err
	})
}

func readCompletionBytes(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("original artifact path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("original artifact must be bounded private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return nil, fmt.Errorf("original artifact changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || int64(len(raw)) > limit || after.Size() != actual.Size() || !after.ModTime().Equal(actual.ModTime()) {
		return nil, fmt.Errorf("original artifact unstable or exceeds bound")
	}
	return raw, nil
}

func validCompletedAudit(r AttemptReceipt, audit CLIAudit) bool {
	t := audit.Terminal
	return t != nil && t.ChildStarted && t.ChildWaited && t.JoinedAt != nil && t.ExitCode != nil && *t.ExitCode == 0 && t.DeadlineProducer == "" && !t.OutputTruncated && t.InputSHA256 == r.Identity.BundleSHA256 && t.StdoutBytes > 0 && contentHashValid(t.StdoutSHA256) && audit.RequestID != "" && audit.SessionID != "" && audit.ResolvedModel != "" && audit.FailureKind == "" && audit.RequestedModel == r.Settings.Model && audit.ReasoningEffort == r.Settings.Effort && audit.AttemptID == r.ID && audit.BundleSHA256 == r.Identity.BundleSHA256
}

func (a *Admission) loadCompletion(ctx context.Context, id string) (completionSeal, string, string, int, Publication, error) {
	var seal completionSeal
	var raw, digest, state, pubRaw string
	var pid int
	err := a.db.QueryRowContext(ctx, "SELECT payload,digest,state,publisher_pid,publication FROM review_completion_seals WHERE attempt=?", id).Scan(&raw, &digest, &state, &pid, &pubRaw)
	if err != nil {
		return seal, "", "", 0, Publication{}, fmt.Errorf("authenticated original completion seal unavailable: %w", err)
	}
	if len(raw) > 32<<20 || receiptHash([]byte(raw)) != digest || decodeStrictJSON([]byte(raw), &seal) != nil || seal.SchemaVersion != "squad.review-completion.seal.v1" || seal.Original.ID != id || ValidateModelFindings(seal.Result) != nil || !validCompletedAudit(seal.Original, seal.Audit) {
		return seal, "", "", 0, Publication{}, fmt.Errorf("completion result/input/CLI provenance unqualified")
	}
	var pub Publication
	if err := json.Unmarshal([]byte(pubRaw), &pub); err != nil {
		return seal, "", "", 0, pub, err
	}
	return seal, digest, state, pid, pub, nil
}

// Complete publishes only a previously sealed execution. It never resolves or
// calls a model and never allocates a new attempt or recovery root.
func (a *Admission) Complete(ctx context.Context, id string, c CompletionCustody, github PullRequestGateway, token, checkName, coreHash, policyHash string) (CompletionResult, error) {
	result := CompletionResult{Attempt: id}
	if !validAttemptID(id) || github == nil || a.publicationLookup == nil {
		return result, fmt.Errorf("same-execution completion dependencies missing")
	}
	original, err := a.load(ctx, id)
	if err != nil {
		return result, err
	}
	if !original.Joined || original.CompletedAt <= 0 || original.Settings != a.settings || (original.Verdict != VerdictApproved && original.Verdict != VerdictBlocking) || original.FailureKind != "" || (original.FailureStage != "" && original.FailureStage != "identity" && original.FailureStage != "publishing") {
		return result, fmt.Errorf("original is not a joined valid completed execution")
	}
	if err := a.processAbsent(original.WrapperPID); err != nil {
		return result, err
	}
	if err := a.processAbsent(original.ReviewerPID); err != nil {
		return result, err
	}
	var journal joinJournal
	joinRaw, err := readCompletionBytes(filepath.Join(a.dir, "joins", id+".json"), 64<<10)
	if err != nil {
		return result, fmt.Errorf("original durable join proof unavailable: %w", err)
	}
	if decodeStrictJSON(joinRaw, &journal) != nil || journal.SchemaVersion != "squad.review-terminal-join.v1" || journal.WrapperPID != original.WrapperPID {
		return result, fmt.Errorf("original join provenance changed")
	}
	// Exact-attempt Check reconciliation can append a publication, never alter
	// the original sampling/result/native/usage evidence recorded at join.
	journal.Receipt.Publication = original.Publication
	if !completionValuesEqual(journal.Receipt, original) {
		return result, fmt.Errorf("original joined receipt changed")
	}
	seal, digest, state, pid, priorPub, err := a.loadCompletion(ctx, id)
	if err != nil {
		return result, err
	}
	if seal.Original.Joined {
		if !completionValuesEqual(seal.Original, original) {
			return result, fmt.Errorf("legacy original receipt changed after authentication")
		}
	} else {
		comparable := seal.Original
		comparable.Joined = original.Joined
		comparable.CompletedAt = original.CompletedAt
		comparable.FailureStage = original.FailureStage
		comparable.Publication = original.Publication
		if !completionValuesEqual(comparable, original) {
			return result, fmt.Errorf("original sealed sampling custody changed")
		}
	}
	if seal.Custody == nil {
		return result, fmt.Errorf("original owner/disclosure binding unavailable; no result import or force publication")
	}
	priorCustody := *seal.Custody
	currentCustody := c
	priorCustody.DecisionRevision = 0
	currentCustody.DecisionRevision = 0
	if !reflect.DeepEqual(priorCustody, currentCustody) {
		return result, fmt.Errorf("original completion custody/grant changed")
	}
	if err := a.validateCompletionCustody(ctx, c, original.Identity); err != nil {
		return result, err
	}
	var frozen FrozenReviewBundle
	if err := decodeStrictJSON(seal.Bundle, &frozen); err != nil {
		return result, err
	}
	identity, err := identityFor(seal.Bundle)
	if err != nil || identity != original.Identity || seal.Original.Identity != original.Identity || seal.Original.Settings != original.Settings || frozen.CoreHash != coreHash || frozen.PolicyHash != policyHash || seal.Result.Verdict != original.Verdict || !reflect.DeepEqual(seal.Audit.Terminal, original.Terminal) || seal.Audit.Usage != original.Usage || seal.Audit.CostUSD != original.CostUSD || seal.Audit.RequestID != original.RequestID || seal.Audit.SessionID != original.SessionID || seal.Audit.ResolvedModel != original.ResolvedModel || seal.Audit.Duration.Milliseconds() != original.DurationMS || seal.Original.Parent != original.Parent || seal.Original.WrapperPID != original.WrapperPID || seal.Original.ReviewerPID != original.ReviewerPID {
		return result, fmt.Errorf("original input/result/settings/usage custody changed")
	}
	expectedName := "grok-review"
	if original.Settings.Mode == "shadow" {
		expectedName = "grok-review-shadow"
	}
	if checkName != expectedName {
		return result, fmt.Errorf("original mode/App/Check mismatch")
	}
	if state == "reserved" {
		if pid <= 0 {
			return result, fmt.Errorf("reserved publication provenance conflicts")
		}
		if err := a.processAbsent(pid); err != nil {
			return result, err
		}
		err := store.WithTxRetry(ctx, a.db, func(tx *sql.Tx) error {
			if err := a.compareReceiptTx(ctx, tx, original); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, "UPDATE review_completion_seals SET state='sealed',publisher_pid=0 WHERE attempt=? AND digest=? AND state='reserved' AND publisher_pid=?", id, digest, pid)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("reserved original completion CAS rejected")
			}
			res, err = tx.ExecContext(ctx, "DELETE FROM review_flights WHERE repository=? AND pr=? AND attempt=?", identity.Repository, identity.PR, id)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("reserved completion flight mismatch")
			}
			return nil
		})
		if err != nil {
			return result, err
		}
		state = "sealed"
	}
	lookupReceipt := original
	lookupReceipt.SamplingCompleted = true
	pub, err := a.publicationLookup(ctx, lookupReceipt)
	if err != nil {
		return result, err
	}
	if state != "sealed" {
		if state != "published" && state != "publishing" {
			return result, fmt.Errorf("unknown completion publication state")
		}
		if state == "publishing" && pid > 0 {
			if err := a.processAbsent(pid); err != nil {
				return result, err
			}
		}
		if pub.CheckRunID <= 0 || (priorPub.CheckRunID > 0 && priorPub.CheckRunID != pub.CheckRunID) {
			return result, fmt.Errorf("original publication uncertain; exact-attempt readback required, no write retry")
		}
		if err := a.validateCompletionCustody(ctx, c, original.Identity); err != nil {
			return result, err
		}
		if state == "published" {
			// Already-published replay is read-only and cannot release a foreign
			// operation. Revalidate the exact original receipt/seal under CAS.
			err = store.WithTxRetry(ctx, a.db, func(tx *sql.Tx) error {
				if err := a.compareReceiptTx(ctx, tx, original); err != nil {
					return err
				}
				var count int
				if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM review_completion_seals WHERE attempt=? AND digest=? AND state='published' AND json_extract(publication,'$.CheckRunID')=?", id, digest, pub.CheckRunID).Scan(&count); err != nil {
					return err
				}
				if count != 1 {
					return fmt.Errorf("original published seal changed")
				}
				return nil
			})
			if err != nil {
				return result, err
			}
			pub = priorPub
		} else if err := a.finishCompletion(ctx, original, digest, pub); err != nil {
			return result, err
		}
		result = CompletionResult{Attempt: id, Publication: pub, Verdict: seal.Result.Verdict, SealSHA256: digest, OriginalInput: identity}
		// Joining an actual old-head publication is bookkeeping, not approval
		// for a changed current input. Do not let drift strand the old flight.
		current, err := github.FetchPullRequest(ctx, identity.Repository, identity.PR, token)
		if err != nil {
			return result, fmt.Errorf("original publication joined; current input unavailable: %w", err)
		}
		if !completionSnapshotMatches(current, frozen) {
			return result, fmt.Errorf("original publication joined; current input is stale, no current-input approval")
		}
		if err := a.validateCompletionCustody(ctx, c, original.Identity); err != nil {
			return result, err
		}
		result.CurrentInputMatches = true
		return result, nil
	}
	current, err := github.FetchPullRequest(ctx, identity.Repository, identity.PR, token)
	if err != nil {
		return result, err
	}
	if !completionSnapshotMatches(current, frozen) {
		return result, fmt.Errorf("live complete original input is stale; publication held")
	}
	if pub.CheckRunID > 0 {
		return result, fmt.Errorf("unexpected publication conflicts with sealed pre-write state")
	}
	if a.check == nil {
		return result, fmt.Errorf("current exact managed Check gate unavailable")
	}
	checkReceipt := AttemptReceipt{Identity: original.Identity, Settings: original.Settings}
	var parentReceipt *AttemptReceipt
	if original.Parent != "" {
		if !validAttemptID(original.Parent) {
			return result, fmt.Errorf("original parent identity invalid")
		}
		parent, err := a.load(ctx, original.Parent)
		if err != nil {
			return result, err
		}
		if !recoverable(parent) || parent.Settings != original.Settings || parent.Identity.Repository != identity.Repository || parent.Identity.PR != identity.PR || parent.Identity.BaseRef != identity.BaseRef || parent.Identity.BaseSHA != identity.BaseSHA || parent.Identity.HeadSHA != identity.HeadSHA {
			return result, fmt.Errorf("original failed Check/parent custody changed")
		}
		parentReceipt = &parent
		checkReceipt = parent
	}
	if err := a.check(ctx, checkReceipt); err != nil {
		return result, err
	}
	if err := a.validateCompletionCustody(ctx, c, original.Identity); err != nil {
		return result, err
	}
	err = store.WithTxRetry(ctx, a.db, func(tx *sql.Tx) error {
		if err := a.compareReceiptTx(ctx, tx, original); err != nil {
			return err
		}
		if parentReceipt != nil {
			if err := a.compareReceiptTx(ctx, tx, *parentReceipt); err != nil {
				return err
			}
		}
		var conflicting int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM review_attempts WHERE id<>? AND json_extract(identity,'$.repository')=? AND json_extract(identity,'$.pr')=? AND json_extract(identity,'$.base_ref')=? AND json_extract(identity,'$.base_sha')=? AND json_extract(identity,'$.head_sha')=? AND json_extract(receipt,'$.verdict') IN ('approved','blocking') AND (json_extract(receipt,'$.settings.mode')=? OR ?<>'')`, id, identity.Repository, identity.PR, identity.BaseRef, identity.BaseSHA, identity.HeadSHA, original.Settings.Mode, original.Parent).Scan(&conflicting); err != nil {
			return err
		}
		if conflicting != 0 {
			return fmt.Errorf("another current-tuple managed verdict exists")
		}
		var parent, child string
		if err := tx.QueryRowContext(ctx, "SELECT parent,child FROM review_recovery_roots WHERE tuple=?", tupleKey(identity)).Scan(&parent, &child); err == nil {
			if original.Parent == "" || parent != original.Parent || child != id {
				return fmt.Errorf("obsolete/exhausted original recovery root")
			}
		} else if err != sql.ErrNoRows {
			return err
		} else if original.Parent != "" {
			return fmt.Errorf("original consumed recovery root unavailable")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO review_flights(repository,pr,attempt) VALUES(?,?,?)", identity.Repository, identity.PR, id); err != nil {
			return fmt.Errorf("completion singleflight fence rejected: %w", err)
		}
		res, err := tx.ExecContext(ctx, "UPDATE review_completion_seals SET state='reserved',publisher_pid=? WHERE attempt=? AND digest=? AND state='sealed'", os.Getpid(), id, digest)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("completion current-attempt CAS rejected")
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	// Recheck ownership and full input after reserving, immediately before the write.
	if err := a.validateCompletionCustody(ctx, c, original.Identity); err != nil {
		return result, err
	}
	current, err = github.FetchPullRequest(ctx, identity.Repository, identity.PR, token)
	if err != nil {
		return result, err
	}
	if current.Title != frozen.Title || current.Description != frozen.Description || current.Diff != frozen.Diff || !samePullRequestTuple(current, PullRequestSnapshot{Repository: frozen.Repository, Number: frozen.PullRequest, BaseRef: frozen.BaseRef, BaseSHA: frozen.BaseSHA, HeadSHA: frozen.HeadSHA}) {
		return result, fmt.Errorf("input changed at publication boundary; held without retry")
	}
	if err := a.validateCompletionCustody(ctx, c, original.Identity); err != nil {
		return result, err
	}
	err = store.WithTxRetry(ctx, a.db, func(tx *sql.Tx) error {
		if err := a.compareReceiptTx(ctx, tx, original); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE review_completion_seals SET state='publishing' WHERE attempt=? AND digest=? AND state='reserved' AND publisher_pid=? AND EXISTS(SELECT 1 FROM review_flights WHERE attempt=?)", id, digest, os.Getpid(), id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("same-execution publication intent CAS rejected")
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	pub, err = github.PublishReview(ctx, token, checkName, current, seal.Result, seal.Audit)
	if err != nil || pub.CheckRunID <= 0 {
		return result, fmt.Errorf("same-execution publication uncertain; exact-attempt lookup only: %w", errors.Join(err, fmt.Errorf("publication response required")))
	}
	if err := a.finishCompletion(ctx, original, digest, pub); err != nil {
		return result, err
	}
	return CompletionResult{Attempt: id, Publication: pub, Verdict: seal.Result.Verdict, SealSHA256: digest, OriginalInput: identity, CurrentInputMatches: true}, nil
}

func (a *Admission) finishCompletion(ctx context.Context, original AttemptReceipt, digest string, pub Publication) error {
	return store.WithTxRetry(ctx, a.db, func(tx *sql.Tx) error {
		if err := a.compareReceiptTx(ctx, tx, original); err != nil {
			return err
		}
		var foreign int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM review_flights WHERE repository=? AND pr=? AND attempt<>?", original.Identity.Repository, original.Identity.PR, original.ID).Scan(&foreign); err != nil {
			return err
		}
		if foreign != 0 {
			return fmt.Errorf("current foreign publication flight must remain held")
		}
		raw, err := json.Marshal(pub)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE review_completion_seals SET state='published',publication=? WHERE attempt=? AND digest=? AND state IN ('publishing','published')", string(raw), original.ID, digest)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("completion finish CAS rejected")
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM review_flights WHERE repository=? AND pr=? AND attempt=?", original.Identity.Repository, original.Identity.PR, original.ID)
		return err
	})
}

// AuthenticateLegacyCompletion can append a seal only for an existing genuine
// joined identity-stale result. The original stale receipt is never rewritten.
// A reconstructed CLI envelope or prose summary cannot pass its original hash.
func (a *Admission) AuthenticateLegacyCompletion(ctx context.Context, id string, c CompletionCustody, path string) error {
	var evidence LegacyCompletionEvidence
	raw, err := readCompletionBytes(path, 64<<10)
	if err != nil {
		return err
	}
	if err := decodeStrictJSON(raw, &evidence); err != nil {
		return err
	}
	if evidence.SchemaVersion != "squad.review-completion.original.v1" || evidence.Attempt != id {
		return fmt.Errorf("original completion evidence mismatched")
	}
	r, err := a.load(ctx, id)
	if err != nil {
		return err
	}
	if !r.Joined || r.CompletedAt <= 0 || r.SamplingCompleted || r.FailureStage != "identity" || r.Publication.CheckRunID != 0 || r.Settings != a.settings || (r.Verdict != VerdictApproved && r.Verdict != VerdictBlocking) || r.FailureKind != "" {
		return fmt.Errorf("legacy original is not an untouched joined identity-stale valid execution")
	}
	if err := a.validateCompletionCustody(ctx, c, r.Identity); err != nil {
		return err
	}
	if (r.OwnerActor != "" && r.OwnerActor != c.Owner.Actor) || (r.OwnerNative != "" && r.OwnerNative != c.Owner.Native) {
		return fmt.Errorf("original stored owner changed")
	}
	if _, _, _, _, _, err := a.loadCompletion(ctx, id); err == nil {
		return nil
	}
	if err := a.processAbsent(r.WrapperPID); err != nil {
		return err
	}
	if err := a.processAbsent(r.ReviewerPID); err != nil {
		return err
	}
	bundle, err := readCompletionBytes(evidence.BundlePath, 16<<20)
	if err != nil {
		return fmt.Errorf("original complete input unavailable: %w", err)
	}
	identity, err := identityFor(bundle)
	if err != nil || identity != r.Identity {
		return fmt.Errorf("original full input bytes/hash changed")
	}
	var frozen FrozenReviewBundle
	if err := decodeStrictJSON(bundle, &frozen); err != nil {
		return err
	}
	if frozen.SchemaVersion != FrozenReviewSchemaVersion {
		return fmt.Errorf("original input schema unsupported")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".codex", "sessions")
	var result FindingsResult
	var audit CLIAudit
	if evidence.WrapperReport != nil {
		if evidence.EnvelopePath != "" {
			return fmt.Errorf("ambiguous original result provenance")
		}
		proof := evidence.WrapperReport
		proof.report, err = readCompletionBytes(proof.ReportPath, 1<<20)
		if err != nil {
			return fmt.Errorf("original private wrapper report unavailable: %w", err)
		}
		proof.verifyBinary = a.completionWrapperVerifier
		if err := verifyCompletionNativeAt(root, evidence.NativeJoin, r, c.Owner, proof); err != nil {
			return err
		}
		result, audit, err = parseOriginalWrapperReport(proof.report, r)
		if err != nil {
			return err
		}
	} else {
		envelope, err := readCompletionBytes(evidence.EnvelopePath, int64(r.Settings.MaxReviewerOutput))
		if err != nil {
			return fmt.Errorf("original raw validated CLI envelope unavailable: %w", err)
		}
		if r.Terminal == nil || int64(len(envelope)) != r.Terminal.StdoutBytes || receiptHash(envelope) != r.Terminal.StdoutSHA256 {
			return fmt.Errorf("original CLI envelope bytes/hash changed; no reconstruction")
		}
		result, audit, err = ParseCLIEnvelope(envelope)
		if err != nil {
			return fmt.Errorf("original CLI envelope invalid")
		}
		audit.Terminal = r.Terminal
		audit.AttemptID = r.ID
		audit.BundleSHA256 = r.Identity.BundleSHA256
		audit.RequestedModel = r.Settings.Model
		audit.ReasoningEffort = r.Settings.Effort
		audit.Duration = time.Duration(r.DurationMS) * time.Millisecond
		if err := verifyCompletionNativeAt(root, evidence.NativeJoin, r, c.Owner); err != nil {
			return err
		}
	}
	if !validCompletedAudit(r, audit) || result.Verdict != r.Verdict || audit.Usage != r.Usage || audit.CostUSD != r.CostUSD || audit.RequestID != r.RequestID || audit.SessionID != r.SessionID || audit.ResolvedModel != r.ResolvedModel {
		return fmt.Errorf("original result/session/usage attribution changed")
	}
	seal := completionSeal{"squad.review-completion.seal.v1", r, append(json.RawMessage(nil), bundle...), result, audit, &c}
	payload, err := json.Marshal(seal)
	if err != nil {
		return err
	}
	if len(payload) > 32<<20 {
		return fmt.Errorf("completion seal exceeds bound")
	}
	if err := a.validateCompletionCustody(ctx, c, r.Identity); err != nil {
		return err
	}
	return store.WithTxRetry(ctx, a.db, func(tx *sql.Tx) error {
		if err := a.compareReceiptTx(ctx, tx, r); err != nil {
			return err
		}
		var flight int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM review_flights WHERE repository=? AND pr=?", r.Identity.Repository, r.Identity.PR).Scan(&flight); err != nil {
			return err
		}
		if flight != 0 {
			return fmt.Errorf("original completion has unresolved flight")
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO review_completion_seals(attempt,digest,payload,state) VALUES(?,?,?,'sealed')", id, receiptHash(payload), string(payload))
		return err
	})
}

// Qualify only the retained Codex launch/write_stdin/wait chain and the narrow
// historical capture wrapper. Caller-supplied artifacts do not assert a join.
func verifyCompletionNativeAt(root string, proof NativeJoinProof, r AttemptReceipt, owner ReviewOwner, reports ...*WrapperCompletionEvidence) error {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	path, err := filepath.EvalSymlinks(proof.RolloutPath)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || proof.NativeSession != owner.Native {
		return fmt.Errorf("original host native provenance unavailable")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 128<<20 {
		return fmt.Errorf("original host proof exceeds bound")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	wanted := map[string]bool{proof.LaunchCall: true, proof.JoinCall: true, proof.TerminalCall: true}
	var report *WrapperCompletionEvidence
	if len(reports) == 1 {
		report = reports[0]
		wanted[report.CaptureCall] = true
	}
	if len(reports) > 1 {
		return fmt.Errorf("ambiguous original wrapper provenance")
	}
	positions := map[string]int{}
	position := 0
	if len(wanted) < 2 {
		return fmt.Errorf("original host tool chain missing")
	}
	calls := map[string]nativeRecord{}
	outputs := map[string]nativeRecord{}
	hashes := map[string]string{}
	first := true
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		position++
		if !first {
			found := false
			for id := range wanted {
				if bytes.Contains(line, []byte(id)) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		row, err := completionNativeRecord(line)
		if err != nil {
			return err
		}
		if first {
			first = false
			if row.Type != "session_meta" || row.Payload.SessionID != owner.Native {
				return fmt.Errorf("original host session mismatch")
			}
			continue
		}
		id := row.Payload.CallID
		if !wanted[id] {
			continue
		}
		switch row.Payload.Type {
		case "custom_tool_call", "function_call":
			if _, ok := calls[id]; ok {
				return fmt.Errorf("duplicate original native call")
			}
			calls[id] = row
			positions[id] = position
			hashes[id] = receiptHash(line)
		case "custom_tool_call_output", "function_call_output":
			if _, ok := outputs[id]; ok {
				return fmt.Errorf("duplicate original native output")
			}
			outputs[id] = row
			hashes[id+"/output"] = receiptHash(line)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for key, want := range map[string]string{proof.LaunchCall: proof.LaunchSHA256, proof.JoinCall: proof.JoinSHA256, proof.TerminalCall: proof.TerminalSHA256, proof.LaunchCall + "/output": proof.LaunchOutputSHA256, proof.JoinCall + "/output": proof.JoinOutputSHA256, proof.TerminalCall + "/output": proof.TerminalOutputSHA256} {
		if !contentHashValid(want) || hashes[key] != want {
			return fmt.Errorf("original native proof hash changed")
		}
	}
	launch := calls[proof.LaunchCall]
	if launch.Payload.Name != "exec" {
		return fmt.Errorf("original native launch unsupported")
	}
	matches := nativeCommandRE.FindAllStringSubmatch(launch.Payload.Input, -1)
	selected := -1
	selectedCommand := ""
	for i, m := range matches {
		var command string
		if err := json.Unmarshal([]byte(m[1]), &command); err != nil {
			return err
		}
		if !strings.Contains(command, "squad-grok-review") {
			continue
		}
		if selected != -1 {
			return fmt.Errorf("ambiguous original launch")
		}
		if err := verifyCompletionCapture(command, r, owner); err != nil {
			return err
		}
		selected = i
		selectedCommand = command
	}
	if selected < 0 {
		return fmt.Errorf("original managed launch unavailable")
	}
	var launches []int
	for _, b := range outputs[proof.LaunchCall].Payload.Output {
		var out struct {
			SessionID int `json:"session_id"`
		}
		if json.Unmarshal([]byte(b.Text), &out) == nil && out.SessionID > 0 {
			launches = append(launches, out.SessionID)
		}
	}
	if len(launches) != len(matches) || proof.ToolSessionID <= 0 || launches[selected] != proof.ToolSessionID {
		return fmt.Errorf("original native tool session mismatch")
	}
	join := calls[proof.JoinCall]
	m := nativeSessionRE.FindAllStringSubmatch(join.Payload.Input, -1)
	if join.Payload.Name != "exec" || len(m) != 1 || m[0][1] != strconv.Itoa(proof.ToolSessionID) {
		return fmt.Errorf("original native join not qualified")
	}
	if proof.TerminalCall != proof.JoinCall {
		cell := ""
		for _, b := range outputs[proof.JoinCall].Payload.Output {
			if m := nativeCellRE.FindStringSubmatch(b.Text); len(m) > 0 {
				cell = m[1]
			}
		}
		var wait struct {
			CellID string `json:"cell_id"`
		}
		call := calls[proof.TerminalCall]
		if call.Payload.Name != "wait" || json.Unmarshal([]byte(call.Payload.Arguments), &wait) != nil || cell == "" || wait.CellID != cell {
			return fmt.Errorf("original native terminal wait mismatch")
		}
	}
	joined := 0
	for _, b := range outputs[proof.TerminalCall].Payload.Output {
		var out struct {
			ExitCode  *int   `json:"exit_code"`
			SessionID int    `json:"session_id"`
			Output    string `json:"output"`
		}
		if json.Unmarshal([]byte(b.Text), &out) == nil && out.ExitCode != nil && *out.ExitCode == 1 && out.SessionID == 0 && strings.HasSuffix(strings.TrimSpace(out.Output), "pull request base or head changed during review; result was not published") {
			joined++
		}
	}
	if joined != 1 {
		return fmt.Errorf("original native identity-stale terminal outcome unavailable")
	}
	if positions[proof.LaunchCall] >= positions[proof.JoinCall] || positions[proof.JoinCall] > positions[proof.TerminalCall] {
		return fmt.Errorf("original native chain order changed")
	}
	if report != nil {
		if positions[report.CaptureCall] <= positions[proof.TerminalCall] || hashes[report.CaptureCall] != report.CaptureSHA256 || hashes[report.CaptureCall+"/output"] != report.CaptureOutputSHA256 {
			return fmt.Errorf("original wrapper capture provenance changed")
		}
		return verifyOriginalWrapperCapture(selectedCommand, calls[report.CaptureCall], outputs[report.CaptureCall], report)
	}
	return nil
}

var completionCaptureRE = regexp.MustCompile(`^E=([^\s;&|` + "`" + `$]+); export SQUAD_AGENT=([^\s;]+) SQUAD_SESSION_ID=codex:([^\s;]+); (/[^\s;&|` + "`" + `$]+/squad-grok-review) (.+) --status-dir "\$E/([^/"\s]+)" > "\$E/([^/"\s]+)" 2>&1; result=\$\?; tail -([0-9]+) "\$E/([^/"\s]+)"; exit \$result$`)

func verifyCompletionCapture(command string, r AttemptReceipt, owner ReviewOwner) error {
	m := completionCaptureRE.FindStringSubmatch(command)
	if len(m) != 10 || !filepath.IsAbs(m[1]) || m[2] != owner.Actor || m[3] != owner.Native || m[7] != m[9] {
		return fmt.Errorf("original owner/managed capture not qualified")
	}
	count, err := strconv.Atoi(m[8])
	if err != nil || count < 1 || count > 200 {
		return fmt.Errorf("original capture bound invalid")
	}
	args := strings.Fields(m[5])
	allowed := map[string]bool{"--repo": true, "--pr": true, "--mode": true, "--model": true, "--reasoning-effort": true, "--timeout": true, "--max-github-output": true, "--max-reviewer-output": true}
	for i := 0; i < len(args); i += 2 {
		if !allowed[args[i]] {
			return fmt.Errorf("original capture flags unsupported")
		}
	}
	return verifyLegacyArguments(args, r)
}

func (a *Admission) SetCompletionCheck(check RecoveryCheck) { a.check = check }

func completionNativeRecord(line []byte) (nativeRecord, error) {
	var row nativeRecord
	var outer struct {
		Type    string `json:"type"`
		Payload struct {
			Type      string          `json:"type"`
			Name      string          `json:"name"`
			SessionID string          `json:"session_id"`
			CallID    string          `json:"call_id"`
			Input     string          `json:"input"`
			Arguments string          `json:"arguments"`
			Output    json.RawMessage `json:"output"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &outer); err != nil {
		return row, err
	}
	row.Type = outer.Type
	row.Payload.Type = outer.Payload.Type
	row.Payload.Name = outer.Payload.Name
	row.Payload.SessionID = outer.Payload.SessionID
	row.Payload.CallID = outer.Payload.CallID
	row.Payload.Input = outer.Payload.Input
	row.Payload.Arguments = outer.Payload.Arguments
	if len(outer.Payload.Output) == 0 {
		return row, nil
	}
	var text string
	if json.Unmarshal(outer.Payload.Output, &text) == nil {
		row.Payload.Output = append(row.Payload.Output, struct {
			Text string `json:"text"`
		}{text})
		return row, nil
	}
	if err := json.Unmarshal(outer.Payload.Output, &row.Payload.Output); err != nil {
		return row, err
	}
	return row, nil
}

func completionValuesEqual(left, right any) bool {
	a, err := json.Marshal(left)
	if err != nil {
		return false
	}
	b, err := json.Marshal(right)
	return err == nil && bytes.Equal(a, b)
}

// WrapperCompletionEvidence names host-retained records. The report copy alone
// is never authority: its complete bytes must occur in the authenticated native
// capture of the original managed wrapper, whose historical binary is pinned.
type WrapperCompletionEvidence struct {
	ReportPath          string `json:"report_path"`
	CaptureCall         string `json:"capture_call"`
	CaptureSHA256       string `json:"capture_sha256"`
	CaptureOutputSHA256 string `json:"capture_output_sha256"`
	report              []byte
	verifyBinary        func(string) error
}

const originalWrapperIdentityError = "pull request base or head changed during review; result was not published"

func verifyOriginalWrapperBinary(path string) error {
	// This exact reviewed historical wrapper parses the complete CLI envelope and
	// validates model findings before emitting the structured commandOutput.
	const supported = "e1c56e0b18205a71e8e55cf08693d78f2de7d8e24316dce504f3736d7e82946a"
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 128<<20 {
		return fmt.Errorf("original wrapper binary unsupported")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("original wrapper binary changed")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (128<<20)+1))
	after, statErr := f.Stat()
	if err != nil || statErr != nil || n != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || hex.EncodeToString(h.Sum(nil)) != supported {
		return fmt.Errorf("original wrapper validation contract/hash unavailable")
	}
	return nil
}

func verifyOriginalWrapperCapture(launch string, call, output nativeRecord, proof *WrapperCompletionEvidence) error {
	m := completionCaptureRE.FindStringSubmatch(launch)
	if len(m) != 10 || call.Payload.Name != "exec" || !contentHashValid(proof.CaptureSHA256) || !contentHashValid(proof.CaptureOutputSHA256) {
		return fmt.Errorf("original wrapper capture unsupported")
	}
	verify := proof.verifyBinary
	if verify == nil {
		verify = verifyOriginalWrapperBinary
	}
	if err := verify(m[4]); err != nil {
		return err
	}
	cmds := nativeCommandRE.FindAllStringSubmatch(call.Payload.Input, -1)
	if len(cmds) == 0 {
		return fmt.Errorf("original wrapper capture command missing")
	}
	var command string
	if json.Unmarshal([]byte(cmds[0][1]), &command) != nil || !strings.HasPrefix(command, "E="+m[1]+"; cat \"$E/"+m[7]+"\";") {
		return fmt.Errorf("original wrapper capture path changed")
	}
	// The first exec result must retain the whole original log, including the
	// exact structured JSON and stale-identity trailer. Truncation fails equality.
	for _, block := range output.Payload.Output {
		var out struct {
			ExitCode  *int   `json:"exit_code"`
			SessionID int    `json:"session_id"`
			Output    string `json:"output"`
		}
		if json.Unmarshal([]byte(block.Text), &out) != nil || out.ExitCode == nil {
			continue
		}
		if *out.ExitCode != 0 || out.SessionID != 0 || !strings.HasPrefix(out.Output, string(proof.report)) {
			return fmt.Errorf("original complete wrapper report not retained in native capture")
		}
		return nil
	}
	return fmt.Errorf("original wrapper capture terminal output unavailable")
}

type originalWrapperReport struct {
	Terminal     *TerminalDiagnostics `json:"terminal_diagnostics"`
	Attempt      string               `json:"attempt_id"`
	Repository   string               `json:"repository"`
	PR           int                  `json:"pull_request"`
	Base         string               `json:"base_sha"`
	Head         string               `json:"head_sha"`
	Verdict      Verdict              `json:"verdict"`
	Summary      string               `json:"summary"`
	Findings     []Finding            `json:"findings"`
	Request      string               `json:"request_id"`
	Session      string               `json:"session_id"`
	Model        string               `json:"requested_model"`
	Effort       string               `json:"reasoning_effort"`
	Resolved     string               `json:"resolved_model"`
	Input        int64                `json:"input_tokens"`
	Output       int64                `json:"output_tokens"`
	Reasoning    int64                `json:"reasoning_tokens"`
	Total        int64                `json:"total_tokens"`
	Cost         float64              `json:"cost_usd"`
	Duration     int64                `json:"duration_ms"`
	FailureStage string               `json:"failure_stage"`
}

func parseOriginalWrapperReport(raw []byte, r AttemptReceipt) (FindingsResult, CLIAudit, error) {
	var report originalWrapperReport
	var result FindingsResult
	var audit CLIAudit
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return result, audit, fmt.Errorf("original wrapper report incomplete/invalid")
	}
	rest := raw[decoder.InputOffset():]
	if string(rest) != "\n"+originalWrapperIdentityError+"\n" {
		return result, audit, fmt.Errorf("original wrapper terminal trailer changed")
	}
	if report.Attempt != r.ID || report.Repository != r.Identity.Repository || report.PR != r.Identity.PR || report.Base != r.Identity.BaseSHA || report.Head != r.Identity.HeadSHA || report.Verdict != r.Verdict || report.FailureStage != "identity" || !completionValuesEqual(report.Terminal, r.Terminal) || report.Request != r.RequestID || report.Session != r.SessionID || report.Model != r.Settings.Model || report.Effort != r.Settings.Effort || report.Resolved != r.ResolvedModel || report.Input != r.Usage.InputTokens || report.Output != r.Usage.OutputTokens || report.Reasoning != r.Usage.ReasoningTokens || report.Total != r.Usage.TotalTokens || report.Cost != r.CostUSD || report.Duration != r.DurationMS || report.Findings == nil {
		return result, audit, fmt.Errorf("original wrapper attribution/result/accounting changed")
	}
	result = FindingsResult{SchemaVersion: FindingsSchemaVersion, Verdict: report.Verdict, Summary: report.Summary, Findings: report.Findings}
	if err := ValidateModelFindings(result); err != nil {
		return result, audit, err
	}
	// Cache counters were not emitted by this wrapper contract. Preserve the
	// untouched canonical accounting, authenticated by the original join journal.
	audit = CLIAudit{Terminal: report.Terminal, AttemptID: r.ID, BundleSHA256: r.Identity.BundleSHA256, RequestID: report.Request, SessionID: report.Session, RequestedModel: report.Model, ReasoningEffort: report.Effort, ResolvedModel: report.Resolved, Usage: r.Usage, CostUSD: report.Cost, Duration: time.Duration(report.Duration) * time.Millisecond}
	return result, audit, nil
}

func completionSnapshotMatches(current PullRequestSnapshot, frozen FrozenReviewBundle) bool {
	return current.Repository == frozen.Repository && current.Number == frozen.PullRequest && current.BaseRef == frozen.BaseRef && current.BaseSHA == frozen.BaseSHA && current.HeadSHA == frozen.HeadSHA && current.Title == frozen.Title && current.Description == frozen.Description && current.Diff == frozen.Diff
}

// JoinReasonSupersededInput records that the reviewed input changed after
// admission, so the unjoined attempt was abandoned without a verdict.
const JoinReasonSupersededInput = "superseded-input"

// SetJoinReason selects the audited reason stored on an interruption join. It
// is ignored when the attempt already has a terminal receipt.
func (a *Admission) SetJoinReason(reason string) error {
	if reason != "" && reason != JoinReasonSupersededInput {
		return fmt.Errorf("unsupported join reason %q", reason)
	}
	a.joinReason = reason
	return nil
}

// TerminalizeStatus closes the bound local status file of an attempt that
// Reconcile joined without a verdict, so monitors stop seeing it as sampling.
// It rewrites only a non-terminal file whose attempt, repository, PR, tuple and
// mode match the joined receipt; a missing file is not an error.
func (a *Admission) TerminalizeStatus(ctx context.Context, statusDir, id string) (bool, error) {
	r, err := a.load(ctx, id)
	if err != nil {
		return false, err
	}
	if !r.Joined || r.Verdict != VerdictError || r.FailureKind != CLIFailureCanceled {
		return false, nil
	}
	path := filepath.Join(statusDir, reviewStatusFilename(r.Identity.Repository, r.Identity.PR, r.Identity.HeadSHA, r.ID))
	if info, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	} else if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return false, fmt.Errorf("review status file must be a private regular file")
	}
	var status ReviewStatus
	if _, err = readBoundedJSON(path, &status); err != nil {
		return false, err
	}
	if status.SchemaVersion != ReviewStatusSchemaVersion || status.Attempt != r.ID || status.Repository != r.Identity.Repository || status.PullRequest != r.Identity.PR || status.BaseRef != r.Identity.BaseRef || status.BaseSHA != r.Identity.BaseSHA || status.HeadSHA != r.Identity.HeadSHA || status.Mode != r.Settings.Mode {
		return false, fmt.Errorf("review status does not match joined custody")
	}
	if status.CompletedAt != 0 || terminalReviewState(status.State) {
		return false, nil
	}
	now := time.Now()
	status.State = ReviewStateError
	status.Verdict = VerdictError
	status.FailureStage = "sampling"
	status.FailureKind = CLIFailureCanceled
	status.UpdatedAt = now.Unix()
	status.CompletedAt = r.CompletedAt
	if status.CompletedAt <= 0 {
		status.CompletedAt = now.Unix()
	}
	status.Summary = "Review attempt was joined without a verdict after its processes exited."
	if r.JoinReason != "" {
		status.Summary = "Review attempt was joined without a verdict (" + r.JoinReason + ")."
	}
	return true, writeStatusAtomically(path, status)
}
