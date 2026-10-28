package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// WorkerExecutionInspection reads the exact immutable pin after an ambiguous
// acquire response, including after expiry or a custody transition.
type WorkerExecutionInspection struct {
	Status string `json:"status"`
	Action string `json:"action"`
	ID     string `json:"id"`
	State  string `json:"state"`
	Gate   string `json:"gate,omitempty"`
}

func (s *Store) InspectWorkerExecution(ctx context.Context, actor string, b WorkerExecutionBinding) (WorkerExecutionInspection, error) {
	var out WorkerExecutionInspection
	if err := b.validate(actor); err != nil {
		return out, err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return out, err
	}
	var binding, holder, item, state, gate string
	var generation int64
	err = s.db.QueryRowContext(ctx, `SELECT binding,holder,item_id,generation,state,last_step FROM execution_authorizations WHERE repo_id=? AND id=?`, s.repoID, b.ID).Scan(&binding, &holder, &item, &generation, &state, &gate)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkerExecutionInspection{Status: "ok", Action: "inspect", ID: b.ID, State: "absent"}, nil
	}
	if err != nil {
		return out, err
	}
	if binding != string(raw) || holder != actor || item != b.Item || generation != b.ClaimGeneration {
		return out, errors.New("worker execution inspection immutable binding or holder changed")
	}
	if (state != "active" && state != "reconciled") || (gate != "gate-open" && gate != "gate-closed") || (state == "reconciled" && gate != "gate-closed") {
		return out, errors.New("worker execution inspection state or gate is unsupported")
	}
	return WorkerExecutionInspection{Status: "ok", Action: "inspect", ID: b.ID, State: state, Gate: gate}, nil
}
