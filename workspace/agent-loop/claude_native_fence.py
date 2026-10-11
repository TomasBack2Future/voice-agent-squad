"""Fail-closed native fencing; reads custody without scheduling or model calls."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys


def check(config, native, config_path):
    if native != config['native_session_id']:
        raise ValueError('native fence hook identity differs from its selected client')
    env = {k: v for k, v in os.environ.items() if k not in (
        'CODEX_THREAD_ID', 'CODEX_SESSION_ID', 'CLAUDE_SESSION_ID', 'MUSE_SESSION_ID',
        'SQUAD_SESSION_ID', 'SQUAD_NATIVE_SESSION_ID', 'SQUAD_AGENT')}
    env.update(SQUAD_AGENT=config['agent_id'], SQUAD_NATIVE_SESSION_ID=native,
               SQUAD_SESSION_ID='claude:' + native, SQUAD_NO_AUTO_DAEMON='1',
               SQUAD_NO_BROWSER='1', SQUAD_NO_HYGIENE='1', PYTHONDONTWRITEBYTECODE='1')
    result = subprocess.run([config['squad_executable'], 'dispatch', 'worker-native-hook-check',
                             '--native-session', native, '--hook-config', str(config_path.resolve())], cwd=config['ledger_directory'], env=env,
                            capture_output=True, text=True, timeout=10)
    if result.returncode:
        raise ValueError('old native is fenced or ledger verification unavailable; retain custody and reconcile with its controller')
    receipt = json.loads(result.stdout)
    if receipt != {'state': 'eligible', 'native_session': native}:
        raise ValueError('native fence readback is unqualified')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.config.stat().st_size > 65536:
            raise ValueError('bounded native fence configuration required')
        config = json.loads(args.config.read_text())
        event = json.load(sys.stdin)
        if event.get('hook_event_name') != 'PreToolUse':
            raise ValueError('native fence requires a real PreToolUse event')
        check(config, event.get('session_id'), args.config)
        return 0
    except (OSError, ValueError, KeyError, subprocess.SubprocessError):
        print('Squad native fence blocked this tool. Verify the original session, custody and ledger before continuing.', file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
