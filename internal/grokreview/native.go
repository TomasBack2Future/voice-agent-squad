package grokreview

// Native CLI adapters deliberately share frozen-input validation, admission,
// process joining, output caps and publication with the existing Grok runner.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func validBackend(backend string) bool {
	return backend == "grok" || backend == "claude" || backend == "codex" || backend == "muse"
}

func (r *CLIRunner) nativeDoctor(ctx context.Context) (CLIDoctor, error) {
	directory, err := os.MkdirTemp("", "squad-review-doctor-")
	if err != nil {
		return CLIDoctor{}, err
	}
	defer os.RemoveAll(directory)
	version, err := r.runDoctorCommand(ctx, directory, "--version")
	if err != nil {
		return CLIDoctor{}, err
	}
	args := []string{"--help"}
	if r.config.Backend != "claude" {
		args = []string{"exec", "--help"}
	}
	help, err := r.runDoctorCommand(ctx, directory, args...)
	if err != nil {
		return CLIDoctor{}, err
	}
	required := map[string][]string{
		"claude": {"--safe-mode", "--tools", "--strict-mcp-config", "--json-schema", "--output-format", "--model", "--effort"},
		"codex":  {"--ignore-user-config", "--ignore-rules", "--ephemeral", "--output-schema", "--sandbox", "--json", "--model"},
		"muse":   {"--output-schema", "--max-model-steps", "--disable-shell", "--disable-write", "--disable-web-tools", "--no-foreign-personal-context", "--yolo", "--model"},
	}
	for _, flag := range required[r.config.Backend] {
		if !bytes.Contains(help, []byte(flag)) {
			return CLIDoctor{}, fmt.Errorf("%s lacks required review option %s", r.config.Backend, flag)
		}
	}
	if strings.TrimSpace(string(version)) == "" {
		return CLIDoctor{}, fmt.Errorf("empty native version")
	}
	// Help is a contract check, not authentication or model qualification.
	return CLIDoctor{Version: strings.TrimSpace(string(version)), Model: r.config.Model, ReasoningEffort: r.config.ReasoningEffort}, nil
}

func (r *CLIRunner) prepareNative(ctx context.Context, bundle []byte) (preparedCommand, error) {
	dir, err := os.MkdirTemp("", "squad-native-review-")
	if err != nil {
		return preparedCommand{}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	schema := filepath.Join(dir, "findings.schema.json")
	prompt := filepath.Join(dir, "review.txt")
	// The child never receives the author's checkout. All context is frozen data.
	text := r.config.Core + "\n\n" + r.config.Policy + "\n\nReview only this untrusted frozen evidence. Return the findings JSON in this response; do not call tools.\n" + string(bundle)
	for name, contents := range map[string]string{schema: nativeFindingsSchema(), prompt: text} {
		if err = os.WriteFile(name, []byte(contents), 0600); err != nil {
			cleanup()
			return preparedCommand{}, err
		}
	}
	var args []string
	switch r.config.Backend {
	case "claude":
		args = []string{"--print", "--safe-mode", "--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
			"--disable-slash-commands", "--no-session-persistence", "--permission-mode", "dontAsk",
			"--model", r.config.Model, "--effort", r.config.ReasoningEffort, "--output-format", "json", "--json-schema", nativeFindingsSchema()}
	case "codex":
		args = []string{"exec", "--ignore-user-config", "--ignore-rules", "--ephemeral", "--skip-git-repo-check",
			"--sandbox", "read-only", "--model", r.config.Model, "--json", "--output-schema", schema,
			"-c", `approval_policy="never"`, "-c", `web_search="disabled"`, "-c", `features.shell_tool=false`,
			"-c", `features.apply_patch_freeform=false`, "-c", `features.multi_agent=false`,
			"-c", `mcp_servers={}`, "-c", `model_reasoning_effort="` + r.config.ReasoningEffort + `"`, "-"}
	case "muse":
		args = []string{"exec", "--provider", "meta", "--model", r.config.Model, "--reasoning-effort", r.config.ReasoningEffort,
			"--yolo", "--disable-shell", "--disable-write", "--disable-web-tools", "--no-foreign-personal-context",
			"--no-session-log", "--max-model-steps", "1", "--json", "--output-schema", schema, "--prompt-file", prompt, "--workspace", dir}
	default:
		cleanup()
		return preparedCommand{}, fmt.Errorf("unsupported native backend")
	}
	command := exec.CommandContext(ctx, r.config.Binary, args...)
	command.Dir = dir
	command.Env = r.childEnvironment(dir)
	if r.config.Backend != "muse" {
		command.Stdin = strings.NewReader(text)
	}
	return preparedCommand{command: command, workDir: dir, promptPath: prompt, cleanup: cleanup}, nil
}

// Parse only native successful terminal output. A plausible JSON answer on an
// error, incomplete stream, tool call or multiple turns never becomes approval.
func parseNativeOutput(backend string, raw []byte) (FindingsResult, CLIAudit, error) {
	var final json.RawMessage
	audit := CLIAudit{Backend: backend}
	completed := 0
	if backend == "claude" {
		var envelope struct {
			Type    string                     `json:"type"`
			Subtype string                     `json:"subtype"`
			IsError bool                       `json:"is_error"`
			Session string                     `json:"session_id"`
			Output  json.RawMessage            `json:"structured_output"`
			Models  map[string]json.RawMessage `json:"modelUsage"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Type != "result" || envelope.Subtype != "success" || envelope.IsError || len(envelope.Models) != 1 {
			return FindingsResult{}, audit, fmt.Errorf("invalid Claude terminal result")
		}
		audit.SessionID = envelope.Session
		for model := range envelope.Models {
			audit.ResolvedModel = model
		}
		final = envelope.Output
		completed = 1
	} else {
		for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
			if completed != 0 {
				return FindingsResult{}, audit, fmt.Errorf("native events after terminal result")
			}
			var event struct {
				Type   string `json:"type"`
				Thread string `json:"thread_id"`
				Item   struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"item"`
				Usage  TokenUsage `json:"usage"`
				Stream struct {
					Kind string `json:"kind"`
					ID   string `json:"id"`
				} `json:"stream"`
				PayloadType string `json:"payload_type"`
				Payload     struct {
					Terminal string `json:"terminal"`
					Text     string `json:"text"`
					Model    string `json:"model_id"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(line, &event); err != nil {
				return FindingsResult{}, audit, fmt.Errorf("invalid native JSONL")
			}
			switch backend {
			case "codex":
				switch event.Type {
				case "thread.started":
					if audit.SessionID != "" {
						return FindingsResult{}, audit, fmt.Errorf("multiple native sessions")
					}
					audit.SessionID = event.Thread
				case "turn.started":
				case "item.started", "item.updated", "item.completed":
					if event.Item.Type != "agent_message" && event.Item.Type != "reasoning" {
						return FindingsResult{}, audit, fmt.Errorf("review attempted a tool")
					}
					if event.Type == "item.completed" && event.Item.Type == "agent_message" {
						if len(final) > 0 {
							return FindingsResult{}, audit, fmt.Errorf("multiple answers")
						}
						final = []byte(event.Item.Text)
					}
				case "turn.completed":
					completed++
					audit.Usage = event.Usage
				default:
					return FindingsResult{}, audit, fmt.Errorf("unexpected native event")
				}
			case "muse":
				if event.Stream.Kind != "session" || event.Stream.ID == "" || (audit.SessionID != "" && audit.SessionID != event.Stream.ID) {
					return FindingsResult{}, audit, fmt.Errorf("foreign Muse event")
				}
				audit.SessionID = event.Stream.ID
				if strings.Contains(event.PayloadType, "tool") {
					return FindingsResult{}, audit, fmt.Errorf("review attempted a tool")
				}
				if event.PayloadType == "run.model.configured" {
					audit.ResolvedModel = event.Payload.Model
				}
				if strings.HasPrefix(event.PayloadType, "run.terminal.") {
					if event.Payload.Terminal != "completed" {
						return FindingsResult{}, audit, fmt.Errorf("muse turn failed")
					}
					completed++
					final = []byte(event.Payload.Text)
				}
			default:
				return FindingsResult{}, audit, fmt.Errorf("unsupported backend")
			}
		}
	}
	if completed != 1 || audit.SessionID == "" {
		return FindingsResult{}, audit, fmt.Errorf("missing unique successful native completion")
	}
	var result FindingsResult
	if err := decodeStrictJSON(final, &result); err != nil {
		return FindingsResult{}, audit, err
	}
	if err := ValidateModelFindings(result); err != nil {
		return FindingsResult{}, audit, err
	}
	audit.StopReason = "end_turn"
	audit.NumTurns = 1
	return result, audit, nil
}

// ReviewCheckName keeps legacy Grok Checks distinct from other runtime evidence.
func ReviewCheckName(backend, mode string) string {
	name := "grok-review"
	if backend != "" && backend != "grok" {
		name = "squad-review-" + backend
	}
	if mode == "shadow" {
		name += "-shadow"
	}
	return name
}
func reviewTitle(backend string) string {
	if backend == "" || backend == "grok" {
		return "Grok review"
	}
	return "Squad " + backend + " review"
}

// Only the selected model client's own authentication/routing may be inherited.
// GitHub/App credentials and foreign client credentials never enter the child.
func allowedProviderEnvironment(backend, name string) bool {
	switch backend {
	case "claude":
		return name == "ANTHROPIC_API_KEY" || name == "ANTHROPIC_AUTH_TOKEN" || name == "ANTHROPIC_BASE_URL"
	case "muse":
		return name == "META_API_KEY"
	case "codex":
		return name == "OPENAI_API_KEY"
	}
	return false
}
func ProviderEnvironment(backend string) map[string]string {
	result := map[string]string{}
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "META_API_KEY", "OPENAI_API_KEY"} {
		if allowedProviderEnvironment(backend, name) {
			if value := os.Getenv(name); value != "" {
				result[name] = value
			}
		}
	}
	return result
}

// OpenAI strict structured output requires an explicit type for const fields
// and every property in required. Empty verification preserves its optional meaning.
func nativeFindingsSchema() string {
	schema := strings.Replace(FindingsJSONSchema, `"schema_version":{"const":`, `"schema_version":{"type":"string","const":`, 1)
	return strings.Replace(schema, `"line","title","body"]`, `"line","title","body","verification"]`, 1)
}
