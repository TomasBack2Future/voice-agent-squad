package grokreview

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

const ReviewStatusSchemaVersion = "squad.local-review.status.v1"

type ReviewStatus struct {
	SchemaVersion      string         `json:"schema_version"`
	Attempt            string         `json:"attempt"`
	Repository         string         `json:"repository"`
	PullRequest        int            `json:"pull_request"`
	Mode               string         `json:"mode"`
	BaseRef            string         `json:"base_ref,omitempty"`
	BaseSHA            string         `json:"base_sha,omitempty"`
	HeadSHA            string         `json:"head_sha,omitempty"`
	State              ReviewState    `json:"state"`
	Verdict            Verdict        `json:"verdict,omitempty"`
	Summary            string         `json:"summary,omitempty"`
	FindingCount       int            `json:"finding_count"`
	FindingTitles      []string       `json:"finding_titles,omitempty"`
	Model              string         `json:"model,omitempty"`
	ReasoningEffort    string         `json:"reasoning_effort,omitempty"`
	FailureStage       string         `json:"failure_stage,omitempty"`
	ReviewerDurationMS int64          `json:"reviewer_duration_ms,omitempty"`
	FailureKind        CLIFailureKind `json:"failure_kind,omitempty"`
	StartedAt          int64          `json:"started_at"`
	DeadlineAt         int64          `json:"deadline_at"`
	UpdatedAt          int64          `json:"updated_at"`
	CompletedAt        int64          `json:"completed_at,omitempty"`
	DurationMS         int64          `json:"duration_ms,omitempty"`
	InputTokens        int64          `json:"input_tokens,omitempty"`
	OutputTokens       int64          `json:"output_tokens,omitempty"`
	ReasoningTokens    int64          `json:"reasoning_tokens,omitempty"`
	TotalTokens        int64          `json:"total_tokens,omitempty"`
	CostUSD            float64        `json:"cost_usd,omitempty"`
	CommentURL         string         `json:"comment_url,omitempty"`
	CheckURL           string         `json:"check_url,omitempty"`
}

// ReviewStatusWriter persists only ReviewStatus's allowlisted observation
// fields. Each update is written to a same-directory temporary file and then
// atomically renamed, so the monitor never sees partial JSON.
type ReviewStatusWriter struct {
	mu       sync.Mutex
	dir      string
	mode     string
	attempt  string
	started  time.Time
	deadline time.Time
	path     string
	now      func() time.Time
}

func NewReviewStatusWriter(dir, mode string, timeout time.Duration) (*ReviewStatusWriter, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("review status directory must be absolute")
	}
	if mode != "shadow" && mode != "required" {
		return nil, fmt.Errorf("review status mode must be shadow or required")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("review status timeout must be positive")
	}
	if err := ensureStatusDirectory(dir); err != nil {
		return nil, err
	}
	attempt, err := newReviewAttempt()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return &ReviewStatusWriter{
		dir: dir, mode: mode, attempt: attempt, started: now,
		deadline: now.Add(timeout + 2*time.Minute), now: time.Now,
	}, nil
}

func (w *ReviewStatusWriter) Observe(observation ReviewObservation) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	snapshot := observation.Snapshot
	status := ReviewStatus{
		SchemaVersion: ReviewStatusSchemaVersion,
		Attempt:       w.attempt, Repository: snapshot.Repository, PullRequest: snapshot.Number,
		Mode: w.mode, BaseRef: boundedStatusText(snapshot.BaseRef, 255),
		BaseSHA: boundedStatusText(snapshot.BaseSHA, 128), HeadSHA: boundedStatusText(snapshot.HeadSHA, 128),
		State: observation.State, StartedAt: w.started.Unix(), DeadlineAt: w.deadline.Unix(), UpdatedAt: now.Unix(),
		InputTokens:        observation.Audit.Usage.InputTokens,
		OutputTokens:       observation.Audit.Usage.OutputTokens,
		TotalTokens:        observation.Audit.Usage.TotalTokens,
		CostUSD:            observation.Audit.CostUSD,
		FailureKind:        observation.Audit.FailureKind,
		FailureStage:       observation.FailureStage,
		ReasoningEffort:    boundedStatusText(observation.Audit.ReasoningEffort, 20),
		ReviewerDurationMS: observation.Audit.Duration.Milliseconds(),
		ReasoningTokens:    observation.Audit.Usage.ReasoningTokens,
		CommentURL:         boundedStatusText(observation.Publication.CommentURL, 2048),
		CheckURL:           boundedStatusText(observation.Publication.CheckURL, 2048),
	}
	if observation.Audit.ResolvedModel != "" {
		status.Model = boundedStatusText(observation.Audit.ResolvedModel, 200)
	} else {
		status.Model = boundedStatusText(observation.Audit.RequestedModel, 200)
	}
	if validatedStatusResult(observation.Result) {
		status.Verdict = observation.Result.Verdict
		status.Summary = boundedStatusText(observation.Result.Summary, 1200)
		status.FindingCount = len(observation.Result.Findings)
		for _, finding := range observation.Result.Findings {
			if len(status.FindingTitles) == 20 {
				break
			}
			status.FindingTitles = append(status.FindingTitles, boundedStatusText(finding.Title, 200))
		}
	}
	if terminalReviewState(observation.State) {
		status.CompletedAt = now.Unix()
		status.DurationMS = max(0, now.Sub(w.started).Milliseconds())
		if status.Summary == "" {
			status.Summary = terminalStatusSummary(observation.State)
		}
	}
	path := filepath.Join(w.dir, reviewStatusFilename(snapshot.Repository, snapshot.Number, snapshot.HeadSHA, w.attempt))
	if err := writeStatusAtomically(path, status); err != nil {
		return err
	}
	if w.path != "" && w.path != path {
		_ = os.Remove(w.path)
	}
	w.path = path
	return nil
}

func ensureStatusDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create review status directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect review status directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("review status path must be a real directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure review status directory: %w", err)
	}
	return nil
}

func writeStatusAtomically(path string, status ReviewStatus) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".review-status-*.tmp")
	if err != nil {
		return fmt.Errorf("create review status temp file: %w", err)
	}
	tempPath := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(tempPath)
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("secure review status temp file: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(status); err != nil {
		cleanup()
		return fmt.Errorf("encode review status: %w", err)
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync review status: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close review status: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("publish review status: %w", err)
	}
	return nil
}

func reviewStatusFilename(repository string, number int, headSHA, attempt string) string {
	head := sanitizeStatusFilename(headSHA)
	if head == "" {
		head = "pending"
	}
	return fmt.Sprintf("%s-pr-%d-%s-%s.json", sanitizeStatusFilename(repository), number, head, attempt)
}

func sanitizeStatusFilename(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '-' || char == '_' || char == '.' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('-')
		}
	}
	return strings.Trim(builder.String(), "-.")
}

func newReviewAttempt() (string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("create review status attempt: %w", err)
	}
	return hex.EncodeToString(random), nil
}

func validatedStatusResult(result FindingsResult) bool {
	return result.SchemaVersion == FindingsSchemaVersion && ValidateFindings(result) == nil
}

func terminalReviewState(state ReviewState) bool {
	return state == ReviewStateApproved || state == ReviewStateBlocking || state == ReviewStateError || state == ReviewStateStale
}

func terminalStatusSummary(state ReviewState) string {
	switch state {
	case ReviewStateStale:
		return "PR base or head changed; this result was not published."
	case ReviewStateError:
		return "Review did not complete. See the owning Worker task for diagnostics."
	default:
		return "Review completed."
	}
}

func boundedStatusText(value string, limit int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
