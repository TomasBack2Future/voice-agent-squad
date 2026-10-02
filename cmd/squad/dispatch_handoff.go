package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/dispatch"
)

func dispatchControllerCommands() []*cobra.Command {
	var native string
	var expected int64
	bind := &cobra.Command{Use: "controller-bind", Short: "Bind a verified legacy controller native before owner-initiated handoff", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		b, err := dispatch.New(bc.db, bc.repoID, nil).BindController(cmd.Context(), bc.agentID, native, expected)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(b)
	}}
	bind.Flags().StringVar(&native, "native-session", "", "Independently verified current owner's native identity")
	bind.Flags().Int64Var(&expected, "expected-epoch", 0, "Bootstrap epoch must be zero")
	var actor string
	status := &cobra.Command{Use: "controller-status", Short: "Read current legitimate controller actor/native/epoch", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		selected := actor
		if selected == "" {
			selected = bc.agentID
		}
		b, err := dispatch.New(bc.db, bc.repoID, nil).Controller(cmd.Context(), selected)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(b)
	}}
	status.Flags().StringVar(&actor, "actor", "", "Read-only selected actor; does not impersonate it")
	var path string
	handoff := &cobra.Command{Use: "handoff", Short: "Atomically hand off complete controller custody to a registered distinct actor/native", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil {
			return err
		}
		if len(raw) > 1<<20 {
			return fmt.Errorf("handoff request exceeds bound")
		}
		var request dispatch.HandoffRequest
		if err = json.Unmarshal(raw, &request); err != nil {
			return err
		}
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		receipt, err := dispatch.New(bc.db, bc.repoID, nil).Handoff(cmd.Context(), bc.agentID, request)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(receipt)
	}}
	handoff.Flags().StringVar(&path, "request", "", "Reviewed exact complete inventory JSON; acting identity must be old owner")
	_ = handoff.MarkFlagRequired("request")
	var requestID string
	get := &cobra.Command{Use: "handoff-get", Short: "Read immutable successful handoff receipt", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		receipt, err := dispatch.New(bc.db, bc.repoID, nil).HandoffReceipt(cmd.Context(), requestID)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(receipt)
	}}
	get.Flags().StringVar(&requestID, "request-id", "", "Successful idempotency key")
	return append([]*cobra.Command{bind, status, handoff, get}, dispatchReceiverCommands()...)
}

func dispatchReceiverCommands() []*cobra.Command {
	var commands []*cobra.Command
	for _, release := range []bool{false, true} {
		var native, incarnation string
		var epoch int64
		name := "receiver-bind"
		if release {
			name = "receiver-release"
		}
		cmd := &cobra.Command{Use: name, Short: "Exact native/epoch session-owned receiver custody", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			bc, err := bootClaimContext(cmd.Context())
			if err != nil {
				return err
			}
			defer bc.Close()
			s := dispatch.New(bc.db, bc.repoID, nil)
			if release {
				return s.ReleaseReceiver(cmd.Context(), bc.agentID, native, incarnation, epoch)
			}
			return s.BindReceiver(cmd.Context(), bc.agentID, native, incarnation, epoch)
		}}
		cmd.Flags().StringVar(&native, "native-session", "", "Exact controller native")
		cmd.Flags().StringVar(&incarnation, "incarnation", "", "Fresh receiver incarnation")
		cmd.Flags().Int64Var(&epoch, "epoch", 0, "Exact controller epoch")
		commands = append(commands, cmd)
	}
	return commands
}
