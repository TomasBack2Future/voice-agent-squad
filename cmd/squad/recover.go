package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zsiec/squad/internal/claims"
)

type RecoverArgs struct {
	Store                *claims.Store
	ItemID               string
	ExpectedHolder       string
	RecoveringAgent      string
	HolderSession        string
	Reason               string
	Evidence             string
	ConfirmHolderStopped bool
}

func Recover(ctx context.Context, args RecoverArgs) (*claims.RecoveryResult, error) {
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(args.ItemID)), "ENV-") {
		return nil, fmt.Errorf("recover is restricted to protected ENV-* claims")
	}
	return args.Store.Recover(ctx, claims.RecoveryRequest{
		ItemID: args.ItemID, ExpectedHolder: args.ExpectedHolder,
		RecoveringAgent: args.RecoveringAgent, HolderSession: args.HolderSession,
		Reason: args.Reason, Evidence: args.Evidence,
		ConfirmHolderStopped: args.ConfirmHolderStopped,
	})
}

func newRecoverCmd() *cobra.Command {
	var from, holderSession, reason, evidence string
	var confirm, asJSON bool
	cmd := &cobra.Command{
		Use:   "recover <ENV-ID>",
		Short: "Atomically take over a stopped holder's protected environment claim",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			bc, err := bootClaimContext(ctx)
			if err != nil {
				return err
			}
			defer bc.Close()
			res, err := Recover(ctx, RecoverArgs{
				Store: bc.store, ItemID: args[0], ExpectedHolder: from,
				RecoveringAgent: bc.agentID, HolderSession: holderSession,
				Reason: reason, Evidence: evidence, ConfirmHolderStopped: confirm,
			})
			if err != nil {
				if errors.Is(err, claims.ErrHolderRecentlyActive) {
					return fmt.Errorf("refusing recovery: %w", err)
				}
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "recovered %s from %s; generation=%d\n",
				res.ItemID, res.FromAgent, res.Generation)
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "expected current holder (required CAS guard)")
	cmd.Flags().StringVar(&holderSession, "holder-session", "", "verified stopped Codex task/session id")
	cmd.Flags().StringVar(&reason, "reason", "", "why recovery is required")
	cmd.Flags().StringVar(&evidence, "evidence", "", "task-stop and external-operation evidence")
	cmd.Flags().BoolVar(&confirm, "confirm-holder-stopped", false, "confirm the holder task was independently verified stopped")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("holder-session")
	_ = cmd.MarkFlagRequired("reason")
	_ = cmd.MarkFlagRequired("evidence")
	return cmd
}
