package main

import (
	"context"
	"crypto/sha256"
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
	backend        string
	reviewerBinary string

	humanRestart           bool
	humanGrantPath         string
	provider               string
	repositoryHost         string
	cloneLayout            string
	worktree               string
	baseSHA                string
	descriptionFile        string
	configPath             string
	doctor                 bool
	reconcile              bool
	recovery               bool
	prospective            bool
	completion             bool
	completionCustodyPath  string
	completionEvidencePath string
	recoveryFrom           string
	admissionDir           string
	repository             string
	pullRequest            int
	checkName              string
	appID                  int64
	installationID         int64
	appPrivateKey          string
	grokBinary             string
	githubBinary           string
	grokHome               string
	model                  string
	reasoningEffort        string
	mode                   string
	statusDir              string
	timeout                time.Duration
	maxGitHubOutput        int
	maxReviewerOutput      int
}

type localConfig struct {
	Backend        string `json:"backend,omitempty"`
	ReviewerBinary string `json:"reviewer_bin,omitempty"`

	AppID           int64  `json:"app_id"`
	InstallationID  int64  `json:"installation_id"`
	AppPrivateKey   string `json:"app_private_key"`
	GrokHome        string `json:"grok_home,omitempty"`
	GrokBinary      string `json:"grok_bin,omitempty"`
	GitHubBinary    string `json:"gh_bin,omitempty"`
	StatusDir       string `json:"status_dir,omitempty"`
	AdmissionDir    string `json:"admission_dir,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type runtimeDependencies struct {
	grokBinary        string
	githubBinary      string
	installationToken string
	github            grokreview.PullRequestGateway
	model             *grokreview.CLIRunner
	bundle            reviewer.Bundle
}

type doctorOutput struct {
	Backend     string `json:"backend"`
	ModelAccess string `json:"model_access"`

	Repository           string `json:"repository,omitempty"`
	RepositoryAccess     string `json:"repository_access"`
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
	ReasoningEffort      string `json:"reasoning_effort"`
	GrokVersion          string `json:"grok_version"`
	GrokAuthentication   string `json:"grok_authentication"`
	GrokCLIContract      string `json:"grok_cli_contract"`
	GrokSessionStorage   string `json:"grok_session_storage"`
	GitHubAuthentication string `json:"github_authentication"`
	ReviewerBundle       string `json:"reviewer_bundle"`
}

type commandOutput struct {
	Backend string `json:"backend,omitempty"`

	HumanGrantID       string                              `json:"human_grant_id,omitempty"`
	Terminal           *grokreview.TerminalDiagnostics     `json:"terminal_diagnostics,omitempty"`
	RendererProvenance *grokreview.PatchRendererProvenance `json:"renderer_provenance,omitempty"`
	ParentAttempt      string                              `json:"parent_attempt,omitempty"`
	InputProvenance    string                              `json:"input_provenance,omitempty"`
	AttemptID          string                              `json:"attempt_id,omitempty"`
	Provider           string                              `json:"provider,omitempty"`
	RepositoryHost     string                              `json:"repository_host,omitempty"`
	DiffSHA256         string                              `json:"diff_sha256,omitempty"`
	ContractSHA256     string                              `json:"contract_sha256,omitempty"`
	Repository         string                              `json:"repository"`
	PullRequest        int                                 `json:"pull_request"`
	BaseSHA            string                              `json:"base_sha"`
	HeadSHA            string                              `json:"head_sha"`
	Verdict            grokreview.Verdict                  `json:"verdict"`
	Summary            string                              `json:"summary"`
	Findings           []grokreview.Finding                `json:"findings"`
	RequestID          string                              `json:"request_id,omitempty"`
	SessionID          string                              `json:"session_id,omitempty"`
	RequestedModel     string                              `json:"requested_model,omitempty"`
	ReasoningEffort    string                              `json:"reasoning_effort,omitempty"`
	InputTokens        int64                               `json:"input_tokens,omitempty"`
	OutputTokens       int64                               `json:"output_tokens,omitempty"`
	ReasoningTokens    int64                               `json:"reasoning_tokens,omitempty"`
	FailureStage       string                              `json:"failure_stage,omitempty"`
	ResolvedModel      string                              `json:"resolved_model,omitempty"`
	TotalTokens        int64                               `json:"total_tokens,omitempty"`
	CostUSD            float64                             `json:"cost_usd,omitempty"`
	DurationMillis     int64                               `json:"duration_ms,omitempty"`
	FailureKind        grokreview.CLIFailureKind           `json:"failure_kind,omitempty"`
	FailureRule        grokreview.EnvelopeRule             `json:"failure_rule,omitempty"`
	CommentID          int64                               `json:"comment_id,omitempty"`
	CommentURL         string                              `json:"comment_url,omitempty"`
	CheckRunID         int64                               `json:"check_run_id,omitempty"`
	CheckURL           string                              `json:"check_url,omitempty"`
	CheckConclusion    string                              `json:"check_conclusion,omitempty"`
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func currentReviewOwner() (grokreview.ReviewOwner, error) {
	native := os.Getenv("SQUAD_NATIVE_SESSION_ID")
	session := os.Getenv("SQUAD_SESSION_ID")
	if session != "" {
		parts := strings.Split(session, ":")
		if len(parts) < 2 || len(parts) > 3 || (parts[0] != "codex" && parts[0] != "claude" && parts[0] != "muse") || parts[1] == "" {
			return grokreview.ReviewOwner{}, fmt.Errorf("explicit runtime/native owner required")
		}
		if len(parts) == 3 {
			if len(parts[2]) != 12 {
				return grokreview.ReviewOwner{}, fmt.Errorf("invalid ledger identity suffix")
			}
			for _, c := range parts[2] {
				if !strings.ContainsRune("0123456789abcdef", c) {
					return grokreview.ReviewOwner{}, fmt.Errorf("invalid ledger identity suffix")
				}
			}
		}
		if native != "" && native != parts[1] {
			return grokreview.ReviewOwner{}, fmt.Errorf("native delivery owner mismatch")
		}
		native = parts[1]
	}
	for _, name := range []string{"CODEX_THREAD_ID", "CODEX_SESSION_ID", "CLAUDE_SESSION_ID", "MUSE_SESSION_ID"} {
		if value := os.Getenv(name); value != "" {
			if native != "" && native != value {
				return grokreview.ReviewOwner{}, fmt.Errorf("native delivery owner mismatch")
			}
			native = value
		}
	}
	owner := grokreview.ReviewOwner{Actor: os.Getenv("SQUAD_AGENT"), Native: native}
	if owner.Actor == "" || owner.Native == "" {
		return owner, fmt.Errorf("original review owner identity unavailable")
	}
	return owner, nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "authorization-readback" {
		return runAuthorizationReadback(args[1:], stdout, stderr)
	}
	configuration, err := parseConfig(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if configuration.completion {
		return runCompletion(ctx, configuration, stdout, stderr)
	}
	if configuration.reconcile {
		settings := grokreview.ReviewSettings{Backend: configuration.backend, Mode: configuration.mode, Model: configuration.model, Effort: configuration.reasoningEffort, TimeoutMS: configuration.timeout.Milliseconds(), MaxGitHubOutput: configuration.maxGitHubOutput, MaxReviewerOutput: configuration.maxReviewerOutput, AppID: configuration.appID, InstallationID: configuration.installationID}
		admission, openErr := grokreview.OpenAdmission(configuration.admissionDir, settings, configuration.recoveryFrom, nil)
		if openErr != nil {
			_, _ = fmt.Fprintln(stderr, openErr)
			return 1
		}
		defer func() { _ = admission.Close() }()
		admission.SetPublicationLookup(func(ctx context.Context, r grokreview.AttemptReceipt) (grokreview.Publication, error) {
			github, token, err := prepareGitHubReadback(ctx, configuration)
			if err != nil {
				return grokreview.Publication{}, err
			}
			return github.FindAttemptPublication(ctx, token, configuration.appID, r)
		})
		id := configuration.recoveryFrom
		if filepath.IsAbs(id) {
			if err := admission.ImportLegacy(id); err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return 1
			}
			id = admission.LegacyAttemptID()
		}
		if err := admission.Reconcile(ctx, id, configuration.repository, configuration.pullRequest); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		return encodeJSON(stdout, stderr, map[string]any{"attempt": id, "joined": true, "sampled": false, "published": false, "recovery_slot": "unchanged"})
	}
	dependencies, err := prepareRuntime(ctx, configuration)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if configuration.doctor {
		repositoryAccess := "not_checked"
		if configuration.repository != "" {
			if github, ok := dependencies.github.(*grokreview.GitHubCLI); ok {
				if err := github.CheckRepositoryAccess(ctx, configuration.repository, dependencies.installationToken); err != nil {
					_, _ = fmt.Fprintln(stderr, err)
					return 1
				}
			}
			if configuration.pullRequest > 0 || configuration.provider == "local-git" {
				if _, err := dependencies.github.FetchPullRequestIdentity(ctx, configuration.repository, configuration.pullRequest, dependencies.installationToken); err != nil {
					_, _ = fmt.Fprintln(stderr, err)
					return 1
				}
			}
			repositoryAccess = "readable"
		}
		grokHealth, err := dependencies.model.Doctor(ctx)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		version, revision := buildIdentity()
		return encodeJSON(stdout, stderr, doctorOutput{
			Status: "ok", Version: version, Revision: revision, Backend: selectedBackend(configuration.backend), ModelAccess: "not_checked",
			Repository: configuration.repository, RepositoryAccess: repositoryAccess,
			ConfigPath: configuration.configPath, AppID: configuration.appID,
			InstallationID: configuration.installationID,
			GrokBinary:     dependencies.grokBinary, GitHubBinary: dependencies.githubBinary,
			GrokHome: configuration.grokHome, Model: grokHealth.Model,
			ReasoningEffort: grokHealth.ReasoningEffort,
			GrokVersion:     grokHealth.Version, GrokAuthentication: map[bool]string{true: "ok", false: "not_checked"}[configuration.backend == ""],
			GrokCLIContract: "ok", GrokSessionStorage: map[bool]string{true: "writable", false: "not_checked"}[configuration.backend == ""],
			GitHubAuthentication: map[bool]string{true: "ok", false: "not_applicable"}[configuration.provider == "github"], ReviewerBundle: "ok",
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
	var admission *grokreview.Admission
	if configuration.provider == "github" {
		settings := grokreview.ReviewSettings{Backend: configuration.backend, Mode: configuration.mode, Model: configuration.model, Effort: configuration.reasoningEffort, TimeoutMS: configuration.timeout.Milliseconds(), MaxGitHubOutput: configuration.maxGitHubOutput, MaxReviewerOutput: configuration.maxReviewerOutput, AppID: configuration.appID, InstallationID: configuration.installationID}
		check := func(checkCtx context.Context, prior grokreview.AttemptReceipt) error {
			return dependencies.github.(*grokreview.GitHubCLI).VerifyRecoveryCheck(checkCtx, dependencies.installationToken, configuration.appID, prior)
		}
		admission, err = grokreview.OpenAdmission(configuration.admissionDir, settings, configuration.recoveryFrom, check)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		defer func() { _ = admission.Close() }()
		if configuration.recovery && filepath.IsAbs(configuration.recoveryFrom) {
			if configuration.prospective {
				owner, ownerErr := currentReviewOwner()
				if ownerErr != nil {
					_, _ = fmt.Fprintln(stderr, ownerErr)
					return 1
				}
				err = admission.ImportProspective(configuration.recoveryFrom, owner)
			} else {
				err = admission.ImportLegacy(configuration.recoveryFrom)
			}
			if err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return 1
			}
		}
		if configuration.completionCustodyPath != "" {
			custody, err := loadCompletionCustody(configuration.completionCustodyPath)
			if err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return 1
			}
			owner, err := currentReviewOwner()
			if err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return 1
			}
			admission.BindCompletionCustody(custody, func(ctx context.Context, c grokreview.CompletionCustody) error {
				return grokreview.VerifyCompletionCustody(ctx, c, owner)
			})
		}
		if configuration.humanRestart {
			owner, ownerErr := currentReviewOwner()
			if ownerErr != nil {
				_, _ = fmt.Fprintln(stderr, ownerErr)
				return 1
			}
			if err := admission.ImportHumanRestart(configuration.humanGrantPath, owner); err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return 1
			}
		}
		if statusWriter != nil {
			if err = statusWriter.BindAttempt(admission.AttemptID()); err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return 1
			}
		}
		dependencies.model.SetLaunchObserver(admission.ReviewerLaunching)
		dependencies.model.SetProcessObserver(admission.ReviewerStarted)
		service.SetAdmission(admission)
	}
	var report grokreview.ReviewReport
	var reviewErr error
	if configuration.provider == "local-git" {
		report, reviewErr = service.ReviewWorktree(ctx, configuration.repository)
	} else {
		report, reviewErr = service.ReviewPullRequest(ctx, dependencies.installationToken, configuration.checkName, configuration.repository, configuration.pullRequest)
	}
	if report.Snapshot.HeadSHA != "" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		output := newCommandOutput(report)
		if admission != nil {
			output.AttemptID = admission.AttemptID()
			output.HumanGrantID = admission.Receipt().HumanGrantID
			output.RendererProvenance = admission.Receipt().RendererProvenance
			output.ParentAttempt = admission.Receipt().Parent
			output.InputProvenance = admission.Receipt().InputProvenance
		}
		if configuration.provider == "local-git" {
			output.Provider = "local-git"
			output.RepositoryHost = configuration.repositoryHost
			output.DiffSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(report.Snapshot.Diff)))
			output.ContractSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(report.Snapshot.Description)))
		}
		if err := encoder.Encode(output); err != nil {
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

func runAuthorizationReadback(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("authorization-readback", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var request, receipt string
	flags.StringVar(&request, "request", "", "absolute current exact review scope JSON")
	flags.StringVar(&receipt, "receipt", "", "existing real disclosure receipt; omission reports unavailable")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 1
	}
	owner, err := currentReviewOwner()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	result, err := grokreview.ReadDisclosure(receipt, request, owner)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return encodeJSON(stdout, stderr, result)
}

func prepareRuntime(ctx context.Context, configuration config) (runtimeDependencies, error) {
	grokBinary, err := resolveBinary(configuration.grokBinary)
	if err != nil {
		return runtimeDependencies{}, err
	}
	var githubBinary, installationToken string
	var github grokreview.PullRequestGateway
	if configuration.provider == "local-git" {
		github, err = grokreview.NewLocalGitGateway(configuration.worktree, configuration.repositoryHost, configuration.cloneLayout, configuration.baseSHA, configuration.descriptionFile, configuration.maxGitHubOutput)
		if err != nil {
			return runtimeDependencies{}, err
		}
	} else {
		githubBinary, err = resolveBinary(configuration.githubBinary)
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
		github, err = grokreview.NewGitHubCLI(githubBinary, time.Minute, configuration.maxGitHubOutput)
		if err != nil {
			return runtimeDependencies{}, err
		}
		installationToken, err = github.(*grokreview.GitHubCLI).MintInstallationToken(ctx, appJWT, configuration.installationID)
		if err != nil {
			return runtimeDependencies{}, err
		}
	}
	bundle, err := reviewer.Load()
	if err != nil {
		return runtimeDependencies{}, err
	}
	model, err := grokreview.NewCLIRunner(grokreview.CLIConfig{
		Backend: configuration.backend, Binary: grokBinary, HomeDir: configuration.grokHome,
		Model: configuration.model, ReasoningEffort: configuration.reasoningEffort,
		Timeout: configuration.timeout, MaxOutputBytes: configuration.maxReviewerOutput,
		Core: string(bundle.Core.Content), Policy: string(bundle.Policy.Content),
		SafeEnvironment: safeProcessEnvironment(), ProviderEnvironment: grokreview.ProviderEnvironment(configuration.backend),
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
	if len(args) > 0 && args[0] == "restart" {
		configuration.humanRestart = true
		args = args[1:]
	} else if len(args) > 0 && args[0] == "readmit" {
		configuration.recovery = true
		configuration.prospective = true
		args = args[1:]
	} else if len(args) > 0 && args[0] == "complete" {
		configuration.completion = true
		args = args[1:]
	} else if len(args) > 0 && args[0] == "reconcile" {
		configuration.reconcile = true
		args = args[1:]
	} else if len(args) > 0 && args[0] == "recover" {
		configuration.recovery = true
		args = args[1:]
	} else if len(args) > 0 && args[0] == "doctor" {
		configuration.doctor = true
		args = args[1:]
	}
	flags := flag.NewFlagSet("squad-grok-review", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&configuration.humanGrantPath, "human-grant", "", "absolute attributable human ONE-use restart scope receipt")
	flags.StringVar(&configuration.recoveryFrom, "from", "", "joined timeout attempt ID or absolute legacy custody receipt")
	flags.StringVar(&configuration.admissionDir, "admission-dir", "", "canonical reviewer admission directory (shared by all invocations)")
	flags.StringVar(&configuration.provider, "provider", "github", "review input: github or local-git (no publication)")
	flags.StringVar(&configuration.cloneLayout, "clone-layout", "plain", "HTTPS clone path layout: plain or bitbucket-server")
	flags.StringVar(&configuration.repositoryHost, "repository-host", "", "explicit implementation Git host for local review")
	flags.StringVar(&configuration.worktree, "worktree", "", "absolute clean local worktree")
	flags.StringVar(&configuration.baseSHA, "base-sha", "", "frozen 40-character base SHA for local review")
	flags.StringVar(&configuration.descriptionFile, "description-file", "", "absolute frozen local requirements/acceptance file")
	flags.StringVar(&configuration.configPath, "config", "", "path to local reviewer configuration JSON")
	flags.StringVar(&configuration.repository, "repo", "", "GitHub repository as owner/name")
	flags.IntVar(&configuration.pullRequest, "pr", 0, "pull request number")
	flags.StringVar(&configuration.mode, "mode", "shadow", "publication mode: shadow or required")
	flags.Int64Var(&configuration.appID, "app-id", 0, "GitHub App ID")
	flags.Int64Var(&configuration.installationID, "installation-id", 0, "GitHub App installation ID")
	flags.StringVar(&configuration.appPrivateKey, "app-private-key", "", "path to the GitHub App private key PEM")
	flags.StringVar(&configuration.backend, "backend", "grok", "review runtime: grok, claude, codex or muse (no automatic fallback)")
	flags.StringVar(&configuration.reviewerBinary, "reviewer-bin", "", "selected reviewer CLI executable")
	flags.StringVar(&configuration.grokBinary, "grok-bin", "grok", "Grok CLI binary")
	flags.StringVar(&configuration.githubBinary, "gh-bin", "gh", "GitHub CLI binary")
	flags.StringVar(&configuration.grokHome, "grok-home", "", "home directory containing the dedicated Grok login")
	flags.StringVar(&configuration.statusDir, "status-dir", "", "directory for safe local review status JSON")
	flags.StringVar(&configuration.model, "model", "grok-4.6", "Grok CLI model selector")
	flags.StringVar(&configuration.reasoningEffort, "reasoning-effort", grokreview.DefaultReasoningEffort, "Grok reasoning effort: low, medium, high, or xhigh (never inherits global effort)")
	flags.DurationVar(&configuration.timeout, "timeout", 20*time.Minute, "Grok review timeout")
	flags.IntVar(&configuration.maxGitHubOutput, "max-github-output", 8<<20, "maximum GitHub CLI response bytes")
	flags.IntVar(&configuration.maxReviewerOutput, "max-reviewer-output", 1<<20, "maximum Grok CLI response bytes")
	flags.StringVar(&configuration.completionCustodyPath, "completion-custody", "", "absolute original native/claim/reservation/disclosure binding")
	flags.StringVar(&configuration.completionEvidencePath, "completion-evidence", "", "absolute authentic original envelope or wrapper-report/native proof; never reconstruct")
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
	if configuration.backend == "grok" {
		configuration.backend = ""
	}
	if configuration.backend != "" && configuration.backend != "claude" && configuration.backend != "codex" && configuration.backend != "muse" {
		return config{}, fmt.Errorf("unsupported review backend")
	}
	if configuration.backend != "" {
		if !visited["model"] && (fileConfiguration.Backend != configuration.backend || fileConfiguration.Model == "") {
			return config{}, fmt.Errorf("non-Grok backend requires an explicit --model or matching backend configuration")
		}
		if visited["grok-bin"] {
			return config{}, fmt.Errorf("use --reviewer-bin for a non-Grok backend")
		}
		configuration.grokBinary = configuration.backend
		if configuration.humanRestart || configuration.recovery || configuration.prospective || configuration.completion || configuration.completionCustodyPath != "" {
			return config{}, fmt.Errorf("grok recovery cannot be applied to another backend; retain original attempt")
		}
	}
	if configuration.reviewerBinary != "" {
		configuration.grokBinary = configuration.reviewerBinary
	}
	if err := grokreview.ValidateReasoningEffort(configuration.reasoningEffort); err != nil {
		return config{}, err
	}
	switch configuration.mode {
	case "shadow":
		configuration.checkName = grokreview.ReviewCheckName(configuration.backend, "shadow")
	case "required":
		configuration.checkName = grokreview.ReviewCheckName(configuration.backend, "required")
	default:
		return config{}, fmt.Errorf("mode must be shadow or required")
	}
	if configuration.doctor && (configuration.pullRequest < 0 || (configuration.pullRequest > 0 && configuration.repository == "")) {
		return config{}, fmt.Errorf("doctor --pr requires --repo and a positive PR number")
	}
	if configuration.provider == "github" && !configuration.doctor && (configuration.repository == "" || configuration.pullRequest <= 0) {
		return config{}, fmt.Errorf("--repo and --pr are required")
	}
	switch configuration.provider {
	case "github":
		if configuration.appID <= 0 || configuration.installationID <= 0 || configuration.appPrivateKey == "" {
			return config{}, fmt.Errorf("reviewer identity is missing; configure %s or pass --app-id, --installation-id, and --app-private-key", configuration.configPath)
		}
		if !filepath.IsAbs(configuration.appPrivateKey) {
			return config{}, fmt.Errorf("GitHub App private key path must be absolute")
		}
	case "local-git":
		if configuration.mode != "shadow" || configuration.repository == "" || configuration.pullRequest != 0 {
			return config{}, fmt.Errorf("local review requires --repo, forbids --pr and cannot run in required mode")
		}
		if _, err := grokreview.NewLocalGitGateway(configuration.worktree, configuration.repositoryHost, configuration.cloneLayout, configuration.baseSHA, configuration.descriptionFile, configuration.maxGitHubOutput); err != nil {
			return config{}, err
		}
	default:
		return config{}, fmt.Errorf("unsupported review provider")
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
	if configuration.admissionDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return config{}, err
		}
		configuration.admissionDir = filepath.Join(home, ".squad", "grok-review-admission")
	}
	if !filepath.IsAbs(configuration.admissionDir) {
		return config{}, fmt.Errorf("--admission-dir must be absolute")
	}
	if configuration.humanRestart && (configuration.provider != "github" || !filepath.IsAbs(configuration.humanGrantPath) || configuration.completionCustodyPath != "" || visited["admission-dir"]) {
		return config{}, fmt.Errorf("restart requires GitHub, absolute human grant and configured canonical store; no admission-dir override")
	}
	if !configuration.humanRestart && configuration.humanGrantPath != "" {
		return config{}, fmt.Errorf("human grant requires explicit restart")
	}
	if configuration.completion && (configuration.provider != "github" || !validCompletionAttempt(configuration.recoveryFrom) || !filepath.IsAbs(configuration.completionCustodyPath)) {
		return config{}, fmt.Errorf("complete requires original attempt ID, GitHub provider and absolute completion custody")
	}
	if configuration.completionEvidencePath != "" && (!configuration.completion || !filepath.IsAbs(configuration.completionEvidencePath)) {
		return config{}, fmt.Errorf("original completion evidence requires explicit complete operation")
	}
	if configuration.completionCustodyPath != "" && (configuration.provider != "github" || configuration.recovery || configuration.reconcile || configuration.prospective || !filepath.IsAbs(configuration.completionCustodyPath)) {
		return config{}, fmt.Errorf("completion custody requires normal GitHub sampling or explicit complete and an absolute path")
	}
	if configuration.recovery && (configuration.recoveryFrom == "" || configuration.provider != "github") {
		return config{}, fmt.Errorf("recover requires --from and GitHub provider; original mode must be preserved")
	}
	if configuration.prospective && !filepath.IsAbs(configuration.recoveryFrom) {
		return config{}, fmt.Errorf("readmit requires an absolute prospective terminal-legacy custody/disclosure receipt")
	}
	if configuration.reconcile && (configuration.recoveryFrom == "" || configuration.provider != "github") {
		return config{}, fmt.Errorf("reconcile requires --from and GitHub provider; original mode must be preserved")
	}
	if !configuration.recovery && !configuration.reconcile && !configuration.completion && configuration.recoveryFrom != "" {
		return config{}, fmt.Errorf("--from requires recover")
	}
	if configuration.timeout <= 0 || configuration.maxGitHubOutput <= 0 || configuration.maxReviewerOutput <= 0 {
		return config{}, fmt.Errorf("timeouts and output limits must be positive")
	}
	if configuration.provider == "local-git" {
		configuration.statusDir = filepath.Join(configuration.statusDir, "local-git", configuration.repositoryHost)
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
	if !visited["backend"] && local.Backend != "" {
		configuration.backend = local.Backend
	}
	if !visited["reviewer-bin"] && local.ReviewerBinary != "" && selectedBackend(configuration.backend) == selectedBackend(local.Backend) {
		configuration.reviewerBinary = local.ReviewerBinary
	}

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
	if !visited["admission-dir"] && local.AdmissionDir != "" {
		configuration.admissionDir = local.AdmissionDir
	}
	if !visited["status-dir"] && local.StatusDir != "" {
		configuration.statusDir = local.StatusDir
	}
	if !visited["model"] && local.Model != "" {
		configuration.model = local.Model
	}
	if !visited["reasoning-effort"] && local.ReasoningEffort != "" {
		configuration.reasoningEffort = local.ReasoningEffort
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

func selectedBackend(backend string) string {
	if backend == "" {
		return "grok"
	}
	return backend
}

func newCommandOutput(report grokreview.ReviewReport) commandOutput {
	return commandOutput{Backend: report.Audit.Backend,
		Terminal:   report.Audit.Terminal,
		Repository: report.Snapshot.Repository, PullRequest: report.Snapshot.Number,
		BaseSHA: report.Snapshot.BaseSHA, HeadSHA: report.Snapshot.HeadSHA,
		Verdict: report.Result.Verdict, Summary: report.Result.Summary,
		Findings:  report.Result.Findings,
		RequestID: report.Audit.RequestID, SessionID: report.Audit.SessionID,
		RequestedModel: report.Audit.RequestedModel, ResolvedModel: report.Audit.ResolvedModel,
		ReasoningEffort: report.Audit.ReasoningEffort,
		InputTokens:     report.Audit.Usage.InputTokens, OutputTokens: report.Audit.Usage.OutputTokens,
		ReasoningTokens: report.Audit.Usage.ReasoningTokens, FailureStage: report.FailureStage,
		TotalTokens: report.Audit.Usage.TotalTokens, CostUSD: report.Audit.CostUSD,
		DurationMillis: report.Audit.Duration.Milliseconds(),
		FailureKind:    report.Audit.FailureKind,
		FailureRule:    report.Audit.FailureRule,
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

// Reconcile only authenticates GitHub if an actual publication response was
// lost. It never resolves/initializes Grok or loads a model runtime.
func prepareGitHubReadback(ctx context.Context, c config) (*grokreview.GitHubCLI, string, error) {
	binary, err := resolveBinary(c.githubBinary)
	if err != nil {
		return nil, "", err
	}
	key, err := os.ReadFile(c.appPrivateKey)
	if err != nil {
		return nil, "", fmt.Errorf("read GitHub App key: %w", err)
	}
	jwt, err := grokreview.MintAppJWT(c.appID, key, time.Now())
	if err != nil {
		return nil, "", err
	}
	github, err := grokreview.NewGitHubCLI(binary, time.Minute, c.maxGitHubOutput)
	if err != nil {
		return nil, "", err
	}
	token, err := github.MintInstallationToken(ctx, jwt, c.installationID)
	return github, token, err
}

func validCompletionAttempt(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func loadCompletionCustody(path string) (grokreview.CompletionCustody, error) {
	var c grokreview.CompletionCustody
	info, err := os.Lstat(path)
	if err != nil {
		return c, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 || info.Mode().Perm()&0077 != 0 {
		return c, fmt.Errorf("completion custody must be bounded private regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, fmt.Errorf("completion custody trailing JSON")
	}
	return c, nil
}
func runCompletion(ctx context.Context, c config, stdout, stderr io.Writer) int {
	// Authenticate local original custody before resolving GitHub; never resolve Grok.
	owner, err := currentReviewOwner()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	custody, err := loadCompletionCustody(c.completionCustodyPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if custody.Disclosure.Identity.Repository != c.repository || custody.Disclosure.Identity.PR != c.pullRequest {
		_, _ = fmt.Fprintln(stderr, "completion target differs from original disclosure")
		return 1
	}
	if err := grokreview.VerifyCompletionCustody(ctx, custody, owner); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	settings := grokreview.ReviewSettings{Backend: c.backend, Mode: c.mode, Model: c.model, Effort: c.reasoningEffort, TimeoutMS: c.timeout.Milliseconds(), MaxGitHubOutput: c.maxGitHubOutput, MaxReviewerOutput: c.maxReviewerOutput, AppID: c.appID, InstallationID: c.installationID}
	a, err := grokreview.OpenAdmission(c.admissionDir, settings, "", nil)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = a.Close() }()
	a.BindCompletionCustody(custody, func(ctx context.Context, q grokreview.CompletionCustody) error {
		return grokreview.VerifyCompletionCustody(ctx, q, owner)
	})
	if c.completionEvidencePath != "" {
		if err := a.AuthenticateLegacyCompletion(ctx, c.recoveryFrom, custody, c.completionEvidencePath); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
	}
	github, token, err := prepareGitHubReadback(ctx, c)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	a.SetPublicationLookup(func(ctx context.Context, r grokreview.AttemptReceipt) (grokreview.Publication, error) {
		return github.FindAttemptPublication(ctx, token, c.appID, r)
	})
	a.SetCompletionCheck(func(ctx context.Context, r grokreview.AttemptReceipt) error {
		return github.VerifyRecoveryCheck(ctx, token, c.appID, r)
	})
	bundle, err := reviewer.Load()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	result, err := a.Complete(ctx, c.recoveryFrom, custody, github, token, c.checkName, bundle.Core.SHA256, bundle.Policy.SHA256)
	return writeCompletionResult(result, err, stdout, stderr)
}

func writeCompletionResult(result grokreview.CompletionResult, completionErr error, stdout, stderr io.Writer) int {
	if completionErr != nil {
		if result.Publication.CheckRunID > 0 {
			if code := encodeJSON(stdout, stderr, result); code != 0 {
				return code
			}
		}
		_, _ = fmt.Fprintln(stderr, completionErr)
		return 1
	}
	if code := encodeJSON(stdout, stderr, result); code != 0 {
		return code
	}
	if result.Verdict == grokreview.VerdictBlocking {
		return 2
	}
	return 0
}
