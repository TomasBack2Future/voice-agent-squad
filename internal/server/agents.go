package server

import (
	"database/sql"
	"net/http"
)

type agentRow struct {
	AgentID     string `json:"agent_id"`
	DisplayName string `json:"display_name"`
	Worktree    string `json:"worktree"`
	LastTickAt  int64  `json:"last_tick_at"`
	Status      string `json:"status"`
	RepoID      string `json:"repo_id"`
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	// Workspace mode (cfg.RepoID == "") returns agents across all repos;
	// single-repo mode keeps the scoped query.
	var (
		rows *sql.Rows
		err  error
	)
	if s.cfg.RepoID == "" {
		rows, err = s.db.QueryContext(r.Context(), `
			SELECT id, COALESCE(display_name,''), COALESCE(worktree,''), last_tick_at, status, repo_id
			FROM agents ORDER BY last_tick_at DESC
		`)
	} else {
		rows, err = s.db.QueryContext(r.Context(), `
			SELECT id, COALESCE(display_name,''), COALESCE(worktree,''), last_tick_at, status, repo_id
			FROM agents WHERE repo_id = ? ORDER BY last_tick_at DESC
		`, s.cfg.RepoID)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []agentRow{}
	for rows.Next() {
		var a agentRow
		if err := rows.Scan(&a.AgentID, &a.DisplayName, &a.Worktree, &a.LastTickAt, &a.Status, &a.RepoID); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	agent := s.actingAgent(r)
	display := agent
	if agent != "" {
		var name sql.NullString
		err := s.db.QueryRowContext(r.Context(),
			`SELECT display_name FROM agents WHERE id = ?`, agent).Scan(&name)
		if err == nil && name.Valid && name.String != "" {
			display = name.String
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agent, "display_name": display})
}

type claimWireRow struct {
	ItemID          string `json:"item_id"`
	AgentID         string `json:"agent_id"`
	Intent          string `json:"intent"`
	ClaimedAt       int64  `json:"claimed_at"`
	LastTouch       int64  `json:"last_touch"`
	Worktree        string `json:"worktree,omitempty"`
	RepoID          string `json:"repo_id"`
	State           string `json:"state"`
	Generation      int64  `json:"generation"`
	PreviousAgentID string `json:"previous_agent_id,omitempty"`
}

func (s *Server) handleClaims(w http.ResponseWriter, r *http.Request) {
	var (
		rows *sql.Rows
		err  error
	)
	if s.cfg.RepoID == "" {
		rows, err = s.db.QueryContext(r.Context(), `
			SELECT item_id, agent_id, COALESCE(intent, ''), claimed_at, last_touch,
			       COALESCE(worktree, ''), repo_id, state, generation, previous_agent_id
			FROM claims ORDER BY claimed_at
		`)
	} else {
		rows, err = s.db.QueryContext(r.Context(), `
			SELECT item_id, agent_id, COALESCE(intent, ''), claimed_at, last_touch,
			       COALESCE(worktree, ''), repo_id, state, generation, previous_agent_id
			FROM claims WHERE repo_id = ? ORDER BY claimed_at
		`, s.cfg.RepoID)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []claimWireRow{}
	for rows.Next() {
		var c claimWireRow
		if err := rows.Scan(&c.ItemID, &c.AgentID, &c.Intent, &c.ClaimedAt, &c.LastTouch,
			&c.Worktree, &c.RepoID, &c.State, &c.Generation, &c.PreviousAgentID); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, out)
}
