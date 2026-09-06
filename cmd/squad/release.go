package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/zsiec/squad/internal/claims"
	"github.com/zsiec/squad/internal/notify"
)

type ReleaseArgs struct {
	DB            *sql.DB                       `json:"-"`
	RepoID        string                        `json:"repo_id"`
	AgentID       string                        `json:"agent_id"`
	ItemID        string                        `json:"item_id"`
	Outcome       string                        `json:"outcome,omitempty"`
	NotifyWaiters func(context.Context, string) `json:"-"`
}

type ReleaseResult struct {
	ItemID     string `json:"item_id"`
	Outcome    string `json:"outcome"`
	ReleasedAt int64  `json:"released_at"`
}

func Release(ctx context.Context, args ReleaseArgs) (*ReleaseResult, error) {
	outcome := args.Outcome
	if outcome == "" {
		outcome = "released"
	}
	store := claims.New(args.DB, args.RepoID, nil)
	if err := store.Release(ctx, args.ItemID, args.AgentID, outcome); err != nil {
		return nil, err
	}
	if args.NotifyWaiters != nil {
		args.NotifyWaiters(ctx, args.ItemID)
	}
	return &ReleaseResult{
		ItemID:     args.ItemID,
		Outcome:    outcome,
		ReleasedAt: time.Now().Unix(),
	}, nil
}

func newReleaseCmd() *cobra.Command {
	var outcome string
	cmd := &cobra.Command{
		Use:   "release <ITEM-ID>",
		Short: "Release your claim on an item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			itemID := args[0]
			ctx := cmd.Context()
			bc, err := bootClaimContext(ctx)
			if err != nil {
				return err
			}
			defer bc.Close()

			res, err := Release(ctx, ReleaseArgs{
				DB:      bc.db,
				RepoID:  bc.repoID,
				AgentID: bc.agentID,
				ItemID:  itemID,
				Outcome: outcome,
				NotifyWaiters: func(ctx context.Context, itemID string) {
					notifyClaimWaiters(ctx, notify.NewRegistry(bc.db), bc.repoID, itemID)
				},
			})
			if err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "released %s (%s)\n", res.ItemID, res.Outcome)
				return nil
			}
			if errors.Is(err, claims.ErrNotYours) {
				fmt.Fprintln(cmd.ErrOrStderr(), "not your claim")
				os.Exit(1)
			}
			if errors.Is(err, claims.ErrNotClaimed) {
				fmt.Fprintf(cmd.ErrOrStderr(), "no active claim on %s\n", itemID)
				os.Exit(1)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&outcome, "outcome", "released", "outcome string recorded in claim_history")
	return cmd
}
