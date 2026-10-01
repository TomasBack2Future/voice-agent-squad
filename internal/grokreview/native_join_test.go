package grokreview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func nativeProofFixture(t *testing.T) (string, NativeJoinProof, AttemptReceipt) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	identity, _ := identityFor(admissionBundle())
	r := AttemptReceipt{ID: "original", Identity: identity, Settings: admissionSettings(), Joined: true, Verdict: VerdictError, FailureStage: "sampling", FailureKind: CLIFailureTimeout, Publication: Publication{CheckRunID: 1, Conclusion: "failure"}, CompletedAt: 1}
	proof := NativeJoinProof{NativeSession: "owning-native", RolloutPath: path, LaunchCall: "launch", JoinCall: "join", TerminalCall: "wait"}
	var rows []map[string]any
	row := func(typ string, payload any) { rows = append(rows, map[string]any{"type": typ, "payload": payload}) }
	row("session_meta", map[string]any{"session_id": proof.NativeSession})
	command := `text(await tools.exec_command({cmd:"squad-grok-review --repo owner/repo --pr 9 --mode required --model grok-4.7 --reasoning-effort medium --timeout 20m"}));`
	row("response_item", map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "launch", "input": command})
	row("response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "launch", "output": []any{map[string]any{"text": `{"session_id":123}`}}})
	row("response_item", map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "join", "input": `text(await tools.write_stdin({session_id:123,chars:"",yield_time_ms:1000}));`})
	row("response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "join", "output": []any{map[string]any{"text": "Script running with cell ID 7\n"}}})
	row("response_item", map[string]any{"type": "function_call", "name": "wait", "call_id": "wait", "arguments": `{"cell_id":"7"}`})
	report, _ := json.Marshal(map[string]any{"repository": "owner/repo", "pull_request": 9, "base_sha": "base", "head_sha": "head", "verdict": "error", "failure_stage": "sampling", "failure_kind": "timeout", "requested_model": "grok-4.7", "reasoning_effort": "medium", "check_run_id": 1, "check_conclusion": "failure", "findings": []any{}})
	output, _ := json.Marshal(map[string]any{"exit_code": 1, "output": string(report) + "\nlocal Grok review failed: timeout"})
	row("response_item", map[string]any{"type": "function_call_output", "call_id": "wait", "output": []any{map[string]any{"text": string(output)}}})
	var raw []byte
	for _, v := range rows {
		line, _ := json.Marshal(v)
		p := v["payload"].(map[string]any)
		if p["type"] == "custom_tool_call" || p["type"] == "function_call" {
			switch p["call_id"] {
			case "launch":
				proof.LaunchSHA256 = receiptHash(line)
			case "join":
				proof.JoinSHA256 = receiptHash(line)
			case "wait":
				proof.TerminalSHA256 = receiptHash(line)
			}
		}
		if p["type"] == "custom_tool_call_output" || p["type"] == "function_call_output" {
			switch p["call_id"] {
			case "launch":
				proof.LaunchOutputSHA256 = receiptHash(line)
			case "join":
				proof.JoinOutputSHA256 = receiptHash(line)
			case "wait":
				proof.TerminalOutputSHA256 = receiptHash(line)
			}
		}
		raw = append(raw, line...)
		raw = append(raw, '\n')
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return root, proof, r
}
func TestNativeJoinQualifiedToolCompletionAndNegativeProvenance(t *testing.T) {
	root, proof, r := nativeProofFixture(t)
	if err := verifyNativeJoinAt(root, proof, r); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"native", "hash", "session", "effort", "tuple", "check", "root"} {
		t.Run(change, func(t *testing.T) {
			p, c := proof, r
			historyRoot := root
			switch change {
			case "native":
				p.NativeSession = "foreign"
			case "hash":
				p.JoinSHA256 = "changed"
			case "session":
				p.JoinCall = "unknown"
			case "effort":
				c.Settings.Effort = "high"
			case "tuple":
				c.Identity.HeadSHA = "changed"
			case "check":
				c.Publication.CheckRunID++
			case "root":
				historyRoot = t.TempDir()
			}
			if err := verifyNativeJoinAt(historyRoot, p, c); err == nil {
				t.Fatal("unverified native join accepted")
			}
		})
	}
}
