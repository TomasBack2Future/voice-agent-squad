package grokreview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/zsiec/squad/internal/store"
)

// RestartGrant is issued by the trusted host review adapter after the current
// Coordinator controller durably admitted the single restart of an original
// review whose joined attempt produced no verdict. It never applies to an
// attempt with a valid verdict, so it cannot select a different outcome.
type RestartGrant struct {
	Schema        string `json:"schema"`
	Parent        string `json:"parent"`
	ReviewID      string `json:"review_id"`
	Authorization string `json:"authorization"`
	Evidence      string `json:"evidence"`
}

func (a *Admission) ImportRestart(path string) error {
	var grant RestartGrant
	raw, err := readBoundedJSON(path, &grant)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || grant.Schema != "coordinator.review-restart.v1" || grant.Parent == "" || grant.ReviewID == "" || grant.Authorization == "" || grant.Evidence == "" || a.from != "" || a.humanRestart != nil || a.reconsider != nil || a.legacy != nil || a.prospective != nil {
		return fmt.Errorf("private exact controller restart grant required")
	}
	a.restart = &grant
	a.restartDigest = receiptHash(raw)
	return nil
}

// unverdicted reports an original joined attempt whose sampling ended without
// any valid verdict and whose only publication, if any, is a failed Check.
func unverdicted(r AttemptReceipt) bool {
	return r.Joined && r.CompletedAt > 0 && r.Parent == "" && r.Verdict == VerdictError && r.FailureStage == "sampling" &&
		(r.FailureKind == CLIFailureTimeout || r.FailureKind == CLIFailureCanceled) &&
		(r.Publication.CheckRunID == 0 || r.Publication.Conclusion == "failure")
}

func (a *Admission) startRestart(ctx context.Context, bundle []byte, identity ReviewIdentity) error {
	g := a.restart
	prior, err := a.load(ctx, g.Parent)
	if err != nil {
		return err
	}
	if !unverdicted(prior) || prior.Identity != identity || prior.Settings != a.settings {
		return fmt.Errorf("restart requires an original joined unverdicted sampling attempt with exact input/settings")
	}
	for _, pid := range []int{prior.WrapperPID, prior.ReviewerPID} {
		if pid > 0 {
			if err = a.processAbsent(pid); err != nil {
				return err
			}
		}
	}
	candidate := AttemptReceipt{ID: a.receipt.ID, Parent: prior.ID, Identity: identity, Settings: a.settings, WrapperPID: os.Getpid(), LaunchStage: "admitted", InputProvenance: "controller-unverdicted-restart", AuthorizationReference: g.ReviewID, AuthorizationSHA256: a.restartDigest}
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
			return fmt.Errorf("original review flight remains")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var used int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM review_attempts WHERE json_extract(receipt,'$.parent')=?", prior.ID).Scan(&used); err != nil {
			return err
		}
		if used != 0 {
			return fmt.Errorf("original restart already consumed; no chain or retry")
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO review_attempts(id,identity,receipt) VALUES(?,?,?)", candidate.ID, string(identityRaw), string(raw)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO review_flights(repository,pr,attempt) VALUES(?,?,?)", identity.Repository, identity.PR, candidate.ID)
		return err
	})
	if err == nil {
		a.receipt = candidate
		a.frozenBundle = append([]byte(nil), bundle...)
	}
	return err
}
