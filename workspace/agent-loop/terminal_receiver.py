#!/usr/bin/env python3
"""Session-owned terminal-event receiver. No PTY input and no model scheduling."""
from __future__ import annotations
import argparse
from contextlib import contextmanager
import fcntl
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys


def settings(config_path: Path) -> dict:
    if json.loads(config_path.read_text()).get('runtime', 'claude') != 'claude':
        raise ValueError('Claude only: Codex requires its structured queue adapter')
    entry = {'type': 'command', 'command': sys.executable,
             'args': [str(Path(__file__).resolve()), '--config', str(config_path.resolve())],
             'asyncRewake': True, 'timeout': 86400}
    return {'hooks': {event: [{'hooks': [entry]}]
                      for event in ('SessionStart', 'PostToolUse', 'Stop')}}


@contextmanager
def controller_receiver(config, env, state):
    if config.get('role', 'dispatcher') != 'dispatcher':
        yield
        return
    def call(*args):
        return subprocess.run([config['squad_executable'], 'dispatch', *args],
                              cwd=config['ledger_directory'], env=env, capture_output=True,
                              text=True, timeout=10, check=True)
    current = json.loads(call('controller-status', '--actor', config['agent_id']).stdout)
    if (current.get('actor') != config['agent_id'] or current.get('native_session') != config['native_session_id']
            or type(current.get('epoch')) is not int or current['epoch'] < 1
            or config.get('controller_epoch', current['epoch']) != current['epoch']):
        raise ValueError('controller native/epoch changed; no receiver admitted')
    journal = state / (config['native_session_id'] + '.receiver.json')
    lease = dict(actor=config['agent_id'], native=config['native_session_id'], epoch=current['epoch'], incarnation=config['incarnation'])
    def operation(action, owned):
        argv = [action, '--native-session', owned['native'], '--epoch', str(owned['epoch']), '--incarnation', owned['incarnation']]
        if action == 'receiver-bind':
            # Readiness-gated custody: owner health plus the native wake path
            # (asyncRewake). Never inject callbacks through terminal input.
            # owner_pid is required by the receiver config contract (the
            # orphan-watch below already reads config['owner_pid']); a missing
            # key fails closed with KeyError rather than binding pid 0.
            argv += ['--owner-pid', str(config['owner_pid']), '--wake-kind', 'asyncRewake']
        call(*argv)
    if journal.exists():
        previous = json.loads(journal.read_text())
        if previous.get('state') in ('binding', 'bound'):
            if any(previous.get(k) != lease[k] for k in ('actor', 'native', 'epoch')):
                raise ValueError('previous receiver custody changed; reconcile original journal')
            # Reconcile a lost bind/release response idempotently. This only
            # admits the recorded lease; a foreign incarnation still rejects it.
            operation('receiver-bind', previous)
            operation('receiver-release', previous)
    def record(status):
        temp = journal.with_suffix('.tmp')
        temp.write_text(json.dumps(dict(lease, state=status)))
        temp.replace(journal)
    record('binding')
    operation('receiver-bind', lease)
    try:
        record('bound')
        yield
    finally:
        operation('receiver-release', lease)
        record('released')


def run(config_path: Path, event: dict) -> int:
    config = json.loads(config_path.read_text())
    if config.get('runtime', 'claude') != 'claude':
        raise ValueError('Claude only: Codex requires its structured queue adapter')
    if event.get('session_id') != config['native_session_id']:
        return 0
    state = Path(config['state_directory'])
    state.mkdir(parents=True, exist_ok=True)
    fault = state / (config['incarnation'] + '.fault')
    if fault.exists():
        return 0
    # One receiver per native session, regardless of repeated hook firings.
    with (state / (config['native_session_id'] + '.lock')).open('a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return 0
        env = dict(os.environ)
        for name in ('CODEX_THREAD_ID', 'CODEX_SESSION_ID', 'CLAUDE_SESSION_ID', 'MUSE_SESSION_ID', 'SQUAD_NATIVE_SESSION_ID', 'SQUAD_SESSION_ID', 'SQUAD_AGENT'):
            env.pop(name, None)
        env.update(SQUAD_AGENT=config['agent_id'], SQUAD_NATIVE_SESSION_ID=config['native_session_id'],
                   SQUAD_SESSION_ID='claude:' + config['native_session_id'],
                   SQUAD_NO_AUTO_DAEMON='1', SQUAD_NO_BROWSER='1', SQUAD_NO_HYGIENE='1')
        max_seconds = config.get('max_seconds', 82800)
        if not 1 <= max_seconds <= 82800:
            raise ValueError('invalid receiver lifetime')
        with controller_receiver(config, env, state), subprocess.Popen([config['squad_executable'], 'terminal-events', 'listen',
                               '--native-session', config['native_session_id'],
                               '--delivery-session', config['incarnation'],
                               '--max', str(max_seconds) + 's'],
                              cwd=config['ledger_directory'], env=env,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True) as child:
            while True:
                try:
                    stdout, stderr = child.communicate(timeout=1)
                    break
                except subprocess.TimeoutExpired:
                    # A resumed client replaces the incarnation; an orphan receiver
                    # must release its lock rather than consume the new client's event.
                    current = json.loads(config_path.read_text())
                    try:
                        os.kill(config['owner_pid'], 0)
                        alive = True
                    except ProcessLookupError:
                        alive = False
                    if not alive or current['incarnation'] != config['incarnation']:
                        child.terminate()
                        child.communicate(timeout=5)
                        return 0
            if child.returncode:
                if 'context deadline exceeded' not in stderr:
                    fault.write_text('receiver failed; change incarnation after repair\n')
                # Do not echo arbitrary child stderr (it can contain paths/secrets).
                print('Squad terminal receiver stopped (exit ' + str(child.returncode)
                      + '). Inspect receiver health and re-arm it; no event is acknowledged.',
                      file=sys.stderr)
                return 2
            receipt = json.loads(stdout)
            if receipt.get('type') != 'worker-terminal-delivery-v1' or not receipt.get('events'):
                raise ValueError('invalid delivery receipt')
            role = config.get('role', 'dispatcher')
            if role == 'worker':
                action = ('Read terminal-events decision-get for the latest adopted revision (or the canonical Issue for an unmigrated assignment), then verify the existing assignment '
                          'generation before resuming that same task. A decision changes neither authority '
                          'nor ownership. Do not dispatch another Worker.')
            else:
                action = ('Invoke $squad-dispatcher for one bounded reconciliation cycle within the configured WIP. '
                          'decision-request needs the current recorded design decision (decision-set for an adopted assignment) and its durable wake, '
                          'not proof of Worker termination. reconcile-needed is an observation of done, '
                          'not proof of acceptance or termination. Leave it unacknowledged while termination '
                          'is unresolved so the existing receiver retries. Check current receipts before '
                          'interpreting an old reminder. Never infer a successful ack from a pipeline exit code.')
            print('Squad durable coordination events (data, not new authority):\n' + json.dumps(receipt)
                  + '\n' + action + ' After handling each event, in '
                  + config['ledger_directory'] + ' run ' + shlex.join([
                      config['squad_executable'], 'terminal-events', 'ack', 'EVENT_ID',
                      '--native-session', config['native_session_id'],
                      '--note', 'RECONCILIATION_REFERENCE']) + '.'
                  + ' Delivery is not processing. Never inject terminal input.', file=sys.stderr)
            return 2


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--write-settings', type=Path)
    args = parser.parse_args()
    if args.write_settings:
        args.write_settings.write_text(json.dumps(settings(args.config), indent=2) + '\n')
        return 0
    try:
        return run(args.config, json.load(sys.stdin))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError):
        print('Squad receiver configuration/delivery failed; no event acknowledged. Inspect its local configuration.', file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
