package main

import "testing"

func TestLifecycleMutationRejectsUnboundOrWrongNative(t *testing.T) {
	for _, key := range []string{"SQUAD_NATIVE_SESSION_ID", "CODEX_THREAD_ID", "CODEX_SESSION_ID", "CLAUDE_SESSION_ID", "MUSE_SESSION_ID"} {
		t.Setenv(key, "")
	}
	t.Setenv("SQUAD_AGENT", "legitimate-actor-from-worktree")
	if err := requireBoundNative("native"); err == nil {
		t.Fatal("persisted actor supplied native authority")
	}
	t.Setenv("SQUAD_NATIVE_SESSION_ID", "other")
	if err := requireBoundNative("native"); err == nil {
		t.Fatal("wrong native accepted")
	}
	t.Setenv("SQUAD_NATIVE_SESSION_ID", "native")
	if err := requireBoundNative("native"); err != nil {
		t.Fatal(err)
	}
}
