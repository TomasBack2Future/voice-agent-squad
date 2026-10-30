package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/dispatch"
)

const defaultCmux = "/Applications/cmux.app/Contents/Resources/bin/cmux"

func retirementFlags(cmd *cobra.Command, q *dispatch.RetirementRequest) {
	cmd.Flags().StringVar(&q.Reservation, "reservation", "", "exact reservation key")
	cmd.Flags().Int64Var(&q.Generation, "generation", 0, "exact reservation generation")
	cmd.Flags().StringVar(&q.WorkerSession, "worker-session", "", "exact original Worker native session")
	_ = cmd.MarkFlagRequired("reservation")
	_ = cmd.MarkFlagRequired("generation")
	_ = cmd.MarkFlagRequired("worker-session")
}

// retirementController reports whether the caller acts as the owning Dispatcher
// controller (true) or as the original Worker bound to the exact native (false).
func retirementController(ctx context.Context, bc *claimContext, q dispatch.RetirementRequest) (bool, error) {
	if requireBoundNative(q.WorkerSession) == nil {
		return false, nil
	}
	if err := requireControllerNative(ctx, bc.db, bc.repoID, bc.agentID); err != nil {
		return false, fmt.Errorf("caller is neither the bound original Worker nor the current controller: %w", err)
	}
	return true, nil
}

func newDispatchRetirementCmd() *cobra.Command {
	var q dispatch.RetirementRequest
	cmd := &cobra.Command{Use: "retirement", Short: "Read terminal client-retirement eligibility and recorded exit/color steps; no mutation", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootDeliveryRead()
		if err != nil {
			return err
		}
		defer bc.Close()
		out, err := dispatch.New(bc.db, bc.repoID, nil).Retirement(cmd.Context(), q)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}}
	retirementFlags(cmd, &q)
	return cmd
}

func newDispatchRetireClientCmd() *cobra.Command {
	var q dispatch.RetirementRequest
	cmd := &cobra.Command{Use: "retire-client", Short: "Normally end the finished assignment's own Claude client and verify its helpers ended", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		controller, err := retirementController(cmd.Context(), bc, q)
		if err != nil {
			return err
		}
		out, err := dispatch.New(bc.db, bc.repoID, nil).RetireClient(cmd.Context(), bc.agentID, controller, q)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}}
	retirementFlags(cmd, &q)
	return cmd
}

func cmuxClearColor(binary string) dispatch.ColorRunner {
	return func(ctx context.Context, workspace string) error {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		caps, err := exec.CommandContext(ctx, binary, "capabilities", "--json").Output()
		if err != nil {
			return fmt.Errorf("cmux capabilities unavailable")
		}
		var mode struct {
			AccessMode string `json:"access_mode"`
		}
		if json.Unmarshal(caps, &mode) != nil || mode.AccessMode != "automation" {
			return fmt.Errorf("cmux automation access required")
		}
		if out, err := exec.CommandContext(ctx, binary, "workspace-action", "--workspace", workspace, "--action", "clear-color").CombinedOutput(); err != nil {
			return fmt.Errorf("%s", strings.TrimSpace(string(out)))
		}
		return nil
	}
}

func newDispatchRetireColorCmd() *cobra.Command {
	var q dispatch.RetirementRequest
	var workspace, binary string
	cmd := &cobra.Command{Use: "retire-color", Short: "Clear the Worker workspace color after the terminal gate; recorded separately from client exit", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		if err := requireControllerNative(cmd.Context(), bc.db, bc.repoID, bc.agentID); err != nil {
			return err
		}
		out, err := dispatch.New(bc.db, bc.repoID, nil).RetireColor(cmd.Context(), bc.agentID, q, workspace, cmuxClearColor(binary))
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}}
	retirementFlags(cmd, &q)
	cmd.Flags().StringVar(&workspace, "workspace", "", "explicit persisted Worker workspace UUID")
	cmd.Flags().StringVar(&binary, "cmux", defaultCmux, "cmux CLI binary")
	_ = cmd.MarkFlagRequired("workspace")
	return cmd
}
