package claims

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var resourceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// ResourceDefinition is ledger-owned policy, never a hardcoded product mapping.
type ResourceDefinition struct {
	ItemID       string `json:"item_id"`
	Group        string `json:"group"`
	DefaultScope string `json:"default_scope"`
	ServiceScope string `json:"service_scope"`
}

// DefineResources installs one complete policy update atomically, only while
// affected groups are idle. Existing definitions outside this set are retained.
func (s *Store) DefineResources(ctx context.Context, definitions []ResourceDefinition) error {
	if len(definitions) == 0 {
		return errors.New("resource definitions cannot be empty")
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, d := range definitions {
			if !strings.HasPrefix(d.ItemID, "ENV-") || !resourceName.MatchString(d.Group) || !resourceName.MatchString(d.ServiceScope) || (d.DefaultScope != "*" && d.DefaultScope != d.ServiceScope) {
				return errors.New("invalid protected resource definition")
			}
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM claims WHERE repo_id=? AND (item_id=? OR resource_group=? OR resource_group=(SELECT resource_group FROM resource_definitions WHERE repo_id=? AND item_id=?))`, s.repoID, d.ItemID, d.Group, s.repoID, d.ItemID).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return errors.New("resource policy change requires idle affected claims")
			}
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM claim_waits WHERE repo_id=? AND expires_at>? AND (item_id=? OR resource_group=? OR resource_group IN (SELECT resource_group FROM resource_definitions WHERE repo_id=? AND item_id=?))`, s.repoID, s.nowUnix(), d.ItemID, d.Group, s.repoID, d.ItemID).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return errors.New("resource policy change requires idle affected waiters")
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO resource_definitions(repo_id,item_id,resource_group,default_scope,service_scope) VALUES(?,?,?,?,?) ON CONFLICT(repo_id,item_id) DO UPDATE SET resource_group=excluded.resource_group,default_scope=excluded.default_scope,service_scope=excluded.service_scope`, s.repoID, d.ItemID, d.Group, d.DefaultScope, d.ServiceScope); err != nil {
				return err
			}
		}
		return nil
	})
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func resolveResource(ctx context.Context, q queryer, repo, item, scope string) (string, string, error) {
	var group, def, service string
	err := q.QueryRowContext(ctx, `SELECT resource_group,default_scope,service_scope FROM resource_definitions WHERE repo_id=? AND item_id=?`, repo, item).Scan(&group, &def, &service)
	if errors.Is(err, sql.ErrNoRows) {
		if scope != "" {
			return "", "", errors.New("scope requires a registered environment resource")
		}
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if scope == "" {
		scope = def
	}
	if scope != "*" && scope != service {
		return "", "", errors.New("scope does not match the registered service")
	}
	return group, scope, nil
}

type Blocker struct {
	ItemID  string `json:"item_id"`
	AgentID string `json:"agent_id"`
}
type ResourceConflictError struct{ Blockers []Blocker }

func (e *ResourceConflictError) Error() string {
	return fmt.Sprintf("resource claim blocked by %v", e.Blockers)
}

var ErrAlreadyHeld = errors.New("claim already held by this agent; do not wait on yourself")

func resourceBlockers(ctx context.Context, q queryer, repo, item, group, scope string) ([]Blocker, error) {
	rows, err := q.QueryContext(ctx, `SELECT item_id,agent_id FROM claims WHERE repo_id=? AND (item_id=? OR (?!='' AND resource_group=? AND (resource_scope='*' OR ?='*' OR resource_scope=?))) ORDER BY item_id`, repo, item, group, group, scope, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Blocker
	for rows.Next() {
		var b Blocker
		if err := rows.Scan(&b.ItemID, &b.AgentID); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

func ClaimWithScope(scope string) ClaimOption { return func(o *claimOpts) { o.resourceScope = scope } }
