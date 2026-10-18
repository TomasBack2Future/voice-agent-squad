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

// ControllerChildEvidence names the host-retained originals of one joined
// controller reconsideration whose seal was never written because admission
// dropped the complete input. Every value is authenticated against the
// untouched canonical receipt; none of it can select a verdict.
type ControllerChildEvidence struct {
	GrantPath    string // the exact private grant bound by AuthorizationSHA256
	OutputPath   string // the wrapper's original stdout result document
	OutputSHA256 string // the controller's durable record of that document
}

type capturedModelInput struct{ raw []byte }

func (c *capturedModelInput) Review(_ context.Context, raw []byte) (FindingsResult, CLIAudit, error) {
	c.raw = append([]byte(nil), raw...)
	return FindingsResult{}, CLIAudit{}, nil
}

// unsealedControllerChild is the single joined state left by a reconsideration
// whose sampling checkpoint was rolled back: a valid verdict, no seal and no
// publication. Any other state uses the ordinary completion/readback paths.
func unsealedControllerChild(r AttemptReceipt, settings ReviewSettings) bool {
	t := r.Terminal
	return r.Joined && r.CompletedAt > 0 && validAttemptID(r.Parent) && r.InputProvenance == "controller-reconsideration" &&
		contentHashValid(r.AuthorizationSHA256) && r.AuthorizationReference != "" && !r.SamplingCompleted &&
		(r.Verdict == VerdictApproved || r.Verdict == VerdictBlocking) && r.FailureKind == "" && r.FailureStage == "" &&
		r.Publication == (Publication{}) && r.Settings == settings && r.RequestID != "" && r.SessionID != "" && r.ResolvedModel != "" &&
		t != nil && t.ChildStarted && t.ChildWaited && t.JoinedAt != nil && t.ExitCode != nil && *t.ExitCode == 0 &&
		t.DeadlineProducer == "" && !t.OutputTruncated && t.StdoutBytes > 0 && contentHashValid(t.StdoutSHA256) && contentHashValid(t.InputSHA256)
}

// controllerChildSeal authenticates the original typed output, grant and the
// complete reviewed input of one unsealed reconsideration, without sampling.
func (a *Admission) controllerChildSeal(ctx context.Context, original AttemptReceipt, ev ControllerChildEvidence, bundle []byte, coreHash, policyHash string) (completionSeal, FrozenReviewBundle, error) {
	var frozen FrozenReviewBundle
	fail := func(format string) (completionSeal, FrozenReviewBundle, error) {
		return completionSeal{}, frozen, fmt.Errorf("%s", format)
	}
	journalRaw, err := readCompletionBytes(filepath.Join(a.dir, "joins", original.ID+".json"), 64<<10)
	if err != nil {
		return completionSeal{}, frozen, fmt.Errorf("original durable join proof unavailable: %w", err)
	}
	var journal joinJournal
	if decodeStrictJSON(journalRaw, &journal) != nil || journal.SchemaVersion != "squad.review-terminal-join.v1" || journal.WrapperPID != original.WrapperPID || !completionValuesEqual(journal.Receipt, original) {
		return fail("original joined receipt changed")
	}
	grantRaw, err := readCompletionBytes(ev.GrantPath, 64<<10)
	if err != nil {
		return completionSeal{}, frozen, fmt.Errorf("original reconsideration grant unavailable: %w", err)
	}
	var grant ReconsiderGrant
	if receiptHash(grantRaw) != original.AuthorizationSHA256 || decodeStrictJSON(grantRaw, &grant) != nil || grant.Schema != "coordinator.review-reconsider.v1" || grant.Parent != original.Parent || grant.ReviewID != original.AuthorizationReference {
		return fail("original reconsideration authorization changed")
	}
	identity, err := identityFor(bundle)
	if err != nil || identity != original.Identity || decodeStrictJSON(bundle, &frozen) != nil || frozen.SchemaVersion != FrozenReviewSchemaVersion || frozen.CoreHash != coreHash || frozen.PolicyHash != policyHash {
		return fail("complete original input bytes/hash changed")
	}
	// The terminal recorded the hash of the exact model input: the frozen
	// bundle plus the admitted dispute. Rebuilding it proves both are original.
	capture := &capturedModelInput{}
	if _, _, err := (ReconsiderReviewer{Model: capture, Grant: grant}).Review(ctx, bundle); err != nil || receiptHash(capture.raw) != original.Terminal.InputSHA256 {
		return fail("original model input not reproduced by complete input and grant")
	}
	outRaw, err := readCompletionBytes(ev.OutputPath, 1<<20)
	if err != nil {
		return completionSeal{}, frozen, fmt.Errorf("original wrapper output unavailable: %w", err)
	}
	var out ReviewCommandOutput
	if !contentHashValid(ev.OutputSHA256) || receiptHash(outRaw) != ev.OutputSHA256 || decodeStrictJSON(outRaw, &out) != nil {
		return fail("original wrapper output bytes/hash changed")
	}
	r := original
	if out.AttemptID != r.ID || out.ParentAttempt != r.Parent || out.InputProvenance != r.InputProvenance || out.Repository != r.Identity.Repository || out.PullRequest != r.Identity.PR || out.BaseSHA != r.Identity.BaseSHA || out.HeadSHA != r.Identity.HeadSHA ||
		out.Verdict != r.Verdict || !completionValuesEqual(out.Terminal, r.Terminal) || out.RequestID != r.RequestID || out.SessionID != r.SessionID || out.RequestedModel != r.Settings.Model || out.ReasoningEffort != r.Settings.Effort || out.ResolvedModel != r.ResolvedModel ||
		out.InputTokens != r.Usage.InputTokens || out.OutputTokens != r.Usage.OutputTokens || out.ReasoningTokens != r.Usage.ReasoningTokens || out.TotalTokens != r.Usage.TotalTokens || out.CostUSD != r.CostUSD || out.DurationMillis != r.DurationMS ||
		out.FailureStage != "" || out.FailureKind != "" || out.FailureRule != "" || out.CheckRunID != 0 || out.CheckURL != "" || out.CommentID != 0 || out.CommentURL != "" || out.CheckConclusion != "" || out.HumanGrantID != "" || out.RendererProvenance != nil || out.Findings == nil {
		return fail("original wrapper attribution/result/accounting changed")
	}
	result := FindingsResult{SchemaVersion: FindingsSchemaVersion, Verdict: out.Verdict, Summary: out.Summary, Findings: out.Findings}
	if err := ValidateModelFindings(result); err != nil {
		return completionSeal{}, frozen, err
	}
	audit := CLIAudit{Backend: out.Backend, Terminal: r.Terminal, AttemptID: r.ID, BundleSHA256: r.Identity.BundleSHA256, RequestID: r.RequestID, SessionID: r.SessionID, RequestedModel: r.Settings.Model, ReasoningEffort: r.Settings.Effort, ResolvedModel: r.ResolvedModel, Usage: r.Usage, CostUSD: r.CostUSD, Duration: time.Duration(r.DurationMS) * time.Millisecond}
	return completionSeal{"squad.review-completion.seal.v1", r, append(json.RawMessage(nil), bundle...), result, audit, nil}, frozen, nil
}

// CompleteControllerChild seals and publishes the one already-joined result of
// a controller reconsideration that could not seal. It never resolves or calls
// a model, never edits the original receipt, and never allocates an attempt.
// A lost write response is joined only by exact-attempt readback.
func (a *Admission) CompleteControllerChild(ctx context.Context, id string, ev ControllerChildEvidence, bundle []byte, github PullRequestGateway, token, checkName, coreHash, policyHash string) (CompletionResult, error) {
	result := CompletionResult{Attempt: id}
	if !validAttemptID(id) || github == nil || a.publicationLookup == nil || a.check == nil {
		return result, fmt.Errorf("same-execution completion dependencies missing")
	}
	original, err := a.load(ctx, id)
	if err != nil {
		return result, err
	}
	if !unsealedControllerChild(original, a.settings) {
		return result, fmt.Errorf("original is not a joined unsealed controller reconsideration")
	}
	if checkName != ReviewCheckName(original.Settings.Backend, original.Settings.Mode) {
		return result, fmt.Errorf("original mode/App/Check mismatch")
	}
	for _, pid := range []int{original.WrapperPID, original.ReviewerPID} {
		if err := a.processAbsent(pid); err != nil {
			return result, err
		}
	}
	parent, err := a.load(ctx, original.Parent)
	if err != nil {
		return result, err
	}
	if !parent.Joined || parent.Parent != "" || parent.Verdict != VerdictBlocking || parent.Identity != original.Identity || parent.Settings != original.Settings || parent.Publication.CheckRunID <= 0 || parent.Publication.Conclusion != "failure" {
		return result, fmt.Errorf("original reconsidered blocking parent custody changed")
	}
	seal, frozen, err := a.controllerChildSeal(ctx, original, ev, bundle, coreHash, policyHash)
	if err != nil {
		return result, err
	}
	payload, err := json.Marshal(seal)
	if err != nil {
		return result, err
	}
	if len(payload) > 32<<20 {
		return result, fmt.Errorf("completion seal exceeds bound")
	}
	digest := receiptHash(payload)
	var state, pubRaw string
	var pid int
	err = store.WithTxRetry(ctx, a.db, func(tx *sql.Tx) error {
		if err := a.compareReceiptTx(ctx, tx, original); err != nil {
			return err
		}
		var prior, priorDigest string
		err := tx.QueryRowContext(ctx, "SELECT payload,digest,state,publisher_pid,publication FROM review_completion_seals WHERE attempt=?", id).Scan(&prior, &priorDigest, &state, &pid, &pubRaw)
		if err == sql.ErrNoRows {
			var flight int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM review_flights WHERE repository=? AND pr=?", original.Identity.Repository, original.Identity.PR).Scan(&flight); err != nil {
				return err
			}
			if flight != 0 {
				return fmt.Errorf("original completion has unresolved flight")
			}
			state, pid, pubRaw = "sealed", 0, "{}"
			_, err = tx.ExecContext(ctx, "INSERT INTO review_completion_seals(attempt,digest,payload,state) VALUES(?,?,?,'sealed')", id, digest, string(payload))
			return err
		}
		if err != nil {
			return err
		}
		if priorDigest != digest || receiptHash([]byte(prior)) != digest {
			return fmt.Errorf("immutable completion seal changed")
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	var priorPub Publication
	if err := json.Unmarshal([]byte(pubRaw), &priorPub); err != nil {
		return result, err
	}
	lookupReceipt := original
	lookupReceipt.SamplingCompleted = true
	if state == "publishing" && pid > 0 {
		if err := a.processAbsent(pid); err != nil {
			return result, err
		}
	}
	pub, err := a.publicationLookup(ctx, lookupReceipt)
	if err != nil {
		return result, err
	}
	switch state {
	case "published":
		if pub.CheckRunID <= 0 || pub.CheckRunID != priorPub.CheckRunID {
			return result, fmt.Errorf("original published seal changed")
		}
		return CompletionResult{Attempt: id, Publication: priorPub, Verdict: seal.Result.Verdict, SealSHA256: digest, OriginalInput: original.Identity, CurrentInputMatches: true}, nil
	case "publishing":
		if pub.CheckRunID <= 0 {
			return result, fmt.Errorf("original publication uncertain; exact-attempt readback required, no write retry")
		}
		if err := a.finishControllerChild(ctx, original, digest, pub); err != nil {
			return result, err
		}
		return CompletionResult{Attempt: id, Publication: pub, Verdict: seal.Result.Verdict, SealSHA256: digest, OriginalInput: original.Identity, CurrentInputMatches: true}, nil
	case "sealed":
	default:
		return result, fmt.Errorf("unknown completion publication state")
	}
	if pub.CheckRunID > 0 {
		return result, fmt.Errorf("unexpected publication conflicts with sealed pre-write state")
	}
	// The reconsidered blocking Check must still be the only managed verdict.
	if err := a.check(ctx, parent); err != nil {
		return result, err
	}
	current, err := github.FetchPullRequest(ctx, original.Identity.Repository, original.Identity.PR, token)
	if err != nil {
		return result, err
	}
	if !completionSnapshotMatches(current, frozen) {
		return result, fmt.Errorf("live complete original input is stale; publication held")
	}
	err = store.WithTxRetry(ctx, a.db, func(tx *sql.Tx) error {
		if err := a.compareReceiptTx(ctx, tx, original); err != nil {
			return err
		}
		if err := a.compareReceiptTx(ctx, tx, parent); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO review_flights(repository,pr,attempt) VALUES(?,?,?)", original.Identity.Repository, original.Identity.PR, id); err != nil {
			return fmt.Errorf("completion singleflight fence rejected: %w", err)
		}
		res, err := tx.ExecContext(ctx, "UPDATE review_completion_seals SET state='publishing',publisher_pid=? WHERE attempt=? AND digest=? AND state='sealed'", os.Getpid(), id, digest)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("same-execution publication intent CAS rejected")
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	pub, err = github.PublishReview(ctx, token, checkName, current, seal.Result, seal.Audit)
	if err != nil || pub.CheckRunID <= 0 {
		return result, fmt.Errorf("same-execution publication uncertain; exact-attempt lookup only: %v", err)
	}
	if err := a.finishControllerChild(ctx, original, digest, pub); err != nil {
		return result, err
	}
	return CompletionResult{Attempt: id, Publication: pub, Verdict: seal.Result.Verdict, SealSHA256: digest, OriginalInput: original.Identity, CurrentInputMatches: true}, nil
}

func (a *Admission) finishControllerChild(ctx context.Context, original AttemptReceipt, digest string, pub Publication) error {
	raw, err := json.Marshal(pub)
	if err != nil {
		return err
	}
	return store.WithTxRetry(ctx, a.db, func(tx *sql.Tx) error {
		if err := a.compareReceiptTx(ctx, tx, original); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE review_completion_seals SET state='published',publication=? WHERE attempt=? AND digest=? AND state='publishing'", string(raw), original.ID, digest)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("completion finish CAS rejected")
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM review_flights WHERE repository=? AND pr=? AND attempt=?", original.Identity.Repository, original.Identity.PR, original.ID)
		return err
	})
}
