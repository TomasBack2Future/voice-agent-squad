package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/dispatch"
)

func newWorkerExecutionCmd() *cobra.Command {
	root := &cobra.Command{Use: "worker-execution", Short: "Pin and check one source Worker's complete custody tuple"}
	for _, action := range []string{"acquire", "check", "check-read", "check-write", "suspend", "outcome", "close"} {
		var path, evidence, report string
		c := &cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
			info, err := os.Lstat(path)
			if err != nil || !filepath.IsAbs(path) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8192 {
				return errors.New("private bounded immutable Worker binding required")
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			var b dispatch.WorkerExecutionBinding
			d := json.NewDecoder(io.LimitReader(f, 8193))
			d.DisallowUnknownFields()
			if err := d.Decode(&b); err != nil {
				return err
			}
			if err := d.Decode(new(any)); err != io.EOF {
				return errors.New("expected one bounded execution binding")
			}
			bc, err := bootClaimContext(c.Context())
			if err != nil {
				return err
			}
			defer bc.Close()
			s := dispatch.New(bc.db, bc.repoID, nil)
			switch action {
			case "acquire":
				err = s.AcquireWorkerExecution(c.Context(), bc.agentID, b)
			case "check":
				err = s.CheckWorkerExecution(c.Context(), bc.agentID, b)
			case "check-read":
				err = s.CheckWorkerRead(c.Context(), bc.agentID, b)
			case "check-write":
				err = s.CheckWorkerWrite(c.Context(), bc.agentID, b)
			case "suspend":
				err = s.SuspendWorkerExecution(c.Context(), bc.agentID, b)
			case "outcome":
				if err := verifyWorkerJoin(c.Context(), b, path, evidence); err != nil {
					return err
				}
				raw, readErr := os.ReadFile(report)
				if readErr != nil {
					return readErr
				}
				var r struct {
					Status  string `json:"status"`
					Summary string `json:"summary"`
				}
				if len(raw) > 32768 || json.Unmarshal(raw, &r) != nil {
					return errors.New("bounded native report required")
				}
				id, revision, err := s.WorkerOutcome(c.Context(), bc.agentID, b, r.Status, r.Summary)
				if err != nil {
					return err
				}
				return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"status": "ok", "action": action, "id": b.ID, "outcome_id": id, "decision_revision": revision})
			case "close":
				if err := verifyWorkerJoin(c.Context(), b, path, evidence); err != nil {
					return err
				}
				err = s.CloseWorkerExecution(c.Context(), bc.agentID, b, evidence)
			}
			if err != nil {
				return err
			}
			return json.NewEncoder(c.OutOrStdout()).Encode(map[string]string{"status": "ok", "action": action, "id": b.ID})
		}}
		c.Flags().StringVar(&path, "binding", "", "absolute immutable Worker execution binding")
		_ = c.MarkFlagRequired("binding")
		if action == "close" || action == "outcome" {
			c.Flags().StringVar(&evidence, "evidence", "", "joined native and owned tools evidence reference")
			_ = c.MarkFlagRequired("evidence")
		}
		if action == "outcome" {
			c.Flags().StringVar(&report, "report", "", "native report file")
			_ = c.MarkFlagRequired("report")
		}
		root.AddCommand(c)
	}
	return root
}
