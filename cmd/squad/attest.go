package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zsiec/squad/internal/attest"
)

// ErrInvalidKind signals that AttestArgs.Kind is not one of the
// attest.Kind enum values. Cobra wrapper renders it as the legacy
// "invalid kind" message; MCP callers can errors.Is it.
var ErrInvalidKind = errors.New("invalid attestation kind")

type AttestArgs struct {
	DB      *sql.DB `json:"-"`
	RepoID  string  `json:"repo_id"`
	AgentID string  `json:"agent_id"`

	ItemID string `json:"item_id"`
	Kind   string `json:"kind"`

	Command       string `json:"command,omitempty"`
	FindingsFile  string `json:"findings_file,omitempty"`
	ReviewerAgent string `json:"reviewer_agent,omitempty"`

	AttDir   string `json:"att_dir,omitempty"`
	RepoRoot string `json:"repo_root,omitempty"`
	WorkDir  string `json:"work_dir,omitempty"`

	Now func() time.Time `json:"-"`
}

type AttestResult struct {
	ID         int64  `json:"id"`
	ItemID     string `json:"item_id"`
	Kind       string `json:"kind"`
	Command    string `json:"command"`
	ExitCode   int    `json:"exit_code"`
	OutputHash string `json:"output_hash"`
	OutputPath string `json:"output_path"`
	AgentID    string `json:"agent_id"`
}

func Attest(ctx context.Context, args AttestArgs) (*AttestResult, error) {
	if args.ItemID == "" {
		return nil, fmt.Errorf("item_id is required")
	}
	k := attest.Kind(args.Kind)
	if !k.Valid() {
		return nil, fmt.Errorf("%w %q (want test|lint|typecheck|build|review|manual)", ErrInvalidKind, args.Kind)
	}

	L := attest.New(args.DB, args.RepoID, args.Now)

	if k == attest.KindReview {
		if args.WorkDir != "" {
			return nil, fmt.Errorf("work_dir is only supported for command attestations")
		}
		rec, err := recordReviewAttestation(ctx, L, recordReviewArgs{
			ItemID:        args.ItemID,
			AgentID:       args.AgentID,
			ReviewerAgent: args.ReviewerAgent,
			FindingsFile:  args.FindingsFile,
			AttDir:        args.AttDir,
		})
		if err != nil {
			return nil, err
		}
		if rec.ExitCode != 0 && args.RepoRoot != "" {
			// A blocking review's narrative is the highest-signal observation
			// in the system. Auto-file it as a proposed gotcha so future
			// reviewers can find the pattern. Failure to file does not
			// abort the attestation itself, but it does surface to stderr
			// so genuine fs/permission errors don't vanish silently.
			if perr := proposeReviewRejectionLearning(ctx, L, args.RepoRoot, args.ItemID, rec, args.FindingsFile); perr != nil {
				fmt.Fprintf(os.Stderr, "warn: auto-learning from blocking review failed: %v\n", perr)
			}
		}
		return recordToResult(rec), nil
	}

	if args.Command == "" {
		return nil, fmt.Errorf("command is required for kind=%s", args.Kind)
	}
	rec, err := L.Run(ctx, attest.RunOpts{
		ItemID:   args.ItemID,
		Kind:     k,
		Command:  args.Command,
		AgentID:  args.AgentID,
		AttDir:   args.AttDir,
		RepoRoot: args.RepoRoot,
		WorkDir:  args.WorkDir,
	})
	if err != nil {
		return nil, err
	}
	return recordToResult(rec), nil
}

func recordToResult(r attest.Record) *AttestResult {
	return &AttestResult{
		ID:         r.ID,
		ItemID:     r.ItemID,
		Kind:       string(r.Kind),
		Command:    r.Command,
		ExitCode:   r.ExitCode,
		OutputHash: r.OutputHash,
		OutputPath: r.OutputPath,
		AgentID:    r.AgentID,
	}
}

func newAttestCmd() *cobra.Command {
	var item, kind, command, findingsFile, reviewerAgent, workDir string
	cmd := &cobra.Command{
		Use:   "attest [<item-id>]",
		Short: "Record a verification artifact (test/lint/build/typecheck/manual) into the evidence ledger",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			itemID := item
			if len(args) == 1 {
				if itemID != "" && itemID != args[0] {
					return fmt.Errorf("item id given twice (positional %q, --item %q)", args[0], itemID)
				}
				itemID = args[0]
			}

			ctx := cmd.Context()
			bc, err := bootClaimContext(ctx)
			if err != nil {
				return err
			}
			defer bc.Close()
			repoRoot, err := discoverRepoRoot()
			if err != nil {
				return err
			}
			attDir := filepath.Join(repoRoot, ".squad", "attestations")

			res, err := Attest(ctx, AttestArgs{
				DB:            bc.db,
				RepoID:        bc.repoID,
				AgentID:       bc.agentID,
				ItemID:        itemID,
				Kind:          kind,
				Command:       command,
				FindingsFile:  findingsFile,
				ReviewerAgent: reviewerAgent,
				AttDir:        attDir,
				RepoRoot:      repoRoot,
				WorkDir:       workDir,
			})
			if err != nil {
				if errors.Is(err, ErrInvalidKind) {
					return fmt.Errorf("invalid kind %q (want test|lint|typecheck|build|review|manual)", kind)
				}
				return err
			}

			if attest.Kind(res.Kind) == attest.KindReview {
				fmt.Fprintf(cmd.OutOrStdout(), "attest review %s exit=%d hash=%s\n", res.ItemID, res.ExitCode, res.OutputHash)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "attest %s %s exit=%d hash=%s\n", res.Kind, res.ItemID, res.ExitCode, res.OutputHash)
			if res.ExitCode != 0 {
				bc.Close()
				os.Exit(res.ExitCode)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&item, "item", "", "item id (or pass as the positional argument)")
	cmd.Flags().StringVar(&kind, "kind", "", "test|lint|typecheck|build|review|manual")
	cmd.Flags().StringVar(&command, "command", "", "shell command to run and capture")
	cmd.Flags().StringVar(&workDir, "work-dir", "", "absolute execution directory (default: selected Squad repository)")
	cmd.Flags().StringVar(&findingsFile, "findings-file", "", "review findings file (kind=review only)")
	cmd.Flags().StringVar(&reviewerAgent, "reviewer-agent", "", "reviewer agent id (kind=review only)")
	return cmd
}

type recordReviewArgs struct {
	ItemID        string
	AgentID       string
	ReviewerAgent string
	FindingsFile  string
	AttDir        string
}

func recordReviewAttestation(ctx context.Context, L *attest.Ledger, a recordReviewArgs) (attest.Record, error) {
	if a.FindingsFile == "" {
		return attest.Record{}, fmt.Errorf("--findings-file is required for kind=review")
	}
	if a.ReviewerAgent == "" {
		return attest.Record{}, fmt.Errorf("--reviewer-agent is required for kind=review")
	}
	body, err := os.ReadFile(a.FindingsFile)
	if err != nil {
		return attest.Record{}, fmt.Errorf("read findings: %w", err)
	}
	exit := parseReviewExit(body)
	hash := L.Hash(body)
	if err := os.MkdirAll(a.AttDir, 0o755); err != nil {
		return attest.Record{}, err
	}
	out := filepath.Join(a.AttDir, hash+".txt")
	if err := os.WriteFile(out, body, 0o644); err != nil {
		return attest.Record{}, err
	}
	rec := attest.Record{
		ItemID:     a.ItemID,
		Kind:       attest.KindReview,
		Command:    "review by " + a.ReviewerAgent,
		ExitCode:   exit,
		OutputHash: hash,
		OutputPath: out,
		AgentID:    a.AgentID,
	}
	id, err := L.Insert(ctx, rec)
	if err != nil {
		return attest.Record{}, err
	}
	rec.ID = id
	return rec, nil
}

// proposeReviewRejectionLearning auto-files a proposed gotcha learning
// when a review attestation lands with a non-zero exit. The rejection
// narrative (everything after the first `---` separator in the findings
// body) becomes the `## Looks like` section. If the same item already
// has a prior blocking review, the new learning is tagged
// `second-round` so triage can prioritize patterns biting twice.
func proposeReviewRejectionLearning(ctx context.Context, L *attest.Ledger, repoRoot, itemID string, rec attest.Record, findingsFile string) error {
	body, err := os.ReadFile(findingsFile)
	if err != nil {
		return err
	}
	narrative := extractRejectionNarrative(body)
	if narrative == "" {
		return nil
	}
	priorBlocking, err := countPriorBlockingReviews(ctx, L, itemID, rec.ID)
	if err != nil {
		return err
	}
	tags := []string{"review-rejection"}
	if priorBlocking > 0 {
		tags = append(tags, "second-round")
	}
	hashSlug := rec.OutputHash
	if len(hashSlug) > 12 {
		hashSlug = hashSlug[:12]
	}
	baseSlug := "review-rejection-" + hashSlug
	args := LearningProposeArgs{
		RepoRoot:     repoRoot,
		Kind:         "gotcha",
		Slug:         baseSlug,
		Title:        "review rejection captured from blocking findings",
		Area:         "review",
		Looks:        narrative,
		RelatedItems: []string{itemID},
		Tags:         tags,
		CreatedBy:    rec.AgentID,
	}
	for n := 1; n <= 9; n++ {
		if n > 1 {
			args.Slug = fmt.Sprintf("%s-%d", baseSlug, n)
		}
		if _, perr := LearningPropose(ctx, args); perr == nil {
			return nil
		} else {
			var sce *SlugCollisionError
			if !errors.As(perr, &sce) {
				return perr
			}
		}
	}
	return fmt.Errorf("review rejection slug %q exhausted suffix range", baseSlug)
}

func extractRejectionNarrative(body []byte) string {
	parts := strings.SplitN(string(body), "\n---\n", 2)
	if len(parts) < 2 {
		return strings.TrimSpace(string(body))
	}
	return strings.TrimSpace(parts[1])
}

func countPriorBlockingReviews(ctx context.Context, L *attest.Ledger, itemID string, excludeID int64) (int, error) {
	all, err := L.ListForItem(ctx, itemID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range all {
		if r.ID != excludeID && r.Kind == attest.KindReview && r.ExitCode != 0 {
			n++
		}
	}
	return n, nil
}

func parseReviewExit(body []byte) int {
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			break
		}
		if !strings.HasPrefix(trimmed, "status:") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(trimmed, "status:"))
		if val == "blocking" {
			return 1
		}
		return 0
	}
	return 0
}
