package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgents_List(t *testing.T) {
	db := newTestDB(t)
	registerAgent(t, db, "agent-aaaa", "Alice")
	s := New(db, testRepoID, Config{RepoID: testRepoID})
	defer s.Close()
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	var out []map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 1 || out[0]["agent_id"] != "agent-aaaa" {
		t.Fatalf("got %v", out)
	}
}

func TestClaims_List(t *testing.T) {
	db := newTestDB(t)
	registerAgent(t, db, "agent-aaaa", "Alice")
	insertClaim(t, db, "agent-aaaa", "BUG-100", "fix it")
	s := New(db, testRepoID, Config{RepoID: testRepoID})
	defer s.Close()
	req := httptest.NewRequest(http.MethodGet, "/api/claims", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var out []map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 1 || out[0]["item_id"] != "BUG-100" || out[0]["intent"] != "fix it" || out[0]["state"] != "held" || out[0]["generation"] != float64(1) {
		t.Fatalf("got %v", out)
	}
}

func TestWhoami_UsesHeaderOverride(t *testing.T) {
	db := newTestDB(t)
	registerAgent(t, db, "agent-aaaa", "Alice")
	s := New(db, testRepoID, Config{})
	defer s.Close()
	req := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	req.Header.Set("X-Squad-Agent", "agent-aaaa")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var out map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if out["agent_id"] != "agent-aaaa" || out["display_name"] != "Alice" {
		t.Fatalf("got %v", out)
	}
}
