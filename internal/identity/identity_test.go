package identity

import (
	"os"
	"strings"
	"testing"
)

func clearSessionEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"SQUAD_SESSION_ID", "SQUAD_AGENT",
		"TERM_SESSION_ID", "ITERM_SESSION_ID",
		"TMUX_PANE", "STY", "WT_SESSION",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

func TestSessionKey_PrefersSquadOverride(t *testing.T) {
	clearSessionEnv(t)
	t.Setenv("SQUAD_SESSION_ID", "explicit")
	t.Setenv("TERM_SESSION_ID", "should-be-ignored")
	if got := sessionKey(); got != "explicit" {
		t.Fatalf("got %q want explicit", got)
	}
}

func TestSessionKey_FallsThroughLadder(t *testing.T) {
	clearSessionEnv(t)
	t.Setenv("TERM_SESSION_ID", "term")
	if got := sessionKey(); got != "term" {
		t.Fatalf("got %q want term", got)
	}
}

func TestSessionKey_EmptyWhenNothingSet(t *testing.T) {
	clearSessionEnv(t)
	if got := sessionKey(); got != "" {
		t.Fatalf("got %q want empty", got)
	}
}

func TestPersistedAgentID_PerSessionFile(t *testing.T) {
	clearSessionEnv(t)
	dir := t.TempDir()
	t.Setenv("SQUAD_HOME", dir)
	t.Setenv("SQUAD_SESSION_ID", "sess-A")
	if err := WritePersistedAgentID("agent-foo"); err != nil {
		t.Fatal(err)
	}
	if got := readPersistedAgentID(); got != "agent-foo" {
		t.Fatalf("read: got %q", got)
	}
	t.Setenv("SQUAD_SESSION_ID", "sess-B")
	if got := readPersistedAgentID(); got != "" {
		t.Fatalf("session-B should not see session-A id, got %q", got)
	}
}

func TestSessionIdentityDoesNotInheritLegacyActor(t *testing.T) {
	for _, runtime := range []string{"claude", "codex", "muse"} {
		t.Run(runtime, func(t *testing.T) {
			clearSessionEnv(t)
			t.Setenv("SQUAD_HOME", t.TempDir())
			if err := WritePersistedAgentID("legacy-owner"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SQUAD_SESSION_ID", runtime+":new-session")
			if got := PersistedAgentID(); got != "" {
				t.Fatalf("new session borrowed persisted actor %q", got)
			}
			got, err := AgentID()
			if err != nil || got != "agent-"+SessionSuffix() {
				t.Fatalf("new session identity = %q, %v", got, err)
			}
			if err := WritePersistedAgentID("scoped-owner"); err != nil {
				t.Fatal(err)
			}
			if got := PersistedAgentID(); got != "scoped-owner" {
				t.Fatalf("scoped identity lost: %q", got)
			}
			t.Setenv("SQUAD_AGENT", "explicit-owner")
			if got, err := AgentID(); err != nil || got != "explicit-owner" {
				t.Fatalf("explicit identity = %q, %v", got, err)
			}
			t.Setenv("SQUAD_AGENT", "")
			t.Setenv("SQUAD_SESSION_ID", "")
			if got := PersistedAgentID(); got != "legacy-owner" {
				t.Fatalf("unscoped legacy identity changed: %q", got)
			}
		})
	}
}

func TestAgentID_HonorsEnvOverride(t *testing.T) {
	clearSessionEnv(t)
	t.Setenv("SQUAD_AGENT", "from-env")
	got, err := AgentID()
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-env" {
		t.Fatalf("got %q", got)
	}
}

func TestAgentID_PrefersPersistedOverDerivation(t *testing.T) {
	clearSessionEnv(t)
	dir := t.TempDir()
	t.Setenv("SQUAD_HOME", dir)
	t.Setenv("SQUAD_SESSION_ID", "sess-X")
	if err := WritePersistedAgentID("agent-persisted"); err != nil {
		t.Fatal(err)
	}
	got, err := AgentID()
	if err != nil {
		t.Fatal(err)
	}
	if got != "agent-persisted" {
		t.Fatalf("got %q want agent-persisted", got)
	}
}

func TestAgentID_DerivesFromSessionSuffix(t *testing.T) {
	clearSessionEnv(t)
	t.Setenv("TERM_SESSION_ID", "iterm-xyz")
	dir := t.TempDir()
	t.Setenv("SQUAD_HOME", dir)
	id, err := AgentID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "agent-") {
		t.Fatalf("want agent- prefix, got %q", id)
	}
	if len(id) != len("agent-")+4 {
		t.Fatalf("want agent-XXXX format, got %q", id)
	}
}

func TestSessionSuffix_StableForSameSession(t *testing.T) {
	clearSessionEnv(t)
	t.Setenv("TERM_SESSION_ID", "iterm-abc-123")
	a := SessionSuffix()
	b := SessionSuffix()
	if a != b {
		t.Fatalf("suffix not stable: %q vs %q", a, b)
	}
	if len(a) != 4 {
		t.Fatalf("want 4-char suffix, got %q", a)
	}
}

func TestSessionSuffix_DiffersAcrossSessions(t *testing.T) {
	clearSessionEnv(t)
	t.Setenv("TERM_SESSION_ID", "session-A")
	a := SessionSuffix()
	t.Setenv("TERM_SESSION_ID", "session-B")
	b := SessionSuffix()
	if a == b {
		t.Fatalf("two distinct sessions produced same suffix %q", a)
	}
}

func TestSessionSuffix_FallbackWhenNoSignals(t *testing.T) {
	clearSessionEnv(t)
	if got := SessionSuffix(); got == "" {
		t.Fatal("SessionSuffix should never return empty")
	}
}
