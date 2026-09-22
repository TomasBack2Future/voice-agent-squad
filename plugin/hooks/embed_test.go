package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAll_IncludesNewR1Hooks(t *testing.T) {
	want := map[string]string{
		"stop-listen":         "Stop",
		"post-tool-flush":     "PostToolUse",
		"session-end-cleanup": "SessionEnd",
		"async-rewake":        "Stop",
	}
	got := map[string]string{}
	for _, h := range All {
		got[h.Name] = h.EventType
	}
	for name, ev := range want {
		if got[name] != ev {
			t.Errorf("hook %q: event=%q want %q", name, got[name], ev)
		}
	}
}

func TestAll_StopListenIsDefaultOn(t *testing.T) {
	for _, h := range All {
		if h.Name == "stop-listen" && !h.DefaultOn {
			t.Fatal("stop-listen must be default-on")
		}
	}
}

func TestAll_AsyncRewakeIsDefaultOff(t *testing.T) {
	for _, h := range All {
		if h.Name == "async-rewake" && h.DefaultOn {
			t.Fatal("async-rewake must be opt-in (default off)")
		}
	}
}

func TestEmbedAndHooksJSONStayInSync(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	hooksJSONPath := filepath.Join(filepath.Dir(thisFile), "..", "hooks.json")
	raw, err := os.ReadFile(hooksJSONPath)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var manifest struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Squad   string `json:"squad"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}

	type entry struct {
		hookName string
		matcher  string
		script   string
	}
	manifestEntries := map[string]entry{}
	for eventType, blocks := range manifest.Hooks {
		for _, b := range blocks {
			for _, h := range b.Hooks {
				name := strings.SplitN(h.Squad, "@", 2)[0]
				manifestEntries[name] = entry{
					hookName: eventType,
					matcher:  b.Matcher,
					script:   filepath.Base(h.Command),
				}
			}
		}
	}

	for _, h := range All {
		if !h.DefaultOn {
			continue
		}
		got, ok := manifestEntries[h.Name]
		if !ok {
			t.Errorf("hook %q DefaultOn but absent from hooks.json", h.Name)
			continue
		}
		if got.script != h.Filename {
			t.Errorf("hook %q script mismatch: hooks.json=%s embed.go=%s", h.Name, got.script, h.Filename)
		}
		if got.hookName != h.EventType {
			t.Errorf("hook %q event type mismatch: hooks.json=%s embed.go=%s", h.Name, got.hookName, h.EventType)
		}
		if got.matcher != h.Matcher {
			t.Errorf("hook %q matcher mismatch: hooks.json=%s embed.go=%s", h.Name, got.matcher, h.Matcher)
		}
	}
}

// Hooks shipped as scripts in this package must appear in hooks.json so Claude
// Code's plugin loader sees them — otherwise they cannot be enabled at all.
// Pinned by name because every regression of this kind is "we wrote a script
// and forgot the manifest registration."
func TestHooksJSONIncludesShippedOptInHooks(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	hooksJSONPath := filepath.Join(filepath.Dir(thisFile), "..", "hooks.json")
	raw, err := os.ReadFile(hooksJSONPath)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}

	required := map[string]string{
		"async_rewake.sh":       "Stop",
		"loop_pre_bash_tick.sh": "PreToolUse",
	}

	manifest := string(raw)
	for script, eventType := range required {
		if !strings.Contains(manifest, script) {
			t.Errorf("hooks.json missing entry for %s (expected under %s)", script, eventType)
		}
	}
}

func TestHooksJSONHasNoOrphanEntries(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	hooksJSONPath := filepath.Join(filepath.Dir(thisFile), "..", "hooks.json")
	raw, err := os.ReadFile(hooksJSONPath)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var manifest struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Squad   string `json:"squad"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}

	embedByName := map[string]Hook{}
	for _, h := range All {
		embedByName[h.Name] = h
	}

	const pluginPrefix = "${CLAUDE_PLUGIN_ROOT}/hooks/"
	for eventType, blocks := range manifest.Hooks {
		for _, b := range blocks {
			for _, h := range b.Hooks {
				if !strings.HasPrefix(h.Command, pluginPrefix) {
					continue
				}
				name := strings.SplitN(h.Squad, "@", 2)[0]
				script := strings.TrimPrefix(h.Command, pluginPrefix)

				if _, err := FS.ReadFile(script); err != nil {
					t.Errorf("hooks.json script %q (hook %q) not present in hooks.FS: %v", script, name, err)
				}
				em, ok := embedByName[name]
				if !ok {
					t.Errorf("hooks.json hook %q (event %q) has no entry in embed.All", name, eventType)
					continue
				}
				if em.EventType != eventType {
					t.Errorf("hook %q event-type mismatch: hooks.json=%s embed.go=%s", name, eventType, em.EventType)
				}
				if em.Filename != script {
					t.Errorf("hook %q filename mismatch: hooks.json=%s embed.go=%s", name, script, em.Filename)
				}
				if em.Matcher != b.Matcher {
					t.Errorf("hook %q matcher mismatch: hooks.json=%s embed.go=%s", name, b.Matcher, em.Matcher)
				}
			}
		}
	}
}
