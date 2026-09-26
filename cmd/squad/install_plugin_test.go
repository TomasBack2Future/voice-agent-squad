package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallPlugin_CreatesDestDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("SQUAD_PLUGIN_DEST", filepath.Join(tmp, "plugins"))
	t.Setenv("HOME", tmp)
	t.Setenv("SQUAD_HOME", filepath.Join(tmp, ".squad"))

	var stdout, stderr bytes.Buffer
	cmd := newInstallPluginCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v\nstderr: %s", err, stderr.String())
	}

	manifest := filepath.Join(tmp, "plugins", "squad", ".claude-plugin", "plugin.json")
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("manifest should exist at %s: %v", manifest, err)
	}
}

func TestInstallPlugin_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("SQUAD_PLUGIN_DEST", filepath.Join(tmp, "plugins"))
	t.Setenv("HOME", tmp)
	t.Setenv("SQUAD_HOME", filepath.Join(tmp, ".squad"))

	for i := 0; i < 2; i++ {
		cmd := newInstallPluginCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("install pass %d: %v", i, err)
		}
	}
}

func TestInstallPlugin_Uninstall(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("SQUAD_PLUGIN_DEST", filepath.Join(tmp, "plugins"))
	t.Setenv("HOME", tmp)
	t.Setenv("SQUAD_HOME", filepath.Join(tmp, ".squad"))

	cmd := newInstallPluginCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("install: %v", err)
	}

	pluginDir := filepath.Join(tmp, "plugins", "squad")
	if _, err := os.Stat(pluginDir); err != nil {
		t.Fatalf("plugin dir should exist after install: %v", err)
	}

	cmd2 := newInstallPluginCmd()
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"--uninstall"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(pluginDir); !os.IsNotExist(err) {
		t.Fatalf("plugin dir should be gone after uninstall, err=%v", err)
	}
}

func TestInstallPlugin_DoesNotShipGoSources(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("SQUAD_PLUGIN_DEST", filepath.Join(tmp, "plugins"))
	t.Setenv("HOME", tmp)
	t.Setenv("SQUAD_HOME", filepath.Join(tmp, ".squad"))

	cmd := newInstallPluginCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	pluginDir := filepath.Join(tmp, "plugins", "squad")
	var leaked []string
	_ = filepath.WalkDir(pluginDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".go") {
			rel, _ := filepath.Rel(pluginDir, path)
			leaked = append(leaked, rel)
		}
		return nil
	})
	if len(leaked) > 0 {
		t.Fatalf("plugin install shipped Go source files: %v", leaked)
	}
}

func TestInstallPlugin_UninstallIdempotent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("SQUAD_PLUGIN_DEST", filepath.Join(tmp, "plugins"))
	t.Setenv("HOME", tmp)
	t.Setenv("SQUAD_HOME", filepath.Join(tmp, ".squad"))

	cmd := newInstallPluginCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--uninstall"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("uninstall on absent should not error: %v", err)
	}
}

func TestInstallPlugin_RegistersAlwaysOnHooks(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("SQUAD_PLUGIN_DEST", filepath.Join(tmp, "plugins"))
	t.Setenv("HOME", tmp)
	t.Setenv("SQUAD_HOME", filepath.Join(tmp, ".squad"))

	cmd := newInstallPluginCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	settings, err := os.ReadFile(filepath.Join(tmp, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("settings.json absent: %v", err)
	}
	text := string(settings)
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PreCompact"} {
		if !strings.Contains(text, ev) {
			t.Errorf("settings.json missing %s entry", ev)
		}
	}
}

func TestInstallPlugin_WarnsOnLegacyManifestPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("SQUAD_PLUGIN_DEST", filepath.Join(tmp, "plugins"))
	t.Setenv("HOME", tmp)
	t.Setenv("SQUAD_HOME", filepath.Join(tmp, ".squad"))

	legacyDir := filepath.Join(tmp, "plugins", "squad")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "plugin.json"),
		[]byte(`{"name":"squad","version":"0.1.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	cmd := newInstallPluginCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--register-mcp=false", "--skip-hooks"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "legacy plugin.json detected") {
		t.Errorf("expected legacy-warning in output; got:\n%s\n%s", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(legacyDir, "plugin.json")); !os.IsNotExist(err) {
		t.Errorf("legacy plugin.json should be cleaned up by atomic install")
	}
	if _, err := os.Stat(filepath.Join(legacyDir, ".claude-plugin", "plugin.json")); err != nil {
		t.Errorf("new manifest missing: %v", err)
	}
}

func TestInstallPlugin_UninstallRemovesHooks(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("SQUAD_PLUGIN_DEST", filepath.Join(tmp, "plugins"))
	t.Setenv("HOME", tmp)
	t.Setenv("SQUAD_HOME", filepath.Join(tmp, ".squad"))

	cmd := newInstallPluginCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("install: %v", err)
	}

	settingsPath := filepath.Join(tmp, ".claude", "settings.json")
	pre, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json absent after install: %v", err)
	}
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PreCompact"} {
		if !strings.Contains(string(pre), ev) {
			t.Fatalf("install: settings.json missing %s entry", ev)
		}
	}
	hooksDir := filepath.Join(tmp, ".squad", "hooks")
	if _, err := os.Stat(hooksDir); err != nil {
		t.Fatalf("install: materialized hooks dir missing: %v", err)
	}

	cmd2 := newInstallPluginCmd()
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"--uninstall"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	post, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json missing after uninstall: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(post, &got); err != nil {
		t.Fatalf("settings.json parse: %v", err)
	}
	servers, _ := got["mcpServers"].(map[string]any)
	if _, ok := servers["squad"]; ok {
		t.Errorf("uninstall: mcpServers.squad still present")
	}
	for _, marker := range []string{"session-start@", "user-prompt-tick@", "pre-compact@"} {
		if strings.Contains(string(post), marker) {
			t.Errorf("uninstall: settings.json still contains squad hook marker %q:\n%s", marker, post)
		}
	}
	if _, err := os.Stat(hooksDir); !os.IsNotExist(err) {
		t.Errorf("uninstall: materialized hooks dir should be gone, err=%v", err)
	}
}

func TestInstallPlugin_RegistersMCPServer(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("SQUAD_PLUGIN_DEST", filepath.Join(tmp, "plugins"))
	t.Setenv("HOME", tmp)
	t.Setenv("SQUAD_HOME", filepath.Join(tmp, ".squad"))

	cmd := newInstallPluginCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(tmp, ".claude", "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json absent: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	servers, _ := got["mcpServers"].(map[string]any)
	if servers == nil {
		t.Fatal("mcpServers missing")
	}
	sq, _ := servers["squad"].(map[string]any)
	if sq == nil {
		t.Fatal("mcpServers.squad missing")
	}
	if sq["command"] != "squad" {
		t.Errorf("command=%v", sq["command"])
	}
}
