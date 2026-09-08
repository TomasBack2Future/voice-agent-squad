package grokreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

const FindingsSchemaVersion = "squad.review.findings.v2"

type Verdict string

type CLIFailureKind string

const (
	VerdictApproved Verdict = "approved"
	VerdictBlocking Verdict = "blocking"
	VerdictError    Verdict = "error"

	CLIFailurePromptFileFormat CLIFailureKind = "prompt_file_format"
	CLIFailureInputTooLarge    CLIFailureKind = "input_too_large"
	CLIFailureAuthentication   CLIFailureKind = "authentication"
	CLIFailureInvalidArguments CLIFailureKind = "invalid_arguments"
	CLIFailureTransport        CLIFailureKind = "transport"
	CLIFailureUnknown          CLIFailureKind = "unknown"
)

const FindingsJSONSchema = `{"type":"object","additionalProperties":false,"required":["schema_version","verdict","summary","findings"],"properties":{"schema_version":{"const":"squad.review.findings.v2"},"verdict":{"type":"string","enum":["approved","blocking","error"]},"summary":{"type":"string","minLength":1,"maxLength":4000},"findings":{"type":"array","maxItems":100,"items":{"type":"object","additionalProperties":false,"required":["category","severity","blocking","path","line","title","body"],"properties":{"category":{"type":"string","enum":["correctness","security","data_loss","concurrency","compatibility","contract","migration","rollback","critical_test"]},"severity":{"type":"string","enum":["critical","high","medium","low"]},"blocking":{"type":"boolean"},"path":{"type":"string","minLength":1,"maxLength":1024},"line":{"type":"integer","minimum":1},"title":{"type":"string","minLength":1,"maxLength":200},"body":{"type":"string","minLength":1,"maxLength":4000},"verification":{"type":"string","maxLength":2000}}}}}}`

type Finding struct {
	Category     string `json:"category"`
	Severity     string `json:"severity"`
	Blocking     bool   `json:"blocking"`
	Path         string `json:"path"`
	Line         int    `json:"line"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	Verification string `json:"verification,omitempty"`
}

type FindingsResult struct {
	SchemaVersion string    `json:"schema_version"`
	Verdict       Verdict   `json:"verdict"`
	Summary       string    `json:"summary"`
	Findings      []Finding `json:"findings"`
}

type TokenUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	ReasoningTokens          int64 `json:"reasoning_tokens"`
	TotalTokens              int64 `json:"total_tokens"`
}

type CLIAudit struct {
	RequestID      string
	SessionID      string
	RequestedModel string
	ResolvedModel  string
	StopReason     string
	Usage          TokenUsage
	NumTurns       int
	CostUSD        float64
	Duration       time.Duration
	StderrSHA256   string
	FailureKind    CLIFailureKind
}

type CLIConfig struct {
	Binary          string
	HomeDir         string
	Model           string
	ReasoningEffort string
	Timeout         time.Duration
	MaxOutputBytes  int
	SandboxProfile  string
	Core            string
	Policy          string
	SafeEnvironment map[string]string
}

type CLIRunner struct {
	config CLIConfig
}

type preparedCommand struct {
	command    *exec.Cmd
	workDir    string
	promptPath string
	cleanup    func()
}

type cliEnvelope struct {
	Text                string                `json:"text"`
	StopReason          string                `json:"stopReason"`
	SessionID           string                `json:"sessionId"`
	RequestID           string                `json:"requestId"`
	Thought             json.RawMessage       `json:"thought,omitempty"`
	Usage               TokenUsage            `json:"usage"`
	NumTurns            int                   `json:"num_turns"`
	TotalCostUSD        float64               `json:"total_cost_usd"`
	TotalCostUSDTicks   int64                 `json:"total_cost_usd_ticks"`
	ModelUsage          map[string]modelUsage `json:"modelUsage"`
	StructuredOutput    json.RawMessage       `json:"structuredOutput"`
	PartialMessageCount int                   `json:"partial_message_count,omitempty"`
}

type modelUsage struct {
	InputTokens              int64   `json:"inputTokens"`
	OutputTokens             int64   `json:"outputTokens"`
	CacheReadInputTokens     int64   `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64   `json:"cacheCreationInputTokens"`
	ModelCalls               int     `json:"modelCalls"`
	CostUSD                  float64 `json:"costUSD"`
}

func NewCLIRunner(config CLIConfig) (*CLIRunner, error) {
	if !filepath.IsAbs(config.Binary) {
		return nil, fmt.Errorf("grok CLI binary must be an absolute path")
	}
	if !filepath.IsAbs(config.HomeDir) {
		return nil, fmt.Errorf("grok CLI home must be an absolute path")
	}
	if config.Model == "" {
		return nil, fmt.Errorf("grok CLI model is required")
	}
	if config.Timeout <= 0 {
		return nil, fmt.Errorf("grok CLI timeout must be positive")
	}
	if config.MaxOutputBytes <= 0 {
		return nil, fmt.Errorf("grok CLI output limit must be positive")
	}
	if strings.TrimSpace(config.Core) == "" || strings.TrimSpace(config.Policy) == "" {
		return nil, fmt.Errorf("grok CLI reviewer core and policy are required")
	}
	for name, value := range config.SafeEnvironment {
		if !allowedChildEnvironment(name) {
			return nil, fmt.Errorf("grok CLI child environment variable %q is not allowlisted", name)
		}
		if strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("grok CLI child environment variable %q contains NUL", name)
		}
	}
	return &CLIRunner{config: config}, nil
}

func (r *CLIRunner) Review(ctx context.Context, frozenBundle []byte) (FindingsResult, CLIAudit, error) {
	if len(frozenBundle) == 0 {
		return FindingsResult{}, CLIAudit{}, fmt.Errorf("frozen review bundle is empty")
	}
	callCtx, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()
	prepared, err := r.prepare(callCtx, frozenBundle)
	if err != nil {
		return FindingsResult{}, CLIAudit{}, err
	}
	defer prepared.cleanup()

	stdout := &cappedBuffer{limit: r.config.MaxOutputBytes}
	stderr := &cappedBuffer{limit: r.config.MaxOutputBytes}
	prepared.command.Stdout = stdout
	prepared.command.Stderr = stderr
	started := time.Now()
	err = prepared.command.Run()
	duration := time.Since(started)
	stderrSum := sha256.Sum256(stderr.Bytes())
	audit := CLIAudit{Duration: duration, StderrSHA256: hex.EncodeToString(stderrSum[:])}
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return FindingsResult{}, audit, fmt.Errorf("grok CLI exceeded %s", r.config.Timeout)
	}
	if err != nil {
		audit.FailureKind = classifyCLIFailure(stderr.Bytes())
		return FindingsResult{}, audit, fmt.Errorf(
			"grok CLI failed (kind=%s, stderr_sha256=%s): %w",
			audit.FailureKind, audit.StderrSHA256, err,
		)
	}
	if stdout.overflow || stderr.overflow {
		return FindingsResult{}, audit, fmt.Errorf("grok CLI output exceeded %d bytes", r.config.MaxOutputBytes)
	}
	result, parsedAudit, err := ParseCLIEnvelope(stdout.Bytes())
	if err != nil {
		return FindingsResult{}, audit, err
	}
	parsedAudit.Duration = duration
	parsedAudit.StderrSHA256 = audit.StderrSHA256
	parsedAudit.RequestedModel = r.config.Model
	return result, parsedAudit, nil
}

func (r *CLIRunner) prepare(ctx context.Context, frozenBundle []byte) (preparedCommand, error) {
	workDir, err := os.MkdirTemp("", "squad-grok-review-")
	if err != nil {
		return preparedCommand{}, fmt.Errorf("create Grok CLI work directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(workDir) }
	if err := os.Chmod(workDir, 0o700); err != nil {
		cleanup()
		return preparedCommand{}, fmt.Errorf("secure Grok CLI work directory: %w", err)
	}
	// Grok treats .json prompt files as typed ACP content envelopes. The frozen
	// review bundle is deliberately plain prompt text whose contents happen to
	// be JSON, so keep the prompt-file extension textual.
	promptPath := filepath.Join(workDir, "review-bundle.txt")
	if err := os.WriteFile(promptPath, frozenBundle, 0o600); err != nil {
		cleanup()
		return preparedCommand{}, fmt.Errorf("write frozen Grok review bundle: %w", err)
	}

	args := []string{
		"--prompt-file", promptPath,
		"--json-schema", FindingsJSONSchema,
		"--output-format", "json",
		"--no-subagents",
		"--disable-web-search",
		"--permission-mode", "plan",
	}
	if r.config.SandboxProfile != "" {
		args = append(args, "--sandbox", r.config.SandboxProfile)
	}
	args = append(args,
		"--tools", "",
		"--max-turns", "1",
		"--model", r.config.Model,
	)
	if r.config.ReasoningEffort != "" {
		args = append(args, "--reasoning-effort", r.config.ReasoningEffort)
	}
	args = append(args,
		"--cwd", workDir,
		"--system-prompt-override", r.config.Core+"\n\n"+r.config.Policy,
		"--verbatim",
	)
	command := exec.CommandContext(ctx, r.config.Binary, args...)
	command.Dir = workDir
	command.Env = r.childEnvironment(workDir)
	return preparedCommand{
		command: command, workDir: workDir, promptPath: promptPath, cleanup: cleanup,
	}, nil
}

func (r *CLIRunner) childEnvironment(workDir string) []string {
	environment := []string{"HOME=" + r.config.HomeDir}
	names := make([]string, 0, len(r.config.SafeEnvironment))
	for name := range r.config.SafeEnvironment {
		if name != "HOME" && name != "TMPDIR" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		environment = append(environment, name+"="+r.config.SafeEnvironment[name])
	}
	return append(environment, "TMPDIR="+workDir)
}

func ParseCLIEnvelope(raw []byte) (FindingsResult, CLIAudit, error) {
	var envelope cliEnvelope
	if err := decodeStrictJSON(raw, &envelope); err != nil {
		return FindingsResult{}, CLIAudit{}, fmt.Errorf("parse Grok CLI envelope: %w", err)
	}
	if envelope.StopReason != "end_turn" {
		return FindingsResult{}, CLIAudit{}, fmt.Errorf("grok CLI stop reason is %q", envelope.StopReason)
	}
	if envelope.SessionID == "" || envelope.RequestID == "" {
		return FindingsResult{}, CLIAudit{}, fmt.Errorf("grok CLI envelope lacks session or request identity")
	}
	if envelope.NumTurns != 1 {
		return FindingsResult{}, CLIAudit{}, fmt.Errorf("grok CLI used %d turns, expected one", envelope.NumTurns)
	}
	if len(envelope.ModelUsage) != 1 {
		return FindingsResult{}, CLIAudit{}, fmt.Errorf("grok CLI reported %d models, expected one", len(envelope.ModelUsage))
	}
	model := ""
	for name, usage := range envelope.ModelUsage {
		if usage.ModelCalls != 1 {
			return FindingsResult{}, CLIAudit{}, fmt.Errorf("grok CLI model %q made %d calls, expected one", name, usage.ModelCalls)
		}
		model = name
	}

	var result FindingsResult
	if len(envelope.StructuredOutput) == 0 || bytes.Equal(envelope.StructuredOutput, []byte("null")) {
		return FindingsResult{}, CLIAudit{}, fmt.Errorf("grok CLI envelope lacks structured output")
	}
	if err := decodeStrictJSON(envelope.StructuredOutput, &result); err != nil {
		return FindingsResult{}, CLIAudit{}, fmt.Errorf("parse Grok structured output: %w", err)
	}
	if err := ValidateFindings(result); err != nil {
		return FindingsResult{}, CLIAudit{}, err
	}
	var textValue, structuredValue any
	if err := decodeStrictJSON([]byte(envelope.Text), &textValue); err != nil {
		return FindingsResult{}, CLIAudit{}, fmt.Errorf("grok CLI text is not exact JSON: %w", err)
	}
	if err := decodeStrictJSON(envelope.StructuredOutput, &structuredValue); err != nil {
		return FindingsResult{}, CLIAudit{}, fmt.Errorf("compare Grok structured output: %w", err)
	}
	if !reflect.DeepEqual(textValue, structuredValue) {
		return FindingsResult{}, CLIAudit{}, fmt.Errorf("grok CLI text does not match structured output")
	}

	return result, CLIAudit{
		RequestID: envelope.RequestID, SessionID: envelope.SessionID, ResolvedModel: model,
		StopReason: envelope.StopReason, Usage: envelope.Usage,
		NumTurns: envelope.NumTurns, CostUSD: envelope.TotalCostUSD,
	}, nil
}

func ValidateFindings(result FindingsResult) error {
	if result.SchemaVersion != FindingsSchemaVersion {
		return fmt.Errorf("unsupported Grok findings schema %q", result.SchemaVersion)
	}
	if result.Verdict != VerdictApproved && result.Verdict != VerdictBlocking && result.Verdict != VerdictError {
		return fmt.Errorf("invalid Grok verdict %q", result.Verdict)
	}
	if strings.TrimSpace(result.Summary) == "" || len(result.Summary) > 4000 {
		return fmt.Errorf("grok summary is empty or oversized")
	}
	if len(result.Findings) > 100 {
		return fmt.Errorf("grok returned too many findings")
	}
	blocking := 0
	for index, finding := range result.Findings {
		if !allowedCategory(finding.Category) {
			return fmt.Errorf("finding %d has invalid category %q", index, finding.Category)
		}
		if !allowedSeverity(finding.Severity) {
			return fmt.Errorf("finding %d has invalid severity %q", index, finding.Severity)
		}
		if finding.Path == "" || len(finding.Path) > 1024 || finding.Line <= 0 {
			return fmt.Errorf("finding %d has invalid location", index)
		}
		if strings.TrimSpace(finding.Title) == "" || len(finding.Title) > 200 ||
			strings.TrimSpace(finding.Body) == "" || len(finding.Body) > 4000 ||
			len(finding.Verification) > 2000 {
			return fmt.Errorf("finding %d has empty or oversized content", index)
		}
		if finding.Blocking {
			blocking++
		}
	}
	if result.Verdict == VerdictApproved && blocking > 0 {
		return fmt.Errorf("approved Grok result contains blocking findings")
	}
	if result.Verdict == VerdictBlocking && blocking == 0 {
		return fmt.Errorf("blocking Grok result contains no blocking finding")
	}
	return nil
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("multiple JSON values")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func allowedChildEnvironment(name string) bool {
	switch name {
	case "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR":
		return true
	default:
		return false
	}
}

func classifyCLIFailure(stderr []byte) CLIFailureKind {
	message := strings.ToLower(string(stderr))
	switch {
	case strings.Contains(message, `json object must have a "type" field`) ||
		strings.Contains(message, "prompt file") && strings.Contains(message, "content"):
		return CLIFailurePromptFileFormat
	case strings.Contains(message, "input is too long") ||
		strings.Contains(message, "prompt is too long") ||
		strings.Contains(message, "context length"):
		return CLIFailureInputTooLarge
	case strings.Contains(message, "not logged in") ||
		strings.Contains(message, "authentication") ||
		strings.Contains(message, "unauthorized") ||
		strings.Contains(message, "oauth"):
		return CLIFailureAuthentication
	case strings.Contains(message, "unexpected argument") ||
		strings.Contains(message, "invalid value") ||
		strings.Contains(message, "usage:"):
		return CLIFailureInvalidArguments
	case strings.Contains(message, "connection") ||
		strings.Contains(message, "network") ||
		strings.Contains(message, "websocket") ||
		strings.Contains(message, "timed out"):
		return CLIFailureTransport
	default:
		return CLIFailureUnknown
	}
}

func allowedCategory(category string) bool {
	switch category {
	case "correctness", "security", "data_loss", "concurrency", "compatibility",
		"contract", "migration", "rollback", "critical_test":
		return true
	default:
		return false
	}
}

func allowedSeverity(severity string) bool {
	switch severity {
	case "critical", "high", "medium", "low":
		return true
	default:
		return false
	}
}

type cappedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if remaining > len(data) {
			remaining = len(data)
		}
		_, _ = b.buffer.Write(data[:remaining])
	}
	if remaining < len(data) {
		b.overflow = true
	}
	return written, nil
}

func (b *cappedBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}
