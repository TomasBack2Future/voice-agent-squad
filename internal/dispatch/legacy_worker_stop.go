package dispatch

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/store"
)

type LegacyProcess struct {
	PID        int    `json:"pid"`
	Start      string `json:"start"`
	Executable string `json:"executable"`
}

type LegacyExternalOperation struct {
	Kind        string `json:"kind"`
	Identity    string `json:"identity"`
	ReceiptPath string `json:"receipt_path,omitempty"`
	Repository  string `json:"repository,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`
	Attempt     int    `json:"attempt,omitempty"`
}

type LegacyStopInput struct {
	Handoff    WorkerHandoffRequest      `json:"handoff"`
	Workspace  string                    `json:"workspace"`
	Assignment json.RawMessage           `json:"assignment"`
	LeasePID   int                       `json:"lease_pid"`
	Inventory  []LegacyExternalOperation `json:"inventory"`
}

type LegacyStopReceipt struct {
	ID                string          `json:"id"`
	State             string          `json:"state"`
	Input             LegacyStopInput `json:"input"`
	Processes         []LegacyProcess `json:"processes"`
	WorkspaceSHA256   string          `json:"workspace_sha256"`
	ConsentOutcomeID  int64           `json:"consent_outcome_id"`
	PreparedAt        int64           `json:"prepared_at"`
	ObservedAt        int64           `json:"observed_at,omitempty"`
	ObservationSHA256 string          `json:"observation_sha256,omitempty"`
}

func validLegacyHash(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, c := range v {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func (s *Store) CheckWorkerNative(ctx context.Context, actor, native string) error {
	if !controllerToken.MatchString(actor) || !controllerToken.MatchString(native) {
		return errors.New("exact Worker native identity required")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM worker_native_fences WHERE repo_id=? AND native_session=?`, s.repoID, native).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("legacy native is persistently fenced; use the legitimate replacement custody path")
	}
	return nil
}

func (s *Store) legacyCustody(ctx context.Context, tx *sql.Tx, q WorkerHandoffRequest) error {
	var actor, native string
	var epoch int64
	if err := tx.QueryRowContext(ctx, `SELECT actor,native_session,epoch FROM dispatch_controller_bindings WHERE repo_id=? AND actor=?`, s.repoID, q.Controller.Actor).Scan(&actor, &native, &epoch); err != nil {
		return err
	}
	if q.Controller != (ControllerBinding{actor, native, epoch}) {
		return errors.New("legacy stop controller identity changed")
	}
	r, err := scanReservation(tx.QueryRowContext(ctx, reservationSelect+` WHERE repo_id=? AND item_id=?`, s.repoID, q.Expected.ItemID))
	if err != nil {
		return err
	}
	if *r != q.Expected || r.ReservedBy != actor || (r.State != "dispatched" && r.State != "completed" && r.State != "failed") {
		return errors.New("legacy stop reservation changed")
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM dispatch_retired_controllers WHERE repo_id=? AND actor=?`, s.repoID, actor).Scan(&count); err != nil || count != 0 {
		return errors.New("legacy stop controller retired")
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM claims WHERE repo_id=? AND item_id=? AND agent_id=? AND generation=? AND claimed_at=? AND last_touch>=? AND state='held' AND resource_group=''`, s.repoID, q.Claim.Item, q.Claim.Actor, q.Claim.Generation, q.Claim.ClaimedAt, q.Claim.LastTouch).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		if err = s.legacyReleasedCustody(ctx, tx, q); err != nil {
			return err
		}
	}
	if strings.HasPrefix(q.Claim.Item, "ENV-") || q.Claim.Item != r.CanonicalItemID {
		return errors.New("legacy stop requires ordinary canonical custody")
	}
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM claims WHERE repo_id=? AND agent_id=? AND item_id!=?)+(SELECT count(*) FROM execution_authorizations WHERE repo_id=? AND holder=? AND state='active')`, s.repoID, q.Claim.Actor, q.Claim.Item, s.repoID, q.Claim.Actor).Scan(&count); err != nil || count != 0 {
		return errors.New("legacy owner retains ENV, other claims or unjoined executions")
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM dispatch_decisions WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=? AND worker_agent=? AND revision=? AND action='hold'`, s.repoID, r.ItemID, r.Generation, r.CanonicalItemID, q.Claim.Actor, q.DecisionRevision).Scan(&count); err != nil || count != 1 {
		return errors.New("legacy stop requires exact current hold and owner")
	}
	return nil
}

// PrepareLegacyWorkerStop must execute inside the ORIGINAL native CLI process
// tree. There is no process/stop receipt import API, and no MCP preparation API.
// It establishes a persistent native fence but never stops a client or tool.
func (s *Store) PrepareLegacyWorkerStop(ctx context.Context, actor string, input LegacyStopInput) (LegacyStopReceipt, error) {
	var out LegacyStopReceipt
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	q := input.Handoff
	now := s.now().Unix()
	if actor != q.Claim.Actor || !controllerToken.MatchString(q.LegacyStopID) || q.ExecutionID != "" || !validLegacyHash(q.HumanAuthoritySHA256) || q.LegacyExpiresAt <= now || q.LegacyExpiresAt > now+86400 || q.LegacyStopSHA256 != "" || q.LegacyAttestationID != 0 || q.ConsentOutcomeID != 0 || !filepath.IsAbs(input.Workspace) || len(input.Assignment) == 0 || len(input.Assignment) > 32768 || input.Inventory == nil || len(input.Inventory) > 32 {
		return out, errors.New("bounded native-owner legacy preparation and human authority required")
	}
	if !controllerToken.MatchString(q.RequestID) || q.Controller.Epoch < 1 || q.Expected.RepoID != s.repoID || q.Expected.ReservedBy != q.Controller.Actor || q.Expected.Generation < 1 || q.Claim.Generation < 1 || q.DecisionRevision < 1 || q.NewActor == actor || q.NewActor == q.Controller.Actor || q.NewNative == q.Expected.WorkerThreadID || q.NewNative == q.Controller.Native || strings.TrimSpace(q.CustodyEvidence) == "" || len(q.CustodyEvidence) > 8192 {
		return out, errors.New("distinct exact replacement and current source custody required")
	}
	for _, v := range []string{q.Controller.Actor, q.Controller.Native, q.Expected.ItemID, q.Expected.WorkerThreadID, q.Claim.Actor, q.NewActor, q.NewNative} {
		if !controllerToken.MatchString(v) {
			return out, errors.New("exact legacy stop identities required")
		}
	}
	var assignment struct {
		Schema         string `json:"schema_version"`
		AssignmentID   string `json:"assignment_id"`
		Repository     string `json:"repository"`
		RepositoryHost string `json:"repository_host,omitempty"`
		CloneLayout    string `json:"clone_layout,omitempty"`
		Branch         string `json:"branch"`
		BaseSHA        string `json:"base_sha"`
		ProjectProfile struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
			Path    string `json:"path"`
		} `json:"project_profile"`
		DesignAdmission *struct {
			Section  string `json:"section"`
			Revision string `json:"revision"`
			Status   string `json:"status"`
		} `json:"design_admission,omitempty"`
		EvidenceRequired []string `json:"evidence_required"`
		Issue            string   `json:"issue"`
		Item             string   `json:"item"`
		Worktree         string   `json:"worktree"`
		Reservation      struct {
			Key        string `json:"key"`
			Generation int64  `json:"generation"`
		} `json:"reservation"`
		Role struct {
			ID      string `json:"id"`
			Skill   string `json:"skill"`
			Version int    `json:"version"`
		} `json:"role"`
		Authorization struct {
			Source      bool `json:"source_mutation"`
			Production  bool `json:"production"`
			PullRequest bool `json:"pull_request"`
			Merge       bool `json:"merge"`
			Staging     bool `json:"staging"`
			IssueClose  bool `json:"issue_close"`
		} `json:"authorization"`
	}
	decoder := json.NewDecoder(bytes.NewReader(input.Assignment))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&assignment)
	trailingErr := decoder.Decode(new(any))
	var required map[string]json.RawMessage
	_ = json.Unmarshal(input.Assignment, &required)
	complete := true
	for _, key := range []string{"schema_version", "assignment_id", "issue", "item", "reservation", "repository", "worktree", "branch", "base_sha", "role", "project_profile", "authorization", "evidence_required"} {
		if _, ok := required[key]; !ok {
			complete = false
		}
	}
	if decodeErr != nil || trailingErr != io.EOF || !complete || assignment.AssignmentID == "" || assignment.Repository == "" || assignment.Branch == "" || len(assignment.BaseSHA) != 40 || assignment.ProjectProfile.ID == "" || assignment.ProjectProfile.Version < 1 || assignment.ProjectProfile.Path == "" || assignment.Schema != "agent-loop.assignment.v1" || assignment.Issue != strings.TrimPrefix(q.Expected.SourceRef, "github:") || assignment.Item != q.Expected.CanonicalItemID || assignment.Worktree != input.Workspace || assignment.Reservation.Key != q.Expected.ItemID || assignment.Reservation.Generation != q.Expected.Generation || assignment.Role.ID != "worker" || assignment.Role.Skill != "agent-loop-worker" || assignment.Role.Version != 1 || !assignment.Authorization.Source || assignment.Authorization.Production {
		return out, errors.New("exact original ordinary Worker assignment required; Deployer and production execution need their separate route")
	}
	probe := s.legacyOwnerProbe
	if probe == nil {
		probe = observeLegacyOwner
	}
	processes, err := probe(ctx, q.Expected.WorkerThreadID, input.LeasePID)
	if err != nil {
		return out, err
	}
	if len(processes) == 0 || len(processes) > 128 {
		return out, errors.New("bounded actual native and owned process identities required")
	}
	workspaceProbe := s.legacyWorkspaceProbe
	if workspaceProbe == nil {
		workspaceProbe = legacyWorkspaceDigest
	}
	workspace, err := workspaceProbe(ctx, input.Workspace)
	if err != nil {
		return out, err
	}
	if err = verifyLegacyExternal(ctx, input.Inventory, q.Claim.Actor, q.Expected.WorkerThreadID); err != nil {
		return out, err
	}
	digest, err := WorkerHandoffDigest(q)
	if err != nil {
		return out, err
	}
	processesRaw, _ := json.Marshal(processes)
	err = store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		out = LegacyStopReceipt{}
		if err := s.legacyCustody(ctx, tx, q); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM terminal_event_receipts WHERE repo_id=? AND reservation_key=? AND generation=? AND recipient=? AND processed_at=0`, s.repoID, q.Expected.ItemID, q.Expected.Generation, actor).Scan(&pending); err != nil || pending != 0 {
			return errors.New("original Worker must handle its pending decisions before native stop preparation")
		}
		var previous string
		err := tx.QueryRowContext(ctx, `SELECT preparation FROM legacy_worker_stops WHERE repo_id=? AND id=?`, s.repoID, q.LegacyStopID).Scan(&previous)
		if err == nil {
			var stored LegacyStopReceipt
			if json.Unmarshal([]byte(previous), &stored) != nil {
				return errors.New("legacy preparation receipt invalid")
			}
			raw, _ := json.Marshal(input)
			original, _ := json.Marshal(stored.Input)
			if string(raw) != string(original) {
				return errors.New("legacy preparation replay changed input")
			}
			out = stored
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		body, _ := json.Marshal(map[string]any{"schema_version": "squad.worker-handoff-consent.v1", "request_sha256": digest, "legacy_stop_id": q.LegacyStopID, "workspace_sha256": workspace, "inventory": input.Inventory})
		m, err := tx.ExecContext(ctx, `INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?,'worker-stop-preparation',?,'[]','high')`, s.repoID, now, actor, q.Expected.CanonicalItemID, string(body))
		if err != nil {
			return err
		}
		consent, err := m.LastInsertId()
		if err != nil {
			return err
		}
		out = LegacyStopReceipt{ID: q.LegacyStopID, State: "prepared", Input: input, Processes: processes, WorkspaceSHA256: workspace, ConsentOutcomeID: consent, PreparedAt: now}
		raw, _ := json.Marshal(out)
		if _, err = tx.ExecContext(ctx, `INSERT INTO legacy_worker_stops(repo_id,id,request_sha256,preparation,processes,state) VALUES(?,?,?,?,?,'prepared')`, s.repoID, out.ID, digest, string(raw), string(processesRaw)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO worker_native_fences(repo_id,native_session,actor,reservation_key,generation,stop_id) VALUES(?,?,?,?,?,?)`, s.repoID, q.Expected.WorkerThreadID, actor, q.Expected.ItemID, q.Expected.Generation, out.ID)
		return err
	})
	return out, err
}

func (s *Store) legacyReleasedCustody(ctx context.Context, tx *sql.Tx, q WorkerHandoffRequest) error {
	if q.LegacyStopID == "" || (q.Expected.State != "completed" && q.Expected.State != "failed") || (q.RetainedPhase != "source" && q.RetainedPhase != "review" && q.RetainedPhase != "acceptance") || q.Claim.Item != q.Expected.CanonicalItemID || q.Claim.Generation < 1 || q.Claim.ClaimedAt < q.Expected.ReservedAt {
		return errors.New("legacy ordinary claim unavailable; terminal continuation needs explicit retained phase")
	}
	var claims int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM claims WHERE repo_id=? AND (item_id=? OR agent_id=?)`, s.repoID, q.Claim.Item, q.Claim.Actor).Scan(&claims); err != nil || claims != 0 {
		return errors.New("released terminal custody has another current holder or claim")
	}
	var actor, outcome string
	var claimed, released int64
	if err := tx.QueryRowContext(ctx, `SELECT agent_id,claimed_at,released_at,outcome FROM claim_history WHERE repo_id=? AND item_id=? ORDER BY claimed_at DESC,released_at DESC LIMIT 1`, s.repoID, q.Claim.Item).Scan(&actor, &claimed, &released, &outcome); err != nil || actor != q.Claim.Actor || claimed != q.Claim.ClaimedAt || released < claimed || (outcome != "done" && outcome != "released") {
		return errors.New("exact last original terminal release history unavailable")
	}
	return nil
}

func (s *Store) LegacyWorkerStopReceipt(ctx context.Context, id string) (LegacyStopReceipt, error) {
	var out LegacyStopReceipt
	if !controllerToken.MatchString(id) {
		return out, errors.New("exact legacy stop identity required")
	}
	var raw, state, hash, processes string
	if err := s.db.QueryRowContext(ctx, `SELECT preparation,state,observation_sha256,processes FROM legacy_worker_stops WHERE repo_id=? AND id=?`, s.repoID, id).Scan(&raw, &state, &hash, &processes); err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return out, err
	}
	recorded, _ := json.Marshal(out.Processes)
	if string(recorded) != processes {
		return out, errors.New("native process preparation provenance changed")
	}
	out.State = state
	out.ObservationSHA256 = hash
	return out, nil
}

func (s *Store) CheckLegacyWorkerSource(ctx context.Context, id, workspace string) (string, error) {
	stop, err := s.LegacyWorkerStopReceipt(ctx, id)
	if err != nil {
		return "", err
	}
	if (stop.State != "observed" && stop.State != "transferred") || stop.Input.Workspace != workspace {
		return "", errors.New("exact observed legacy source custody required")
	}
	probe := s.legacyWorkspaceProbe
	if probe == nil {
		probe = legacyWorkspaceDigest
	}
	hash, err := probe(ctx, workspace)
	if err != nil || hash != stop.WorkspaceSHA256 {
		return "", errors.New("retained legacy source changed; no replacement writer admitted")
	}
	return hash, nil
}

// ObserveLegacyWorkerStop obtains the stop result from the host and immutable
// preparation record; a controller cannot supply arbitrary stopped JSON.
func (s *Store) ObserveLegacyWorkerStop(ctx context.Context, actor string, controller ControllerBinding, id string) (LegacyStopReceipt, error) {
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	out, err := s.LegacyWorkerStopReceipt(ctx, id)
	if err != nil {
		return out, err
	}
	q := out.Input.Handoff
	if actor != q.Controller.Actor || controller != q.Controller || out.State == "transferred" || q.LegacyExpiresAt <= s.now().Unix() {
		return out, errors.New("current controller and unexpired legacy custody required")
	}
	for _, p := range out.Processes {
		if p.Start == "" || p.Executable == "" {
			return out, errors.New("native process provenance unavailable")
		}
		if err = absentWorkerProcess(p.PID); err != nil {
			return out, err
		}
	}
	workspaceProbe := s.legacyWorkspaceProbe
	if workspaceProbe == nil {
		workspaceProbe = legacyWorkspaceDigest
	}
	workspace, err := workspaceProbe(ctx, out.Input.Workspace)
	if err != nil || workspace != out.WorkspaceSHA256 {
		return out, errors.New("legacy retained worktree changed after preparation")
	}
	if err = verifyLegacyExternal(ctx, out.Input.Inventory, q.Claim.Actor, q.Expected.WorkerThreadID); err != nil {
		return out, err
	}
	if out.State == "observed" {
		err = store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error { return s.legacyCustody(ctx, tx, q) })
		return out, err
	}
	out.State = "observed"
	out.ObservedAt = s.now().Unix()
	hash, err := handoffJSONHash(out)
	if err != nil {
		return out, err
	}
	out.ObservationSHA256 = hash
	raw, _ := json.Marshal(out)
	err = store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		if err := s.legacyCustody(ctx, tx, q); err != nil {
			return err
		}
		r, err := tx.ExecContext(ctx, `UPDATE legacy_worker_stops SET preparation=?,state='observed',observation_sha256=? WHERE repo_id=? AND id=? AND state='prepared'`, string(raw), hash, s.repoID, id)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("legacy stop observation state changed; read the immutable winning observation before retry")
		}
		return nil
	})
	return out, err
}

func (s *Store) validateLegacyHandoff(ctx context.Context, tx *sql.Tx, q WorkerHandoffRequest, digest string) (LegacyStopReceipt, error) {
	var out LegacyStopReceipt
	var raw, state, hash, request, processes string
	if err := tx.QueryRowContext(ctx, `SELECT preparation,state,observation_sha256,request_sha256,processes FROM legacy_worker_stops WHERE repo_id=? AND id=?`, s.repoID, q.LegacyStopID).Scan(&raw, &state, &hash, &request, &processes); err != nil {
		return out, errors.New("trusted native/host stop observation unavailable; no receipt import")
	}
	if json.Unmarshal([]byte(raw), &out) != nil || state != "observed" || hash != q.LegacyStopSHA256 || request != digest || out.ConsentOutcomeID != q.ConsentOutcomeID || out.ObservedAt < out.PreparedAt || out.Input.Handoff.Expected != q.Expected || q.LegacyExpiresAt <= s.now().Unix() {
		return out, errors.New("legacy stop hash, native custody, consent or expiration changed")
	}
	recorded, _ := json.Marshal(out.Processes)
	var fences int
	if string(recorded) != processes {
		return out, errors.New("native process preparation provenance changed")
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worker_native_fences WHERE repo_id=? AND native_session=? AND actor=? AND reservation_key=? AND generation=? AND stop_id=?`, s.repoID, q.Expected.WorkerThreadID, q.Claim.Actor, q.Expected.ItemID, q.Expected.Generation, q.LegacyStopID).Scan(&fences); err != nil || fences != 1 {
		return out, errors.New("persistent original-native fence unavailable")
	}
	for _, process := range out.Processes {
		if err := absentWorkerProcess(process.PID); err != nil {
			return out, err
		}
	}
	probe := s.legacyWorkspaceProbe
	if probe == nil {
		probe = legacyWorkspaceDigest
	}
	workspace, err := probe(ctx, out.Input.Workspace)
	if err != nil || workspace != out.WorkspaceSHA256 {
		return out, errors.New("legacy preserved source snapshot changed")
	}
	if err = verifyLegacyExternal(ctx, out.Input.Inventory, q.Claim.Actor, q.Expected.WorkerThreadID); err != nil {
		return out, err
	}
	var body string
	if err = tx.QueryRowContext(ctx, `SELECT body FROM messages WHERE repo_id=? AND id=? AND agent_id=? AND thread=? AND ts>=?`, s.repoID, q.LegacyAttestationID, q.Controller.Actor, q.Expected.CanonicalItemID, out.ObservedAt).Scan(&body); err != nil {
		return out, errors.New("current controller legacy supervisory attestation unavailable")
	}
	var attestation struct {
		Schema          string `json:"schema_version"`
		RequestSHA256   string `json:"request_sha256"`
		StopSHA256      string `json:"stop_sha256"`
		AuthoritySHA256 string `json:"human_authority_sha256"`
		ExpiresAt       int64  `json:"expires_at"`
	}
	if len(body) > 16384 || json.Unmarshal([]byte(body), &attestation) != nil || attestation.Schema != "squad.legacy-worker-stop-attestation.v1" || attestation.RequestSHA256 != digest || attestation.StopSHA256 != hash || attestation.AuthoritySHA256 != q.HumanAuthoritySHA256 || attestation.ExpiresAt != q.LegacyExpiresAt {
		return out, errors.New("exact controller stopped-custody attestation and human authority required")
	}
	return out, nil
}
