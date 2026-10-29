package store

import (
	"context"
	"database/sql"
)

type OperationQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// UnresolvedOperation observes only this exact assignment/actor. It neither
// infers OS liveness nor consumes the owning Worker's event.
func UnresolvedOperation(ctx context.Context, q OperationQuery, repo, actor, key, native string, generation int64) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages m WHERE m.repo_id=? AND m.agent_id=? AND m.kind='owned-operation' AND json_valid(m.body) AND json_extract(m.body,'$.request.reservation')=? AND json_extract(m.body,'$.request.generation')=? AND json_extract(m.body,'$.request.worker_session')=? AND json_extract(m.body,'$.state') IN ('started','result-unavailable') AND NOT EXISTS(SELECT 1 FROM messages newer WHERE newer.repo_id=m.repo_id AND newer.agent_id=m.agent_id AND newer.kind=m.kind AND newer.id>m.id AND json_valid(newer.body) AND json_extract(newer.body,'$.request.reservation')=json_extract(m.body,'$.request.reservation') AND json_extract(newer.body,'$.request.generation')=json_extract(m.body,'$.request.generation') AND json_extract(newer.body,'$.request.worker_session')=json_extract(m.body,'$.request.worker_session') AND json_extract(newer.body,'$.request.request_key')=json_extract(m.body,'$.request.request_key'))) OR EXISTS(SELECT 1 FROM terminal_event_receipts e WHERE e.repo_id=? AND e.recipient=? AND e.reservation_key=? AND e.generation=? AND e.worker_session=? AND e.kind='operation-exit' AND e.processed_at=0)`, repo, actor, key, generation, native, repo, actor, key, generation, native).Scan(&n)
	return n != 0, err
}
