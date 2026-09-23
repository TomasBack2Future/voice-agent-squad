package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/repo"
	"github.com/zsiec/squad/internal/store"
)

// ClaimInspection is a point-in-time ledger observation, not a lease or grant.
type ClaimInspection struct {
	EnvClaim *InspectedClaim `json:"env_claim"`
}

type InspectedClaim struct {
	Item       string `json:"item"`
	Holder     string `json:"holder"`
	Generation int64  `json:"generation"`
	ClaimedAt  string `json:"claimed_at"`
	State      string `json:"state"`
}

func inspectClaim(ctx context.Context, db *sql.DB, repoID, itemID string) (*ClaimInspection, error) {
	if strings.TrimSpace(itemID) == "" || repoID == "" {
		return nil, fmt.Errorf("repo and item are required")
	}
	var row InspectedClaim
	var claimedAt int64
	err := db.QueryRowContext(ctx, `SELECT item_id, agent_id, generation, claimed_at, state FROM claims WHERE repo_id = ? AND item_id = ?`, repoID, itemID).Scan(&row.Item, &row.Holder, &row.Generation, &claimedAt, &row.State)
	if errors.Is(err, sql.ErrNoRows) {
		return &ClaimInspection{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect claim: %w", err)
	}
	row.ClaimedAt = time.Unix(claimedAt, 0).UTC().Format(time.RFC3339)
	return &ClaimInspection{EnvClaim: &row}, nil
}

func newClaimInspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "claim-inspect <item>",
		Short: "Read the current repository's exact claim as JSON without changing it",
		Args:  cobra.ExactArgs(1),
		// Override the root post-run sweep: inspection must not mutate ownership.
		PersistentPostRunE: func(_ *cobra.Command, _ []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			root, err := repo.Discover(wd)
			if err != nil {
				return err
			}
			repoID, err := repo.IDFor(root)
			if err != nil {
				return err
			}
			path, err := store.DBPath()
			if err != nil {
				return err
			}
			path, err = filepath.Abs(path)
			if err != nil {
				return err
			}
			uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
			db, err := sql.Open("sqlite", uri.String())
			if err != nil {
				return err
			}
			defer db.Close()
			result, err := inspectClaim(cmd.Context(), db, repoID, args[0])
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
}
