package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zsiec/squad/internal/store"
	"github.com/zsiec/squad/plugin/hooks"
)

func mergeSquadHooks(settingsPath string, enabled map[string]bool) error {
	settings, err := loadSettings(settingsPath)
	if err != nil {
		return err
	}

	hooksRaw, _ := settings["hooks"].(map[string]any)
	if hooksRaw == nil {
		hooksRaw = map[string]any{}
	}
	hooksRaw = stripSquadEntries(hooksRaw)

	hooksDir, err := materializeHooks()
	if err != nil {
		return err
	}

	for _, h := range hooks.All {
		if !enabled[h.Name] {
			continue
		}
		entry := map[string]any{
			"matcher": h.Matcher,
			"hooks": []map[string]any{{
				"type":    "command",
				"command": filepath.Join(hooksDir, h.Filename),
			}},
			"squad": h.Name + "@" + versionString,
		}
		if h.Name == "async-rewake" {
			child := entry["hooks"].([]map[string]any)[0]
			child["asyncRewake"] = true
			child["timeout"] = 1900
		}
		list, _ := hooksRaw[h.EventType].([]any)
		hooksRaw[h.EventType] = append(list, entry)
	}

	settings["hooks"] = hooksRaw
	return writeSettingsAtomic(settingsPath, settings)
}

func uninstallSquadHooks(settingsPath string) error {
	settings, err := loadSettings(settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	hooksRaw, _ := settings["hooks"].(map[string]any)
	if hooksRaw == nil {
		return nil
	}
	settings["hooks"] = stripSquadEntries(hooksRaw)
	return writeSettingsAtomic(settingsPath, settings)
}

func stripSquadEntries(hooksRaw map[string]any) map[string]any {
	out := map[string]any{}
	for event, raw := range hooksRaw {
		list, ok := raw.([]any)
		if !ok {
			out[event] = raw
			continue
		}
		filtered := make([]any, 0, len(list))
		for _, e := range list {
			m, ok := e.(map[string]any)
			if !ok {
				filtered = append(filtered, e)
				continue
			}
			if _, isSquad := m["squad"]; isSquad {
				continue
			}
			filtered = append(filtered, e)
		}
		if len(filtered) > 0 {
			out[event] = filtered
		}
	}
	return out
}

func loadSettings(path string) (map[string]any, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(body) == 0 {
		return map[string]any{}, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if parsed == nil {
		parsed = map[string]any{}
	}
	return parsed, nil
}

func writeSettingsAtomic(path string, data map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func materializeHooks() (string, error) {
	dir, err := materializedHooksDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, h := range hooks.All {
		body, err := hooks.FS.ReadFile(h.Filename)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, h.Filename), body, 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func materializedHooksDir() (string, error) {
	squadHome, err := store.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(squadHome, "hooks"), nil
}
