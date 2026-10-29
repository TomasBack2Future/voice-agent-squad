package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type OperationQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// UnresolvedOperationKeys returns current unresolved requests across the exact
// assignment. Empty actor is for the read projection, never a mutation fence.
func UnresolvedOperationKeys(ctx context.Context, q OperationQuery, repo, actor, key, native string, generation int64) ([]string, error) {
	var encoded string
	err := q.QueryRowContext(ctx, `SELECT json_group_array(request_key) FROM (SELECT json_extract(m.body,'$.request.request_key') AS request_key FROM messages m WHERE m.repo_id=? AND (?='' OR m.agent_id=?) AND m.kind='owned-operation' AND json_valid(m.body) AND json_extract(m.body,'$.request.reservation')=? AND json_extract(m.body,'$.request.generation')=? AND json_extract(m.body,'$.request.worker_session')=? AND json_extract(m.body,'$.state') IN ('started','result-unavailable') AND NOT EXISTS(SELECT 1 FROM messages newer WHERE newer.repo_id=m.repo_id AND newer.agent_id=m.agent_id AND newer.kind=m.kind AND newer.id>m.id AND json_valid(newer.body) AND json_extract(newer.body,'$.request.reservation')=json_extract(m.body,'$.request.reservation') AND json_extract(newer.body,'$.request.generation')=json_extract(m.body,'$.request.generation') AND json_extract(newer.body,'$.request.worker_session')=json_extract(m.body,'$.request.worker_session') AND json_extract(newer.body,'$.request.request_key')=json_extract(m.body,'$.request.request_key')) ORDER BY m.id LIMIT 101)`, repo, actor, actor, key, generation, native).Scan(&encoded)
	if err != nil {
		return nil, err
	}
	var keys []string
	if err = json.Unmarshal([]byte(encoded), &keys); err != nil {
		return nil, err
	}
	if len(keys) > 100 {
		return nil, fmt.Errorf("unresolved operation projection exceeds bounded limit")
	}
	return keys, nil
}

// UnresolvedOperation checks custody and pending handling for the exact owner.
// It neither infers external liveness nor consumes the Worker's event.
func UnresolvedOperation(ctx context.Context, q OperationQuery, repo, actor, key, native string, generation int64) (bool, error) {
	keys, err := UnresolvedOperationKeys(ctx, q, repo, actor, key, native, generation)
	if err != nil || len(keys) > 0 {
		return len(keys) > 0, err
	}
	var n int
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM terminal_event_receipts WHERE repo_id=? AND recipient=? AND reservation_key=? AND generation=? AND worker_session=? AND kind='operation-exit' AND processed_at=0`, repo, actor, key, generation, native).Scan(&n)
	return n > 0, err
}
