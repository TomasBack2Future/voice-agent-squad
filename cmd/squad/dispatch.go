package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/zsiec/squad/internal/dispatch"
)

func newDispatchCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "dispatch", Short: "Manage durable Dispatcher-to-Worker reservations"}
	cmd.AddCommand(newDispatchReserveCmd(), newDispatchAttachCmd(), newDispatchBindCmd(), newDispatchCloseCmd(), newDispatchListCmd())
	return cmd
}

func newDispatchReserveCmd() *cobra.Command {
	var source, note string
	var ttl time.Duration
	var asJSON bool
	cmd := &cobra.Command{
		Use: "reserve <RESERVATION-KEY>", Short: "Atomically reserve one canonical source before creating its Squad item", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bc, err := bootClaimContext(cmd.Context())
			if err != nil {
				return err
			}
			defer bc.Close()
			res, err := dispatch.New(bc.db, bc.repoID, nil).Reserve(cmd.Context(), args[0], source, bc.agentID, note, ttl)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "reserved %s generation=%d until=%d\n", res.ItemID, res.Generation, res.ExpiresAt)
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "canonical source reference, e.g. github:owner/repo#123")
	cmd.Flags().StringVar(&note, "note", "", "selection rationale")
	cmd.Flags().DurationVar(&ttl, "ttl", 15*time.Minute, "reservation lifetime before Worker binding")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	_ = cmd.MarkFlagRequired("source")
	return cmd
}

func newDispatchAttachCmd() *cobra.Command {
	var itemID string
	var generation int64
	var asJSON bool
	cmd := &cobra.Command{
		Use: "attach <RESERVATION-KEY>", Short: "Attach the canonical Squad item created by the reservation winner", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bc, err := bootClaimContext(cmd.Context())
			if err != nil {
				return err
			}
			defer bc.Close()
			res, err := dispatch.New(bc.db, bc.repoID, nil).Attach(cmd.Context(), args[0], itemID, bc.agentID, generation)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "attached %s generation=%d item=%s\n", res.ItemID, res.Generation, res.CanonicalItemID)
			return nil
		},
	}
	cmd.Flags().StringVar(&itemID, "item", "", "canonical Squad item id")
	cmd.Flags().Int64Var(&generation, "generation", 0, "reservation generation returned by reserve")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	_ = cmd.MarkFlagRequired("item")
	_ = cmd.MarkFlagRequired("generation")
	return cmd
}

func newDispatchBindCmd() *cobra.Command {
	var threadID string
	var generation int64
	var asJSON bool
	cmd := &cobra.Command{
		Use: "bind <RESERVATION-KEY>", Short: "Bind a created Worker task to the matching reservation generation", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bc, err := bootClaimContext(cmd.Context())
			if err != nil {
				return err
			}
			defer bc.Close()
			res, err := dispatch.New(bc.db, bc.repoID, nil).Bind(cmd.Context(), args[0], bc.agentID, threadID, generation)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "bound %s generation=%d worker=%s\n", res.ItemID, res.Generation, res.WorkerThreadID)
			return nil
		},
	}
	cmd.Flags().StringVar(&threadID, "thread-id", "", "created Worker Codex task id")
	cmd.Flags().Int64Var(&generation, "generation", 0, "reservation generation returned by reserve")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	_ = cmd.MarkFlagRequired("thread-id")
	_ = cmd.MarkFlagRequired("generation")
	return cmd
}

func newDispatchCloseCmd() *cobra.Command {
	var state, note string
	var generation int64
	var asJSON bool
	cmd := &cobra.Command{
		Use: "close <RESERVATION-KEY>", Short: "Close a reservation after reconciling Worker outcome", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bc, err := bootClaimContext(cmd.Context())
			if err != nil {
				return err
			}
			defer bc.Close()
			res, err := dispatch.New(bc.db, bc.repoID, nil).Close(cmd.Context(), args[0], bc.agentID, state, note, generation)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "closed %s generation=%d state=%s\n", res.ItemID, res.Generation, res.State)
			return nil
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "completed, failed, or cancelled")
	cmd.Flags().StringVar(&note, "note", "", "outcome evidence or retry reason")
	cmd.Flags().Int64Var(&generation, "generation", 0, "reservation generation")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	_ = cmd.MarkFlagRequired("state")
	_ = cmd.MarkFlagRequired("generation")
	return cmd
}

func newDispatchListCmd() *cobra.Command {
	var active, asJSON bool
	cmd := &cobra.Command{
		Use: "list", Short: "List dispatch reservations", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			bc, err := bootClaimContext(cmd.Context())
			if err != nil {
				return err
			}
			defer bc.Close()
			rows, err := dispatch.New(bc.db, bc.repoID, nil).List(cmd.Context(), active)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
			}
			for _, r := range rows {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\titem=%s\t%s\tgeneration=%d\tworker=%s\t%s\n", r.ItemID, r.CanonicalItemID, r.State, r.Generation, r.WorkerThreadID, r.SourceRef)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&active, "active", false, "show only reserved or dispatched rows")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}
