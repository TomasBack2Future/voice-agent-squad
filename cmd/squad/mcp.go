package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zsiec/squad/internal/chat"
	"github.com/zsiec/squad/internal/mcp"
	"github.com/zsiec/squad/internal/mcp/bootstrap"
	"github.com/zsiec/squad/internal/notify"
	"github.com/zsiec/squad/internal/repo"
	"github.com/zsiec/squad/internal/store"
	"github.com/zsiec/squad/internal/tui/daemon"
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP server over stdio (Claude Code transport).",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, repoID, repoRoot, closeFn, err := openMCPContext()
			if err != nil {
				return err
			}
			defer closeFn()
			if os.Getenv("SQUAD_SERVICE_CHILD") == "1" {
				return runMCP(cmd.Context(), db, repoID, repoRoot, os.Stdin, os.Stdout)
			}
			return runMCP(cmd.Context(), db, repoID, repoRoot, os.Stdin, os.Stdout, WithBootstrap(realBootstrap))
		},
	}
}

// runMCPOption tunes runMCP without changing its required parameters.
// Tests use WithBootstrap to inject a deterministic hook; production
// passes the real bootstrap so the dashboard daemon comes up on first
// boot.
type runMCPOption func(*runMCPConfig)

type runMCPConfig struct {
	bootstrapFn func(context.Context)
}

// WithBootstrap registers a function to invoke after tools are
// registered and before the JSON-RPC loop blocks on stdin. nil hook is
// equivalent to passing no option (no bootstrap).
func WithBootstrap(fn func(context.Context)) runMCPOption {
	return func(c *runMCPConfig) { c.bootstrapFn = fn }
}

// realBootstrap is the production hook: bring the dashboard daemon up
// (or upgrade / reinstall) and run the welcome flow on first run. Both
// failures are non-fatal — MCP must keep serving tools even if the UI
// layer can't come up.
func realBootstrap(ctx context.Context) {
	bin, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "squad: dashboard auto-install skipped: resolve binary: %v\n", err)
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "squad: dashboard auto-install skipped: resolve home: %v\n", err)
		return
	}
	squadHome, err := store.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "squad: dashboard auto-install skipped: resolve squad home: %v\n", err)
		return
	}
	opts := bootstrap.Options{
		BinaryPath: bin,
		Bind:       "127.0.0.1",
		Port:       7777,
		HomeDir:    home,
		SquadHome:  squadHome,
		Manager:    daemon.New(),
		Version:    versionString,
	}
	if err := bootstrap.Ensure(ctx, opts); err != nil {
		fmt.Fprintf(os.Stderr, "squad: dashboard auto-install skipped: %v\n", err)
		return
	}
	if err := bootstrap.Welcome(ctx, opts); err != nil {
		fmt.Fprintf(os.Stderr, "squad: welcome flow skipped: %v\n", err)
	}
}

// openMCPContext opens the global squad store and best-effort discovers the
// caller's repo. MCP must start even when no repo is found (claude-installed
// in a non-repo dir): missing repo surfaces as repoID="" and repoRoot="" so
// per-repo handlers can return a structured error rather than crashing init.
func openMCPContext() (*sql.DB, string, string, func(), error) {
	if err := store.EnsureHome(); err != nil {
		return nil, "", "", func() {}, err
	}
	db, err := store.OpenDefault()
	if err != nil {
		return nil, "", "", func() {}, err
	}
	closeFn := func() { _ = db.Close() }

	repoID := ""
	repoRoot := ""
	if wd, err := os.Getwd(); err == nil {
		if root, err := repo.Discover(wd); err == nil {
			repoRoot = root
			if id, err := repo.IDFor(root); err == nil {
				repoID = id
			}
		}
	}
	return db, repoID, repoRoot, closeFn, nil
}

func runMCP(ctx context.Context, db *sql.DB, repoID, repoRoot string, in io.Reader, out io.Writer, opts ...runMCPOption) error {
	cfg := runMCPConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	srv := mcp.NewServer(mcp.ServerInfo{Name: "squad", Version: versionString})
	registerTools(srv, db, repoID, repoRoot)
	if env := os.Getenv("SQUAD_MCP_TOOLS"); env != "" {
		var allow []string
		for _, name := range strings.Split(env, ",") {
			if name = strings.TrimSpace(name); name != "" {
				allow = append(allow, name)
			}
		}
		srv.RestrictTo(allow)
	}
	if cfg.bootstrapFn != nil {
		cfg.bootstrapFn(ctx)
	}
	return srv.Serve(ctx, in, out)
}

// newChatService mirrors bootClaimContext's chat wiring without bringing in
// the full claim-context machinery. MCP handlers that post to chat get the
// same notify-wake side-effect the CLI uses.
func newChatService(db *sql.DB, repoID string) *chat.Chat {
	c := chat.New(db, repoID)
	registry := notify.NewRegistry(db)
	c.SetNotifier(func(ctx context.Context, repoID string) {
		_ = notify.Wake(ctx, registry, repoID, 100*time.Millisecond)
	})
	return c
}
