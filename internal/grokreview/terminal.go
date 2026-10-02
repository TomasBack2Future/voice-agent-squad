package grokreview

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// TerminalDiagnostics is local process and CLI-reported evidence only. It is
// never a verdict, provider cancellation acknowledgment, or retry authority.
type TerminalDiagnostics struct {
	InputSHA256      string           `json:"input_sha256"`
	StartedAt        time.Time        `json:"started_at"`
	FinishedAt       time.Time        `json:"finished_at"`
	JoinedAt         *time.Time       `json:"joined_at,omitempty"`
	ChildStarted     bool             `json:"child_started"`
	ChildWaited      bool             `json:"child_waited"`
	ExitCode         *int             `json:"exit_code,omitempty"`
	DeadlineProducer string           `json:"deadline_producer,omitempty"`
	StdoutBytes      int64            `json:"stdout_bytes"`
	StderrBytes      int64            `json:"stderr_bytes"`
	StdoutSHA256     string           `json:"stdout_sha256"`
	StderrSHA256     string           `json:"stderr_sha256"`
	OutputTruncated  bool             `json:"output_truncated"`
	SessionEvidence  string           `json:"session_evidence"`
	Session          *TerminalSession `json:"session,omitempty"`
}

type TerminalSession struct {
	SessionID              string     `json:"session_id"`
	LocalRequestID         string     `json:"local_request_id"`
	EventsSHA256           string     `json:"events_sha256"`
	FirstTokenAt           *time.Time `json:"first_token_at,omitempty"`
	FirstReasoningAt       *time.Time `json:"first_reasoning_at,omitempty"`
	LastReasoningAt        *time.Time `json:"last_reasoning_at,omitempty"`
	ReasoningNotifications int        `json:"reasoning_notifications"`
}

var terminalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Read only the invocation's private cwd index, never search the global session
// history. Bound every read and reject symlinks, duplicate keys and ambiguous
// sessions. The prompt's exact bytes qualify attribution; phase events do not
// establish transport health or token usage.
func (r *CLIRunner) terminalSession(workDir string, input []byte, started, joined time.Time) (*TerminalSession, string) {
	physicalWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return nil, "unavailable"
	}
	root, err := os.OpenRoot(r.config.HomeDir)
	if err != nil {
		return nil, "unavailable"
	}
	defer func() { _ = root.Close() }()
	index := filepath.Join(".grok", "sessions", strings.ReplaceAll(url.QueryEscape(physicalWorkDir), "+", "%20"))
	for _, p := range []string{".grok", filepath.Join(".grok", "sessions"), index} {
		s, err := root.Lstat(p)
		if err != nil {
			return nil, "unavailable"
		}
		if !s.IsDir() {
			return nil, "unqualified"
		}
	}
	dir, err := root.Open(index)
	if err != nil {
		return nil, "unavailable"
	}
	entries, err := dir.ReadDir(5)
	_ = dir.Close()
	if err != nil && err != io.EOF {
		return nil, "unavailable"
	}
	if len(entries) > 4 {
		return nil, "limit_exceeded"
	}
	id := ""
	for _, e := range entries {
		if e.IsDir() && terminalUUID.MatchString(e.Name()) {
			if id != "" {
				return nil, "unqualified"
			}
			id = e.Name()
		}
	}
	if id == "" {
		return nil, "unavailable"
	}
	sessionDir := filepath.Join(index, id)
	s, err := root.Lstat(sessionDir)
	if err != nil || !s.IsDir() {
		return nil, "unqualified"
	}
	history, err := terminalRead(root, filepath.Join(index, "prompt_history.jsonl"), 32<<20)
	if err != nil {
		return nil, "unqualified"
	}
	fields, err := terminalObject(history)
	if err != nil {
		return nil, "unqualified"
	}
	var prompt, sessionID string
	if !bytes.Equal(bytes.TrimSpace(fields["is_bash"]), []byte("false")) {
		return nil, "unqualified"
	}
	if json.Unmarshal(fields["prompt"], &prompt) != nil || json.Unmarshal(fields["session_id"], &sessionID) != nil || sessionID != id || !bytes.Equal([]byte(prompt), input) {
		return nil, "unqualified"
	}
	summary, err := terminalRead(root, filepath.Join(sessionDir, "summary.json"), 64<<10)
	if err != nil {
		return nil, "unqualified"
	}
	fields, err = terminalObject(summary)
	if err != nil {
		return nil, "unqualified"
	}
	var request, model, effort string
	if json.Unmarshal(fields["request_id"], &request) != nil || !terminalUUID.MatchString(request) || json.Unmarshal(fields["current_model_id"], &model) != nil || model != r.config.Model || json.Unmarshal(fields["reasoning_effort"], &effort) != nil || effort != r.config.ReasoningEffort {
		return nil, "unqualified"
	}
	events, err := terminalRead(root, filepath.Join(sessionDir, "events.jsonl"), 4<<20)
	if err != nil {
		return nil, "unqualified"
	}
	result, err := terminalEvents(events, id, model, started, joined)
	if err != nil {
		return nil, "unqualified"
	}
	result.LocalRequestID = request
	return result, "qualified_local"
}

func terminalRead(root *os.Root, path string, limit int64) ([]byte, error) {
	info, err := root.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("invalid diagnostic file")
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("diagnostic file changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, fmt.Errorf("diagnostic read failed")
	}
	after, err := root.Lstat(path)
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, fmt.Errorf("diagnostic file changed")
	}
	return raw, nil
}

// Decode object members explicitly so duplicate identities/timestamps cannot
// silently replace earlier values. Unknown values remain private and are not
// copied to any diagnostic output.
func terminalObject(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("invalid diagnostic object")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("invalid key")
		}
		if _, ok := fields[key]; ok {
			return nil, fmt.Errorf("duplicate key")
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing diagnostic data")
	}
	return fields, nil
}

func terminalEvents(raw []byte, id, model string, started, joined time.Time) (*TerminalSession, error) {
	sum := sha256.Sum256(raw)
	result := &TerminalSession{SessionID: id, EventsSHA256: hex.EncodeToString(sum[:])}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 4096)
	var last time.Time
	count, turns := 0, 0
	for scanner.Scan() {
		count++
		if count > 20000 {
			return nil, fmt.Errorf("event limit")
		}
		fields, err := terminalObject(scanner.Bytes())
		if err != nil {
			return nil, err
		}
		var kind, stamp string
		if json.Unmarshal(fields["type"], &kind) != nil || json.Unmarshal(fields["ts"], &stamp) != nil {
			return nil, fmt.Errorf("invalid event")
		}
		ts, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil || ts.Before(started.Add(-time.Second)) || ts.After(joined.Add(time.Second)) || ts.Before(last) {
			return nil, fmt.Errorf("invalid event time")
		}
		last = ts
		allowed := map[string]bool{"ts": true, "type": true}
		switch kind {
		case "turn_started":
			for _, key := range []string{"session_id", "turn_number", "model_id", "yolo_mode", "conversation_message_count", "session_relationship", "schema_version"} {
				allowed[key] = true
			}
			var sid, mid string
			if json.Unmarshal(fields["session_id"], &sid) != nil || sid != id || json.Unmarshal(fields["model_id"], &mid) != nil || mid != model {
				return nil, fmt.Errorf("event identity mismatch")
			}
			turns++
		case "loop_started":
			allowed["loop_index"] = true
		case "first_token":
			if turns == 0 {
				return nil, fmt.Errorf("orphan token event")
			}
			if result.FirstTokenAt == nil {
				result.FirstTokenAt = &ts
			}
		case "phase_changed":
			allowed["phase"] = true
			var phase string
			if turns == 0 || json.Unmarshal(fields["phase"], &phase) != nil {
				return nil, fmt.Errorf("orphan phase")
			}
			switch phase {
			case "waiting_for_model":
			case "streaming_reasoning":
				if result.FirstReasoningAt == nil {
					result.FirstReasoningAt = &ts
				}
				result.LastReasoningAt = &ts
				result.ReasoningNotifications++
			default:
				return nil, fmt.Errorf("unsupported phase")
			}
		default:
			return nil, fmt.Errorf("unsupported event")
		}
		for key := range fields {
			if !allowed[key] {
				return nil, fmt.Errorf("unsupported event field")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if turns == 0 {
		return nil, fmt.Errorf("missing turn identity")
	}
	return result, nil
}
