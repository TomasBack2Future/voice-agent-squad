package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/terminalevents"
)

func terminalDecisionCommands() []*cobra.Command {
	var q terminalevents.DecisionRequest
	var mergePR string
	var mergeRelease terminalevents.MergeRelease
	var mergeTTL, timeBox time.Duration
	var waitSpecs []string
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
		if q.WaitsFor, err = parseWaits(waitSpecs); err != nil {
			return err
		}
		if timeBox > 0 {
			q.WaitDeadline = time.Now().Add(timeBox).Unix()
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
	set.Flags().StringArrayVar(&waitSpecs, "wait-for", nil, "Structured hold dependency RESERVATION:PHASE:executed|passed or RESERVATION::terminal (repeatable)")
	set.Flags().StringVar(&q.BlocksPhase, "blocks-phase", "", "Phase of this assignment the hold stops (empty: every phase)")
	set.Flags().DurationVar(&timeBox, "wait-time-box", 0, "Optional time box for the waits; expiry is reported for reconciliation, never auto-proceeds")
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
	var reorderFile string
	reorder := &cobra.Command{Use: "decision-reorder", Short: "Apply several decision CAS steps atomically, in order (predecessor proceed before successor wait)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var qs []terminalevents.DecisionRequest
		if err := readDeliveryJSON(reorderFile, &qs); err != nil {
			return err
		}
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		ds, err := (terminalevents.Store{DB: bc.db, Repo: bc.repoID, PRFacts: ghPRFacts}).DecideBatch(cmd.Context(), bc.agentID, qs)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(ds)
	}}
	reorder.Flags().StringVar(&reorderFile, "request", "", "JSON array of decision requests (same fields as the MCP decision tool; wait_deadline is unix seconds)")
	_ = reorder.MarkFlagRequired("request")
	var p terminalevents.PhaseResult
	phase := &cobra.Command{Use: "phase-result", Short: "Record that a phase's checks ran (all/partial) and its verdict; wakes the Dispatcher to consume it", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		out, err := (terminalevents.Store{DB: bc.db, Repo: bc.repoID}).RecordPhaseResult(cmd.Context(), bc.agentID, p)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}}
	phase.Flags().StringVar(&p.Reservation, "reservation", "", "Exact reservation")
	phase.Flags().Int64Var(&p.Generation, "generation", 0, "Generation")
	phase.Flags().StringVar(&p.WorkerSession, "worker-session", "", "Bound native session")
	phase.Flags().StringVar(&p.Phase, "phase", "", "Phase name, for example s6-acceptance or merge")
	phase.Flags().StringVar(&p.Checks, "checks", "", "all or partial: whether every planned check of the phase ran")
	phase.Flags().StringVar(&p.Verdict, "verdict", "", "passed, failed or partial")
	phase.Flags().StringVar(&p.Deferred, "deferred", "", "What moved to a later phase, for example a re-verification on the next candidate")
	phase.Flags().Int64Var(&p.OutcomeID, "outcome", 0, "Your own canonical-thread message carrying the evidence")
	return []*cobra.Command{set, get, reorder, phase}
}

// parseWaits reads RESERVATION:PHASE:UNTIL; the reservation may itself contain
// colons, so the last two fields are split from the right.
func parseWaits(specs []string) ([]terminalevents.Wait, error) {
	out := make([]terminalevents.Wait, 0, len(specs))
	for _, spec := range specs {
		i := strings.LastIndex(spec, ":")
		j := -1
		if i > 0 {
			j = strings.LastIndex(spec[:i], ":")
		}
		if j <= 0 {
			return nil, fmt.Errorf("--wait-for %q: want RESERVATION:PHASE:UNTIL or RESERVATION::terminal", spec)
		}
		out = append(out, terminalevents.Wait{Reservation: spec[:j], Phase: spec[j+1 : i], Until: spec[i+1:]})
	}
	return out, nil
}

var prRef = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*)#([1-9][0-9]*)$`)
