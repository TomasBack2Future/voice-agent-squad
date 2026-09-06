package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/zsiec/squad/internal/claims"
)

type forceReleaseReq struct {
	Reason string `json:"reason"`
}

func (s *Server) handleItemForceRelease(w http.ResponseWriter, r *http.Request) {
	agent := s.actingAgent(r)
	if agent == "" {
		writeErr(w, http.StatusBadRequest, "X-Squad-Agent header required")
		return
	}
	id := r.PathValue("id")
	var req forceReleaseReq
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeErr(w, http.StatusBadRequest, "reason required")
		return
	}

	repoID, _, statusCode, rerr := s.resolveItemRepo(r.Context(), id, r.URL.Query().Get("repo_id"))
	if rerr != nil {
		writeResolveErr(w, statusCode, rerr)
		return
	}
	store := claims.New(s.db, repoID, nil)
	prior, err := store.ForceRelease(r.Context(), id, agent, req.Reason)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "prior_holder": prior})
	case errors.Is(err, claims.ErrNotClaimed):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, claims.ErrReasonRequired):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, claims.ErrProtectedResource):
		writeErr(w, http.StatusConflict, "protected environment requires fenced recovery")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}
