// Package remote exposes bounded coordination operations, never a remote shell.
package remote

import (
	"encoding/json"
	"fmt"
	"strings"
)

var readCommands = strings.Fields("version whoami status who next inbox ready claim-inspect history stats touches")
var writeCommands = strings.Fields("register new accept reject claim release done handoff blocked say ask thinking stuck milestone fyi progress review-request tick heartbeat touch untouch")
var controllerCommands = strings.Fields("dispatch terminal-events resources")
var readTools = strings.Fields("squad_whoami squad_status squad_who squad_next squad_list_items squad_get_item squad_claim_inspect squad_history squad_stats squad_resources_check squad_terminal_decision_get")
var writeTools = strings.Fields("squad_register squad_new squad_accept squad_reject squad_done squad_handoff squad_claim squad_release squad_blocked squad_say squad_ask squad_tick squad_progress squad_review_request squad_heartbeat squad_touch squad_untouch squad_terminal_events_poll squad_terminal_events_publish squad_terminal_events_ack squad_terminal_events_delivered")
var controllerTools = strings.Fields("squad_dispatch_controller_bind squad_dispatch_controller_status squad_dispatch_receiver_preflight squad_dispatch_continue squad_terminal_decision_set")

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Tools is the explicitly supported MCP surface. File execution, arbitrary
// attestation commands, recovery and host administration remain local-only.
func Tools(role string) []string {
	out := append([]string{}, readTools...)
	if role != "observer" {
		out = append(out, writeTools...)
	}
	if role == "controller" {
		out = append(out, controllerTools...)
	}
	return out
}

func CheckCommand(c Client, args []string) error {
	if len(args) == 0 || len(args) > 128 {
		return fmt.Errorf("invalid command")
	}
	allowed := contains(readCommands, args[0]) || (c.Role != "observer" && contains(writeCommands, args[0])) || (c.Role == "controller" && contains(controllerCommands, args[0]))
	// Workers need their own event publication and decision reads; server ownership
	// checks still enforce the exact reservation and native generation.
	if c.Role == "worker" && len(args) > 1 && args[0] == "terminal-events" {
		allowed = contains([]string{"poll", "publish", "ack", "delivered", "decision-get"}, args[1])
	}
	if c.Role == "worker" && len(args) > 1 && args[0] == "resources" {
		allowed = args[1] == "check"
	}
	if !allowed {
		return fmt.Errorf("command not available for this client")
	}
	for i, a := range args {
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("NUL in command")
		}
		key, value, hasValue := strings.Cut(a, "=")
		if !hasValue && i+1 < len(args) {
			value = args[i+1]
		}
		switch key {
		case "--repo", "--worktree", "--wait", "--tail", "--force", "--skip-verify", "--stdin", "--owner-pid", "--wake-kind":
			return fmt.Errorf("flag %s is local-only", key)
		case "--worker-session":
			if c.Role != "controller" && value != c.Session {
				return fmt.Errorf("worker identity cannot be overridden")
			}
		case "--as", "--agent", "--agent-id":
			if value != c.Agent {
				return fmt.Errorf("client identity cannot be overridden")
			}
		case "--native-session":
			if value != c.Session {
				return fmt.Errorf("native identity cannot be overridden")
			}
		}
	}
	// Operators define policy and perform takeover/recovery locally. Remote
	// dispatch is restricted to the ordinary fenced lifecycle.
	if args[0] == "dispatch" && (len(args) < 2 || !contains(strings.Fields("reserve attach bind list close continue controller-bind controller-status receiver-preflight receiver-bind receiver-release handoff-get"), args[1])) {
		return fmt.Errorf("dispatch operation not available remotely")
	}
	if args[0] == "resources" && (len(args) < 2 || args[1] != "check") {
		return fmt.Errorf("resource policy is managed locally")
	}
	if args[0] == "terminal-events" && (len(args) < 2 || !contains(strings.Fields("poll publish ack delivered decision-get decision-set"), args[1])) {
		return fmt.Errorf("event operation not available remotely")
	}
	return nil
}

func CheckMCP(c Client, body []byte) (notification bool, err error) {
	var r struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  struct {
			Name      string                     `json:"name"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if err = json.Unmarshal(body, &r); err != nil || r.JSONRPC != "2.0" {
		return false, fmt.Errorf("invalid JSON-RPC request")
	}
	notification = len(r.ID) == 0 || string(r.ID) == "null"
	switch r.Method {
	case "notifications/initialized", "notifications/cancelled":
		return true, nil
	case "initialize", "tools/list", "ping":
		return notification, nil
	case "tools/call":
		if notification {
			return false, fmt.Errorf("tool calls require an id")
		}
		if !contains(Tools(c.Role), r.Params.Name) {
			return false, fmt.Errorf("tool not available for this client")
		}
		for k, v := range r.Params.Arguments {
			// encoding/json struct fields use Unicode case folding; accepting
			// aliases here would let a key such as aſ bypass the as check.
			for _, ch := range k {
				if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '_' {
					return false, fmt.Errorf("noncanonical MCP argument name")
				}
			}
			expected := ""
			switch strings.ToLower(k) {
			case "repo", "repo_root", "worktree", "force", "skip_verify", "owner_pid", "wake_kind":
				return false, fmt.Errorf("argument %s is local-only", k)
			case "worker_session":
				if c.Role != "controller" {
					expected = c.Session
				}
			case "agent_id", "as":
				expected = c.Agent
			case "native_session":
				expected = c.Session
			}
			if expected != "" {
				var value string
				if string(v) == "null" || json.Unmarshal(v, &value) != nil || (value != "" && value != expected) {
					return false, fmt.Errorf("client identity cannot be overridden")
				}
			}
		}
		return false, nil
	default:
		return false, fmt.Errorf("MCP method not available")
	}
}
