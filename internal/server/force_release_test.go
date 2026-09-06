package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zsiec/squad/internal/claims"
)

func TestForceRelease_PostHappyPath(t *testing.T) {
	db := newTestDB(t)
	registerAgent(t, db, "agent-aaaa", "Alice")
	registerAgent(t, db, "agent-bbbb", "Bob")
	insertClaim(t, db, "agent-aaaa", "BUG-700", "stuck claim")

	s := New(db, testRepoID, Config{RepoID: testRepoID, SquadDir: t.TempDir()})
	defer s.Close()

	body, _ := json.Marshal(map[string]any{"reason": "agent-aaaa offline 6h"})
	req := httptest.NewRequest(http.MethodPost, "/api/items/BUG-700/force-release", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Squad-Agent", "agent-bbbb")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["prior_holder"] != "agent-aaaa" {
		t.Fatalf("prior_holder=%v, want agent-aaaa", resp["prior_holder"])
	}

	// claim row removed
	holder, err := claims.HolderOf(context.Background(), db, testRepoID, "BUG-700")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("claim should be gone, got holder=%q err=%v", holder, err)
	}
}

func TestForceRelease_PostNoAgentHeader(t *testing.T) {
	db := newTestDB(t)
	s := New(db, testRepoID, Config{RepoID: testRepoID, SquadDir: t.TempDir()})
	defer s.Close()
	body, _ := json.Marshal(map[string]any{"reason": "x"})
	req := httptest.NewRequest(http.MethodPost, "/api/items/BUG-700/force-release", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestForceRelease_PostEmptyReasonRejected(t *testing.T) {
	db := newTestDB(t)
	registerAgent(t, db, "agent-bbbb", "Bob")
	s := New(db, testRepoID, Config{RepoID: testRepoID, SquadDir: t.TempDir()})
	defer s.Close()
	body, _ := json.Marshal(map[string]any{"reason": ""})
	req := httptest.NewRequest(http.MethodPost, "/api/items/BUG-700/force-release", bytes.NewReader(body))
	req.Header.Set("X-Squad-Agent", "agent-bbbb")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestForceRelease_ProtectedEnvironmentRejected(t *testing.T) {
	db := newTestDB(t)
	registerAgent(t, db, "agent-aaaa", "Alice")
	registerAgent(t, db, "agent-bbbb", "Bob")
	insertClaim(t, db, "agent-aaaa", "ENV-001", "deploy")
	s := New(db, testRepoID, Config{RepoID: testRepoID, SquadDir: t.TempDir()})
	defer s.Close()
	body, _ := json.Marshal(map[string]any{"reason": "holder stopped"})
	req := httptest.NewRequest(http.MethodPost, "/api/items/ENV-001/force-release", bytes.NewReader(body))
	req.Header.Set("X-Squad-Agent", "agent-bbbb")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rec.Code, rec.Body.String())
	}
	holder, err := claims.HolderOf(context.Background(), db, testRepoID, "ENV-001")
	if err != nil || holder != "agent-aaaa" {
		t.Fatalf("protected claim changed: holder=%q err=%v", holder, err)
	}
}
