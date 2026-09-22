// Package hooks is the canonical Go source of truth for squad's Claude Code
// hooks. The All array drives both squad install-hooks (interactive, per-hook
// confirmation) and squad install-plugin (one-shot, registers DefaultOn==true
// hooks). The static plugin/hooks.json file is a separate manifest read by
// Claude Code at plugin-load time; if you change anything here that affects
// what plugin loaders should see, update hooks.json to match.
package hooks

import "embed"

//go:embed *.sh
var FS embed.FS

type Hook struct {
	Name        string
	Filename    string
	DefaultOn   bool
	Description string
	TradeOff    string
	EventType   string
	Matcher     string
}

var All = []Hook{
	{"session-start", "session_start.sh", true, "Auto-register and tick at session start; inject a one-line context block.", "Adds ~150ms to session startup. Always recommended.", "SessionStart", "*"},
	{"user-prompt-tick", "user_prompt_submit.sh", true, "Auto-tick before Claude reads each prompt; inject pending mentions/handoffs as context.", "Adds ~50ms per turn; ensures peer chat is delivered at every prompt boundary. Recommended.", "UserPromptSubmit", "*"},
	{"pre-compact", "pre_compact.sh", true, "Inject the agent's current claim + recent chat as additionalContext before context compaction so identity survives.", "Adds ~100ms when Claude compacts. The tradeoff is the post-compact context still knows what claim it owns. Recommended.", "PreCompact", "*"},
	{"stop-listen", "stop_listen.sh", true, "Stop hook: long-block on a localhost TCP listener; wake on any peer say/ask/fyi and inject the inbox.", "Adds a per-session listener bound to 127.0.0.1:0; install probe disables it on hosts that deny loopback bind.", "Stop", "*"},
	{"post-tool-flush", "post_tool_flush.sh", true, "Post-tool mailbox flush: deliver peer chat as additionalContext between tool calls without waiting for Stop.", "Adds ~5ms per tool call (no-op when inbox empty); the tradeoff is sub-second mid-turn delivery.", "PostToolUse", "*"},
	{"session-end-cleanup", "session_end_cleanup.sh", true, "SessionEnd hook: drop this session's notify_endpoints rows so peer senders stop dialing a dead port.", "Necessary companion to stop-listen; harmless on its own.", "SessionEnd", "*"},
	{"async-rewake", "async_rewake.sh", false, "asyncRewake background hook: wake an idle session from outside the IDE within seconds.", "Opt-in until asyncRewake's contract stabilizes; spawns a background process on Stop.", "Stop", "*"},
	{"pre-commit-pm-traces", "pre_commit_pm_traces.sh", false, "Block git commit if backlog IDs leak into staged diff or commit message.", "Catches PM noise pre-commit; harmless if you follow the no-PM-traces rule.", "PreToolUse", "Bash"},
	{"pre-commit-agents-md", "pre_commit_agents_md.sh", false, "Block git commit when AGENTS.md drifts from `squad scaffold agents-md` output.", "Only fires when AGENTS.md is in the staged set and the squad binary is on PATH; CLAUDE.md is unaffected.", "PreToolUse", "Bash"},
	{"pre-edit-touch-check", "pre_edit_touch_check.sh", false, "Warn (do not block) if another agent is touching the same file.", "Useful in multi-agent setups; pure noise solo.", "PreToolUse", "Edit|Write"},
	{"stop-learning-prompt", "stop_learning_prompt.sh", false,
		"At session end, prompt the agent to file a learning if non-trivial code changed.",
		"Adds ~50ms to Stop. Off by default; opt in if your team finds learnings worth filing.",
		"Stop", "*"},
	{"loop-pre-bash-tick", "loop_pre_bash_tick.sh", false,
		"Skill-scoped PreToolUse:Bash tick — fires only while squad-loop is the active skill.",
		"Cheaper than user-prompt-tick (Bash-boundaries only) but only fires when the loop skill is loaded.",
		"PreToolUse", "Bash"},
	{"subagent-start", "subagent_event.sh", true,
		"Record subagent start as event + parent-agent heartbeat.",
		"Adds <50ms per subagent spawn; keeps long subagent work from being marked stale.",
		"SubagentStart", "*"},
	{"subagent-stop", "subagent_event.sh", true,
		"Record subagent stop as event with duration_ms.",
		"Adds <50ms per subagent finish; powers dashboard subagent observability.",
		"SubagentStop", "*"},
	{"task-created", "subagent_event.sh", true,
		"Record Task tool spawn as event + parent-agent heartbeat.",
		"Adds <50ms per Task call; same heartbeat benefit as subagent-start.",
		"TaskCreated", "*"},
	{"task-completed", "subagent_event.sh", true,
		"Record Task tool completion as event with duration_ms.",
		"Adds <50ms per Task finish.",
		"TaskCompleted", "*"},
	{"pre-tool-event", "pre_tool_event.sh", true,
		"Record a pre_tool event into agent_events for the activity stream.",
		"Adds one squad call per tool invocation; SQUAD_EVENTS_FILTER_READ=1 skips Read.",
		"PreToolUse", "*"},
	{"post-tool-event", "post_tool_event.sh", true,
		"Record a post_tool event with exit code and duration into agent_events.",
		"Adds one squad call per tool finish; same opt-out flags as pre-tool-event.",
		"PostToolUse", "*"},
}
