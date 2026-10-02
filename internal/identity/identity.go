// Package identity derives a stable agent id from session-local
// environment (TMUX_PANE, TERM_SESSION_ID, SQUAD_SESSION_ID, plus a
// fallback hash) and persists it under ~/.squad/agent-id.txt for resume.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zsiec/squad/internal/store"
)

// AgentID resolves the identity for this session with the precedence:
// SQUAD_AGENT env > persisted per-session file > fresh "agent-XXXX" derived
// from the session suffix. It never returns an empty id and never errors in
// practice — the error return is preserved for forward-compatibility with a
// future store-backed lookup.
func AgentID() (string, error) {
	if env := strings.TrimSpace(os.Getenv("SQUAD_AGENT")); env != "" {
		return env, nil
	}
	if persisted := readPersistedAgentID(); persisted != "" {
		return persisted, nil
	}
	return "agent-" + SessionSuffix(), nil
}

// PersistedAgentID exposes the session-keyed persisted id for callers that
// need to compare against an --as override before writing. Returns "" when
// no persisted file exists.
func PersistedAgentID() string {
	return readPersistedAgentID()
}

func readPersistedAgentID() string {
	home, err := store.Home()
	if err != nil {
		return ""
	}
	if sessionPath := sessionAgentIDPath(home); sessionPath != "" {
		if data, err := os.ReadFile(sessionPath); err == nil {
			return strings.TrimSpace(string(data))
		}
		// A missing or unreadable scoped identity never grants the legacy actor.
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, "agent-id.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func WritePersistedAgentID(id string) error {
	home, err := store.Home()
	if err != nil {
		return err
	}
	path := sessionAgentIDPath(home)
	if path == "" {
		path = filepath.Join(home, "agent-id.txt")
	}
	return os.WriteFile(path, []byte(id+"\n"), 0o644)
}

func sessionAgentIDPath(home string) string {
	key := sessionKey()
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(home, "agent-id."+hex.EncodeToString(sum[:6])+".txt")
}

func sessionKey() string {
	for _, env := range []string{
		"SQUAD_SESSION_ID",
		"TERM_SESSION_ID",
		"ITERM_SESSION_ID",
		"TMUX_PANE",
		"STY",
		"WT_SESSION",
	} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	return ""
}

// SessionSuffix returns a stable 4-hex-char suffix derived from the session
// key. Same session => same suffix. Two sessions in one worktree => two
// distinct suffixes. Falls back to a hash of pid+worktree when no session
// env signals are present so callers never see an empty value.
func SessionSuffix() string {
	key := sessionKey()
	if key == "" {
		wd, _ := os.Getwd()
		key = fmt.Sprintf("pid:%d:wd:%s", os.Getpid(), wd)
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:2])
}

func DetectWorktree() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}
