package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
)

// New lifecycle mutations never derive native authority from a worktree's
// persisted actor fallback. Launchers supply this exact native binding.
func requireBoundNative(expected string) error {
	actual := os.Getenv("SQUAD_NATIVE_SESSION_ID")
	if actual == "" {
		for _, key := range []string{"CODEX_THREAD_ID", "CODEX_SESSION_ID", "CLAUDE_SESSION_ID", "MUSE_SESSION_ID"} {
			if v := os.Getenv(key); v != "" {
				actual = v
				break
			}
		}
	}
	if expected == "" || actual == "" || actual != expected {
		return fmt.Errorf("current native session does not match assignment; an unbound shell cannot mutate lifecycle state")
	}
	return nil
}
func requireControllerNative(ctx context.Context, db *sql.DB, repo, actor string) error {
	var native string
	if err := db.QueryRowContext(ctx, `SELECT native_session FROM dispatch_controller_bindings b WHERE repo_id=? AND actor=? AND NOT EXISTS(SELECT 1 FROM dispatch_retired_controllers r WHERE r.repo_id=b.repo_id AND r.actor=b.actor)`, repo, actor).Scan(&native); err != nil {
		return fmt.Errorf("current owner controller binding required: %w", err)
	}
	return requireBoundNative(native)
}
