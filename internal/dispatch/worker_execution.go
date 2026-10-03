package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zsiec/squad/internal/store"
)

type WorkerToolRuntime struct {
	Executable string `json:"executable"`
	Context    string `json:"context"`
	DaemonID   string `json:"daemon_id"`
	Image      string `json:"image"`
}

type WorkerExecutionBinding struct {
	ToolRuntime      WorkerToolRuntime `json:"tool_runtime"`
	Kind             string            `json:"kind"`
	ID               string            `json:"id"`
	Item             string            `json:"item"`
	Actor            string            `json:"actor"`
	Native           string            `json:"native"`
	ClaimGeneration  int64             `json:"claim_generation"`
	Reservation      string            `json:"reservation"`
	SourceRef        string            `json:"source_ref"`
	Generation       int64             `json:"generation"`
	Controller       string            `json:"controller"`
	ControllerNative string            `json:"controller_native"`
	Epoch            int64             `json:"epoch"`
	ExpiresAt        int64             `json:"expires_at"`
	ClientPID        int               `json:"client_pid"`
}

func (b WorkerExecutionBinding) validate(actor string) error {
	for _, value := range []string{b.ID, b.Item, b.Actor, b.Native, b.Reservation, b.Controller, b.ControllerNative} {
		if !controllerToken.MatchString(value) {
			return errors.New("exact worker execution identities required")
		}
	}
	if !strings.HasPrefix(b.ToolRuntime.Executable, "/") || b.ToolRuntime.Context == "" || b.ToolRuntime.DaemonID == "" || len(b.ToolRuntime.Image) != 71 || !strings.HasPrefix(b.ToolRuntime.Image, "sha256:") {
		return errors.New("qualified immutable source tool runtime required")
	}
	if b.Kind != "worker" || actor != b.Actor || strings.HasPrefix(b.Item, "ENV-") || !strings.HasPrefix(b.SourceRef, "github:") || len(b.SourceRef) > 512 || b.ClaimGeneration < 1 || b.Generation < 1 || b.Epoch < 1 || b.ClientPID < 1 {
		return errors.New("invalid source Worker execution binding")
	}
	return nil
}

func (s *Store) workerExecutionCustody(ctx context.Context, tx *sql.Tx, b WorkerExecutionBinding) error {
	var matches int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM claims c
JOIN dispatch_reservations r ON r.repo_id=c.repo_id AND r.canonical_item_id=c.item_id
JOIN dispatch_controller_bindings d ON d.repo_id=r.repo_id AND d.actor=r.reserved_by
WHERE c.repo_id=? AND c.item_id=? AND c.agent_id=? AND c.generation=? AND c.state='held' AND c.resource_group=''
AND r.item_id=? AND r.generation=? AND r.worker_thread_id=? AND r.source_ref=? AND r.state='dispatched'
AND d.actor=? AND d.native_session=? AND d.epoch=?
AND NOT EXISTS(SELECT 1 FROM dispatch_retired_controllers f WHERE f.repo_id=d.repo_id AND f.actor=d.actor)`,
		s.repoID, b.Item, b.Actor, b.ClaimGeneration, b.Reservation, b.Generation, b.Native, b.SourceRef, b.Controller, b.ControllerNative, b.Epoch).Scan(&matches)
	if err != nil {
		return err
	}
	if matches != 1 {
		return errors.New("worker claim, reservation or controller custody changed")
	}
	return nil
}

func (s *Store) AcquireWorkerExecution(ctx context.Context, actor string, b WorkerExecutionBinding) error {
	if err := b.validate(actor); err != nil {
		return err
	}
	now := s.now().Unix()
	if b.ExpiresAt <= now || b.ExpiresAt > now+86400 {
		return errors.New("worker execution must expire within 24 hours")
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		if err := s.workerExecutionCustody(ctx, tx, b); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO execution_authorizations(repo_id,id,item_id,holder,generation,binding,state,last_step,created_at,updated_at)
VALUES(?,?,?,?,?,?,'active','gate-open',?,?)`, s.repoID, b.ID, b.Item, b.Actor, b.ClaimGeneration, string(raw), now, now)
		return err
	})
}

func (s *Store) CheckWorkerWrite(ctx context.Context, actor string, b WorkerExecutionBinding) error {
	if err := s.CheckWorkerExecution(ctx, actor, b); err != nil {
		return err
	}
	var held int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM dispatch_decisions WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=? AND action='hold'`, s.repoID, b.Reservation, b.Generation, b.Item).Scan(&held)
	if err != nil {
		return err
	}
	if held != 0 {
		return errors.New("assignment decision is hold; no source mutation admitted")
	}
	return nil
}

func (s *Store) CheckWorkerExecution(ctx context.Context, actor string, b WorkerExecutionBinding) error {
	return s.checkWorkerCustody(ctx, actor, b, true)
}

// CheckWorkerRead permits only coordination after suspension while exact custody remains held.
func (s *Store) CheckWorkerRead(ctx context.Context, actor string, b WorkerExecutionBinding) error {
	return s.checkWorkerCustody(ctx, actor, b, false)
}

func (s *Store) checkWorkerCustody(ctx context.Context, actor string, b WorkerExecutionBinding, requireOpen bool) error {
	if err := b.validate(actor); err != nil {
		return err
	}
	if b.ExpiresAt <= s.now().Unix() {
		return errors.New("worker execution expired; custody remains pinned")
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		var binding, state, gate string
		if err := tx.QueryRowContext(ctx, `SELECT binding,state,last_step FROM execution_authorizations WHERE repo_id=? AND id=?`, s.repoID, b.ID).Scan(&binding, &state, &gate); err != nil {
			return err
		}
		if binding != string(raw) || state != "active" || (gate != "gate-open" && gate != "gate-closed") || (requireOpen && gate != "gate-open") {
			return errors.New("worker execution is closed or its immutable binding changed")
		}
		return s.workerExecutionCustody(ctx, tx, b)
	})
}

func (s *Store) SuspendWorkerExecution(ctx context.Context, actor string, b WorkerExecutionBinding) error {
	if err := b.validate(actor); err != nil {
		return err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE execution_authorizations SET last_step='gate-closed',updated_at=?
WHERE repo_id=? AND id=? AND holder=? AND binding=? AND state='active'`, s.now().Unix(), s.repoID, b.ID, actor, string(raw))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("worker execution identity or state changed")
	}
	return nil
}

// CloseWorkerExecution reconciles a suspended pin for the supervising owner after joining its native host
// and owned tool processes. Expiry and heartbeat failure never release custody.
func (s *Store) CloseWorkerExecution(ctx context.Context, actor string, b WorkerExecutionBinding, evidence string) error {
	if err := b.validate(actor); err != nil {
		return err
	}
	if strings.TrimSpace(evidence) == "" || len(evidence) > 8192 {
		return errors.New("native/tool join evidence required")
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE execution_authorizations SET state='reconciled',reconciliation=?,updated_at=?
WHERE repo_id=? AND id=? AND holder=? AND binding=? AND state='active' AND last_step='gate-closed'`, evidence, s.now().Unix(), s.repoID, b.ID, actor, string(raw))
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("worker execution identity or state changed")
		}
		return nil
	})
}

// WorkerOutcome records a supervisor-joined native report exactly once while
// all custody pins remain held. Publication is separate from recipient handling.
func (s *Store) WorkerOutcome(ctx context.Context, actor string, b WorkerExecutionBinding, status, summary string) (int64, int64, error) {
	if err := b.validate(actor); err != nil {
		return 0, 0, err
	}
	if (status != "completed" && status != "blocked") || strings.TrimSpace(summary) == "" || len(summary) > 16384 {
		return 0, 0, errors.New("bounded native completion or blocked report required")
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return 0, 0, err
	}
	var message, revision int64
	err = store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		if err := s.workerExecutionCustody(ctx, tx, b); err != nil {
			return err
		}
		var stored, state, gate string
		if err := tx.QueryRowContext(ctx, `SELECT binding,state,last_step,worker_outcome_id FROM execution_authorizations WHERE repo_id=? AND id=?`, s.repoID, b.ID).Scan(&stored, &state, &gate, &message); err != nil {
			return err
		}
		if stored != string(raw) || state != "active" || gate != "gate-closed" {
			return errors.New("joined Worker report requires its suspended active pin")
		}
		var action string
		err := tx.QueryRowContext(ctx, `SELECT revision,action FROM dispatch_decisions WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=?`, s.repoID, b.Reservation, b.Generation, b.Item).Scan(&revision, &action)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if action == "hold" && status == "completed" {
			return errors.New("held assignment cannot report completion")
		}
		body := "Muse native " + b.Native + " execution " + b.ID + " " + status + ":\n" + summary
		if message != 0 {
			var old string
			if err := tx.QueryRowContext(ctx, `SELECT body FROM messages WHERE id=? AND repo_id=? AND agent_id=? AND thread=?`, message, s.repoID, actor, b.Item).Scan(&old); err != nil {
				return err
			}
			if old != body {
				return errors.New("worker outcome replay changed")
			}
			return nil
		}
		kind := "milestone"
		if status == "blocked" {
			kind = "stuck"
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO messages(repo_id,agent_id,ts,kind,thread,body,mentions) VALUES(?,?,?,?,?,?,'[]')`, s.repoID, actor, s.now().Unix(), kind, b.Item, body)
		if err != nil {
			return err
		}
		message, err = result.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE execution_authorizations SET worker_outcome_id=?,updated_at=? WHERE repo_id=? AND id=?`, message, s.now().Unix(), s.repoID, b.ID)
		return err
	})
	return message, revision, err
}
