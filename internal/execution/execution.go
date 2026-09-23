// Package execution provides online production admission against the live ledger.
// Issuance and terminal reconciliation are trusted local operations. A runner can
// only begin/check a previously authorized immutable binding, never release it.
package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/store"
)

type Binding struct {
	ID             string            `json:"id"`
	ItemID         string            `json:"item_id"`
	Holder         string            `json:"holder"`
	Generation     int64             `json:"generation"`
	Scope          string            `json:"scope"`
	ManifestSHA256 string            `json:"manifest_sha256"`
	Repository     string            `json:"repository"`
	ApplicationSHA string            `json:"application_sha"`
	WorkflowSHA    string            `json:"workflow_sha"`
	WorkflowPath   string            `json:"workflow_path"`
	Operation      string            `json:"operation"`
	Target         map[string]string `json:"target"`
	ExpiresAt      int64             `json:"expires_at"`
	ApprovalRef    string            `json:"approval_ref"`
}

type Request struct {
	Binding    Binding `json:"binding"`
	RunID      int64   `json:"run_id"`
	RunAttempt int64   `json:"run_attempt"`
	Step       string  `json:"step"`
}

type Receipt struct {
	ManifestSHA256 string `json:"manifest_sha256"`
	Holder         string `json:"holder"`
	Generation     int64  `json:"generation"`
	ID             string `json:"id"`
	State          string `json:"state"`
	RunID          int64  `json:"run_id"`
	RunAttempt     int64  `json:"run_attempt"`
	Step           string `json:"step"`
	CheckedAt      int64  `json:"checked_at"`
}

type Store struct {
	db     *sql.DB
	repoID string
	now    func() time.Time
}

func New(db *sql.DB, repoID string, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db, repoID, now}
}

var ident = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var sha = regexp.MustCompile(`^[a-f0-9]{40}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var repository = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (b Binding) Validate(now int64) error {
	if !ident.MatchString(b.ID) || !ident.MatchString(b.Holder) || b.ItemID != "ENV-002" || b.Generation < 1 ||
		(b.Scope != "*" && b.Scope != "studio") || !digest.MatchString(b.ManifestSHA256) || !repository.MatchString(b.Repository) ||
		!sha.MatchString(b.ApplicationSHA) || !sha.MatchString(b.WorkflowSHA) || b.WorkflowPath != ".github/workflows/deploy-go-uap-production.yml" ||
		strings.TrimSpace(b.ApprovalRef) == "" || len(b.ApprovalRef) > 2048 {
		return errors.New("invalid execution binding")
	}
	switch b.Operation {
	case "api-health-only", "preview", "candidate-upgrade", "cutover", "rollback", "candidate-rollback", "candidate-retirement":
	default:
		return errors.New("unsupported operation")
	}
	if b.ExpiresAt <= now || b.ExpiresAt > now+86400 {
		return errors.New("authorization must expire within 24 hours")
	}
	if len(b.Target) != 6 || b.Target["environment"] != "production" {
		return errors.New("invalid production target")
	}
	for _, k := range []string{"cluster_context", "namespace", "public_host", "go_release", "frontend_release"} {
		if strings.TrimSpace(b.Target[k]) == "" || len(b.Target[k]) > 256 {
			return fmt.Errorf("missing target %s", k)
		}
	}
	candidate := b.Operation == "api-health-only" || b.Operation == "preview" || b.Operation == "candidate-upgrade" || b.Operation == "candidate-rollback" || b.Operation == "candidate-retirement"
	goRelease, frontRelease := "studio-go", "studio-frontend"
	if candidate {
		goRelease += "-production-candidate"
		frontRelease += "-production-candidate"
	}
	if b.Target["go_release"] != goRelease || b.Target["frontend_release"] != frontRelease {
		return errors.New("target release mismatch")
	}
	return nil
}

func (s *Store) holder(ctx context.Context, tx *sql.Tx, b Binding) error {
	var holder, state, scope string
	var generation int64
	err := tx.QueryRowContext(ctx, `SELECT agent_id,generation,state,resource_scope FROM claims WHERE repo_id=? AND item_id=?`, s.repoID, b.ItemID).Scan(&holder, &generation, &state, &scope)
	if err != nil {
		return fmt.Errorf("live claim unavailable: %w", err)
	}
	// Empty scope is the legacy exclusive ENV-002 claim. No narrower permission is inferred.
	if scope == "" {
		scope = "*"
	}
	if holder != b.Holder || generation != b.Generation || state != "held" || scope != b.Scope {
		return errors.New("live claim holder, generation, state or scope mismatch")
	}
	return nil
}

func (s *Store) Authorize(ctx context.Context, b Binding, actor string) error {
	if actor != b.Holder {
		return errors.New("only the live holder can authorize execution")
	}
	if err := b.Validate(s.now().Unix()); err != nil {
		return err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		if err := s.holder(ctx, tx, b); err != nil {
			return err
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_authorizations WHERE repo_id=? AND item_id=? AND state='active'`, s.repoID, b.ItemID).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return errors.New("active execution requires reconciliation")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO execution_authorizations(repo_id,id,item_id,holder,generation,binding,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'authorized',?,?)`, s.repoID, b.ID, b.ItemID, b.Holder, b.Generation, string(raw), s.now().Unix(), s.now().Unix())
		return err
	})
}

func (s *Store) Admit(ctx context.Context, r Request, begin bool) (Receipt, error) {
	result := Receipt{}
	if err := r.Binding.Validate(s.now().Unix()); err != nil {
		return result, err
	}
	if r.RunID < 1 || r.RunAttempt != 1 || !ident.MatchString(r.Step) {
		return result, errors.New("invalid run/attempt/step")
	}
	raw, err := json.Marshal(r.Binding)
	if err != nil {
		return result, err
	}
	err = store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		var binding, state string
		var runID, attempt int64
		if err := tx.QueryRowContext(ctx, `SELECT binding,state,run_id,run_attempt FROM execution_authorizations WHERE repo_id=? AND id=?`, s.repoID, r.Binding.ID).Scan(&binding, &state, &runID, &attempt); err != nil {
			return err
		}
		if binding != string(raw) {
			return errors.New("authorization binding mismatch")
		}
		if err := s.holder(ctx, tx, r.Binding); err != nil {
			return err
		}
		if state == "authorized" && begin {
			_, err := tx.ExecContext(ctx, `UPDATE execution_authorizations SET state='active',run_id=?,run_attempt=?,last_step=?,updated_at=? WHERE repo_id=? AND id=?`, r.RunID, r.RunAttempt, r.Step, s.now().Unix(), s.repoID, r.Binding.ID)
			if err != nil {
				return err
			}
		} else if state != "active" || runID != r.RunID || attempt != r.RunAttempt {
			return errors.New("execution not active for this run")
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE execution_authorizations SET last_step=?,updated_at=? WHERE repo_id=? AND id=?`, r.Step, s.now().Unix(), s.repoID, r.Binding.ID); err != nil {
				return err
			}
		}
		result = Receipt{ID: r.Binding.ID, State: "active", RunID: r.RunID, RunAttempt: r.RunAttempt, Step: r.Step, CheckedAt: s.now().Unix(), ManifestSHA256: r.Binding.ManifestSHA256, Holder: r.Binding.Holder, Generation: r.Binding.Generation}
		return nil
	})
	return result, err
}

// Reconcile requires fresh external terminal evidence from the trusted control
// plane plus an explicit declaration that cluster work/child processes stopped.
// A runner has no HTTP endpoint for this operation. No timeout unpins ownership.
func (s *Store) Reconcile(ctx context.Context, id, actor, evidence string, runID, attempt int64, stopped bool) error {
	if !stopped || strings.TrimSpace(evidence) == "" || len(evidence) > 8192 || runID < 1 || attempt != 1 {
		return errors.New("terminal run and stopped external operation evidence required")
	}
	return store.WithTxRetry(ctx, s.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE execution_authorizations SET state='reconciled',reconciliation=?,updated_at=? WHERE repo_id=? AND id=? AND holder=? AND state='active' AND run_id=? AND run_attempt=?`, evidence, s.now().Unix(), s.repoID, id, actor, runID, attempt)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("active execution identity mismatch")
		}
		return nil
	})
}

func (s *Store) Binding(ctx context.Context, id string) (Binding, error) {
	var raw string
	var b Binding
	err := s.db.QueryRowContext(ctx, `SELECT binding FROM execution_authorizations WHERE repo_id=? AND id=?`, s.repoID, id).Scan(&raw)
	if err != nil {
		return b, err
	}
	err = json.Unmarshal([]byte(raw), &b)
	return b, err
}

// Snapshot is sanitized durable visibility, including a pin whose begin response
// was lost. Binding alone is not evidence that a run started or finished.
func (s *Store) Snapshot(ctx context.Context, id string) (map[string]any, error) {
	b, err := s.Binding(ctx, id)
	if err != nil {
		return nil, err
	}
	var state, step, evidence string
	var runID, attempt, updated int64
	err = s.db.QueryRowContext(ctx, `SELECT state,run_id,run_attempt,last_step,updated_at,reconciliation FROM execution_authorizations WHERE repo_id=? AND id=?`, s.repoID, id).Scan(&state, &runID, &attempt, &step, &updated, &evidence)
	if err != nil {
		return nil, err
	}
	return map[string]any{"binding": b, "state": state, "run_id": runID, "run_attempt": attempt, "last_step": step, "updated_at": updated, "reconciliation": evidence}, nil
}
