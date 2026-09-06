package server

import (
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"

	"github.com/zsiec/squad/internal/items"
)

type itemListRow struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Type             string   `json:"type"`
	Priority         string   `json:"priority"`
	Area             string   `json:"area"`
	Status           string   `json:"status"`
	Estimate         string   `json:"estimate"`
	Risk             string   `json:"risk"`
	Created          string   `json:"created"`
	Updated          string   `json:"updated"`
	ACTotal          int      `json:"ac_total"`
	ACChecked        int      `json:"ac_checked"`
	Progress         int      `json:"progress_pct"`
	Epic             string   `json:"epic"`
	DependsOn        []string `json:"depends_on"`
	Parallel         bool     `json:"parallel"`
	EvidenceRequired []string `json:"evidence_required"`
	ClaimedBy        string   `json:"claimed_by"`
	LastTouch        int64    `json:"last_touch"`
	ClaimState       string   `json:"claim_state,omitempty"`
	ClaimGeneration  int64    `json:"claim_generation,omitempty"`
	RepoID           string   `json:"repo_id"`
}

// itemWithRepo carries a parsed Item alongside the repo it was walked
// from. In single-repo mode RepoID matches s.cfg.RepoID; in workspace
// mode it's the per-repo identifier from the global DB's `repos` table.
type itemWithRepo struct {
	items.Item
	RepoID string
}

// walkAll returns all active + done items in the configured scope.
// Single-repo mode (cfg.RepoID set): walks s.cfg.SquadDir.
// Workspace mode (cfg.RepoID empty): enumerates every repo from the
// global DB's `repos` table and walks each repo's `<root>/.squad/`,
// tagging items with their per-repo identifier so the API/SPA can
// disambiguate.
func (s *Server) walkAll() ([]itemWithRepo, error) {
	if s.cfg.RepoID != "" {
		w, err := items.Walk(s.cfg.SquadDir)
		if err != nil {
			return nil, err
		}
		out := make([]itemWithRepo, 0, len(w.Active)+len(w.Done))
		for _, it := range w.Active {
			out = append(out, itemWithRepo{Item: it, RepoID: s.cfg.RepoID})
		}
		for _, it := range w.Done {
			out = append(out, itemWithRepo{Item: it, RepoID: s.cfg.RepoID})
		}
		return out, nil
	}
	rows, err := s.db.Query(`SELECT id, COALESCE(root_path, '') FROM repos ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []itemWithRepo
	for rows.Next() {
		var id, root string
		if err := rows.Scan(&id, &root); err != nil {
			continue
		}
		if root == "" {
			continue
		}
		w, err := items.Walk(filepath.Join(root, ".squad"))
		if err != nil {
			// Skip repos whose .squad/ is unreadable rather than failing the
			// whole aggregation — operators see partial data instead of an
			// opaque 500 when one repo is missing.
			continue
		}
		for _, it := range w.Active {
			out = append(out, itemWithRepo{Item: it, RepoID: id})
		}
		for _, it := range w.Done {
			out = append(out, itemWithRepo{Item: it, RepoID: id})
		}
	}
	return out, rows.Err()
}

func (s *Server) handleItemsList(w http.ResponseWriter, r *http.Request) {
	all, err := s.walkAll()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	type claimKey struct{ Repo, Item string }
	type claimInfo struct {
		Agent      string
		LastTouch  int64
		State      string
		Generation int64
	}
	claimByItem := map[claimKey]claimInfo{}
	// Workspace mode (cfg.RepoID == "") needs all repos' claims; single-repo
	// mode keeps the existing scoped query.
	var (
		rows *sql.Rows
		qErr error
	)
	if s.cfg.RepoID == "" {
		rows, qErr = s.db.QueryContext(r.Context(),
			`SELECT repo_id, item_id, agent_id, last_touch, state, generation FROM claims`)
	} else {
		rows, qErr = s.db.QueryContext(r.Context(),
			`SELECT repo_id, item_id, agent_id, last_touch, state, generation FROM claims WHERE repo_id = ?`, s.cfg.RepoID)
	}
	if qErr != nil {
		writeErr(w, http.StatusInternalServerError, qErr.Error())
		return
	}
	for rows.Next() {
		var repoID, id, agent, state string
		var lt, generation int64
		if err := rows.Scan(&repoID, &id, &agent, &lt, &state, &generation); err == nil {
			claimByItem[claimKey{Repo: repoID, Item: id}] = claimInfo{
				Agent: agent, LastTouch: lt, State: state, Generation: generation,
			}
		}
	}
	rows.Close()

	statusFilter := r.URL.Query().Get("status")
	epicFilter := r.URL.Query().Get("epic")
	repoFilter := r.URL.Query().Get("repo_id")
	out := make([]itemListRow, 0, len(all))
	for _, it := range all {
		if statusFilter != "" && it.Status != statusFilter {
			continue
		}
		if epicFilter != "" && it.Epic != epicFilter {
			continue
		}
		if repoFilter != "" && it.RepoID != repoFilter {
			continue
		}
		deps := it.DependsOn
		if deps == nil {
			deps = []string{}
		}
		evReq := it.EvidenceRequired
		if evReq == nil {
			evReq = []string{}
		}
		row := itemListRow{
			ID: it.ID, Title: it.Title, Type: it.Type, Priority: it.Priority,
			Area: it.Area, Status: it.Status, Estimate: it.Estimate, Risk: it.Risk,
			Created: it.Created, Updated: it.Updated,
			ACTotal: it.ACTotal, ACChecked: it.ACChecked, Progress: it.ProgressPct(),
			Epic: it.Epic, DependsOn: deps, Parallel: it.Parallel, EvidenceRequired: evReq,
			RepoID: it.RepoID,
		}
		if c, ok := claimByItem[claimKey{Repo: it.RepoID, Item: it.ID}]; ok {
			row.ClaimedBy = c.Agent
			row.LastTouch = c.LastTouch
			row.ClaimState = c.State
			row.ClaimGeneration = c.Generation
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleItemDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// resolveItemRepo scopes the lookup to a single repo so collisions
	// across repos surface as 409 AmbiguousRepoError instead of silently
	// returning the first match in walk order.
	repoID, squadDir, statusCode, rerr := s.resolveItemRepo(r.Context(), id, r.URL.Query().Get("repo_id"))
	if rerr != nil {
		writeResolveErr(w, statusCode, rerr)
		return
	}
	walk, err := items.Walk(squadDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var it *items.Item
	for _, group := range [][]items.Item{walk.Active, walk.Done} {
		for i := range group {
			if group[i].ID == id {
				it = &group[i]
				break
			}
		}
		if it != nil {
			break
		}
	}
	if it == nil {
		writeErr(w, http.StatusNotFound, "item not found: "+id)
		return
	}

	var currentClaim any
	var lastTouch int64
	row := s.db.QueryRowContext(r.Context(),
		`SELECT agent_id, COALESCE(intent, ''), claimed_at, last_touch,
		        COALESCE(worktree, ''), state, generation, previous_agent_id
		 FROM claims WHERE item_id = ? AND repo_id = ?`,
		id, repoID)
	var cc struct {
		AgentID         string `json:"agent_id"`
		Intent          string `json:"intent"`
		ClaimedAt       int64  `json:"claimed_at"`
		Worktree        string `json:"worktree,omitempty"`
		State           string `json:"state"`
		Generation      int64  `json:"generation"`
		PreviousAgentID string `json:"previous_agent_id,omitempty"`
	}
	switch err := row.Scan(&cc.AgentID, &cc.Intent, &cc.ClaimedAt, &lastTouch,
		&cc.Worktree, &cc.State, &cc.Generation, &cc.PreviousAgentID); {
	case err == nil:
		currentClaim = cc
	case errors.Is(err, sql.ErrNoRows):
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if it.ACItems == nil {
		it.ACItems = []items.ACItem{}
	}
	if it.BlockedBy == nil {
		it.BlockedBy = []string{}
	}
	if it.RelatesTo == nil {
		it.RelatesTo = []string{}
	}
	if it.References == nil {
		it.References = []string{}
	}
	deps := it.DependsOn
	if deps == nil {
		deps = []string{}
	}
	evReq := it.EvidenceRequired
	if evReq == nil {
		evReq = []string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id": it.ID, "title": it.Title, "type": it.Type, "priority": it.Priority,
		"area": it.Area, "status": it.Status, "estimate": it.Estimate, "risk": it.Risk,
		"created": it.Created, "updated": it.Updated,
		"ac_total": it.ACTotal, "ac_checked": it.ACChecked, "progress_pct": it.ProgressPct(),
		"body_markdown": it.Body, "ac": it.ACItems,
		"blocked_by": it.BlockedBy, "relates_to": it.RelatesTo, "references": it.References,
		"current_claim":     currentClaim,
		"epic":              it.Epic,
		"depends_on":        deps,
		"parallel":          it.Parallel,
		"evidence_required": evReq,
		"last_touch":        lastTouch,
		"repo_id":           repoID,
	})
}
