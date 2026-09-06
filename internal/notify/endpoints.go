package notify

import (
	"context"
	"database/sql"
	"fmt"
)

const (
	KindListen      = "listen"
	KindRewake      = "rewake"
	claimWaitPrefix = "claim-wait:"
)

func ClaimWaitKind(itemID string) string {
	return claimWaitPrefix + itemID
}

type Endpoint struct {
	Instance  string
	RepoID    string
	Kind      string
	Port      int
	StartedAt int64
}

type Registry struct {
	db  *sql.DB
	now func() int64
}

func NewRegistry(db *sql.DB) *Registry {
	return &Registry{db: db, now: defaultNow}
}

func (r *Registry) Register(ctx context.Context, e Endpoint) error {
	if e.Instance == "" || e.RepoID == "" || e.Kind == "" || e.Port == 0 {
		return fmt.Errorf("notify register: instance, repo_id, kind, port all required")
	}
	ts := e.StartedAt
	if ts == 0 {
		ts = r.now()
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO notify_endpoints (instance, repo_id, kind, port, started_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(instance, kind) DO UPDATE SET
			repo_id = excluded.repo_id,
			port = excluded.port,
			started_at = excluded.started_at
	`, e.Instance, e.RepoID, e.Kind, e.Port, ts)
	return err
}

func (r *Registry) Unregister(ctx context.Context, instance, kind string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM notify_endpoints WHERE instance = ? AND kind = ?`,
		instance, kind)
	return err
}

func (r *Registry) UnregisterInstance(ctx context.Context, instance string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM notify_endpoints WHERE instance = ?`, instance)
	return err
}

func (r *Registry) LookupRepo(ctx context.Context, repoID string) ([]Endpoint, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT instance, repo_id, kind, port, started_at
		   FROM notify_endpoints WHERE repo_id = ?`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Endpoint
	for rows.Next() {
		var e Endpoint
		if err := rows.Scan(&e.Instance, &e.RepoID, &e.Kind, &e.Port, &e.StartedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
