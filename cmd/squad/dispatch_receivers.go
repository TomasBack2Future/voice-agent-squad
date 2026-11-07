package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/dispatch"
)

// newDispatchReceiversCmd reports, read-only, which squad executable each live
// local receiver and MCP process runs, against the installed target that new
// launches resolve. Installation and adoption are verified separately.
func newDispatchReceiversCmd() *cobra.Command {
	var installed string
	cmd := &cobra.Command{Use: "receivers", Short: "Read which squad binary each local receiver/MCP process runs versus the installed target (no mutation)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if installed == "" {
			if self, err := os.Executable(); err == nil {
				installed = self
			}
		}
		if resolved, err := filepath.EvalSymlinks(installed); err == nil {
			installed = resolved
		}
		out, err := exec.CommandContext(cmd.Context(), "ps", "-axo", "pid=,args=").Output()
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(dispatch.ParseReceiverProcesses(string(out), installed))
	}}
	cmd.Flags().StringVar(&installed, "installed", "", "Installed squad path (default: this executable, symlinks resolved)")
	return cmd
}
