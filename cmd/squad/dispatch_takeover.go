package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/dispatch"
)

// Shared by the CLI and MCP: unknown fields fail before any live mutation.
func decodeTakeover(raw []byte) (dispatch.TakeoverRequest, error) {
	var q dispatch.TakeoverRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&q); err != nil {
		return q, err
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return q, fmt.Errorf("expected exactly one takeover request")
	}
	return q, nil
}
func newDispatchTakeoverCmd() *cobra.Command {
	var path string
	c := &cobra.Command{Use: "takeover", Short: "Operator CAS of stopped session custody; ordinary claims only", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		q, e := decodeTakeover(raw)
		if e != nil {
			return e
		}
		bc, e := bootClaimContext(c.Context())
		if e != nil {
			return e
		}
		defer bc.Close()
		r, e := dispatch.New(bc.db, bc.repoID, nil).Takeover(c.Context(), bc.agentID, q)
		if e != nil {
			return e
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(r)
	}}
	c.Flags().StringVar(&path, "request", "", "JSON stopped-holder handoff request")
	_ = c.MarkFlagRequired("request")
	return c
}
