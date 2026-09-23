package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"

	"github.com/zsiec/squad/internal/execution"
	"github.com/zsiec/squad/internal/identity"
	"github.com/zsiec/squad/internal/mcp"
)

func registerExecutionTools(srv *mcp.Server, db *sql.DB, repoID, repoRoot string) {
	for _, action := range []string{"authorize", "show", "reconcile"} {
		schema := `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`
		if action == "authorize" {
			schema = `{"type":"object","properties":{"binding":{"type":"object"}},"required":["binding"],"additionalProperties":false}`
		}
		if action == "reconcile" {
			schema = `{"type":"object","properties":{"id":{"type":"string"},"run_id":{"type":"integer"},"run_attempt":{"type":"integer"},"evidence":{"type":"string"},"confirm_external_stopped":{"type":"boolean"}},"required":["id","run_id","run_attempt","evidence","confirm_external_stopped"],"additionalProperties":false}`
		}
		srv.Register(mcp.Tool{Name: "squad_execution_" + action, Description: "Production execution " + action + " against the authoritative ledger; reconciliation verifies terminal GitHub identity and requires external-stop evidence.", InputSchema: json.RawMessage(schema), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			if err := requireRepo(repoRoot, repoID); err != nil {
				return nil, err
			}
			var a struct {
				Binding    execution.Binding `json:"binding"`
				ID         string            `json:"id"`
				RunID      int64             `json:"run_id"`
				RunAttempt int64             `json:"run_attempt"`
				Evidence   string            `json:"evidence"`
				Stopped    bool              `json:"confirm_external_stopped"`
			}
			d := json.NewDecoder(bytes.NewReader(raw))
			d.DisallowUnknownFields()
			if err := d.Decode(&a); err != nil {
				return nil, err
			}
			if err := d.Decode(new(any)); err != io.EOF {
				return nil, errors.New("unexpected trailing JSON")
			}
			s := execution.New(db, repoID, nil)
			if action == "show" {
				return s.Snapshot(ctx, a.ID)
			}
			actor, err := identity.AgentID()
			if err != nil {
				return nil, err
			}
			if action == "authorize" {
				err = s.Authorize(ctx, a.Binding, actor)
			} else {
				b, readErr := s.Binding(ctx, a.ID)
				if readErr != nil {
					return nil, readErr
				}
				if err = execution.VerifyGitHubRun(ctx, b, a.RunID, a.RunAttempt, true); err == nil {
					err = s.Reconcile(ctx, a.ID, actor, a.Evidence, a.RunID, a.RunAttempt, a.Stopped)
				}
			}
			if err != nil {
				return nil, err
			}
			return map[string]string{"status": "ok"}, nil
		}})
	}
}
