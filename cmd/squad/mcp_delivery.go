package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"

	"github.com/zsiec/squad/internal/closeout"
	"github.com/zsiec/squad/internal/terminalevents"

	"github.com/zsiec/squad/internal/dispatch"
	"github.com/zsiec/squad/internal/mcp"
)

func registerDeliveryTools(srv *mcp.Server, db *sql.DB, repoID, repoRoot string) {
	srv.Register(mcp.Tool{Name: "squad_closeout", Description: "Explicit local-only resumable closeout; runs existing done gates, upserts evidence comment, closes Issue and submits one outcome.", InputSchema: json.RawMessage(`{"type":"object","required":["request"],"properties":{"request":{"type":"object"},"agent_id":{"type":"string"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			Request closeout.Request `json:"request"`
			AgentID string           `json:"agent_id"`
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
		if err := requireBoundNative(a.Request.WorkerSession); err != nil {
			return nil, err
		}
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		return (closeout.Engine{DB: db, Repo: repoID, Actor: actor, Backend: closeout.LocalBackend{DB: db, Repo: repoID, Actor: actor, Directory: repoRoot, Executable: executable}}).Run(ctx, a.Request)
	}})
	srv.Register(mcp.Tool{Name: "squad_operation_run", Description: "Local-only owned command wrapper: persists start/exit and delivers one original-native operation-exit observation. Never implicitly reruns an unresolved start.", InputSchema: json.RawMessage(`{"type":"object","required":["operation"],"properties":{"operation":{"type":"object"},"agent_id":{"type":"string"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			Operation operationInput `json:"operation"`
			AgentID   string         `json:"agent_id"`
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
		return runOwnedOperation(ctx, &claimContext{db: db, repoID: repoID, agentID: actor}, repoRoot, a.Operation)
	}})
	srv.Register(mcp.Tool{Name: "squad_operation_status", Description: "Read one original actor's owned operation record; a start without a result stays unresolved.", InputSchema: json.RawMessage(`{"type":"object","required":["request"],"properties":{"request":{"type":"object"},"agent_id":{"type":"string"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			Request terminalevents.OperationRequest `json:"request"`
			AgentID string                          `json:"agent_id"`
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
		return (terminalevents.Store{DB: db, Repo: repoID}).Operation(ctx, actor, a.Request)
	}})

	srv.Register(mcp.Tool{Name: "squad_dispatch_delivery", Description: "Read one reservation's Issue, item, custody, result and drift; no ledger writes. Observations expire after five minutes.", InputSchema: json.RawMessage(`{"type":"object","required":["reservation"],"properties":{"reservation":{"type":"string"},"issue":{"type":"object","properties":{"source_ref":{"type":"string"},"state":{"enum":["OPEN","CLOSED"]},"verified_at":{"type":"integer"}},"additionalProperties":false}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			Reservation string                    `json:"reservation"`
			Issue       dispatch.IssueObservation `json:"issue"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		if err := requireRepo(repoRoot, repoID); err != nil {
			return nil, err
		}
		return dispatch.New(db, repoID, nil).Delivery(ctx, a.Reservation, a.Issue)
	}})
	srv.Register(mcp.Tool{Name: "squad_dispatch_retire_partial", Description: "Owner-only audited retirement of a held, blocked execution; keeps Issue open and permits a new generation.", InputSchema: json.RawMessage(`{"type":"object","required":["request"],"properties":{"request":{"type":"object"},"agent_id":{"type":"string"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			Request dispatch.PartialRetirement `json:"request"`
			AgentID string                     `json:"agent_id"`
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
		if err := requireControllerNative(ctx, db, repoID, actor); err != nil {
			return nil, err
		}
		return dispatch.New(db, repoID, nil).RetirePartial(ctx, actor, a.Request)
	}})
}
