package main

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/zsiec/squad/internal/mcp"
	"github.com/zsiec/squad/internal/terminalevents"
)

func registerDecisionTools(srv *mcp.Server, db *sql.DB, repoID, repoRoot string) {
	srv.Register(mcp.Tool{Name: "squad_terminal_decision_set", Description: "CAS the current decision and atomically enqueue a Worker wake. Existing scope and claims are unchanged.", InputSchema: json.RawMessage(`{"type":"object","required":["reservation","generation","worker_session","expected_revision","outcome_id","action"],"properties":{"reservation":{"type":"string"},"generation":{"type":"integer","minimum":1},"worker_session":{"type":"string"},"expected_revision":{"type":"integer","minimum":0},"outcome_id":{"type":"integer","minimum":1},"action":{"enum":["proceed","hold"]},"condition":{"type":"string","maxLength":500},"agent_id":{"type":"string"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			terminalevents.DecisionRequest
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		if err := requireRepo(repoRoot, repoID); err != nil {
			return nil, err
		}
		actor, err := resolveAgentID(a.AgentID)
		if err != nil {
			return nil, err
		}
		return (terminalevents.Store{DB: db, Repo: repoID}).Decide(ctx, actor, a.DecisionRequest)
	}})
	srv.Register(mcp.Tool{Name: "squad_terminal_decision_get", Description: "Read the latest decision for an exact live assignment.", InputSchema: json.RawMessage(`{"type":"object","required":["reservation","generation","worker_session"],"properties":{"reservation":{"type":"string"},"generation":{"type":"integer","minimum":1},"worker_session":{"type":"string"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var q terminalevents.DecisionRequest
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, err
		}
		if err := requireRepo(repoRoot, repoID); err != nil {
			return nil, err
		}
		return (terminalevents.Store{DB: db, Repo: repoID}).CurrentDecision(ctx, q.Reservation, q.Generation, q.WorkerSession)
	}})
}
