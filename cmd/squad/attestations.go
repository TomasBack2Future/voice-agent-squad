package main

import (
	"context"
	"database/sql"

	"github.com/zsiec/squad/internal/attest"
)

type AttestationsArgs struct {
	DB     *sql.DB `json:"-"`
	RepoID string  `json:"repo_id"`
	ItemID string  `json:"item_id"`
}

// AttestationRow is the MCP-stable projection of attest.Record. The
// underlying type has no json tags, so we re-tag here to lock in
// snake_case for the wire format.
type AttestationRow struct {
	Revocation *attest.Revocation `json:"revocation,omitempty"`
	ID         int64              `json:"id"`
	ItemID     string             `json:"item_id"`
	Kind       string             `json:"kind"`
	Command    string             `json:"command"`
	ExitCode   int                `json:"exit_code"`
	OutputHash string             `json:"output_hash"`
	OutputPath string             `json:"output_path"`
	CreatedAt  int64              `json:"created_at"`
	AgentID    string             `json:"agent_id"`
	RepoID     string             `json:"repo_id"`
}

type AttestationsResult struct {
	Items []AttestationRow `json:"items"`
	Count int              `json:"count"`
}

func Attestations(ctx context.Context, args AttestationsArgs) (*AttestationsResult, error) {
	L := attest.New(args.DB, args.RepoID, nil)
	recs, err := L.ListForItem(ctx, args.ItemID)
	if err != nil {
		return nil, err
	}
	rows := make([]AttestationRow, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, AttestationRow{
			ID:         r.ID,
			Revocation: r.Revocation,
			ItemID:     r.ItemID,
			Kind:       string(r.Kind),
			Command:    r.Command,
			ExitCode:   r.ExitCode,
			OutputHash: r.OutputHash,
			OutputPath: r.OutputPath,
			CreatedAt:  r.CreatedAt,
			AgentID:    r.AgentID,
			RepoID:     r.RepoID,
		})
	}
	return &AttestationsResult{Items: rows, Count: len(rows)}, nil
}
