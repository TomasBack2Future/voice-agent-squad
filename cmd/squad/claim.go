package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zsiec/squad/internal/claims"
	"github.com/zsiec/squad/internal/config"
	"github.com/zsiec/squad/internal/items"
	"github.com/zsiec/squad/internal/notify"
	"github.com/zsiec/squad/internal/repo"
	"github.com/zsiec/squad/internal/stats"
	"github.com/zsiec/squad/internal/touch"
	"github.com/zsiec/squad/internal/worktree"
)

// ConcurrencyExceededError signals the agent has already met or exceeded its
// agent.claim_concurrency cap. Carries the values the cobra wrapper renders
// for users.
type ConcurrencyExceededError struct {
	AgentID string
	Held    int
	Cap     int
}

func (e *ConcurrencyExceededError) Error() string {
	return fmt.Sprintf("%s already holds %d claim(s); agent.claim_concurrency=%d", e.AgentID, e.Held, e.Cap)
}

// ClaimHeldError augments claims.ErrClaimTaken with the holding agent (when
// looked up successfully) so callers can render "X is already claimed by Y"
// without re-querying.
type ClaimHeldError struct {
	ItemID string
	Holder string
}

func (e *ClaimHeldError) Error() string {
	if e.Holder != "" {
		return fmt.Sprintf("%s is already claimed by %s", e.ItemID, e.Holder)
	}
	return fmt.Sprintf("%s is already claimed", e.ItemID)
}

func (e *ClaimHeldError) Unwrap() error { return claims.ErrClaimTaken }

type ClaimArgs struct {
	DB             *sql.DB  `json:"-"`
	RepoID         string   `json:"repo_id"`
	AgentID        string   `json:"agent_id"`
	ItemID         string   `json:"item_id"`
	Intent         string   `json:"intent,omitempty"`
	Touches        []string `json:"touches,omitempty"`
	Long           bool     `json:"long,omitempty"`
	ItemsDir       string   `json:"items_dir,omitempty"`
	DoneDir        string   `json:"done_dir,omitempty"`
	ConcurrencyCap int      `json:"concurrency_cap,omitempty"`
	// Worktree, when true, provisions an isolated git worktree under
	// <repoRoot>/.squad/worktrees/<agentID>-<itemID>/ before inserting
	// the claim row. Failure to provision aborts the claim — no row is
	// inserted, no chat is posted.
	Worktree bool   `json:"worktree,omitempty"`
	RepoRoot string `json:"repo_root,omitempty"`
	// BaseBranch is the branch the worktree forks from. Empty defers to
	// the current HEAD of repoRoot.
	BaseBranch string `json:"base_branch,omitempty"`
}

type ClaimResult struct {
	ItemID       string   `json:"item_id"`
	AgentID      string   `json:"agent_id"`
	Intent       string   `json:"intent,omitempty"`
	ClaimedAt    int64    `json:"claimed_at"`
	Tips         []string `json:"tips,omitempty"`
	WorktreePath string   `json:"worktree_path,omitempty"`
}

func Claim(ctx context.Context, args ClaimArgs) (*ClaimResult, error) {
	if args.ConcurrencyCap > 0 {
		var n int
		if err := args.DB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM claims WHERE agent_id = ? AND repo_id = ?`,
			args.AgentID, args.RepoID).Scan(&n); err != nil {
			return nil, fmt.Errorf("claim: count active claims: %w", err)
		}
		if n >= args.ConcurrencyCap {
			_ = stats.RecordWIPViolation(ctx, args.DB, args.RepoID, args.AgentID, int64(n), int64(args.ConcurrencyCap))
			return nil, &ConcurrencyExceededError{
				AgentID: args.AgentID,
				Held:    n,
				Cap:     args.ConcurrencyCap,
			}
		}
	}

	var worktreePath string
	var clOpts []claims.ClaimOption
	clOpts = append(clOpts, claims.ClaimWithPreflight(args.ItemsDir, args.DoneDir))
	if args.Worktree {
		if args.RepoRoot == "" {
			return nil, fmt.Errorf("claim --worktree: repo root not discovered")
		}
		base := args.BaseBranch
		if base == "" {
			base = currentHEADBranch(args.RepoRoot)
		}
		path, _, perr := worktree.Provision(args.RepoRoot, base, args.ItemID, args.AgentID)
		if perr != nil && !errors.Is(perr, worktree.ErrExists) {
			return nil, fmt.Errorf("claim --worktree: %w", perr)
		}
		worktreePath = path
		clOpts = append(clOpts, claims.ClaimWithWorktree(path))
	}

	store := claims.New(args.DB, args.RepoID, nil)
	err := store.Claim(ctx, args.ItemID, args.AgentID, args.Intent, args.Touches, args.Long, clOpts...)
	if err == nil {
		return &ClaimResult{
			ItemID:       args.ItemID,
			AgentID:      args.AgentID,
			Intent:       args.Intent,
			ClaimedAt:    time.Now().Unix(),
			WorktreePath: worktreePath,
		}, nil
	}
	if args.Worktree && worktreePath != "" {
		_ = worktree.Cleanup(args.RepoRoot, worktreePath)
	}
	if errors.Is(err, claims.ErrClaimTaken) {
		holder := lookupClaimHolderDB(ctx, args.DB, args.RepoID, args.ItemID)
		return nil, &ClaimHeldError{ItemID: args.ItemID, Holder: holder}
	}
	return nil, err
}

// currentHEADBranch reads the symbolic short ref of HEAD in repoRoot. Used
// to default --worktree's base branch to whatever the user is currently on.
// Empty string on detached HEAD or git failure — Provision will let git
// surface the error verbatim.
func currentHEADBranch(repoRoot string) string {
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func lookupClaimHolderDB(ctx context.Context, db *sql.DB, repoID, itemID string) string {
	agent, _ := claims.HolderOf(ctx, db, repoID, itemID)
	return agent
}

func newClaimCmd() *cobra.Command {
	var (
		intent       string
		touches      string
		long         bool
		worktreeFlag bool
		wait         bool
		waitTimeout  time.Duration
		waitFallback time.Duration
	)
	cmd := &cobra.Command{
		Use:   "claim <ITEM-ID>",
		Short: "Atomically claim an item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			itemID := args[0]
			ctx := cmd.Context()
			bc, err := bootClaimContext(ctx)
			if err != nil {
				return err
			}
			defer bc.Close()

			var touchList []string
			if touches != "" {
				for _, p := range strings.Split(touches, ",") {
					if p = strings.TrimSpace(p); p != "" {
						touchList = append(touchList, p)
					}
				}
			}

			repoRoot, _ := discoverRepoRoot()
			useWorktree := worktreeFlag || worktreeDefault()
			claimArgs := ClaimArgs{
				DB:             bc.db,
				RepoID:         bc.repoID,
				AgentID:        bc.agentID,
				ItemID:         itemID,
				Intent:         intent,
				Touches:        touchList,
				Long:           long,
				ItemsDir:       bc.itemsDir,
				DoneDir:        bc.doneDir,
				ConcurrencyCap: claimConcurrencyCap(),
				Worktree:       useWorktree,
				RepoRoot:       repoRoot,
			}
			var res *ClaimResult
			if wait {
				res, err = ClaimWithWait(ctx, ClaimWaitArgs{
					Claim:      claimArgs,
					Registry:   notify.NewRegistry(bc.db),
					Fallback:   waitFallback,
					Timeout:    waitTimeout,
					WaitWriter: cmd.ErrOrStderr(),
				})
			} else {
				res, err = Claim(ctx, claimArgs)
			}
			if err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "claimed %s\n", res.ItemID)
				printCadenceNudge(cmd.ErrOrStderr(), "claim")
				if itemPath := findItemPath(bc.itemsDir, itemID); itemPath != "" {
					if parsed, perr := items.Parse(itemPath); perr == nil {
						printSecondOpinionNudge(cmd.ErrOrStderr(), parsed.Priority, parsed.Risk)
						acTotal := items.CountAC(parsed.Body)
						printMilestoneTargetNudge(cmd.ErrOrStderr(), acTotal)
						printDecomposeNudge(cmd.ErrOrStderr(), itemID, acTotal, items.CountFileRefs(parsed.Body))
						refs := append([]string{}, parsed.References...)
						refs = append(refs, items.ListFileRefs(parsed.Body)...)
						if peer, perr := touch.New(bc.db, bc.repoID).ListOthersSince(ctx, bc.agentID, time.Now().Add(-24*time.Hour)); perr == nil {
							printPeerTouchOverlapNudge(cmd.ErrOrStderr(), refs, peer, time.Now())
						}
					}
				}
				if res.WorktreePath != "" {
					printWorktreeNudge(cmd.ErrOrStderr(), res.WorktreePath)
				}
				return nil
			}
			var capErr *ConcurrencyExceededError
			if errors.As(err, &capErr) {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"%s already holds %d claim(s); agent.claim_concurrency=%d in .squad/config.yaml.\n"+
						"  release one first, or raise the cap (set claim_concurrency: 100 to effectively disable).\n",
					capErr.AgentID, capErr.Held, capErr.Cap)
				os.Exit(1)
			}
			var heldErr *ClaimHeldError
			if errors.As(err, &heldErr) {
				if heldErr.Holder != "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "%s is already claimed by %s\n", heldErr.ItemID, heldErr.Holder)
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "%s is already claimed\n", heldErr.ItemID)
				}
				os.Exit(1)
			}
			if errors.Is(err, claims.ErrBlockedByOpen) {
				fmt.Fprintln(cmd.ErrOrStderr(), err.Error())
				os.Exit(1)
			}
			if errors.Is(err, claims.ErrItemNotFound) {
				fmt.Fprintf(cmd.ErrOrStderr(), "no item file found for %q in .squad/items/\n", itemID)
				os.Exit(1)
			}
			if errors.Is(err, claims.ErrItemAlreadyDone) {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s is already done (in .squad/done/) — cannot claim\n", itemID)
				os.Exit(1)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&intent, "intent", "", "short sentence describing intent")
	cmd.Flags().StringVar(&touches, "touches", "", "comma-separated file paths you'll modify")
	cmd.Flags().BoolVar(&long, "long", false, "use the 2h long-running threshold instead of hygiene.stale_claim_minutes")
	cmd.Flags().BoolVar(&worktreeFlag, "worktree", false, "provision a per-claim isolated git worktree under .squad/worktrees/")
	cmd.Flags().BoolVar(&wait, "wait", false, "block without model polling until the item can be atomically claimed")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 2*time.Hour, "maximum time to wait for a claim")
	cmd.Flags().DurationVar(&waitFallback, "wait-fallback", 30*time.Second, "internal missed-notification recovery interval (0 disables)")
	return cmd
}

func worktreeDefault() bool {
	wd, err := os.Getwd()
	if err != nil {
		return false
	}
	root, err := repo.Discover(wd)
	if err != nil {
		return false
	}
	cfg, err := config.Load(root)
	if err != nil {
		return false
	}
	return cfg.Agent.DefaultWorktreePerClaim
}

func claimConcurrencyCap() int {
	wd, err := os.Getwd()
	if err != nil {
		return config.DefaultClaimConcurrency
	}
	root, err := repo.Discover(wd)
	if err != nil {
		return config.DefaultClaimConcurrency
	}
	cfg, err := config.Load(root)
	if err != nil {
		return config.DefaultClaimConcurrency
	}
	if cfg.Agent.ClaimConcurrency > 0 {
		return cfg.Agent.ClaimConcurrency
	}
	return config.DefaultClaimConcurrency
}
