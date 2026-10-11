package dispatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/zsiec/squad/internal/store"
)

type HandoffClaim struct {
	Item       string `json:"item"`
	Actor      string `json:"actor"`
	Generation int64  `json:"generation"`
	ClaimedAt  int64  `json:"claimed_at"`
	LastTouch  int64  `json:"last_touch"`
}

// WorkerHandoffRequest is a supervised source custody transition, never a way
// to revoke a live native, bypass execution pins, or recover protected ENV.
// For a terminal reservation, Claim may name a same-owner claimed continuation.
type WorkerHandoffRequest struct {
	RequestID            string            `json:"request_id"`
	Controller           ControllerBinding `json:"controller"`
	Expected             Reservation       `json:"expected"`
	Claim                HandoffClaim      `json:"claim"`
	ExecutionID          string            `json:"execution_id"`
	NewActor             string            `json:"new_actor"`
	NewNative            string            `json:"new_native"`
	DecisionRevision     int64             `json:"decision_revision"`
	ConsentOutcomeID     int64             `json:"consent_outcome_id"`
	CustodyEvidence      string            `json:"custody_evidence"`
	LegacyStopID         string            `json:"legacy_stop_id,omitempty"`
	LegacyStopSHA256     string            `json:"legacy_stop_sha256,omitempty"`
	HumanAuthoritySHA256 string            `json:"human_authority_sha256,omitempty"`
	LegacyExpiresAt      int64             `json:"legacy_expires_at,omitempty"`
	LegacyAttestationID  int64             `json:"legacy_attestation_id,omitempty"`
	RetainedPhase        string            `json:"retained_phase,omitempty"`
}

type WorkerHandoffReceipt struct {
	RequestID         string             `json:"request_id"`
	Reservation       Reservation        `json:"reservation"`
	ClaimGeneration   int64              `json:"claim_generation"`
	DecisionOutcomeID int64              `json:"decision_outcome_id"`
	DecisionRevision  int64              `json:"decision_revision"`
	Previous          Reservation        `json:"previous"`
	PreviousActor     string             `json:"previous_actor"`
	ConsentOutcomeID  int64              `json:"consent_outcome_id"`
	ExecutionID       string             `json:"execution_id"`
	LegacyStop        *LegacyStopReceipt `json:"legacy_stop,omitempty"`
}

func (s *Store) WorkerHandoffReceipt(ctx context.Context, requestID string) (WorkerHandoffReceipt, error) {
	var out WorkerHandoffReceipt
	if !controllerToken.MatchString(requestID) {
		return out, errors.New("exact Worker handoff request identity required")
	}
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT receipt FROM worker_handoffs WHERE repo_id=? AND request_id=?`, s.repoID, requestID).Scan(&raw); err != nil {
		return out, err
	}
	err := json.Unmarshal([]byte(raw), &out)
	return out, err
}

func handoffJSONHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	// Canonical object-key ordering also supports independently authored CLI JSON.
	var object any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err = d.Decode(&object); err != nil {
		return "", err
	}
	raw, err = json.Marshal(object)
	return fmt.Sprintf("%x", sha256.Sum256(raw)), err
}

// WorkerHandoffDigest is the exact request hash the ORIGINAL owner publishes
// in its canonical consent message, with consent_outcome_id set to zero.
func WorkerHandoffDigest(q WorkerHandoffRequest) (string, error) {
	q.ConsentOutcomeID = 0
	q.LegacyStopSHA256 = ""
	q.LegacyAttestationID = 0
	return handoffJSONHash(q)
}

func absentWorkerProcess(pid int) error {
	if pid < 1 {
		return errors.New("original native process identity unavailable")
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	err = p.Signal(syscall.Signal(0))
	if !errors.Is(err, syscall.ESRCH) && !errors.Is(err, os.ErrProcessDone) {
		return errors.New("original native remains live or process join unavailable")
	}
	return nil
}

// WorkerHandoff preserves the controller and effective hold. Original consent,
// reconciled source pin, closed native, claims, decision and routing are checked
// in the same transaction. Pending events must first be handled by their owner;
// no historical receipt is rewritten into a replacement Worker's generation.
func (s *Store) WorkerHandoff(ctx context.Context, actor string, q WorkerHandoffRequest) (WorkerHandoffReceipt, error) {
	var out WorkerHandoffReceipt
	identities := []string{actor, q.RequestID, q.Controller.Actor, q.Controller.Native, q.Expected.ItemID, q.Expected.WorkerThreadID, q.Claim.Item, q.Claim.Actor, q.NewActor, q.NewNative}
	if q.LegacyStopID == "" {
		identities = append(identities, q.ExecutionID)
	} else {
		identities = append(identities, q.LegacyStopID)
	}
	for _, v := range identities {
		if !controllerToken.MatchString(v) {
			return out, errors.New("exact Worker handoff identities required")
		}
	}
	if q.LegacyStopID == "" && (q.LegacyStopSHA256 != "" || q.HumanAuthoritySHA256 != "" || q.LegacyExpiresAt != 0 || q.LegacyAttestationID != 0 || q.RetainedPhase != "") {
		return out, errors.New("source pin and legacy stopped-custody modes are separate")
	}
	if q.LegacyStopID != "" && (q.ExecutionID != "" || !validLegacyHash(q.LegacyStopSHA256) || !validLegacyHash(q.HumanAuthoritySHA256) || q.LegacyAttestationID < 1) {
		return out, errors.New("legacy handoff requires an actual observed stop and current controller attestation")
	}
	if actor != q.Controller.Actor || q.Controller.Epoch < 1 || q.Expected.RepoID != s.repoID || q.Expected.ReservedBy != actor || q.Expected.Generation < 1 || q.Claim.Generation < 1 || q.DecisionRevision < 1 || q.ConsentOutcomeID < 1 || q.NewActor == q.Claim.Actor || q.NewActor == actor || q.NewNative == q.Expected.WorkerThreadID || q.NewNative == q.Controller.Native || strings.TrimSpace(q.CustodyEvidence) == "" || len(q.CustodyEvidence) > 8192 {
		return out, errors.New("distinct Worker, current controller, exact custody and original consent required")
	}
	hash, err := handoffJSONHash(q)
	if err != nil {
		return out, err
	}
	consentHash, err := WorkerHandoffDigest(q)
	if err != nil {
		return out, err
	}
	err = store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		out = WorkerHandoffReceipt{}
		var priorHash, priorReceipt string
		err := tx.QueryRowContext(ctx, `SELECT request_sha256,receipt FROM worker_handoffs WHERE repo_id=? AND request_id=?`, s.repoID, q.RequestID).Scan(&priorHash, &priorReceipt)
		if err == nil {
			if priorHash != hash {
				return errors.New("worker handoff replay changed input")
			}
			return json.Unmarshal([]byte(priorReceipt), &out)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var b ControllerBinding
		if err = tx.QueryRowContext(ctx, `SELECT actor,native_session,epoch FROM dispatch_controller_bindings d WHERE repo_id=? AND actor=? AND NOT EXISTS(SELECT 1 FROM dispatch_retired_controllers f WHERE f.repo_id=d.repo_id AND f.actor=d.actor)`, s.repoID, actor).Scan(&b.Actor, &b.Native, &b.Epoch); err != nil || b != q.Controller {
			return errors.New("worker handoff controller fence changed")
		}
		r, err := scanReservation(tx.QueryRowContext(ctx, reservationSelect+` WHERE repo_id=? AND item_id=?`, s.repoID, q.Expected.ItemID))
		if err != nil {
			return err
		}
		if *r != q.Expected || (r.State != "dispatched" && r.State != "completed" && r.State != "failed") {
			return errors.New("worker handoff reservation fence changed or unsupported state")
		}
		var claim HandoffClaim
		var state, group string
		releasedClaim := false
		if err = tx.QueryRowContext(ctx, `SELECT item_id,agent_id,generation,claimed_at,last_touch,state,resource_group FROM claims WHERE repo_id=? AND item_id=?`, s.repoID, q.Claim.Item).Scan(&claim.Item, &claim.Actor, &claim.Generation, &claim.ClaimedAt, &claim.LastTouch, &state, &group); err != nil {
			if !errors.Is(err, sql.ErrNoRows) || q.LegacyStopID == "" {
				return err
			}
			if err = s.legacyReleasedCustody(ctx, tx, q); err != nil {
				return err
			}
			claim = q.Claim
			state = "held"
			releasedClaim = true
		}
		if claim.Item != q.Claim.Item || claim.Actor != q.Claim.Actor || claim.Generation != q.Claim.Generation || claim.ClaimedAt != q.Claim.ClaimedAt || claim.LastTouch < q.Claim.LastTouch || claim.ClaimedAt < r.ReservedAt || state != "held" || group != "" || strings.HasPrefix(claim.Item, "ENV-") || strings.HasPrefix(r.CanonicalItemID, "ENV-") {
			return errors.New("worker handoff ordinary claim fence changed or protected")
		}
		var count int
		if claim.Item != r.CanonicalItemID {
			return errors.New("worker handoff must retain the original canonical assignment; cross-item continuation is unsupported")
		}
		// No other ordinary or protected claim, active pin or ambiguous historical
		// owner may be silently abandoned or transferred with this one item.
		if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM claims WHERE repo_id=? AND agent_id=? AND item_id!=?)+(SELECT count(*) FROM execution_authorizations WHERE repo_id=? AND holder=? AND state='active')`, s.repoID, claim.Actor, claim.Item, s.repoID, claim.Actor).Scan(&count); err != nil || count != 0 {
			return errors.New("original owner retains other claims, ENV or unjoined execution pins")
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM claim_history WHERE repo_id=? AND item_id=? AND agent_id!=? AND claimed_at>=?`, s.repoID, r.CanonicalItemID, claim.Actor, r.ReservedAt).Scan(&count); err != nil || count != 0 {
			return errors.New("ambiguous original Worker custody")
		}
		var legacyStop *LegacyStopReceipt
		if q.LegacyStopID != "" {
			stopped, err := s.validateLegacyHandoff(ctx, tx, q, consentHash)
			if err != nil {
				return err
			}
			legacyStop = &stopped
		} else {
			var executionRaw, reconciliation, executionState string
			if err = tx.QueryRowContext(ctx, `SELECT binding,state,reconciliation FROM execution_authorizations WHERE repo_id=? AND id=? AND holder=?`, s.repoID, q.ExecutionID, claim.Actor).Scan(&executionRaw, &executionState, &reconciliation); err != nil {
				return errors.New("reconciled original source execution unavailable; legacy/App writer transfer remains unsupported")
			}
			var execution WorkerExecutionBinding
			if json.Unmarshal([]byte(executionRaw), &execution) != nil || executionState != "reconciled" || reconciliation == "" || execution.Kind != "worker" || execution.Actor != claim.Actor || execution.Native != r.WorkerThreadID || execution.Reservation != r.ItemID || execution.Generation != r.Generation || execution.SourceRef != r.SourceRef || execution.Item != r.CanonicalItemID || (claim.Item == r.CanonicalItemID && execution.ClaimGeneration != claim.Generation) {
				return errors.New("original source execution/join custody changed")
			}
			if err = absentWorkerProcess(execution.ClientPID); err != nil {
				return err
			}
		}
		var revision, outcome int64
		var action, condition, worker string
		if err = tx.QueryRowContext(ctx, `SELECT revision,outcome_id,action,condition,worker_agent FROM dispatch_decisions WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=?`, s.repoID, r.ItemID, r.Generation, r.CanonicalItemID).Scan(&revision, &outcome, &action, &condition, &worker); err != nil || revision != q.DecisionRevision || action != "hold" || worker != claim.Actor {
			return errors.New("exact original handled hold decision required")
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM terminal_event_receipts WHERE repo_id=? AND reservation_key=? AND generation=? AND processed_at=0`, s.repoID, r.ItemID, r.Generation).Scan(&count); err != nil || count != 0 {
			return errors.New("original pending events require owner reconciliation before handoff")
		}
		var body string
		if err = tx.QueryRowContext(ctx, `SELECT body FROM messages WHERE repo_id=? AND id=? AND agent_id=? AND thread=? AND ts>=?`, s.repoID, q.ConsentOutcomeID, claim.Actor, r.CanonicalItemID, r.ReservedAt).Scan(&body); err != nil {
			return errors.New("original owner canonical consent unavailable")
		}
		var consent struct {
			Schema string `json:"schema_version"`
			Hash   string `json:"request_sha256"`
		}
		if len(body) > workerHandoffConsentLimit || json.Unmarshal([]byte(body), &consent) != nil || consent.Schema != "squad.worker-handoff-consent.v1" || consent.Hash != consentHash {
			return errors.New("original owner did not authorize this exact Worker handoff")
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM agents WHERE repo_id=? AND id=?`, s.repoID, q.NewActor).Scan(&count); err != nil || count != 1 {
			return errors.New("replacement actor must register legitimately before handoff")
		}
		if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM claims WHERE repo_id=? AND agent_id=?)+(SELECT count(*) FROM dispatch_reservations WHERE repo_id=? AND worker_thread_id=?)+(SELECT count(*) FROM dispatch_controller_bindings WHERE repo_id=? AND (actor=? OR native_session=?))+(SELECT count(*) FROM dispatch_retired_controllers WHERE repo_id=? AND actor=?)`, s.repoID, q.NewActor, s.repoID, q.NewNative, s.repoID, q.NewActor, q.NewNative, s.repoID, q.NewActor).Scan(&count); err != nil || count != 0 {
			return errors.New("replacement already has task or controller custody")
		}
		now := s.now().Unix()
		if now <= claim.ClaimedAt || now <= r.ReservedAt {
			return errors.New("worker handoff requires a later custody epoch")
		}
		if !releasedClaim {
			if _, err = tx.ExecContext(ctx, `INSERT INTO claim_history(repo_id,item_id,agent_id,claimed_at,released_at,outcome) VALUES(?,?,?,?,?,'worker-handoff')`, s.repoID, claim.Item, claim.Actor, claim.ClaimedAt, now); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE claims SET agent_id=?,generation=generation+1,claimed_at=?,last_touch=?,previous_agent_id=?,recovery_reason=? WHERE repo_id=? AND item_id=?`, q.NewActor, now, now, claim.Actor, q.CustodyEvidence, s.repoID, claim.Item); err != nil {
				return err
			}
		} else {
			if _, err = tx.ExecContext(ctx, `INSERT INTO claims(repo_id,item_id,agent_id,claimed_at,last_touch,generation,previous_agent_id,recovery_reason,worktree) VALUES(?,?,?,?,?,?,?,?,?)`, s.repoID, claim.Item, q.NewActor, now, now, claim.Generation+1, claim.Actor, q.CustodyEvidence, legacyStop.Input.Workspace); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE touches SET released_at=? WHERE repo_id=? AND item_id=? AND agent_id=? AND released_at IS NULL`, now, s.repoID, claim.Item, claim.Actor); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE dispatch_reservations SET canonical_item_id=?,state='dispatched',generation=generation+1,worker_thread_id=?,reserved_at=?,updated_at=?,expires_at=0 WHERE repo_id=? AND item_id=?`, claim.Item, q.NewNative, now, now, s.repoID, r.ItemID); err != nil {
			return err
		}
		previous := *r
		r.CanonicalItemID = claim.Item
		r.State = "dispatched"
		r.Generation++
		r.WorkerThreadID = q.NewNative
		r.ReservedAt = now
		r.UpdatedAt = now
		r.ExpiresAt = 0
		audit, _ := json.Marshal(map[string]any{"request": q, "preserved_action": action, "preserved_condition": condition, "previous_outcome_id": outcome})
		result, err := tx.ExecContext(ctx, `INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?,'worker-handoff',?,'[]','high')`, s.repoID, now, actor, claim.Item, string(audit))
		if err != nil {
			return err
		}
		message, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO dispatch_decisions(repo_id,reservation_key,generation,item_id,revision,outcome_id,action,condition,worker_agent) VALUES(?,?,?,?,?,?,?,?,?)`, s.repoID, r.ItemID, r.Generation, claim.Item, revision, message, action, condition, q.NewActor); err != nil {
			return err
		}
		event := fmt.Sprintf("worker-terminal-v1/%s/%d/%s/decision-resolved/%d", r.ItemID, r.Generation, q.NewNative, message)
		if _, err = tx.ExecContext(ctx, `INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id) VALUES(?,?,?,?,?,?,?,'decision-resolved',?,?)`, s.repoID, q.NewActor, event, r.ItemID, r.Generation, q.NewNative, claim.Item, message, message); err != nil {
			return err
		}
		if legacyStop != nil {
			if _, err = tx.ExecContext(ctx, `UPDATE legacy_worker_stops SET state='transferred' WHERE repo_id=? AND id=? AND state='observed'`, s.repoID, q.LegacyStopID); err != nil {
				return err
			}
			legacyStop.State = "transferred"
		}
		out = WorkerHandoffReceipt{RequestID: q.RequestID, Reservation: *r, ClaimGeneration: claim.Generation + 1, DecisionOutcomeID: message, DecisionRevision: revision, Previous: previous, PreviousActor: claim.Actor, ConsentOutcomeID: q.ConsentOutcomeID, ExecutionID: q.ExecutionID, LegacyStop: legacyStop}
		receipt, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO worker_handoffs(repo_id,request_id,request_sha256,receipt) VALUES(?,?,?,?)`, s.repoID, q.RequestID, hash, string(receipt))
		return err
	})
	return out, err
}
