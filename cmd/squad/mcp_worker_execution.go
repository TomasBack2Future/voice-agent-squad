package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"

	"github.com/zsiec/squad/internal/dispatch"
	"github.com/zsiec/squad/internal/identity"
	"github.com/zsiec/squad/internal/mcp"
)

func registerWorkerExecutionTools(srv *mcp.Server, db *sql.DB, repoID, repoRoot string) {
	for _, action := range []string{"acquire", "check", "check-read", "check-write", "suspend"} {
		srv.Register(mcp.Tool{Name: "squad_worker_execution_" + action,
			Description: "Source Worker execution " + action + " under immutable native/claim/reservation/controller custody; suspension retains the execution pin.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"binding":{"type":"object"}},"required":["binding"],"additionalProperties":false}`),
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				if err := requireRepo(repoRoot, repoID); err != nil {
					return nil, err
				}
				var q struct {
					Binding dispatch.WorkerExecutionBinding `json:"binding"`
				}
				d := json.NewDecoder(bytes.NewReader(raw))
				d.DisallowUnknownFields()
				if err := d.Decode(&q); err != nil {
					return nil, err
				}
				if err := d.Decode(new(any)); err != io.EOF {
					return nil, errors.New("expected one Worker binding")
				}
				actor, err := identity.AgentID()
				if err != nil {
					return nil, err
				}
				s := dispatch.New(db, repoID, nil)
				switch action {
				case "acquire":
					err = s.AcquireWorkerExecution(ctx, actor, q.Binding)
				case "check":
					err = s.CheckWorkerExecution(ctx, actor, q.Binding)
				case "check-read":
					err = s.CheckWorkerRead(ctx, actor, q.Binding)
				case "check-write":
					err = s.CheckWorkerWrite(ctx, actor, q.Binding)
				case "suspend":
					err = s.SuspendWorkerExecution(ctx, actor, q.Binding)
				}
				if err != nil {
					return nil, err
				}
				return map[string]string{"status": "ok", "action": action, "id": q.Binding.ID}, nil
			}})
	}
}
