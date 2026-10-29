package terminalevents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/attest"
	"github.com/zsiec/squad/internal/store"
)

type OperationRequest struct {
	Reservation      string `json:"reservation"`
	Generation       int64  `json:"generation"`
	WorkerSession    string `json:"worker_session"`
	ExpectedDecision int64  `json:"expected_decision"`
	RequestKey       string `json:"request_key"`
	Phase            string `json:"phase,omitempty"`
	Revision         string `json:"revision,omitempty"`
	ResumeStep       string `json:"resume_step,omitempty"`
}
type OperationResult struct {
	Request       OperationRequest `json:"request"`
	Digest        string           `json:"digest"`
	State         string           `json:"state"`
	AttestationID int64            `json:"attestation_id,omitempty"`
	ExitCode      int              `json:"exit_code"`
	EventID       string           `json:"event_id,omitempty"`
}

func operationBody(r OperationResult) string { raw, _ := json.Marshal(r); return string(raw) }

// Operation reads durable state, without probing/killing a process or rerunning
// a command. A started record without an exit is explicitly unresolved.
func (s Store) Operation(ctx context.Context, actor string, q OperationRequest) (OperationResult, error) {
	var r OperationResult
	var body string
	var messageID int64
	err := s.DB.QueryRowContext(ctx, `SELECT id,body FROM messages WHERE repo_id=? AND agent_id=? AND kind='owned-operation' AND json_valid(body) AND json_extract(body,'$.request.reservation')=? AND json_extract(body,'$.request.generation')=? AND json_extract(body,'$.request.worker_session')=? AND json_extract(body,'$.request.request_key')=? ORDER BY id DESC LIMIT 1`, s.Repo, actor, q.Reservation, q.Generation, q.WorkerSession, q.RequestKey).Scan(&messageID, &body)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(body), &r)
	if err != nil {
		return r, err
	}
	_ = s.DB.QueryRowContext(ctx, `SELECT event_id FROM terminal_event_receipts WHERE repo_id=? AND recipient=? AND source_message_id=? AND kind='operation-exit'`, s.Repo, actor, messageID).Scan(&r.EventID)
	return r, nil
}

func (s Store) RunOperation(ctx context.Context, actor string, q OperationRequest, opts attest.RunOpts) (OperationResult, error) {
	var result OperationResult
	if s.Repo == "" || actor == "" || opts.AgentID != actor || q.Generation < 1 || q.RequestKey == "" || len(q.RequestKey) > 96 || strings.ContainsAny(q.RequestKey, "/\x00") {
		return result, fmt.Errorf("bound operation identity and stable request key required")
	}
	for _, field := range []string{q.Phase, q.Revision, q.ResumeStep} {
		if len(field) > 512 || strings.ContainsAny(field, "\x00\r\n") {
			return result, fmt.Errorf("operation phase, revision and resume step must be bounded single-line references")
		}
	}
	raw, _ := json.Marshal(struct {
		Command                     string
		Argv                        []string
		WorkDir, Root, Kind         string
		Phase, Revision, ResumeStep string
	}{opts.Command, opts.Argv, opts.WorkDir, opts.RepoRoot, string(opts.Kind), q.Phase, q.Revision, q.ResumeStep})
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	item := ""
	replay := false
	err := store.WithTxRetry(ctx, s.DB, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT canonical_item_id FROM dispatch_reservations WHERE repo_id=? AND item_id=? AND generation=? AND worker_thread_id=? AND state='dispatched'`, s.Repo, q.Reservation, q.Generation, q.WorkerSession).Scan(&item); err != nil {
			return fmt.Errorf("operation assignment is not live: %w", err)
		}
		if item != opts.ItemID {
			return fmt.Errorf("operation item mismatch")
		}
		recipient, err := workerRecipient(ctx, tx, s.Repo, q.Reservation, q.Generation)
		if err != nil || recipient != actor {
			return fmt.Errorf("operation custody is ambiguous or belongs to another Worker")
		}
		var held int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM claims WHERE repo_id=? AND item_id=? AND agent_id=?`, s.Repo, item, actor).Scan(&held); err != nil || held != 1 {
			return fmt.Errorf("operation requires the Worker's current primary claim")
		}
		var revision int64
		var action string
		err = tx.QueryRowContext(ctx, `SELECT revision,action FROM dispatch_decisions WHERE repo_id=? AND reservation_key=? AND generation=? AND item_id=?`, s.Repo, q.Reservation, q.Generation, item).Scan(&revision, &action)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil && (revision != q.ExpectedDecision || action != "proceed") {
			return fmt.Errorf("read current decision before operation; hold/stale decision refuses execution")
		}
		var body string
		err = tx.QueryRowContext(ctx, `SELECT body FROM messages WHERE repo_id=? AND agent_id=? AND kind='owned-operation' AND json_valid(body) AND json_extract(body,'$.request.reservation')=? AND json_extract(body,'$.request.generation')=? AND json_extract(body,'$.request.worker_session')=? AND json_extract(body,'$.request.request_key')=? ORDER BY id DESC LIMIT 1`, s.Repo, actor, q.Reservation, q.Generation, q.WorkerSession, q.RequestKey).Scan(&body)
		if err == nil {
			if err = json.Unmarshal([]byte(body), &result); err != nil {
				return err
			}
			if result.Digest != hash {
				return fmt.Errorf("operation request key payload conflict")
			}
			replay = true
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		result = OperationResult{Request: q, Digest: hash, State: "started"}
		_, err = tx.ExecContext(ctx, `INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?,'owned-operation',?,'[]','normal')`, s.Repo, time.Now().Unix(), actor, item, operationBody(result))
		return err
	})
	if err != nil {
		return result, err
	}
	if replay {
		if result.State == "started" {
			return result, fmt.Errorf("operation started without a reconciled exit; inspect owned operation, do not rerun")
		}
		return s.Operation(ctx, actor, q)
	}
	opts.OperationKey = q.Reservation + "/" + fmt.Sprint(q.Generation) + "/" + q.WorkerSession + "/" + q.RequestKey
	rec, runErr := attest.New(s.DB, s.Repo, nil).Run(ctx, opts)
	result.State = "exited"
	result.AttestationID = rec.ID
	result.ExitCode = rec.ExitCode
	if runErr != nil && rec.ID == 0 {
		result.State = "result-unavailable"
		result.ExitCode = -1
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err = store.WithTxRetry(saveCtx, s.DB, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(saveCtx, `INSERT INTO messages(repo_id,ts,agent_id,thread,kind,body,mentions,priority) VALUES(?,?,?,?,'owned-operation',?,'[]','normal')`, s.Repo, time.Now().Unix(), actor, item, operationBody(result))
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		// Only the original native/custody may receive the exit; no successor is
		// guessed. The durable result remains available if routing became stale.
		var n int
		err = tx.QueryRowContext(saveCtx, `SELECT count(*) FROM dispatch_reservations WHERE repo_id=? AND item_id=? AND generation=? AND worker_thread_id=? AND state='dispatched'`, s.Repo, q.Reservation, q.Generation, q.WorkerSession).Scan(&n)
		if err != nil {
			return err
		}
		if n != 1 {
			return nil
		}
		recipient, err := workerRecipient(saveCtx, tx, s.Repo, q.Reservation, q.Generation)
		if err != nil || recipient != actor {
			return nil
		}
		result.EventID = fmt.Sprintf("worker-terminal-v1/%s/%d/%s/operation-exit/%d", q.Reservation, q.Generation, q.WorkerSession, id)
		_, err = tx.ExecContext(saveCtx, `INSERT INTO terminal_event_receipts(repo_id,recipient,event_id,reservation_key,generation,worker_session,item_id,kind,outcome_id,source_message_id) VALUES(?,?,?,?,?,?,?,'operation-exit',?,?)`, s.Repo, actor, result.EventID, q.Reservation, q.Generation, q.WorkerSession, item, id, id)
		return err
	})
	if err != nil {
		return result, err
	}
	s.wakeReceivers(saveCtx)
	if runErr != nil {
		return result, runErr
	}
	if result.EventID == "" {
		return result, fmt.Errorf("operation result retained; original native delivery requires owner reconciliation")
	}
	return result, nil
}
