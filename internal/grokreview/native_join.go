package grokreview

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// NativeJoinProof references host-retained tool records, not an agent's prose
// assertion of exit. The qualified format is the Codex exec/write_stdin/wait
// chain; absent or unfamiliar formats fail closed without invented PIDs.
type NativeJoinProof struct {
	LaunchOutputSHA256   string `json:"launch_output_sha256"`
	JoinOutputSHA256     string `json:"join_output_sha256"`
	TerminalOutputSHA256 string `json:"terminal_output_sha256"`
	NativeSession        string `json:"native_session"`
	RolloutPath          string `json:"rollout_path"`
	LaunchCall           string `json:"launch_call"`
	JoinCall             string `json:"join_call"`
	TerminalCall         string `json:"terminal_call"`
	LaunchSHA256         string `json:"launch_sha256"`
	JoinSHA256           string `json:"join_sha256"`
	TerminalSHA256       string `json:"terminal_sha256"`
}

type nativeRecord struct {
	Type    string `json:"type"`
	Payload struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		SessionID string `json:"session_id"`
		CallID    string `json:"call_id"`
		Input     string `json:"input"`
		Arguments string `json:"arguments"`
		Output    []struct {
			Text string `json:"text"`
		} `json:"output"`
	} `json:"payload"`
}

var nativeCommandRE = regexp.MustCompile(`tools\.exec_command\(\{cmd:("(?:\\.|[^"\\])*")`)
var nativeSessionRE = regexp.MustCompile(`tools\.write_stdin\(\{session_id:([0-9]+),chars:""`)
var nativeCellRE = regexp.MustCompile(`^Script running with cell ID ([0-9]+)`)

func verifyNativeJoin(proof NativeJoinProof, r AttemptReceipt) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return verifyNativeJoinAt(filepath.Join(home, ".codex", "sessions"), proof, r)
}
func verifyNativeJoinAt(root string, proof NativeJoinProof, r AttemptReceipt) error {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	path, err := filepath.EvalSymlinks(proof.RolloutPath)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("native proof must reference host-retained Codex history")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 128<<20 {
		return fmt.Errorf("native history exceeds bounded qualification")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	wanted := map[string]bool{proof.LaunchCall: true, proof.JoinCall: true, proof.TerminalCall: true}
	if len(wanted) < 2 || proof.NativeSession == "" {
		return fmt.Errorf("native join references missing")
	}
	calls := map[string]nativeRecord{}
	outputs := map[string]nativeRecord{}
	hashes := map[string]string{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	first := true
	for scanner.Scan() {
		line := scanner.Bytes()
		// Inspect only identity and named tool records, never unrelated conversation.
		if !first {
			matched := false
			for id := range wanted {
				if bytes.Contains(line, []byte(id)) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		var row nativeRecord
		if err = json.Unmarshal(line, &row); err != nil {
			return fmt.Errorf("native tool record malformed")
		}
		if first {
			first = false
			if row.Type != "session_meta" || row.Payload.SessionID != proof.NativeSession {
				return fmt.Errorf("native history identity mismatch")
			}
			continue
		}
		id := row.Payload.CallID
		if !wanted[id] {
			continue
		}
		switch row.Payload.Type {
		case "custom_tool_call", "function_call":
			if _, exists := calls[id]; exists {
				return fmt.Errorf("duplicate native tool call")
			}
			calls[id] = row
			hashes[id] = receiptHash(line)
		case "custom_tool_call_output", "function_call_output":
			if _, exists := outputs[id]; exists {
				return fmt.Errorf("duplicate native tool output")
			}
			outputs[id] = row
			hashes[id+"/output"] = receiptHash(line)
		}
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	if hashes[proof.LaunchCall] != proof.LaunchSHA256 || hashes[proof.JoinCall] != proof.JoinSHA256 || hashes[proof.TerminalCall] != proof.TerminalSHA256 {
		return fmt.Errorf("native invocation provenance changed")
	}
	if hashes[proof.LaunchCall+"/output"] != proof.LaunchOutputSHA256 || hashes[proof.JoinCall+"/output"] != proof.JoinOutputSHA256 || hashes[proof.TerminalCall+"/output"] != proof.TerminalOutputSHA256 {
		return fmt.Errorf("native tool completion provenance changed")
	}
	launch := calls[proof.LaunchCall]
	if launch.Payload.Name != "exec" {
		return fmt.Errorf("native launch tool is not qualified")
	}
	match := nativeCommandRE.FindAllStringSubmatch(launch.Payload.Input, -1)
	selected := false
	for _, m := range match {
		var command string
		if err = json.Unmarshal([]byte(m[1]), &command); err != nil {
			return err
		}
		args := strings.Fields(command)
		if len(args) == 0 || filepath.Base(args[0]) != "squad-grok-review" {
			continue
		}
		if selected {
			return fmt.Errorf("ambiguous native reviewer launch")
		}
		selected = true
		if err = verifyLegacyArguments(args[1:], r); err != nil {
			return err
		}
	}
	if !selected {
		return fmt.Errorf("native launch is not a qualified managed reviewer invocation")
	}
	session := 0
	for _, item := range outputs[proof.LaunchCall].Payload.Output {
		var result struct {
			SessionID int `json:"session_id"`
		}
		if json.Unmarshal([]byte(item.Text), &result) == nil && result.SessionID > 0 {
			session = result.SessionID
		}
	}
	join := calls[proof.JoinCall]
	if join.Payload.Name != "exec" {
		return fmt.Errorf("native join tool is not qualified")
	}
	matches := nativeSessionRE.FindAllStringSubmatch(join.Payload.Input, -1)
	if len(matches) != 1 || matches[0][1] != strconv.Itoa(session) || session == 0 {
		return fmt.Errorf("native join does not reference the launched tool session")
	}
	terminal := outputs[proof.TerminalCall]
	if proof.TerminalCall != proof.JoinCall {
		cell := ""
		for _, item := range outputs[proof.JoinCall].Payload.Output {
			if m := nativeCellRE.FindStringSubmatch(item.Text); len(m) > 0 {
				cell = m[1]
			}
		}
		var wait struct {
			CellID string `json:"cell_id"`
		}
		call := calls[proof.TerminalCall]
		if call.Payload.Name != "wait" || json.Unmarshal([]byte(call.Payload.Arguments), &wait) != nil || cell == "" || wait.CellID != cell {
			return fmt.Errorf("native wait does not join the referenced execution cell")
		}
	}
	found := false
	for _, item := range terminal.Payload.Output {
		var result struct {
			ExitCode  *int   `json:"exit_code"`
			SessionID int    `json:"session_id"`
			Output    string `json:"output"`
		}
		if json.Unmarshal([]byte(item.Text), &result) != nil || result.ExitCode == nil {
			continue
		}
		if *result.ExitCode != 1 || result.SessionID != 0 {
			return fmt.Errorf("native reviewer tool is not terminal failed/joined")
		}
		// Only allow the sanitized structured wrapper report followed by its error.
		d := json.NewDecoder(strings.NewReader(result.Output))
		var report struct {
			Repository      string         `json:"repository"`
			PR              int            `json:"pull_request"`
			BaseSHA         string         `json:"base_sha"`
			HeadSHA         string         `json:"head_sha"`
			Verdict         Verdict        `json:"verdict"`
			FailureStage    string         `json:"failure_stage"`
			FailureKind     CLIFailureKind `json:"failure_kind"`
			Model           string         `json:"requested_model"`
			Effort          string         `json:"reasoning_effort"`
			CheckID         int64          `json:"check_run_id"`
			CheckConclusion string         `json:"check_conclusion"`
			Findings        []Finding      `json:"findings"`
		}
		if d.Decode(&report) != nil {
			continue
		}
		if report.Repository == r.Identity.Repository && report.PR == r.Identity.PR && report.BaseSHA == r.Identity.BaseSHA && report.HeadSHA == r.Identity.HeadSHA && report.Verdict == VerdictError && report.FailureStage == "sampling" && report.FailureKind == CLIFailureTimeout && len(report.Findings) == 0 && report.Model == r.Settings.Model && report.Effort == r.Settings.Effort && report.CheckID == r.Publication.CheckRunID && report.CheckConclusion == "failure" {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("native terminal output lacks the exact failed timeout report")
	}
	return nil
}
func verifyLegacyArguments(args []string, r AttemptReceipt) error {
	values := map[string]string{}
	for i := 0; i < len(args); i += 2 {
		if i+1 >= len(args) || !strings.HasPrefix(args[i], "--") || strings.ContainsAny(args[i+1], ";&|`$\n") {
			return fmt.Errorf("legacy invocation flags not qualified")
		}
		key := strings.TrimPrefix(args[i], "--")
		if _, exists := values[key]; exists {
			return fmt.Errorf("duplicate original setting")
		}
		values[key] = args[i+1]
	}
	timeout, err := time.ParseDuration(values["timeout"])
	if err != nil {
		return err
	}
	if values["repo"] != r.Identity.Repository || values["pr"] != strconv.Itoa(r.Identity.PR) || values["mode"] != r.Settings.Mode || values["model"] != r.Settings.Model || values["reasoning-effort"] != r.Settings.Effort || timeout.Milliseconds() != r.Settings.TimeoutMS {
		return fmt.Errorf("original native invocation settings mismatch")
	}
	for key, want := range map[string]int{"max-github-output": r.Settings.MaxGitHubOutput, "max-reviewer-output": r.Settings.MaxReviewerOutput} {
		defaultValue := 8 << 20
		if key == "max-reviewer-output" {
			defaultValue = 1 << 20
		}
		value := defaultValue
		if values[key] != "" {
			value, err = strconv.Atoi(values[key])
			if err != nil {
				return err
			}
		}
		if value != want {
			return fmt.Errorf("original native output bound mismatch")
		}
	}
	return nil
}
