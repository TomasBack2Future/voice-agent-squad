package grokreview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/zsiec/squad/internal/store"
)

// HumanRestart is an operator-authored scope binding under the existing local
// operator trust boundary, not a signature or an invented remote authorization
// API. The attributable human bytes and current ledger custody are independent.
type HumanRestart struct {
	SchemaVersion   string            `json:"schema_version"`
	DirectivePath   string            `json:"directive_path"`
	DirectiveSHA256 string            `json:"directive_sha256"`
	HumanMessageID  string            `json:"human_message_id"`
	PreviousAttempt string            `json:"previous_attempt"`
	Custody         CompletionCustody `json:"custody"`
	Identity        ReviewIdentity    `json:"identity"`
	Settings        ReviewSettings    `json:"settings"`
}

type restartController struct {
	Repo   string `json:"repo_id"`
	Actor  string `json:"actor"`
	Native string `json:"native_session"`
	Epoch  int64  `json:"epoch"`
}
type restartScope struct {
	Issue     int    `json:"issue"`
	Repo      string `json:"repo"`
	PR        int    `json:"pr"`
	Item      string `json:"item"`
	Key       string `json:"key"`
	Worker    string `json:"worker"`
	Agent     string `json:"agent"`
	Expected  int64  `json:"expected"`
	Revision  string `json:"revision"`
	Head      string `json:"head"`
	Base      string `json:"base"`
	Mode      string `json:"mode"`
	Old       string `json:"old"`
	Check     int64  `json:"check"`
	BodyHash  string `json:"pr_body_sha256"`
	Title     string `json:"pr_title"`
	DiffHash  string `json:"complete_patch_sha256"`
	PatchPath string `json:"complete_patch_path"`
}
type restartDirective struct {
	SchemaVersion       string            `json:"schema_version"`
	Source              string            `json:"source"`
	HumanMessageID      string            `json:"human_message_id"`
	HumanThread         string            `json:"human_thread"`
	HumanTurn           string            `json:"human_turn"`
	HumanMessageHash    string            `json:"human_message_sha256"`
	HumanEvidencePath   string            `json:"human_evidence_path"`
	Controller          restartController `json:"controller"`
	Scope               []restartScope    `json:"scope"`
	Allowance           string            `json:"allowance"`
	Requires            string            `json:"requires"`
	ProductionUnchanged bool              `json:"production_scope_unchanged"`
}
type humanMessageEvidence struct {
	Source  string `json:"source"`
	Thread  string `json:"thread"`
	Host    string `json:"host"`
	Turn    string `json:"turn"`
	Message struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

type humanRestartBinding struct {
	Grant     HumanRestart
	Directive restartDirective
	Scope     restartScope
	UseKey    string
	GrantHash string
}

// ImportHumanRestart never modifies the old attempt or the work ledger.
func (a *Admission) ImportHumanRestart(path string, owner ReviewOwner) error {
	var g HumanRestart
	raw, err := readBoundedJSON(path, &g)
	if err != nil {
		return err
	}
	if g.SchemaVersion != "squad.review-human-restart.v1" || g.PreviousAttempt == "" || g.HumanMessageID == "" || !contentHashValid(g.Identity.BundleSHA256) || g.Custody.Owner != owner || g.Settings != a.settings || a.from != "" || a.legacy != nil || a.prospective != nil {
		return fmt.Errorf("exact human restart scope/settings/original owner required")
	}
	var d restartDirective
	directiveRaw, err := readBoundedJSON(g.DirectivePath, &d)
	if err != nil || receiptHash(directiveRaw) != g.DirectiveSHA256 || d.SchemaVersion != "squad.human-review-restart.directive.v1" || d.HumanMessageID != g.HumanMessageID || !contentHashValid(d.HumanMessageHash) || d.Controller.Native != d.HumanThread || !d.ProductionUnchanged {
		return fmt.Errorf("attributable operator human directive missing or changed")
	}
	var evidence humanMessageEvidence
	if _, err := readBoundedJSON(d.HumanEvidencePath, &evidence); err != nil {
		return err
	}
	if evidence.Message.Type != "userMessage" || evidence.Message.ID != d.HumanMessageID || evidence.Thread != d.HumanThread || evidence.Turn != d.HumanTurn || evidence.Host != "local" || len(evidence.Message.Content) != 1 || evidence.Message.Content[0].Type != "text" || evidence.Message.Content[0].Text == "" || receiptHash([]byte(evidence.Message.Content[0].Text)) != d.HumanMessageHash {
		return fmt.Errorf("original authorizing human message evidence mismatched")
	}
	var selected *restartScope
	for i := range d.Scope {
		s := &d.Scope[i]
		// Directive uses the repository's short name; the owner explicitly binds
		// the full GitHub repository in the disclosure and exact input receipt.
		if s.Repo == strings.TrimPrefix(g.Identity.Repository, strings.Split(g.Identity.Repository, "/")[0]+"/") && s.PR == g.Identity.PR {
			if selected != nil {
				return fmt.Errorf("ambiguous human directive scope")
			}
			selected = s
		}
	}
	if selected == nil {
		return fmt.Errorf("human grant does not cover repository/PR")
	}
	s := *selected
	c := g.Custody
	if d.Controller.Repo != c.LedgerRepo || d.Controller.Actor == "" || d.Controller.Epoch <= 0 || s.Item != c.Item || s.Key != c.Reservation || s.Worker != owner.Native || s.Agent != owner.Actor || s.Expected != c.DecisionRevision || s.Head != g.Identity.HeadSHA || s.Base != g.Identity.BaseSHA || s.Mode != a.settings.Mode || s.Check <= 0 || !contentHashValid(s.BodyHash) || !contentHashValid(s.DiffHash) {
		return fmt.Errorf("human directive tuple/native/decision scope mismatch")
	}
	a.BindCompletionCustody(c, func(ctx context.Context, current CompletionCustody) error {
		return verifyHumanRestartLedger(ctx, current, owner, d.Controller)
	})
	if err := a.validateCompletionCustody(context.Background(), c, g.Identity); err != nil {
		return err
	}
	a.humanRestart = &humanRestartBinding{g, d, s, receiptHash([]byte(d.HumanMessageID + "\n" + tupleKey(g.Identity))), receiptHash(raw)}
	return nil
}

func verifyHumanRestartLedger(ctx context.Context, c CompletionCustody, owner ReviewOwner, controller restartController) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".squad", "global.db")
	if err := verifyCompletionCustodyAt(ctx, path, c, owner); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return err
	}
	defer db.Close()
	var count int
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM dispatch_controller_bindings b JOIN dispatch_reservations r ON r.repo_id=b.repo_id AND r.reserved_by=b.actor WHERE b.repo_id=? AND b.actor=? AND b.native_session=? AND b.epoch=? AND r.item_id=? AND r.generation=? AND NOT EXISTS(SELECT 1 FROM dispatch_retired_controllers f WHERE f.repo_id=b.repo_id AND f.actor=b.actor)`, c.LedgerRepo, controller.Actor, controller.Native, controller.Epoch, c.Reservation, c.Generation).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("authorizing Dispatcher epoch/recipient fence rejected")
	}
	return nil
}

func (a *Admission) startHumanRestart(ctx context.Context, bundle []byte, identity ReviewIdentity) error {
	h := a.humanRestart
	var frozen FrozenReviewBundle
	if err := decodeStrictJSON(bundle, &frozen); err != nil {
		return err
	}
	if identity != h.Grant.Identity || h.Grant.Settings != a.settings || frozen.Title != h.Scope.Title || receiptHash([]byte(frozen.Description)) != h.Scope.BodyHash || receiptHash([]byte(frozen.Diff)) != h.Scope.DiffHash {
		return fmt.Errorf("human restart exact complete input changed")
	}
	prior, err := a.load(ctx, h.Grant.PreviousAttempt)
	if err != nil {
		return err
	}
	if !prior.Joined || prior.Verdict != VerdictError || prior.FailureStage != "sampling" || prior.FailureKind != CLIFailureTimeout || prior.CompletedAt <= 0 || prior.Publication.CheckRunID != h.Scope.Check || prior.Publication.Conclusion != "failure" || prior.Settings != a.settings || prior.Identity != identity || (prior.OwnerActor != "" && prior.OwnerActor != h.Grant.Custody.Owner.Actor) || (prior.OwnerNative != "" && prior.OwnerNative != h.Grant.Custody.Owner.Native) {
		return fmt.Errorf("actual joined old timeout/input/settings required")
	}
	oldLineage := strings.Split(h.Scope.Old, "/")
	if oldLineage[len(oldLineage)-1] != prior.ID {
		return fmt.Errorf("human directive old attempt mismatch")
	}
	if a.check == nil {
		return fmt.Errorf("current original App Check verification unavailable")
	}
	if err := a.check(ctx, prior); err != nil {
		return err
	}
	candidate := AttemptReceipt{ID: a.receipt.ID, Parent: prior.ID, Identity: identity, Settings: a.settings, WrapperPID: os.Getpid(), LaunchStage: "admitted", OwnerActor: h.Grant.Custody.Owner.Actor, OwnerNative: h.Grant.Custody.Owner.Native, AuthorizationReference: h.Directive.HumanMessageID, AuthorizationSHA256: h.GrantHash, InputProvenance: "explicit-human-restart", HumanGrantID: h.UseKey}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	identityRaw, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	err = store.WithTxRetry(ctx, a.db, func(tx *sql.Tx) error {
		if err := a.validateCompletionCustody(ctx, h.Grant.Custody, identity); err != nil {
			return err
		}
		if err := a.compareReceiptTx(ctx, tx, prior); err != nil {
			return err
		}
		var active string
		err := tx.QueryRowContext(ctx, "SELECT attempt FROM review_flights WHERE repository=? AND pr=?", identity.Repository, identity.PR).Scan(&active)
		if err == nil {
			return fmt.Errorf("review invocation %s has not joined", active)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var verdicts int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM review_attempts WHERE json_extract(identity,'$.repository')=? AND json_extract(identity,'$.pr')=? AND json_extract(identity,'$.base_ref')=? AND json_extract(identity,'$.base_sha')=? AND json_extract(identity,'$.head_sha')=? AND json_extract(receipt,'$.verdict') IN ('approved','blocking')`, identity.Repository, identity.PR, identity.BaseRef, identity.BaseSHA, identity.HeadSHA).Scan(&verdicts); err != nil {
			return err
		}
		if verdicts != 0 {
			return fmt.Errorf("current tuple already has a valid managed verdict")
		}
		var parent, child string
		err = tx.QueryRowContext(ctx, "SELECT parent,child FROM review_recovery_roots WHERE tuple=?", tupleKey(identity)).Scan(&parent, &child)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && prior.ID != child {
			return fmt.Errorf("old timeout does not belong to consumed root")
		}
		// Include the immutable old receipt/root in the new grant's consumption.
		lineage, err := json.Marshal(struct {
			Prior      AttemptReceipt `json:"prior"`
			RootParent string         `json:"root_parent"`
			RootChild  string         `json:"root_child"`
		}{prior, parent, child})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO review_human_restarts(grant_id,human_message,tuple,attempt,grant_sha256,lineage) VALUES(?,?,?,?,?,?)", h.UseKey, h.Directive.HumanMessageID, tupleKey(identity), candidate.ID, h.GrantHash, string(lineage)); err != nil {
			return fmt.Errorf("human ONE-use grant already reserved: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO review_attempts(id,identity,receipt) VALUES(?,?,?)", candidate.ID, string(identityRaw), string(raw)); err != nil {
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
