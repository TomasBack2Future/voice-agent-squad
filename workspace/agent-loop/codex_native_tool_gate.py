#!/usr/bin/env python3
"""Only the selected Squad MCP tools may mutate a managed Codex source task."""
import argparse
import json
from pathlib import Path
import sys


def decision(config, event):
    role = config.get('role')
    prefix = 'mcp__squad_worker__' if role == 'worker' else 'mcp__squad__' if role == 'dispatcher' else None
    return bool(prefix and event.get('hook_event_name') == 'PreToolUse'
                and event.get('session_id') == config.get('native_session_id')
                and event.get('tool_name', '').startswith(prefix))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    args = parser.parse_args()
    allowed = False
    try:
        config = json.loads(args.config.read_text())
        event = json.load(sys.stdin)
        allowed = decision(config, event)
        with args.config.with_name('gate-observations.jsonl').open('a') as out:
            out.write(json.dumps({'native': event.get('session_id'), 'tool': event.get('tool_name'), 'allowed': allowed}) + '\n')
    except (OSError, ValueError, KeyError):
        pass
    if allowed:
        print('{}')
    else:
        print(json.dumps({'hookSpecificOutput': {'hookEventName': 'PreToolUse', 'permissionDecision': 'deny',
              'permissionDecisionReason': 'Use the assigned Squad source MCP tools; direct local tools are unavailable for this execution.'}}))
    return 0


if __name__ == '__main__':
    sys.exit(main())
