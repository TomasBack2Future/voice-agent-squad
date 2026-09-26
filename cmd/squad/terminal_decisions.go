package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/terminalevents"
)

func terminalDecisionCommands() []*cobra.Command {
	var q terminalevents.DecisionRequest
	set := &cobra.Command{Use: "decision-set", Short: "CAS the effective assignment decision and enqueue its wake", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		d, err := (terminalevents.Store{DB: bc.db, Repo: bc.repoID}).Decide(cmd.Context(), bc.agentID, q)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(d)
	}}
	set.Flags().StringVar(&q.Reservation, "reservation", "", "Exact reservation")
	set.Flags().Int64Var(&q.Generation, "generation", 0, "Generation")
	set.Flags().StringVar(&q.WorkerSession, "worker-session", "", "Bound native session")
	set.Flags().Int64Var(&q.ExpectedRevision, "expected-revision", 0, "Current revision; zero adopts versioned decisions")
	set.Flags().Int64Var(&q.OutcomeID, "outcome", 0, "Existing owner-authored canonical message")
	set.Flags().StringVar(&q.Action, "action", "", "proceed or hold; does not extend authority")
	set.Flags().StringVar(&q.Condition, "condition", "", "Specific hold condition or verified recovery reference")
	var key, session string
	var generation, expected int64
	get := &cobra.Command{Use: "decision-get", Short: "Read the latest decision; optionally reject an outdated revision", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		d, err := (terminalevents.Store{DB: bc.db, Repo: bc.repoID}).CurrentDecision(cmd.Context(), key, generation, session)
		if err != nil {
			return err
		}
		if err = json.NewEncoder(cmd.OutOrStdout()).Encode(d); err != nil {
			return err
		}
		if expected >= 0 && d.Revision != expected {
			return fmt.Errorf("%w", terminalevents.ErrStaleDecision)
		}
		return nil
	}}
	get.Flags().StringVar(&key, "reservation", "", "Exact reservation")
	get.Flags().Int64Var(&generation, "generation", 0, "Generation")
	get.Flags().StringVar(&session, "worker-session", "", "Bound native session")
	get.Flags().Int64Var(&expected, "expected-revision", -1, "Assert this revision remains current")
	return []*cobra.Command{set, get}
}
