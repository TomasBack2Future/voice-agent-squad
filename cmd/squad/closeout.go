package main

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/closeout"
)

func newCloseoutCmd() *cobra.Command {
	var request string
	cmd := &cobra.Command{Use: "closeout", Short: "Resume an explicitly requested Issue closeout using existing gates", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var q closeout.Request
		if err := readDeliveryJSON(request, &q); err != nil {
			return err
		}
		if err := requireBoundNative(q.WorkerSession); err != nil {
			return err
		}
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		root, err := discoverRepoRoot()
		if err != nil {
			return err
		}
		engine := closeout.Engine{DB: bc.db, Repo: bc.repoID, Actor: bc.agentID, Backend: closeout.LocalBackend{DB: bc.db, Repo: bc.repoID, Actor: bc.agentID, Directory: root, Executable: executable}}
		result, err := engine.Run(cmd.Context(), q)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	cmd.Flags().StringVar(&request, "request", "", "Bound assignment, stable request key and acceptance/cleanup summary JSON")
	_ = cmd.MarkFlagRequired("request")
	return cmd
}
