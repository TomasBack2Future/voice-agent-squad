package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/attest"
)

type AttestRevokeArgs struct {
	DB            *sql.DB `json:"-"`
	RepoID        string  `json:"-"`
	AgentID       string  `json:"agent_id"`
	ID            int64   `json:"id"`
	Reason        string  `json:"reason"`
	ReplacementID int64   `json:"replacement_id,omitempty"`
}

func AttestRevoke(ctx context.Context, args AttestRevokeArgs) (*attest.Revocation, error) {
	return attest.New(args.DB, args.RepoID, nil).Revoke(ctx, args.ID, args.Reason, args.AgentID, args.ReplacementID)
}

func newAttestRevokeCmd() *cobra.Command {
	var reason string
	var replacement int64
	cmd := &cobra.Command{Use: "revoke <attestation-id>", Short: "Append an evidence correction without deleting the original", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return err
		}
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		result, err := AttestRevoke(cmd.Context(), AttestRevokeArgs{DB: bc.db, RepoID: bc.repoID, AgentID: bc.agentID, ID: id, Reason: reason, ReplacementID: replacement})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	cmd.Flags().StringVar(&reason, "reason", "", "Why this evidence must not satisfy acceptance")
	cmd.Flags().Int64Var(&replacement, "replacement", 0, "Newer successful attestation id of the same item and kind")
	return cmd
}

func newAttestListCmd() *cobra.Command {
	return &cobra.Command{Use: "list <item-id>", Short: "List evidence and structured corrections as JSON", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		result, err := Attestations(cmd.Context(), AttestationsArgs{DB: bc.db, RepoID: bc.repoID, ItemID: args[0]})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
}
