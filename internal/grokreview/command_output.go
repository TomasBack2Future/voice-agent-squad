package grokreview

// ReviewCommandOutput is the wrapper's single stdout result document.
type ReviewCommandOutput struct {
	Backend string `json:"backend,omitempty"`

	HumanGrantID       string                   `json:"human_grant_id,omitempty"`
	Terminal           *TerminalDiagnostics     `json:"terminal_diagnostics,omitempty"`
	RendererProvenance *PatchRendererProvenance `json:"renderer_provenance,omitempty"`
	ParentAttempt      string                   `json:"parent_attempt,omitempty"`
	InputProvenance    string                   `json:"input_provenance,omitempty"`
	AttemptID          string                   `json:"attempt_id,omitempty"`
	Provider           string                   `json:"provider,omitempty"`
	RepositoryHost     string                   `json:"repository_host,omitempty"`
	DiffSHA256         string                   `json:"diff_sha256,omitempty"`
	ContractSHA256     string                   `json:"contract_sha256,omitempty"`
	Repository         string                   `json:"repository"`
	PullRequest        int                      `json:"pull_request"`
	BaseSHA            string                   `json:"base_sha"`
	HeadSHA            string                   `json:"head_sha"`
	Verdict            Verdict                  `json:"verdict"`
	Summary            string                   `json:"summary"`
	Findings           []Finding                `json:"findings"`
	RequestID          string                   `json:"request_id,omitempty"`
	SessionID          string                   `json:"session_id,omitempty"`
	RequestedModel     string                   `json:"requested_model,omitempty"`
	ReasoningEffort    string                   `json:"reasoning_effort,omitempty"`
	InputTokens        int64                    `json:"input_tokens,omitempty"`
	OutputTokens       int64                    `json:"output_tokens,omitempty"`
	ReasoningTokens    int64                    `json:"reasoning_tokens,omitempty"`
	FailureStage       string                   `json:"failure_stage,omitempty"`
	ResolvedModel      string                   `json:"resolved_model,omitempty"`
	TotalTokens        int64                    `json:"total_tokens,omitempty"`
	CostUSD            float64                  `json:"cost_usd,omitempty"`
	DurationMillis     int64                    `json:"duration_ms,omitempty"`
	FailureKind        CLIFailureKind           `json:"failure_kind,omitempty"`
	FailureRule        EnvelopeRule             `json:"failure_rule,omitempty"`
	CommentID          int64                    `json:"comment_id,omitempty"`
	CommentURL         string                   `json:"comment_url,omitempty"`
	CheckRunID         int64                    `json:"check_run_id,omitempty"`
	CheckURL           string                   `json:"check_url,omitempty"`
	CheckConclusion    string                   `json:"check_conclusion,omitempty"`
}

// NewReviewCommandOutput renders a report as the wrapper's stdout document.
func NewReviewCommandOutput(report ReviewReport) ReviewCommandOutput {
	return ReviewCommandOutput{Backend: report.Audit.Backend,
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
