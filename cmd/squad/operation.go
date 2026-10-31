package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/attest"
	"github.com/zsiec/squad/internal/terminalevents"
)

type operationInput struct {
	Identity       terminalevents.OperationRequest `json:"identity"`
	Kind           attest.Kind                     `json:"kind"`
	Command        string                          `json:"command,omitempty"`
	Argv           []string                        `json:"argv,omitempty"`
	WorkDir        string                          `json:"work_dir,omitempty"`
	TimeoutSeconds int                             `json:"timeout_seconds"`
}

func runOwnedOperation(ctx context.Context, bc *claimContext, root string, input operationInput) (terminalevents.OperationResult, error) {
	if err := requireBoundNative(input.Identity.WorkerSession); err != nil {
		return terminalevents.OperationResult{}, err
	}
	if input.TimeoutSeconds <= 0 || input.TimeoutSeconds > 86400 {
		return terminalevents.OperationResult{}, fmt.Errorf("operation timeout_seconds must be in 1..86400")
	}
	var err error
	opCtx, cancel := context.WithTimeout(ctx, time.Duration(input.TimeoutSeconds)*time.Second)
	defer cancel()
	var item string
	err = bc.db.QueryRowContext(ctx, `SELECT canonical_item_id FROM dispatch_reservations WHERE repo_id=? AND item_id=? AND generation=? AND worker_thread_id=?`, bc.repoID, input.Identity.Reservation, input.Identity.Generation, input.Identity.WorkerSession).Scan(&item)
	if err != nil {
		return terminalevents.OperationResult{}, err
	}
	return (terminalevents.Store{DB: bc.db, Repo: bc.repoID}).RunOperation(opCtx, bc.agentID, input.Identity, attest.RunOpts{ItemID: item, AgentID: bc.agentID, Kind: input.Kind, Command: input.Command, Argv: input.Argv, WorkDir: input.WorkDir, RepoRoot: root, AttDir: filepath.Join(root, ".squad", "attestations")})
}

// operationSignalContext turns a catchable external cancellation (a client
// stopping its tracked task) into the existing cancellation path: the command's
// process group is killed and the failed result and operation-exit are still
// recorded, instead of the wrapper dying with an unresolved start.
func operationSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	// A wrapper deliberately detached (nohup, background job) ignores hangup/interrupt;
	// keep honoring that. A second signal after the first restores default handling.
	var signals []os.Signal
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !signal.Ignored(sig) {
			signals = append(signals, sig)
		}
	}
	if len(signals) == 0 {
		return parent, func() {}
	}
	ctx, stop := signal.NotifyContext(parent, signals...)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}

func newOperationCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "operation", Short: "Track an owned command's start, exit and original Worker delivery"}
	var runFile, statusFile string
	run := &cobra.Command{Use: "run", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var q operationInput
		if err := readDeliveryJSON(runFile, &q); err != nil {
			return err
		}
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		root, err := discoverRepoRoot()
		if err != nil {
			return err
		}
		ctx, stop := operationSignalContext(cmd.Context())
		defer stop()
		result, err := runOwnedOperation(ctx, bc, root, q)
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		return err
	}}
	run.Flags().StringVar(&runFile, "request", "", "Owned operation JSON; background this wrapper through the client's tracked task when needed")
	_ = run.MarkFlagRequired("request")
	status := &cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var q terminalevents.OperationRequest
		if err := readDeliveryJSON(statusFile, &q); err != nil {
			return err
		}
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		result, err := (terminalevents.Store{DB: bc.db, Repo: bc.repoID}).Operation(cmd.Context(), bc.agentID, q)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	status.Flags().StringVar(&statusFile, "request", "", "Exact operation identity JSON; started without exit remains unresolved")
	_ = status.MarkFlagRequired("request")
	cmd.AddCommand(run, status)
	return cmd
}
