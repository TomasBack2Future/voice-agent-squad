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
	"runtime/debug"
	"strings"
	"time"

	"github.com/zsiec/squad/internal/grokreview"
	"github.com/zsiec/squad/reviewer"
)

type config struct {
	configPath        string
	doctor            bool
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
	mode              string
	statusDir         string
	timeout           time.Duration
	maxGitHubOutput   int
	maxReviewerOutput int
}

type localConfig struct {
	AppID          int64  `json:"app_id"`
	InstallationID int64  `json:"installation_id"`
	AppPrivateKey  string `json:"app_private_key"`
	GrokHome       string `json:"grok_home,omitempty"`
	GrokBinary     string `json:"grok_bin,omitempty"`
	GitHubBinary   string `json:"gh_bin,omitempty"`
	StatusDir      string `json:"status_dir,omitempty"`
	Model          string `json:"model,omitempty"`
}

type runtimeDependencies struct {
	grokBinary        string
	githubBinary      string
	installationToken string
	github            *grokreview.GitHubCLI
	model             *grokreview.CLIRunner
	bundle            reviewer.Bundle
}

type doctorOutput struct {
	Status               string `json:"status"`
	Version              string `json:"version"`
	Revision             string `json:"revision,omitempty"`
	ConfigPath           string `json:"config_path"`
	AppID                int64  `json:"app_id"`
	InstallationID       int64  `json:"installation_id"`
	GrokBinary           string `json:"grok_binary"`
	GitHubBinary         string `json:"github_binary"`
	GrokHome             string `json:"grok_home"`
	Model                string `json:"model"`
	GitHubAuthentication string `json:"github_authentication"`
	ReviewerBundle       string `json:"reviewer_bundle"`
}

type commandOutput struct {
	Repository      string                    `json:"repository"`
	PullRequest     int                       `json:"pull_request"`
	BaseSHA         string                    `json:"base_sha"`
	HeadSHA         string                    `json:"head_sha"`
	Verdict         grokreview.Verdict        `json:"verdict"`
	Summary         string                    `json:"summary"`
	Findings        []grokreview.Finding      `json:"findings"`
	RequestID       string                    `json:"request_id,omitempty"`
	SessionID       string                    `json:"session_id,omitempty"`
	RequestedModel  string                    `json:"requested_model,omitempty"`
	ResolvedModel   string                    `json:"resolved_model,omitempty"`
	TotalTokens     int64                     `json:"total_tokens,omitempty"`
	CostUSD         float64                   `json:"cost_usd,omitempty"`
	DurationMillis  int64                     `json:"duration_ms,omitempty"`
	FailureKind     grokreview.CLIFailureKind `json:"failure_kind,omitempty"`
	CommentID       int64                     `json:"comment_id,omitempty"`
	CommentURL      string                    `json:"comment_url,omitempty"`
	CheckRunID      int64                     `json:"check_run_id,omitempty"`
	CheckURL        string                    `json:"check_url,omitempty"`
	CheckConclusion string                    `json:"check_conclusion,omitempty"`
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
	dependencies, err := prepareRuntime(ctx, configuration)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if configuration.doctor {
		version, revision := buildIdentity()
		return encodeJSON(stdout, stderr, doctorOutput{
			Status: "ok", Version: version, Revision: revision,
			ConfigPath: configuration.configPath, AppID: configuration.appID,
			InstallationID: configuration.installationID,
			GrokBinary:     dependencies.grokBinary, GitHubBinary: dependencies.githubBinary,
			GrokHome: configuration.grokHome, Model: configuration.model,
			GitHubAuthentication: "ok", ReviewerBundle: "ok",
		})
	}
	var observers []grokreview.ReviewObserver
	statusWriter, statusErr := grokreview.NewReviewStatusWriter(configuration.statusDir, configuration.mode, configuration.timeout)
	if statusErr != nil {
		_, _ = fmt.Fprintln(stderr, "review status disabled:", statusErr)
	} else {
		observers = append(observers, statusWriter)
	}
	service, err := grokreview.NewLocalReviewService(
		dependencies.model, dependencies.github,
		dependencies.bundle.Core.SHA256, dependencies.bundle.Policy.SHA256, observers...,
	)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	report, reviewErr := service.ReviewPullRequest(
		ctx, dependencies.installationToken, configuration.checkName, configuration.repository, configuration.pullRequest,
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

func prepareRuntime(ctx context.Context, configuration config) (runtimeDependencies, error) {
	grokBinary, err := resolveBinary(configuration.grokBinary)
	if err != nil {
		return runtimeDependencies{}, err
	}
	githubBinary, err := resolveBinary(configuration.githubBinary)
	if err != nil {
		return runtimeDependencies{}, err
	}
	privateKey, err := os.ReadFile(configuration.appPrivateKey)
	if err != nil {
		return runtimeDependencies{}, fmt.Errorf("read GitHub App private key: %w", err)
	}
	appJWT, err := grokreview.MintAppJWT(configuration.appID, privateKey, time.Now())
	if err != nil {
		return runtimeDependencies{}, err
	}
	github, err := grokreview.NewGitHubCLI(githubBinary, time.Minute, configuration.maxGitHubOutput)
	if err != nil {
		return runtimeDependencies{}, err
	}
	installationToken, err := github.MintInstallationToken(ctx, appJWT, configuration.installationID)
	if err != nil {
		return runtimeDependencies{}, err
	}
	bundle, err := reviewer.Load()
	if err != nil {
		return runtimeDependencies{}, err
	}
	model, err := grokreview.NewCLIRunner(grokreview.CLIConfig{
		Binary: grokBinary, HomeDir: configuration.grokHome,
		Model: configuration.model, ReasoningEffort: configuration.reasoningEffort,
		Timeout: configuration.timeout, MaxOutputBytes: configuration.maxReviewerOutput,
		Core: string(bundle.Core.Content), Policy: string(bundle.Policy.Content),
		SafeEnvironment: safeProcessEnvironment(),
	})
	if err != nil {
		return runtimeDependencies{}, err
	}
	return runtimeDependencies{
		grokBinary: grokBinary, githubBinary: githubBinary,
		installationToken: installationToken, github: github, model: model, bundle: bundle,
	}, nil
}

func parseConfig(args []string, output io.Writer) (config, error) {
	var configuration config
	if len(args) > 0 && args[0] == "doctor" {
		configuration.doctor = true
		args = args[1:]
	}
	flags := flag.NewFlagSet("squad-grok-review", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&configuration.configPath, "config", "", "path to local reviewer configuration JSON")
	flags.StringVar(&configuration.repository, "repo", "", "GitHub repository as owner/name")
	flags.IntVar(&configuration.pullRequest, "pr", 0, "pull request number")
	flags.StringVar(&configuration.mode, "mode", "shadow", "publication mode: shadow or required")
	flags.Int64Var(&configuration.appID, "app-id", 0, "GitHub App ID")
	flags.Int64Var(&configuration.installationID, "installation-id", 0, "GitHub App installation ID")
	flags.StringVar(&configuration.appPrivateKey, "app-private-key", "", "path to the GitHub App private key PEM")
	flags.StringVar(&configuration.grokBinary, "grok-bin", "grok", "Grok CLI binary")
	flags.StringVar(&configuration.githubBinary, "gh-bin", "gh", "GitHub CLI binary")
	flags.StringVar(&configuration.grokHome, "grok-home", "", "home directory containing the dedicated Grok login")
	flags.StringVar(&configuration.statusDir, "status-dir", "", "directory for safe local review status JSON")
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
	visited := make(map[string]bool)
	flags.Visit(func(current *flag.Flag) { visited[current.Name] = true })
	configPathRequired := visited["config"]
	if configuration.configPath == "" {
		if environmentPath := strings.TrimSpace(os.Getenv("SQUAD_GROK_REVIEW_CONFIG")); environmentPath != "" {
			configuration.configPath = environmentPath
			configPathRequired = true
		} else {
			userConfigDir, err := os.UserConfigDir()
			if err != nil {
				return config{}, fmt.Errorf("resolve reviewer config directory: %w", err)
			}
			configuration.configPath = filepath.Join(userConfigDir, "squad", "grok-review.json")
		}
	}
	if !filepath.IsAbs(configuration.configPath) {
		return config{}, fmt.Errorf("reviewer config path must be absolute: %s", configuration.configPath)
	}
	fileConfiguration, found, err := loadLocalConfig(configuration.configPath)
	if err != nil {
		return config{}, err
	}
	if !found && configPathRequired {
		return config{}, fmt.Errorf("reviewer config not found: %s", configuration.configPath)
	}
	if found {
		applyLocalConfig(&configuration, fileConfiguration, visited)
	}
	switch configuration.mode {
	case "shadow":
		configuration.checkName = "grok-review-shadow"
	case "required":
		configuration.checkName = "grok-review"
	default:
		return config{}, fmt.Errorf("mode must be shadow or required")
	}
	if !configuration.doctor && (configuration.repository == "" || configuration.pullRequest <= 0) {
		return config{}, fmt.Errorf("--repo and --pr are required")
	}
	if configuration.appID <= 0 || configuration.installationID <= 0 || configuration.appPrivateKey == "" {
		return config{}, fmt.Errorf("reviewer identity is missing; configure %s or pass --app-id, --installation-id, and --app-private-key", configuration.configPath)
	}
	if !filepath.IsAbs(configuration.appPrivateKey) {
		return config{}, fmt.Errorf("GitHub App private key path must be absolute")
	}
	if configuration.grokHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return config{}, fmt.Errorf("resolve Grok home: %w", err)
		}
		configuration.grokHome = home
	}
	if configuration.statusDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return config{}, fmt.Errorf("resolve review status directory: %w", err)
		}
		configuration.statusDir = filepath.Join(home, ".squad", "grok-reviews")
	}
	if !filepath.IsAbs(configuration.statusDir) {
		return config{}, fmt.Errorf("--status-dir must be an absolute path")
	}
	if configuration.timeout <= 0 || configuration.maxGitHubOutput <= 0 || configuration.maxReviewerOutput <= 0 {
		return config{}, fmt.Errorf("timeouts and output limits must be positive")
	}
	return configuration, nil
}

func loadLocalConfig(path string) (localConfig, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return localConfig{}, false, nil
	}
	if err != nil {
		return localConfig{}, false, fmt.Errorf("open reviewer config %s: %w", path, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	var configuration localConfig
	if err := decoder.Decode(&configuration); err != nil {
		return localConfig{}, false, fmt.Errorf("parse reviewer config %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return localConfig{}, false, fmt.Errorf("parse reviewer config %s: multiple JSON values", path)
		}
		return localConfig{}, false, fmt.Errorf("parse reviewer config %s: %w", path, err)
	}
	return configuration, true, nil
}

func applyLocalConfig(configuration *config, local localConfig, visited map[string]bool) {
	if !visited["app-id"] {
		configuration.appID = local.AppID
	}
	if !visited["installation-id"] {
		configuration.installationID = local.InstallationID
	}
	if !visited["app-private-key"] {
		configuration.appPrivateKey = local.AppPrivateKey
	}
	if !visited["grok-home"] && local.GrokHome != "" {
		configuration.grokHome = local.GrokHome
	}
	if !visited["grok-bin"] && local.GrokBinary != "" {
		configuration.grokBinary = local.GrokBinary
	}
	if !visited["gh-bin"] && local.GitHubBinary != "" {
		configuration.githubBinary = local.GitHubBinary
	}
	if !visited["status-dir"] && local.StatusDir != "" {
		configuration.statusDir = local.StatusDir
	}
	if !visited["model"] && local.Model != "" {
		configuration.model = local.Model
	}
}

func encodeJSON(stdout, stderr io.Writer, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_, _ = fmt.Fprintln(stderr, "encode command result:", err)
		return 1
	}
	return 0
}

func buildIdentity() (string, string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown", ""
	}
	version := info.Main.Version
	if version == "" {
		version = "unknown"
	}
	var revision string
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			revision = setting.Value
			break
		}
	}
	return version, revision
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
		FailureKind:    report.Audit.FailureKind,
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
