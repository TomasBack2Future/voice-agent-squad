package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeSettings_FreshFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := mergeSquadHooks(p, map[string]bool{"session-start": true}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "SessionStart") {
		t.Fatalf("expected SessionStart, got %s", body)
	}
	if !strings.Contains(string(body), `"squad": "session-start@`+versionString+`"`) {
		t.Fatalf("expected squad marker, got %s", body)
	}
}

func TestMergeSettings_PreservesNonSquadHooks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "settings.json")
	original := `{
  "model": "sonnet",
  "hooks": {
    "PostToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/usr/local/bin/notify"}]}
    ]
  }
}`
	if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeSquadHooks(p, map[string]bool{"session-start": true, "pre-commit-pm-traces": true}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("re-parse: %v\n%s", err, body)
	}
	if parsed["model"] != "sonnet" {
		t.Fatalf("model lost; got %v", parsed["model"])
	}
	if !strings.Contains(string(body), "/usr/local/bin/notify") {
		t.Fatalf("non-squad hook lost; got %s", body)
	}
	if !strings.Contains(string(body), "session-start@"+versionString) {
		t.Fatalf("session-start missing; got %s", body)
	}
	if !strings.Contains(string(body), "pre-commit-pm-traces@"+versionString) {
		t.Fatalf("pre-commit-pm-traces missing; got %s", body)
	}
}

func TestMergeSettings_Idempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "settings.json")
	for i := 0; i < 3; i++ {
		if err := mergeSquadHooks(p, map[string]bool{"session-start": true}); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	body, _ := os.ReadFile(p)
	if got := strings.Count(string(body), "session-start@"+versionString); got != 1 {
		t.Fatalf("expected 1 entry, got %d:\n%s", got, body)
	}
}

func TestMergeSettings_UninstallRemovesOnlySquadEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "settings.json")
	original := `{
  "hooks": {
    "PostToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/usr/local/bin/notify"}]}
    ]
  }
}`
	if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeSquadHooks(p, map[string]bool{"session-start": true, "pre-commit-pm-traces": true}); err != nil {
		t.Fatal(err)
	}
	if err := uninstallSquadHooks(p); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	if strings.Contains(string(body), "squad") {
		t.Fatalf("expected zero squad markers, got %s", body)
	}
	if !strings.Contains(string(body), "/usr/local/bin/notify") {
		t.Fatalf("non-squad hook lost; got %s", body)
	}
}

func TestMergeSettings_DisableRemovesEnabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := mergeSquadHooks(p, map[string]bool{"session-start": true, "pre-commit-pm-traces": true}); err != nil {
		t.Fatal(err)
	}
	if err := mergeSquadHooks(p, map[string]bool{"session-start": true}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	if strings.Contains(string(body), "pre-commit-pm-traces@"+versionString) {
		t.Fatalf("expected pre-commit-pm-traces removed; got %s", body)
	}
	if !strings.Contains(string(body), "session-start@"+versionString) {
		t.Fatalf("session-start missing; got %s", body)
	}
}

func TestMergeSettings_TwoSpaceIndent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := mergeSquadHooks(p, map[string]bool{"session-start": true}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	if !strings.Contains(string(body), "\n  ") {
		t.Fatalf("expected 2-space indent, got:\n%s", body)
	}
}

func TestMergeSettingsAsyncRewakeUsesStopAndNativeField(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := mergeSquadHooks(p, map[string]bool{"async-rewake": true}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Hooks map[string][]struct {
			Hooks []struct {
				AsyncRewake bool `json:"asyncRewake"`
				Timeout     int  `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err = json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Hooks["asyncRewake"]; ok {
		t.Fatal("asyncRewake is not an event")
	}
	entries := d.Hooks["Stop"]
	if len(entries) != 1 || len(entries[0].Hooks) != 1 || !entries[0].Hooks[0].AsyncRewake || entries[0].Hooks[0].Timeout < 1800 {
		t.Fatalf("invalid native wake config: %s", raw)
	}
}
