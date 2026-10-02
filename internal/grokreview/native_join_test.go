package grokreview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeProofFixture(t *testing.T) (string, NativeJoinProof, AttemptReceipt) {
	return nativeProofFixtureMode(t, "required")
}

func TestNativeJoinMapsParallelLaunchAndAuditedDefaultModel(t *testing.T) {
	root, proof, r := nativeProofFixtureMode(t, "shadow")
	raw, err := os.ReadFile(proof.RolloutPath)
	if err != nil {
		t.Fatal(err)
	}
	var rewritten []byte
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		p := row["payload"].(map[string]any)
		if p["call_id"] == "launch" && p["type"] == "custom_tool_call" {
			code := strings.ReplaceAll(p["input"].(string), " --model grok-4.7", "")
			p["input"] = `text(await tools.exec_command({cmd:"true"}));` + code + `text(await tools.exec_command({cmd:"gh run watch 1"}));`
		}
		if p["call_id"] == "launch" && p["type"] == "custom_tool_call_output" {
			p["output"] = []any{map[string]any{"text": `{"exit_code":0}`}, map[string]any{"text": `{"session_id":123}`}, map[string]any{"text": `{"session_id":999}`}}
		}
		if p["call_id"] == "wait" && p["type"] == "function_call_output" {
			p["output"] = append(p["output"].([]any), map[string]any{"text": `{"exit_code":0,"output":"unrelated joined command"}`})
		}
		encoded, _ := json.Marshal(row)
		switch p["call_id"] {
		case "launch":
			if p["type"] == "custom_tool_call" {
				proof.LaunchSHA256 = receiptHash(encoded)
			} else {
				proof.LaunchOutputSHA256 = receiptHash(encoded)
			}
		case "wait":
			if p["type"] == "function_call_output" {
				proof.TerminalOutputSHA256 = receiptHash(encoded)
			}
		}
		rewritten = append(rewritten, encoded...)
		rewritten = append(rewritten, '\n')
	}
	if err := os.WriteFile(proof.RolloutPath, rewritten, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyNativeJoinAt(root, proof, r); err != nil {
		t.Fatal(err)
	}
	wrong := r
	wrong.Settings.Model = "foreign"
	if err := verifyNativeJoinAt(root, proof, wrong); err == nil {
		t.Fatal("unaudited default model accepted")
	}
}

func nativeProofFixtureMode(t *testing.T, mode string) (string, NativeJoinProof, AttemptReceipt) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	identity, _ := identityFor(admissionBundle())
	r := AttemptReceipt{ID: "original", Identity: identity, Settings: admissionSettings(), Joined: true, Verdict: VerdictError, FailureStage: "sampling", FailureKind: CLIFailureTimeout, Publication: Publication{CheckRunID: 1, Conclusion: "failure"}, CompletedAt: 1}
	r.Settings.Mode = mode
	proof := NativeJoinProof{NativeSession: "owning-native", RolloutPath: path, LaunchCall: "launch", JoinCall: "join", TerminalCall: "wait"}
	var rows []map[string]any
	row := func(typ string, payload any) { rows = append(rows, map[string]any{"type": typ, "payload": payload}) }
	row("session_meta", map[string]any{"session_id": proof.NativeSession})
	command := `text(await tools.exec_command({cmd:"squad-grok-review --repo owner/repo --pr 9 --mode ` + mode + ` --model grok-4.7 --reasoning-effort medium --timeout 20m"}));`
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

func TestHistoricalManagedCaptureRequiresExactExitPropagation(t *testing.T) {
	good := []string{"squad-grok-review", "--mode", "shadow", ">", "/tmp/owned-review.log", "2>&1;", "result=$?;", "tail", "-22", "/tmp/owned-review.log;", "exit", "$result"}
	args, err := legacyManagedArguments(good)
	if err != nil || strings.Join(args, " ") != "--mode shadow" {
		t.Fatalf("qualified capture rejected: %v %v", args, err)
	}
	for _, change := range []string{"relative", "other-file", "fabricated-exit", "unbounded-tail", "command-substitution", "prefix", "extra-command"} {
		t.Run(change, func(t *testing.T) {
			bad := append([]string(nil), good...)
			switch change {
			case "relative":
				bad[4] = "relative"
			case "other-file":
				bad[9] = "/tmp/foreign.log;"
			case "fabricated-exit":
				bad[11] = "0"
			case "unbounded-tail":
				bad[8] = "-201"
			case "command-substitution":
				bad[4] = "/tmp/$(true)"
				bad[9] = bad[4] + ";"
			case "prefix":
				bad[6] = "result=0;"
			case "extra-command":
				bad = append(bad, ";", "true")
			}
			if _, err := legacyManagedArguments(bad); err == nil {
				t.Fatal("unsafe capture accepted")
			}
		})
	}
	root, proof, r := nativeProofFixture(t)
	proof.ToolSessionID = 999
	if err := verifyNativeJoinAt(root, proof, r); err == nil {
		t.Fatal("foreign session joined")
	}
}
