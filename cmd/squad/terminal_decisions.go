package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/terminalevents"
)

func terminalDecisionCommands() []*cobra.Command {
	var q terminalevents.DecisionRequest
	var mergePR string
	var mergeRelease terminalevents.MergeRelease
	var mergeTTL time.Duration
	set := &cobra.Command{Use: "decision-set", Short: "CAS the effective assignment decision and enqueue its wake", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		if mergePR != "" {
			m := prRef.FindStringSubmatch(mergePR)
			if m == nil || mergeTTL <= 0 {
				return fmt.Errorf("--merge-pr owner/repo#N and a positive --merge-ttl are required for a merge release")
			}
			mergeRelease.PRRepo = m[1]
			n, err := strconv.ParseInt(m[2], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid pull request number")
			}
			mergeRelease.PRNumber = n
			mergeRelease.ExpiresAt = time.Now().Add(mergeTTL).Unix()
			q.MergeRelease = &mergeRelease
		}
		d, err := (terminalevents.Store{DB: bc.db, Repo: bc.repoID, PRFacts: ghPRFacts}).Decide(cmd.Context(), bc.agentID, q)
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
	set.Flags().StringVar(&q.MergePolicy, "merge-policy", "", "standing (default) or release-required; empty keeps the current policy")
	set.Flags().StringVar(&mergePR, "merge-pr", "", "Issue a merge release for owner/repo#N (proceed only); verified live against the code host")
	set.Flags().StringVar(&mergeRelease.HeadSHA, "merge-head", "", "Exact 40-hex PR head the release covers")
	set.Flags().StringVar(&mergeRelease.BaseSHA, "merge-base", "", "Exact 40-hex base branch tip the release was prepared against")
	set.Flags().Int64Var(&mergeRelease.EvidenceOutcome, "merge-evidence", 0, "Existing canonical-thread message recording the independent review and CI result")
	set.Flags().DurationVar(&mergeTTL, "merge-ttl", 0, "Release lifetime (at most 4h)")
	var key, session string
	var generation, expected int64
	get := &cobra.Command{Use: "decision-get", Short: "Read the latest decision; --reservation, --generation and --worker-session are all required", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
	get.Flags().StringVar(&key, "reservation", "", "Exact reservation (required)")
	get.Flags().Int64Var(&generation, "generation", 0, "Generation (required, at least 1)")
	get.Flags().StringVar(&session, "worker-session", "", "Bound native session (required; never resolved implicitly)")
	get.Flags().Int64Var(&expected, "expected-revision", -1, "Assert this revision remains current")
	return []*cobra.Command{set, get}
}

var prRef = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*)#([1-9][0-9]*)$`)
