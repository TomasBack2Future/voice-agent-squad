package main

import (
	"encoding/json"
	"errors"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/claims"
)

func newHeartbeatCmd() *cobra.Command {
	var reservation, session string
	var generation int64
	var check, machine, requirePrimary bool
	cmd := &cobra.Command{Use: "heartbeat", Short: "Renew Worker claims under an exact dispatch fence", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cmd.SilenceUsage = machine
		cmd.SilenceErrors = machine
		bc, err := bootClaimContext(cmd.Context())
		actor := ""
		if err == nil {
			defer bc.Close()
			actor = bc.agentID
			err = claims.New(bc.db, bc.repoID, nil).WorkerHeartbeat(cmd.Context(), actor, reservation, session, generation, check, requirePrimary)
		}
		if machine {
			receipt := heartbeatReceipt(err, actor, reservation, session, generation, check, requirePrimary)
			if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(receipt); encodeErr != nil {
				return encodeErr
			}
		}
		return err
	}}
	cmd.Flags().StringVar(&reservation, "reservation", "", "Dispatch reservation key")
	cmd.Flags().StringVar(&session, "worker-session", "", "Bound native session")
	cmd.Flags().Int64Var(&generation, "generation", 0, "Dispatch generation")
	cmd.Flags().BoolVar(&machine, "json", false, "Emit structured custody outcome; database failures are unavailable, never custody rejection")
	cmd.Flags().BoolVar(&requirePrimary, "require-primary", false, "Require the exact primary claim to remain held")
	cmd.Flags().BoolVar(&check, "check", false, "Validate fence without renewing claims")
	return cmd
}

func heartbeatReceipt(err error, actor, reservation, session string, generation int64, check, requirePrimary bool) map[string]any {
	outcome := "renewed"
	if check {
		outcome = "verified"
	}
	if err != nil {
		outcome = "unavailable"
		if errors.Is(err, claims.ErrWorkerFenceRejected) {
			outcome = "custody-rejected"
		}
	}
	return map[string]any{"schema_version": "squad.worker-heartbeat.v1", "outcome": outcome, "agent": actor, "reservation": reservation, "generation": generation, "worker_session": session, "require_primary": requirePrimary}
}
