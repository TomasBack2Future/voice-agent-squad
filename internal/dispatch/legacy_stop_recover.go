package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/store"
)

// LegacyStopRecoveryInput is a current-controller CAS on retained fenced
// custody. It cannot import a stop, change source intent, or reopen the native.
type LegacyStopRecoveryInput struct {
	ID                     string               `json:"id"`
	ExpectedRecoveryID     string               `json:"expected_recovery_id"`
	ExpectedObservationSHA string               `json:"expected_observation_sha256"`
	Reason                 string               `json:"reason"`
	Handoff                WorkerHandoffRequest `json:"handoff"`
}

// LegacyStopRecovery retains a canonical controller audit and the new bounded
// custody tuple while keeping original preparation, consent and source intact.
type LegacyStopRecovery struct {
	Schema        string               `json:"schema_version"`
	ID            string               `json:"id"`
	PreviousID    string               `json:"previous_id"`
	InputSHA256   string               `json:"input_sha256"`
	RequestSHA256 string               `json:"request_sha256"`
	Handoff       WorkerHandoffRequest `json:"handoff"`
	Reason        string               `json:"reason"`
	RecoveredAt   int64                `json:"recovered_at"`
	OutcomeID     int64                `json:"outcome_id"`
}

func legacyRecoveryScope(original, proposed WorkerHandoffRequest) error {
	proposed.Controller = original.Controller
	proposed.Expected.ReservedBy = original.Expected.ReservedBy
	proposed.Expected.State = original.Expected.State
	proposed.Expected.UpdatedAt = original.Expected.UpdatedAt
	proposed.Expected.ExpiresAt = original.Expected.ExpiresAt
	if proposed.Claim.LastTouch < original.Claim.LastTouch {
		return errors.New("recovery cannot rewind original claim custody")
	}
	proposed.Claim.LastTouch = original.Claim.LastTouch
	proposed.DecisionRevision = original.DecisionRevision
	proposed.LegacyExpiresAt = original.LegacyExpiresAt
	proposed.LegacyRecoveryID = ""
	if proposed != original {
		return errors.New("recovery changed immutable original assignment, source, owner, replacement or authority")
	}
	return nil
}

func (s *Store) legacyStoppedHost(ctx context.Context, stop LegacyStopReceipt) error {
	if err := validateLegacyStopAdmission(stop); err != nil {
		return err
	}
	for _, process := range stop.Processes {
		if err := absentWorkerProcess(process.PID); err != nil {
			return err
		}
	}
	if s.legacyOwnerProbe == nil {
		if err := rejectLiveLegacyNative(ctx, stop.Input.Handoff.Expected.WorkerThreadID); err != nil {
			return err
		}
	}
	probe := s.legacyWorkspaceProbe
	if probe == nil {
		probe = legacyWorkspaceDigest
	}
	digest, err := probe(ctx, stop.Input.Workspace)
	if err != nil || digest != stop.WorkspaceSHA256 {
		return errors.New("recovery requires the unchanged retained source; reconcile source drift separately")
	}
	return verifyLegacyExternal(ctx, stop.Input.Inventory, stop.Input.Handoff.Claim.Actor, stop.Input.Handoff.Expected.WorkerThreadID, stop.NativeAdmission)
}

// RecoverLegacyWorkerStop renews or amends only benign current-controller,
// reservation-status, heartbeat and held-decision drift. It re-observes actual
// stopped processes/source/external custody and keeps the permanent fence.
func (s *Store) RecoverLegacyWorkerStop(ctx context.Context, actor string, input LegacyStopRecoveryInput) (LegacyStopReceipt, error) {
	var out LegacyStopReceipt
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	q := input.Handoff
	now := s.now().Unix()
	rawInput, err := json.Marshal(input)
	if err != nil || len(rawInput) > 16384 || !controllerToken.MatchString(input.ID) || q.LegacyRecoveryID != input.ID || !controllerToken.MatchString(q.LegacyStopID) || actor != q.Controller.Actor || q.ConsentOutcomeID != 0 || q.LegacyStopSHA256 != "" || q.LegacyAttestationID != 0 || strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 2048 {
		return out, errors.New("bounded current-controller recovery and fresh authority required")
	}
	inputHash, err := handoffJSONHash(input)
	if err != nil {
		return out, err
	}
	out, err = s.LegacyWorkerStopReceipt(ctx, q.LegacyStopID)
	if err != nil {
		return out, err
	}
	if err := legacyRecoveryScope(out.Input.Handoff, q); err != nil {
		return out, err
	}
	var previousRaw string
	if err = s.db.QueryRowContext(ctx, `SELECT preparation FROM legacy_worker_stops WHERE repo_id=? AND id=?`, s.repoID, out.ID).Scan(&previousRaw); err != nil {
		return out, err
	}
	var captured LegacyStopReceipt
	if json.Unmarshal([]byte(previousRaw), &captured) != nil {
		return out, errors.New("original retained stop provenance unavailable")
	}
	before, _ := handoffJSONHash(out)
	after, _ := handoffJSONHash(captured)
	// A separate reader must not pair an old snapshot with a newer CAS value.
	if before != after {
		if captured.Recovery != nil && captured.Recovery.ID == input.ID && captured.Recovery.InputSHA256 == inputHash {
			winning, readErr := s.LegacyWorkerStopReceipt(ctx, q.LegacyStopID)
			if readErr == nil && winning.Recovery != nil && winning.Recovery.ID == input.ID && winning.Recovery.InputSHA256 == inputHash {
				return winning, nil
			}
		}
		return out, errors.New("retained stop changed during recovery read")
	}
	previousID := ""
	if out.Recovery != nil {
		previousID = out.Recovery.ID
		if previousID == input.ID {
			if out.Recovery.InputSHA256 != inputHash {
				return out, errors.New("recovery replay changed exact input")
			}
			return out, nil
		}
	}
	if out.State == "transferred" || q.LegacyExpiresAt <= now || q.LegacyExpiresAt > now+86400 || now < out.PreparedAt {
		return out, errors.New("retained stop and fresh bounded recovery authority required")
	}
	if input.ExpectedRecoveryID != previousID || input.ExpectedObservationSHA != out.ObservationSHA256 {
		return out, errors.New("retained stop recovery CAS changed")
	}
	if err = s.legacyStoppedHost(ctx, out); err != nil {
		return out, err
	}
	if out.State == "prepared" {
		out.State, out.ObservedAt = "observed", now
		out.ObservationSHA256, err = handoffJSONHash(out)
		if err != nil {
			return out, err
		}
	}
	digest, err := WorkerHandoffDigest(q)
	if err != nil {
		return out, err
	}
	recovery := LegacyStopRecovery{Schema: "squad.legacy-worker-stop-recovery.v1", ID: input.ID, PreviousID: previousID, InputSHA256: inputHash, RequestSHA256: digest, Handoff: q, Reason: input.Reason, RecoveredAt: now}
	err = store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		if q.LegacyExpiresAt <= s.now().Unix() {
			return errors.New("recovery authority expired before commitment")
		}
		if err := s.legacyCustody(ctx, tx, q); err != nil {
			return err
		}
		var fences int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worker_native_fences WHERE repo_id=? AND native_session=? AND actor=? AND reservation_key=? AND generation=? AND stop_id=?`, s.repoID, q.Expected.WorkerThreadID, q.Claim.Actor, q.Expected.ItemID, q.Expected.Generation, out.ID).Scan(&fences); err != nil || fences != 1 {
			return errors.New("original permanent fence changed")
		}
		recovery.OutcomeID = 0
		audit, _ := json.Marshal(recovery)
		if len(audit) > 16384 {
			return errors.New("recovery audit exceeds its read bound")
		}
		message, err := tx.ExecContext(ctx, `INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?, 'worker-stop-recovery',?,'[]','high')`, s.repoID, now, actor, q.Expected.CanonicalItemID, string(audit))
		if err != nil {
			return err
		}
		recovery.OutcomeID, err = message.LastInsertId()
		if err != nil {
			return err
		}
		out.Recovery = &recovery
		updated, _ := json.Marshal(out)
		if len(updated) > 131072 {
			return errors.New("recovered stop exceeds its receipt bound")
		}
		result, err := tx.ExecContext(ctx, `UPDATE legacy_worker_stops SET preparation=?,state='observed',observation_sha256=? WHERE repo_id=? AND id=? AND preparation=? AND state IN ('prepared','observed')`, string(updated), out.ObservationSHA256, s.repoID, out.ID, previousRaw)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return errors.New("retained stop recovery CAS changed; inspect winning receipt")
		}
		return nil
	})
	if err != nil {
		// A concurrent identical caller may have committed the immutable winner.
		winning, readErr := s.LegacyWorkerStopReceipt(ctx, q.LegacyStopID)
		if readErr == nil && winning.Recovery != nil && winning.Recovery.ID == input.ID && winning.Recovery.InputSHA256 == inputHash {
			return winning, nil
		}
	}
	return out, err
}

func (s *Store) validateLegacyRecovery(ctx context.Context, tx *sql.Tx, stop LegacyStopReceipt, q WorkerHandoffRequest, digest, originalDigest string) error {
	r := stop.Recovery
	if r == nil || r.Schema != "squad.legacy-worker-stop-recovery.v1" || r.ID != q.LegacyRecoveryID || r.RequestSHA256 != digest {
		return errors.New("exact current retained-custody recovery required")
	}
	current, err := WorkerHandoffDigest(r.Handoff)
	if err != nil || current != digest {
		return errors.New("recovery changed the admitted current custody tuple")
	}
	if err := legacyRecoveryScope(stop.Input.Handoff, r.Handoff); err != nil {
		return err
	}
	original, err := WorkerHandoffDigest(stop.Input.Handoff)
	if err != nil || original != originalDigest {
		return errors.New("recovery changed original consent history")
	}
	var body string
	if err := tx.QueryRowContext(ctx, `SELECT body FROM messages WHERE repo_id=? AND id=? AND agent_id=? AND thread=? AND kind='worker-stop-recovery' AND ts>=?`, s.repoID, r.OutcomeID, q.Controller.Actor, q.Expected.CanonicalItemID, r.RecoveredAt).Scan(&body); err != nil {
		return errors.New("canonical current-controller recovery audit unavailable")
	}
	proof := *r
	proof.OutcomeID = 0
	expected, _ := json.Marshal(proof)
	if len(body) > 16384 || body != string(expected) {
		return errors.New("retained recovery differs from canonical controller authority")
	}
	return nil
}
