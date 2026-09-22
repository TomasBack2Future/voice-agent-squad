package claims

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type WaitEdge struct {
	AgentID       string `json:"agent_id"`
	RequestedItem string `json:"requested_item"`
	BlockingItem  string `json:"blocking_item"`
	Holder        string `json:"holder"`
}
type DeadlockError struct {
	Cycle []WaitEdge `json:"cycle"`
}

func (e *DeadlockError) Error() string { return fmt.Sprintf("claim deadlock: %v", e.Cycle) }

// Wait records a leased wait and checks the entire live wait-for graph in the
// same write transaction. A rejected edge is rolled back; claims never change.
func (s *Store) Wait(ctx context.Context, id, agent, item, scope string, ttl time.Duration) ([]Blocker, error) {
	if id == "" || agent == "" || ttl <= 0 {
		return nil, fmt.Errorf("invalid claim wait")
	}
	var blockers []Blocker
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM claim_waits WHERE repo_id=? AND expires_at<=?`, s.repoID, s.nowUnix()); err != nil {
			return err
		}
		group, resolved, err := resolveResource(ctx, tx, s.repoID, item, scope)
		if err != nil {
			return err
		}
		blockers, err = resourceBlockers(ctx, tx, s.repoID, item, group, resolved)
		if err != nil {
			return err
		}
		for _, b := range blockers {
			if b.AgentID == agent {
				return ErrAlreadyHeld
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO claim_waits(repo_id,wait_id,agent_id,item_id,resource_group,resource_scope,expires_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(repo_id,wait_id) DO UPDATE SET agent_id=excluded.agent_id,item_id=excluded.item_id,resource_group=excluded.resource_group,resource_scope=excluded.resource_scope,expires_at=excluded.expires_at`, s.repoID, id, agent, item, group, resolved, s.now().Add(ttl).Unix()); err != nil {
			return err
		}
		cycle, err := deadlockCycle(ctx, tx, s.repoID, s.nowUnix())
		if err != nil {
			return err
		}
		if len(cycle) > 0 {
			return &DeadlockError{Cycle: cycle}
		}
		return nil
	})
	return blockers, err
}
func (s *Store) EndWait(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM claim_waits WHERE repo_id=? AND wait_id=?`, s.repoID, id)
	return err
}
func (s *Store) Deadlock(ctx context.Context) ([]WaitEdge, error) {
	return deadlockCycle(ctx, s.db, s.repoID, s.nowUnix())
}
func deadlockCycle(ctx context.Context, q queryer, repo string, now int64) ([]WaitEdge, error) {
	rows, err := q.QueryContext(ctx, `SELECT w.agent_id,w.item_id,c.item_id,c.agent_id FROM claim_waits w JOIN claims c ON c.repo_id=w.repo_id AND (c.item_id=w.item_id OR (w.resource_group!='' AND w.resource_group=c.resource_group AND (w.resource_scope='*' OR c.resource_scope='*' OR w.resource_scope=c.resource_scope))) WHERE w.repo_id=? AND w.expires_at>? ORDER BY w.agent_id,w.item_id,c.item_id`, repo, now)
	if err != nil {
		return nil, err
	}
	edges := map[string][]WaitEdge{}
	var agents []string
	for rows.Next() {
		var e WaitEdge
		if err := rows.Scan(&e.AgentID, &e.RequestedItem, &e.BlockingItem, &e.Holder); err != nil {
			rows.Close()
			return nil, err
		}
		if _, ok := edges[e.AgentID]; !ok {
			agents = append(agents, e.AgentID)
		}
		edges[e.AgentID] = append(edges[e.AgentID], e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	color := map[string]int{}
	positions := map[string]int{}
	var path, cycle []WaitEdge
	var visit func(string) bool
	visit = func(agent string) bool {
		color[agent] = 1
		positions[agent] = len(path)
		for _, e := range edges[agent] {
			path = append(path, e)
			if color[e.Holder] == 1 {
				cycle = append([]WaitEdge(nil), path[positions[e.Holder]:]...)
				return true
			}
			if color[e.Holder] == 0 && visit(e.Holder) {
				return true
			}
			path = path[:len(path)-1]
		}
		color[agent] = 2
		return false
	}
	for _, agent := range agents {
		if color[agent] == 0 && visit(agent) {
			return cycle, nil
		}
	}
	return nil, nil
}
