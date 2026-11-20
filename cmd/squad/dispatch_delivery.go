package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/zsiec/squad/internal/repo"
	"github.com/zsiec/squad/internal/store"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/dispatch"
)

func readDeliveryJSON(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return err
	}
	if len(data) > 65536 {
		return fmt.Errorf("request exceeds 64 KiB")
	}
	return json.Unmarshal(data, out)
}

func newDispatchDeliveryCmd() *cobra.Command {
	var observation string
	cmd := &cobra.Command{Use: "delivery <RESERVATION-KEY>", Short: "Read current delivery, custody and drift without mutating the ledger", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		bc, err := bootDeliveryRead()
		if err != nil {
			return err
		}
		defer bc.Close()
		var obs dispatch.IssueObservation
		if observation != "" {
			if err = readDeliveryJSON(observation, &obs); err != nil {
				return err
			}
		}
		result, err := dispatch.New(bc.db, bc.repoID, nil).Delivery(cmd.Context(), args[0], obs)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	cmd.Flags().StringVar(&observation, "issue-observation", "", "Timestamped GitHub observation JSON; absent or stale means unknown")
	return cmd
}

func newDispatchSweepCmd() *cobra.Command {
	var opts dispatch.SweepOptions
	cmd := &cobra.Command{Use: "sweep", Short: "Read every active reservation and report which need attention and why; no ledger writes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootDeliveryRead()
		if err != nil {
			return err
		}
		defer bc.Close()
		report, err := dispatch.New(bc.db, bc.repoID, nil).Sweep(cmd.Context(), opts)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}}
	cmd.Flags().DurationVar(&opts.RequestAfter, "request-after", 0, "Flag a Worker decision request unhandled for this long (default 10m)")
	cmd.Flags().DurationVar(&opts.DecisionAfter, "decision-after", 0, "Flag a current decision undelivered for this long (default 5m)")
	cmd.Flags().DurationVar(&opts.QuietAfter, "quiet-after", 0, "Flag a proceeding owner with no recorded progress and no operation in flight for this long (default 3h)")
	cmd.Flags().DurationVar(&opts.HoldAfter, "hold-after", 0, "Flag a hold without a structured wait older than this (default 2h)")
	cmd.Flags().StringVar(&opts.ReservedBy, "reserved-by", "", "Only reservations owned by this Dispatcher agent id (default: every active reservation)")
	return cmd
}

func newDispatchRetirePartialCmd() *cobra.Command {
	var request string
	cmd := &cobra.Command{Use: "retire-partial", Short: "Retire a reconciled blocked execution while its Issue remains open", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var q dispatch.PartialRetirement
		if err := readDeliveryJSON(request, &q); err != nil {
			return err
		}
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		if err := requireControllerNative(cmd.Context(), bc.db, bc.repoID, bc.agentID); err != nil {
			return err
		}
		result, err := dispatch.New(bc.db, bc.repoID, nil).RetirePartial(cmd.Context(), bc.agentID, q)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	cmd.Flags().StringVar(&request, "request", "", "Exact expected reservation, OPEN observation and handled blocked outcome JSON")
	_ = cmd.MarkFlagRequired("request")
	return cmd
}

func bootDeliveryRead() (*claimContext, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := repo.Discover(wd)
	if err != nil {
		return nil, err
	}
	id, err := repo.IDFor(root)
	if err != nil {
		return nil, err
	}
	path, err := store.DBPath()
	if err != nil {
		return nil, err
	}
	db, err := store.OpenReadOnly(path)
	if err != nil {
		return nil, err
	}
	return &claimContext{db: db, repoID: id}, nil
}
