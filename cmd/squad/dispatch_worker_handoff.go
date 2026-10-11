package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/dispatch"
)

func decodeWorkerHandoff(raw []byte) (dispatch.WorkerHandoffRequest, error) {
	var q dispatch.WorkerHandoffRequest
	if len(raw) > 32768 {
		return q, errors.New("bounded Worker handoff request required")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&q); err != nil {
		return q, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return q, errors.New("one Worker handoff request required")
	}
	return q, nil
}

func workerHandoffNative(q dispatch.WorkerHandoffRequest) error {
	if os.Getenv("SQUAD_NATIVE_SESSION_ID") != q.Controller.Native {
		return errors.New("explicit original controller native runtime binding required")
	}
	return nil
}

func workerHandoffCommands() []*cobra.Command {
	var commands []*cobra.Command
	for _, digestOnly := range []bool{false, true} {
		var path string
		name := "worker-handoff"
		description := "Current-controller CAS of a joined, consenting source Worker; preserves hold, excludes ENV"
		if digestOnly {
			name = "worker-handoff-digest"
			description = "Hash the exact request for original-owner canonical consent; no mutation"
		}
		cmd := &cobra.Command{Use: name, Short: description, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			raw, err := io.ReadAll(io.LimitReader(file, 32769))
			if err != nil {
				return err
			}
			q, err := decodeWorkerHandoff(raw)
			if err != nil {
				return err
			}
			if digestOnly {
				hash, err := dispatch.WorkerHandoffDigest(q)
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"schema_version": "squad.worker-handoff-consent.v1", "request_sha256": hash})
			}
			if err = workerHandoffNative(q); err != nil {
				return err
			}
			bc, err := bootClaimContext(cmd.Context())
			if err != nil {
				return err
			}
			defer bc.Close()
			receipt, err := dispatch.New(bc.db, bc.repoID, nil).WorkerHandoff(cmd.Context(), bc.agentID, q)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(receipt)
		}}
		cmd.Flags().StringVar(&path, "request", "", "exact JSON Worker custody and original-owner consent request")
		_ = cmd.MarkFlagRequired("request")
		commands = append(commands, cmd)
	}
	get := &cobra.Command{Use: "worker-handoff-get REQUEST_ID", Short: "Read an immutable supervised Worker handoff receipt", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		receipt, err := dispatch.New(bc.db, bc.repoID, nil).WorkerHandoffReceipt(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(receipt)
	}}
	commands = append(commands, get)
	return commands
}
