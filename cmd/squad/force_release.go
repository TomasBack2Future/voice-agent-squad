package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/zsiec/squad/internal/claims"
)

// ForceReleaseArgs is the input for ForceRelease.
type ForceReleaseArgs struct {
	Store   *claims.Store
	ItemID  string
	AgentID string
	Reason  string
}

// ForceReleaseResult reports a successful force-release plus who held the
// claim before.
type ForceReleaseResult struct {
	ItemID     string `json:"item_id"`
	HeldBy     string `json:"held_by"`
	ReleasedBy string `json:"released_by"`
	Reason     string `json:"reason"`
}

// ForceRelease forcibly releases a stuck claim held by another agent.
// Returns claims.ErrReasonRequired when reason is empty (the caller is
// expected to surface that to the user).
func ForceRelease(ctx context.Context, args ForceReleaseArgs) (*ForceReleaseResult, error) {
	prior, err := args.Store.ForceRelease(ctx, args.ItemID, args.AgentID, args.Reason)
	if err != nil {
		return nil, err
	}
	return &ForceReleaseResult{
		ItemID:     args.ItemID,
		HeldBy:     prior,
		ReleasedBy: args.AgentID,
		Reason:     args.Reason,
	}, nil
}

func newForceReleaseCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "force-release <ITEM-ID>",
		Short: "Admin: forcibly release someone else's stuck claim (requires --reason)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			itemID := args[0]
			ctx := cmd.Context()
			bc, err := bootClaimContext(ctx)
			if err != nil {
				return err
			}
			defer bc.Close()

			res, err := ForceRelease(ctx, ForceReleaseArgs{
				Store:   bc.store,
				ItemID:  itemID,
				AgentID: bc.agentID,
				Reason:  reason,
			})
			if err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "force-released %s (was held by %s)\n", res.ItemID, res.HeldBy)
				return nil
			}
			if errors.Is(err, claims.ErrReasonRequired) {
				fmt.Fprintln(cmd.ErrOrStderr(), "--reason is required for force-release")
				os.Exit(2)
			}
			if errors.Is(err, claims.ErrNotClaimed) {
				fmt.Fprintf(cmd.ErrOrStderr(), "no active claim on %s\n", itemID)
				os.Exit(1)
			}
			if errors.Is(err, claims.ErrProtectedResource) {
				return fmt.Errorf("%s is a protected environment; verify the stopped holder and use `squad recover`", itemID)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why the claim is being forcibly released (required)")
	return cmd
}
