package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/zsiec/squad/internal/grokreview"
	"github.com/zsiec/squad/reviewer"
)

type config struct {
	repository        string
	pullRequest       int
	checkName         string
	appID             int64
	installationID    int64
	appPrivateKey     string
	grokBinary        string
	githubBinary      string
	grokHome          string
	model             string
	reasoningEffort   string
	timeout           time.Duration
	maxGitHubOutput   int
	maxReviewerOutput int
}

type commandOutput struct {
	Repository      string               `json:"repository"`
	PullRequest     int                  `json:"pull_request"`
	BaseSHA         string               `json:"base_sha"`
	HeadSHA         string               `json:"head_sha"`
	Verdict         grokreview.Verdict   `json:"verdict"`
	Summary         string               `json:"summary"`
	Findings        []grokreview.Finding `json:"findings"`
	RequestID       string               `json:"request_id,omitempty"`
	SessionID       string               `json:"session_id,omitempty"`
	RequestedModel  string               `json:"requested_model,omitempty"`
	ResolvedModel   string               `json:"resolved_model,omitempty"`
	TotalTokens     int64                `json:"total_tokens,omitempty"`
	CostUSD         float64              `json:"cost_usd,omitempty"`
	DurationMillis  int64                `json:"duration_ms,omitempty"`
	CommentID       int64                `json:"comment_id,omitempty"`
	CommentURL      string               `json:"comment_url,omitempty"`
	CheckRunID      int64                `json:"check_run_id,omitempty"`
	CheckURL        string               `json:"check_url,omitempty"`
	CheckConclusion string               `json:"check_conclusion,omitempty"`
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	configuration, err := parseConfig(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	grokBinary, err := resolveBinary(configuration.grokBinary)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	githubBinary, err := resolveBinary(configuration.githubBinary)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	privateKey, err := os.ReadFile(configuration.appPrivateKey)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "read GitHub App private key:", err)
		return 1
	}
	appJWT, err := grokreview.MintAppJWT(configuration.appID, privateKey, time.Now())
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	github, err := grokreview.NewGitHubCLI(githubBinary, time.Minute, configuration.maxGitHubOutput)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	installationToken, err := github.MintInstallationToken(ctx, appJWT, configuration.installationID)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	bundle, err := reviewer.Load()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	model, err := grokreview.NewCLIRunner(grokreview.CLIConfig{
		Binary: grokBinary, HomeDir: configuration.grokHome,
		Model: configuration.model, ReasoningEffort: configuration.reasoningEffort,
		Timeout: configuration.timeout, MaxOutputBytes: configuration.maxReviewerOutput,
		Core: string(bundle.Core.Content), Policy: string(bundle.Policy.Content),
		SafeEnvironment: safeProcessEnvironment(),
	})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	service, err := grokreview.NewLocalReviewService(model, github, bundle.Core.SHA256, bundle.Policy.SHA256)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	report, reviewErr := service.ReviewPullRequest(
		ctx, installationToken, configuration.checkName, configuration.repository, configuration.pullRequest,
	)
	if report.Snapshot.HeadSHA != "" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(newCommandOutput(report)); err != nil {
			_, _ = fmt.Fprintln(stderr, "encode review result:", err)
			return 1
		}
	}
	if reviewErr != nil {
		_, _ = fmt.Fprintln(stderr, reviewErr)
		return 1
	}
	if report.Result.Verdict == grokreview.VerdictBlocking {
		return 2
	}
	if report.Result.Verdict != grokreview.VerdictApproved {
		return 1
	}
	return 0
}

func parseConfig(args []string, output io.Writer) (config, error) {
	var configuration config
	var mode string
	flags := flag.NewFlagSet("squad-grok-review", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&configuration.repository, "repo", "", "GitHub repository as owner/name")
	flags.IntVar(&configuration.pullRequest, "pr", 0, "pull request number")
	flags.StringVar(&mode, "mode", "shadow", "publication mode: shadow or required")
	flags.Int64Var(&configuration.appID, "app-id", 0, "GitHub App ID")
	flags.Int64Var(&configuration.installationID, "installation-id", 0, "GitHub App installation ID")
	flags.StringVar(&configuration.appPrivateKey, "app-private-key", "", "path to the GitHub App private key PEM")
	flags.StringVar(&configuration.grokBinary, "grok-bin", "grok", "Grok CLI binary")
	flags.StringVar(&configuration.githubBinary, "gh-bin", "gh", "GitHub CLI binary")
	flags.StringVar(&configuration.grokHome, "grok-home", "", "home directory containing the dedicated Grok login")
	flags.StringVar(&configuration.model, "model", "grok-4.6", "Grok CLI model selector")
	flags.StringVar(&configuration.reasoningEffort, "reasoning-effort", "", "optional Grok reasoning effort")
	flags.DurationVar(&configuration.timeout, "timeout", 10*time.Minute, "Grok review timeout")
	flags.IntVar(&configuration.maxGitHubOutput, "max-github-output", 8<<20, "maximum GitHub CLI response bytes")
	flags.IntVar(&configuration.maxReviewerOutput, "max-reviewer-output", 1<<20, "maximum Grok CLI response bytes")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected positional arguments")
	}
	switch mode {
	case "shadow":
		configuration.checkName = "grok-review-shadow"
	case "required":
		configuration.checkName = "grok-review"
	default:
		return config{}, fmt.Errorf("mode must be shadow or required")
	}
	if configuration.repository == "" || configuration.pullRequest <= 0 {
		return config{}, fmt.Errorf("--repo and --pr are required")
	}
	if configuration.appID <= 0 || configuration.installationID <= 0 || configuration.appPrivateKey == "" {
		return config{}, fmt.Errorf("--app-id, --installation-id, and --app-private-key are required")
	}
	if configuration.grokHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return config{}, fmt.Errorf("resolve Grok home: %w", err)
		}
		configuration.grokHome = home
	}
	if configuration.timeout <= 0 || configuration.maxGitHubOutput <= 0 || configuration.maxReviewerOutput <= 0 {
		return config{}, fmt.Errorf("timeouts and output limits must be positive")
	}
	return configuration, nil
}

func newCommandOutput(report grokreview.ReviewReport) commandOutput {
	return commandOutput{
		Repository: report.Snapshot.Repository, PullRequest: report.Snapshot.Number,
		BaseSHA: report.Snapshot.BaseSHA, HeadSHA: report.Snapshot.HeadSHA,
		Verdict: report.Result.Verdict, Summary: report.Result.Summary,
		Findings:  report.Result.Findings,
		RequestID: report.Audit.RequestID, SessionID: report.Audit.SessionID,
		RequestedModel: report.Audit.RequestedModel, ResolvedModel: report.Audit.ResolvedModel,
		TotalTokens: report.Audit.Usage.TotalTokens, CostUSD: report.Audit.CostUSD,
		DurationMillis: report.Audit.Duration.Milliseconds(),
		CommentID:      report.Publication.CommentID, CommentURL: report.Publication.CommentURL,
		CheckRunID: report.Publication.CheckRunID, CheckURL: report.Publication.CheckURL,
		CheckConclusion: report.Publication.Conclusion,
	}
}

func resolveBinary(binary string) (string, error) {
	if filepath.IsAbs(binary) {
		return binary, nil
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("find %s: %w", binary, err)
	}
	return resolved, nil
}

func safeProcessEnvironment() map[string]string {
	environment := make(map[string]string)
	for _, name := range []string{"LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			environment[name] = value
		}
	}
	return environment
}
