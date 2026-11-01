package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zsiec/squad/internal/config"
	"github.com/zsiec/squad/internal/hygiene"
	"github.com/zsiec/squad/internal/identity"
	"github.com/zsiec/squad/internal/items"
	"github.com/zsiec/squad/internal/learning"
	"github.com/zsiec/squad/internal/store"
)

// doctorLearningDebounceDays is the per-(repo, code) debounce window
// for auto-emitted doctor learnings. The same drift class shouldn't
// produce a fresh proposal on every nightly sweep — once a week is
// already aggressive given the proposed-queue triage cost.
const doctorLearningDebounceDays = 7

// proposeDoctorLearnings emits at most one proposed gotcha learning
// per finding code for the current repo, debounced to once per
// doctorLearningDebounceDays. Failures are best-effort with a stderr
// warning so the sweep itself isn't aborted by a learning-write hiccup.
// When findings is empty, returns immediately without DB or fs work.
func proposeDoctorLearnings(ctx context.Context, repoRoot string, findings []hygiene.Finding) {
	if len(findings) == 0 {
		return
	}
	existing, err := learning.Walk(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: doctor learnings: walk learnings: %v\n", err)
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -doctorLearningDebounceDays)
	emittedRecently := map[string]bool{}
	for _, l := range existing {
		if !hasTag(l.Tags, "doctor-finding") {
			continue
		}
		t, err := time.Parse("2006-01-02", l.Created)
		if err != nil || !t.After(cutoff) {
			continue
		}
		for _, tg := range l.Tags {
			if code, ok := strings.CutPrefix(tg, "doctor-code-"); ok {
				emittedRecently[code] = true
			}
		}
	}

	byCode := map[string][]hygiene.Finding{}
	codeOrder := []string{}
	for _, f := range findings {
		if _, seen := byCode[f.Code]; !seen {
			codeOrder = append(codeOrder, f.Code)
		}
		byCode[f.Code] = append(byCode[f.Code], f)
	}
	agentID, _ := identity.AgentID()
	date := time.Now().UTC().Format("2006-01-02")
	for _, code := range codeOrder {
		slugCode := strings.ReplaceAll(code, "_", "-")
		if emittedRecently[slugCode] {
			continue
		}
		group := byCode[code]
		var sb strings.Builder
		fmt.Fprintf(&sb, "`squad doctor` found %d %s finding(s):\n\n", len(group), code)
		for _, f := range group {
			sb.WriteString("- " + f.Message + "\n")
			if f.Fix != "" {
				sb.WriteString("  fix: " + f.Fix + "\n")
			}
		}
		_, perr := LearningPropose(ctx, LearningProposeArgs{
			RepoRoot: repoRoot,
			Kind:     "gotcha",
			Slug:     "doctor-" + slugCode + "-" + date,
			// No colon in the title — stubBody emits frontmatter as
			// unquoted YAML scalars, and "key: value: extra" is not a
			// valid plain scalar. An unparseable artifact silently breaks
			// the debounce check (learning.Walk skips it, emittedRecently
			// stays empty, every sweep re-emits a new file with the same
			// or different slug).
			Title:        "doctor finding " + code,
			Area:         "hygiene",
			Looks:        sb.String(),
			Tags:         []string{"doctor-finding", "doctor-code-" + slugCode},
			RelatedItems: []string{},
			CreatedBy:    agentID,
		})
		if perr != nil {
			var sce *SlugCollisionError
			if !errors.As(perr, &sce) {
				fmt.Fprintf(os.Stderr, "warn: doctor learnings: propose %s: %v\n", code, perr)
			}
		}
	}
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// checkWorktreeOrphans scans <squadDir>/worktrees/ for directories that have
// no matching active claim row. Each unmatched directory becomes a finding
// with the path and a `git worktree remove` fix. Returns empty when the
// worktrees dir is absent (the common case for users who don't opt in).
func checkWorktreeOrphans(ctx context.Context, db *sql.DB, repoID, squadDir string) []hygiene.Finding {
	wtDir := filepath.Join(squadDir, "worktrees")
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		return nil
	}
	active := map[string]struct{}{}
	rows, err := db.QueryContext(ctx,
		`SELECT COALESCE(worktree,'') FROM claims WHERE repo_id = ? AND worktree != ''`, repoID)
	if err == nil {
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err == nil {
				active[canonicalizePath(p)] = struct{}{}
			}
		}
		rows.Close()
	}
	var findings []hygiene.Finding
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		full := filepath.Join(wtDir, e.Name())
		if _, ok := active[canonicalizePath(full)]; ok {
			continue
		}
		findings = append(findings, hygiene.Finding{
			Severity: hygiene.SeverityWarn,
			Code:     "worktree_orphan",
			Message:  "orphan worktree: " + full + " has no matching active claim",
			Fix:      "cd <repo-root> && git worktree remove --force " + full,
		})
	}
	return findings
}

// canonicalizePath resolves a path through filepath.Abs and EvalSymlinks so
// the orphan check matches paths that the worktree library writes (which
// run through EvalSymlinks via filepath.Abs at provision time on macOS,
// where /tmp → /private/tmp).
func canonicalizePath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

const (
	intakeStaleCaptureThreshold    = 30 * 24 * time.Hour
	intakeInboxOverflowThreshold   = 50
	intakeRejectedLogSizeThreshold = 500
)

// DoctorArgs is the input for Doctor. Mirrors bootClaimContext (db + repoID +
// items dir) so MCP can invoke the same sweep CLI uses without re-deriving.
type DoctorArgs struct {
	DB       *sql.DB `json:"-"`
	RepoID   string  `json:"-"`
	RepoRoot string  `json:"-"`
}

// DoctorFinding is the structured per-finding payload MCP callers receive.
// It carries the same fields the CLI prints in its bullet list, plus a
// severity hint so dashboards can color-code without re-parsing.
type DoctorFinding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

// DoctorResult is the structured response shape for MCP callers. Total is
// len(Findings) so callers can do a quick clean/dirty check without walking
// the array.
type DoctorResult struct {
	Findings []DoctorFinding `json:"findings"`
	Total    int             `json:"total"`
}

// Doctor runs the same hygiene sweep `squad doctor` runs from the CLI and
// returns a structured findings list. Hook-drift detection is intentionally
// skipped for MCP callers: it depends on knowing where the plugin is
// installed, which is a CLI-side concern.
func Doctor(ctx context.Context, args DoctorArgs) (*DoctorResult, error) {
	if args.DB == nil || args.RepoID == "" || args.RepoRoot == "" {
		return nil, asInvalidParams(errNoRepo)
	}
	squadDir := filepath.Join(args.RepoRoot, ".squad")
	adapter := itemsHygieneAdapter{squadDir: squadDir}
	sw := hygiene.New(args.DB, args.RepoID, adapter)
	if cfg, err := config.Load(args.RepoRoot); err == nil && cfg.Hygiene.StaleClaimMinutes > 0 {
		sw = sw.WithStaleSeconds(int64(cfg.Hygiene.StaleClaimMinutes) * 60)
	}
	findings, err := sw.Sweep(ctx)
	if err != nil {
		return nil, err
	}
	findings = append(findings, checkWorktreeOrphans(ctx, args.DB, args.RepoID, squadDir)...)
	out := make([]DoctorFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, DoctorFinding{
			Severity: severityName(f.Severity),
			Code:     f.Code,
			Message:  f.Message,
			Fix:      f.Fix,
		})
	}
	return &DoctorResult{Findings: out, Total: len(out)}, nil
}

func severityName(s hygiene.Severity) string {
	switch s {
	case hygiene.SeverityError:
		return "error"
	case hygiene.SeverityWarn:
		return "warn"
	default:
		return "info"
	}
}

// itemsHygieneAdapter walks .squad/items and .squad/done and reports each
// item's id, path, status, and references for the hygiene Sweep.
type itemsHygieneAdapter struct {
	squadDir string
}

func (a itemsHygieneAdapter) List(ctx context.Context) ([]hygiene.ItemRef, error) {
	w, err := items.Walk(a.squadDir)
	if err != nil {
		return nil, err
	}
	var out []hygiene.ItemRef
	for _, group := range [][]items.Item{w.Active, w.Done} {
		for _, it := range group {
			out = append(out, hygiene.ItemRef{
				ID:               it.ID,
				Path:             it.Path,
				Status:           it.Status,
				Area:             it.Area,
				Created:          it.Created,
				Updated:          it.Updated,
				References:       it.References,
				BlockedBy:        it.BlockedBy,
				EvidenceRequired: it.EvidenceRequired,
			})
		}
	}
	return out, nil
}

func (a itemsHygieneAdapter) Broken(ctx context.Context) ([]hygiene.BrokenRef, error) {
	w, err := items.Walk(a.squadDir)
	if err != nil {
		return nil, err
	}
	out := make([]hygiene.BrokenRef, 0, len(w.Broken))
	for _, b := range w.Broken {
		out = append(out, hygiene.BrokenRef{Path: b.Path, Error: b.Error})
	}
	return out, nil
}

func newDoctorCmd() *cobra.Command {
	var strict bool
	var noLearnings bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose stale claims, ghost agents, orphan touches, broken refs, and DB integrity",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			bc, err := bootClaimContext(ctx)
			if err != nil {
				return err
			}
			defer bc.Close()
			squadDir := filepath.Dir(bc.itemsDir)
			repoRoot := filepath.Dir(squadDir)
			adapter := itemsHygieneAdapter{squadDir: squadDir}
			sw := hygiene.New(bc.db, bc.repoID, adapter)
			if cfg, err := config.Load(repoRoot); err == nil && cfg.Hygiene.StaleClaimMinutes > 0 {
				sw = sw.WithStaleSeconds(int64(cfg.Hygiene.StaleClaimMinutes) * 60)
			}
			findings, err := sw.Sweep(ctx)
			if err != nil {
				return err
			}
			intakeFindings := checkIntake(ctx, bc.db, bc.repoID, squadDir)
			findings = append(findings, intakeFindings...)
			findings = append(findings, checkWorktreeOrphans(ctx, bc.db, bc.repoID, squadDir)...)
			hookFindings := checkHookDrift(cmd.OutOrStdout())
			if !noLearnings {
				proposeDoctorLearnings(ctx, repoRoot, findings)
			}
			totalFindings := len(findings) + len(hookFindings)
			if totalFindings == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "doctor: all clear")
				return nil
			}
			if len(findings) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "doctor: %d finding(s):\n", len(findings))
				for _, f := range findings {
					fmt.Fprintf(cmd.OutOrStdout(), "  - [%s] %s\n", f.Code, f.Message)
					if f.Fix != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "      fix: %s\n", f.Fix)
					}
				}
			}
			if strict {
				return fmt.Errorf("doctor: %d finding(s) — see output above", totalFindings)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false,
		"exit non-zero if any findings exist; use this in CI to fail the job")
	cmd.Flags().BoolVar(&noLearnings, "no-learnings", false,
		"skip the auto-emit of proposed gotcha learnings per finding kind (CI/scripted audits)")
	return cmd
}

// checkIntake runs the three intake-health checks (stale captures, inbox
// overflow, rejected-log size) and returns their findings. Folded into the
// main `findings` slice so the existing print loop and strict-mode counter
// pick them up uniformly.
func checkIntake(ctx context.Context, db *sql.DB, repoID, squadDir string) []hygiene.Finding {
	var all []hygiene.Finding
	all = append(all, hygiene.CheckStaleCaptures(ctx, db, repoID, intakeStaleCaptureThreshold)...)
	all = append(all, hygiene.CheckInboxOverflow(ctx, db, repoID, intakeInboxOverflowThreshold)...)
	all = append(all, hygiene.CheckRejectedLogSize(squadDir, intakeRejectedLogSizeThreshold)...)
	return all
}

// checkHookDrift inspects the on-disk hook scripts (the user can edit these,
// and an old install can ship hooks the newer binary has since updated)
// against the embedded canonical bytes and prints any drift. Silent skip
// when no install dir exists — squad doctor should not nag users who have
// not installed the plugin via squad.
func checkHookDrift(out io.Writer) []hygiene.HookFinding {
	dir := resolveHookInstallDir()
	if dir == "" {
		return nil
	}
	findings, err := hygiene.DetectHookDrift(dir)
	if err != nil {
		fmt.Fprintf(out, "doctor: hook drift check skipped: %v\n", err)
		return nil
	}
	if len(findings) == 0 {
		return nil
	}
	fmt.Fprintf(out, "hooks: %d installed hook script(s) differ from this binary (%s)\n", len(findings), dir)
	for _, f := range findings {
		switch f.Kind {
		case hygiene.DriftMissing:
			fmt.Fprintf(out, "  - %s: missing (run `squad install-plugin` to install)\n", f.Filename)
		default:
			fmt.Fprintf(out, "  - %s: %s (run `squad install-plugin --uninstall && squad install-plugin` to restore)\n", f.Filename, f.Kind)
		}
	}
	return findings
}

// resolveHookInstallDir picks the directory squad doctor should diff against:
//  1. ${CLAUDE_PLUGIN_ROOT}/hooks — set by Claude Code at hook-execution time.
//  2. ~/.claude/plugins/squad/hooks — where install-plugin lays the plugin.
//  3. ~/.squad/hooks — where mergeSquadHooks materializes scripts when hooks
//     are registered through settings.json.
//
// Returns "" when none exist (silent skip).
func resolveHookInstallDir() string {
	if root := os.Getenv("CLAUDE_PLUGIN_ROOT"); root != "" {
		dir := filepath.Join(root, "hooks")
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".claude", "plugins", "squad", "hooks"))
	}
	if squadHome, err := store.Home(); err == nil {
		candidates = append(candidates, filepath.Join(squadHome, "hooks"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}
