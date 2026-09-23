package main

import (
	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/claims"
)

func newHeartbeatCmd() *cobra.Command {
	var reservation, session string
	var generation int64
	var check bool
	cmd := &cobra.Command{Use: "heartbeat", Short: "Renew Worker claims under an exact dispatch fence", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		return claims.New(bc.db, bc.repoID, nil).WorkerHeartbeat(cmd.Context(), bc.agentID, reservation, session, generation, check)
	}}
	cmd.Flags().StringVar(&reservation, "reservation", "", "Dispatch reservation key")
	cmd.Flags().StringVar(&session, "worker-session", "", "Bound native session")
	cmd.Flags().Int64Var(&generation, "generation", 0, "Dispatch generation")
	cmd.Flags().BoolVar(&check, "check", false, "Validate fence without renewing claims")
	return cmd
}
