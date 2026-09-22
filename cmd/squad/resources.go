package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/claims"
)

func newResourcesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "resources", Short: "Manage opt-in protected resource scopes"}
	cmd.AddCommand(&cobra.Command{Use: "define <policy.json>", Short: "Atomically register resource policy while affected resources are idle", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		var defs []claims.ResourceDefinition
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&defs); err != nil {
			return err
		}
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		if err = bc.store.DefineResources(cmd.Context(), defs); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "registered %d protected resource definitions\n", len(defs))
		return err
	}})
	return cmd
}
