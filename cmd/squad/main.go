package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zsiec/squad/internal/config"
	"github.com/zsiec/squad/internal/hygiene"
	"github.com/zsiec/squad/internal/repo"
	"github.com/zsiec/squad/internal/store"
)

var versionString string

func init() {
	if versionString != "" {
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := strings.TrimPrefix(info.Main.Version, "v"); v != "" && v != "(devel)" {
			versionString = v
			return
		}
	}
	versionString = "0.0.0-dev"
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "squad",
		Short:         "Project-management framework for AI coding agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersionCmd())
	root.AddCommand(newRegisterCmd())
	root.AddCommand(newWhoamiCmd())
	root.AddCommand(newNewCmd())
	root.AddCommand(newNextCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newClaimCmd())
	root.AddCommand(newReleaseCmd())
	root.AddCommand(newDoneCmd())
	root.AddCommand(newAcceptCmd())
	root.AddCommand(newRejectCmd())
	root.AddCommand(newInboxCmd())
	root.AddCommand(newReadyCmd())
	root.AddCommand(newBlockedCmd())
	root.AddCommand(newForceReleaseCmd())
	root.AddCommand(newRecoverCmd())
	root.AddCommand(newReassignCmd())
	root.AddCommand(newDispatchCmd())
	root.AddCommand(newSayCmd())
	root.AddCommand(newAskCmd())
	root.AddCommand(newChattyCmd(thinkingVerb))
	root.AddCommand(newChattyCmd(stuckVerb))
	root.AddCommand(newChattyCmd(milestoneVerb))
	root.AddCommand(newChattyCmd(fyiVerb))
	root.AddCommand(newHandoffCmd())
	root.AddCommand(newReviewRequestCmd())
	root.AddCommand(newProgressCmd())
	root.AddCommand(newTickCmd())
	root.AddCommand(newStandupCmd())
	root.AddCommand(newRetroCmd())
	root.AddCommand(newPostmortemCmd())
	root.AddCommand(newWhoCmd())
	root.AddCommand(newTailCmd())
	root.AddCommand(newHistoryCmd())
	root.AddCommand(newTouchCmd())
	root.AddCommand(newUntouchCmd())
	root.AddCommand(newTouchesCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newArchiveCmd())
	root.AddCommand(newDumpStatusCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newSpecNewCmd())
	root.AddCommand(newScaffoldCmd())
	root.AddCommand(newDecomposeCmd())
	root.AddCommand(newIntakeCmd())
	root.AddCommand(newEpicNewCmd())
	root.AddCommand(newAnalyzeCmd())
	root.AddCommand(newGoCmd())
	root.AddCommand(newWorkspaceCmd())
	root.AddCommand(newServeCmd())
	root.AddCommand(newTUICmd())
	root.AddCommand(newInstallPluginCmd())
	root.AddCommand(newInstallHooksCmd())
	root.AddCommand(newListenCmd())
	root.AddCommand(newNotifyCleanupCmd())
	root.AddCommand(newMailboxCmd())
	root.AddCommand(newPRCmd())
	root.AddCommand(newPRLinkCmd())
	root.AddCommand(newPRCloseCmd())
	root.AddCommand(newAttestCmd())
	root.AddCommand(newStatsCmd())
	root.AddCommand(newLearningCmd())
	root.AddCommand(newMCPCmd())
	root.AddCommand(newSubagentEventCmd())
	root.AddCommand(newEventCmd())
	addPRDRedirect := func(name string) {
		root.AddCommand(&cobra.Command{
			Use:    name,
			Hidden: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				return fmt.Errorf("squad uses 'spec', not 'prd'. run: squad spec-new <name> \"<title>\"")
			},
		})
	}
	addPRDRedirect("prd")
	addPRDRedirect("prd-new")
	addPRDRedirect("prd-list")
	root.PersistentPostRunE = postRunHygiene
	return root
}

// postRunHygiene fires the hygiene runner after each successful command,
// debounced via ~/.squad/hygiene.lock. Best-effort: any error (missing home
// dir, no repo, write failure) is silently swallowed so user commands never
// fail because hygiene tripped. Disable entirely with SQUAD_NO_HYGIENE=1.
func postRunHygiene(cmd *cobra.Command, args []string) error {
	if os.Getenv("SQUAD_NO_HYGIENE") != "" {
		return nil
	}
	if err := store.EnsureHome(); err != nil {
		return nil
	}
	home, err := store.Home()
	if err != nil {
		return nil
	}
	// Read config before grabbing the debounce lock so the user's explicit
	// `sweep_on_every_command: false` doesn't get half-honored (skip the
	// work but still touch the lockfile). A parse error here used to fall
	// through silently — overrides reverted to defaults with no signal.
	// Surface it once per invocation so the user knows their edits aren't
	// being read.
	var hygieneCfg config.HygieneConfig
	if wd, err := os.Getwd(); err == nil {
		if root, err := repo.Discover(wd); err == nil {
			cfg, lerr := config.Load(root)
			if lerr != nil {
				fmt.Fprintf(os.Stderr, "squad: warning: %v (using defaults)\n", lerr)
			} else {
				hygieneCfg = cfg.Hygiene
			}
		}
	}
	if hygieneCfg.SweepOnEveryCommand != nil && !*hygieneCfg.SweepOnEveryCommand {
		return nil
	}
	lock := filepath.Join(home, "hygiene.lock")
	r := hygiene.NewRunner(lock, 10*time.Second)
	_ = r.RunIfDue(context.Background(), func(ctx context.Context) error {
		bc, err := bootClaimContext(ctx)
		if err != nil {
			return nil
		}
		defer bc.Close()
		adapter := itemsHygieneAdapter{squadDir: filepath.Dir(bc.itemsDir)}
		sw := hygiene.New(bc.db, bc.repoID, adapter)
		if hygieneCfg.StaleClaimMinutes > 0 {
			sw = sw.WithStaleSeconds(int64(hygieneCfg.StaleClaimMinutes) * 60)
		}
		_ = sw.MarkStaleAgents(ctx)
		_, _ = sw.ReclaimStale(ctx)
		return nil
	})
	return nil
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the squad version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), versionString)
			return nil
		},
	}
}

func main() {
	// argv strings are NUL-truncated by execve before Go sees them, so a
	// caller that builds os.Args via Go (or smuggles bytes via env vars)
	// can land NULs that the kernel preserved into argv. Reject those at
	// the boundary so they don't quietly truncate a title, agent id, or
	// path inside squad.
	for i, a := range os.Args[1:] {
		if strings.ContainsRune(a, 0) {
			fmt.Fprintf(os.Stderr, "squad: arg %d contains NUL byte; refusing to truncate silently\n", i+1)
			os.Exit(2)
		}
	}
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
