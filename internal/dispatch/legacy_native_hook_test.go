package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacySelectedHookRejectsDisabledAsyncOrChangedNative(t *testing.T) {
	for _, kind := range []string{"valid", "disabled", "async", "matcher", "other native", "other runtime", "unselected module"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			dir, _ = filepath.EvalSymlinks(dir)
			module := filepath.Join(dir, "claude_native_fence.py")
			config := filepath.Join(dir, "config.json")
			settings := filepath.Join(dir, "settings.json")
			if err := os.WriteFile(module, []byte("isolated hook identity fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "terminal_receiver.py"), []byte("isolated receiver identity fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := map[string]any{"agent_id": "old-worker", "native_session_id": "old-session", "runtime": "claude", "role": "worker", "legacy_native_fence": true}
			command := legacyShellQuote("/isolated/python") + " " + legacyShellQuote(module) + " --config " + legacyShellQuote(config)
			hook := map[string]any{"type": "command", "command": command, "timeout": 15}
			group := map[string]any{"matcher": "*", "hooks": []any{hook}}
			selected := map[string]any{"hooks": map[string]any{"PreToolUse": []any{group}}}
			switch kind {
			case "disabled":
				selected["disableAllHooks"] = true
			case "async":
				hook["async"] = true
			case "matcher":
				group["matcher"] = "Bash"
			case "other native":
				cfg["native_session_id"] = "other"
			case "other runtime":
				cfg["runtime"] = "muse"
			case "unselected module":
				hook["command"] = "/isolated/python /elsewhere/claude_native_fence.py --config " + config
			}
			for path, value := range map[string]any{config: cfg, settings: selected} {
				raw, _ := json.Marshal(value)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			identity, err := legacyHookFiles(settings, "old-worker", "old-session", module, config, "/isolated/python")
			if (err == nil) != (kind == "valid") {
				t.Fatal("incorrect selected hook qualification", identity, err)
			}
			if kind == "valid" && (!validLegacyHash(identity.ModuleSHA256) || !validLegacyHash(identity.ReceiverSHA256) || !validLegacyHash(identity.ConfigSHA256)) {
				t.Fatal(identity)
			}
		})
	}
}

func TestLegacyShellQuoteMatchesPythonHookProducer(t *testing.T) {
	for _, row := range [][2]string{{"/a/b", "/a/b"}, {"a b", "'a b'"}, {"a'b", "'a'\"'\"'b'"}, {"目录", "'目录'"}, {"", "''"}} {
		if actual := legacyShellQuote(row[0]); actual != row[1] {
			t.Fatal(row[0], actual, row[1])
		}
	}
}

func TestLegacyHookReadinessCannotBeImportedFromOrdinaryCaller(t *testing.T) {
	s, q, _ := legacyStopFixture(t)
	if _, err := s.db.Exec(`DELETE FROM worker_native_hook_observations`); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorkerNativeHook(context.Background(), q.Claim.Actor, q.Expected.WorkerThreadID, "/isolated/config.json"); err == nil {
		t.Fatal("ordinary caller forged native hook execution")
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM worker_native_hook_observations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial forged readiness", count, err)
	}
	if _, err := s.WorkerNativeHookObservation(context.Background(), strings.Repeat("x", 400)); err == nil {
		t.Fatal("unbounded native hook lookup accepted")
	}
}
