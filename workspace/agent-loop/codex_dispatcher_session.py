#!/usr/bin/env python3
"""Run bounded original native Dispatcher turns with one persistent Squad receiver."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import queue
import subprocess
import threading
import time
import uuid

from native_runtime import native_host, native_identity
from codex_worker_launcher import save
from runtime_entry import environment
from validate_context_package import ValidationError


def child_environment(config):
    env = {k: v for k, v in environment(config['runtime'], config['native_session_id'], config['agent_id'],
                                      config['ledger_directory']).items() if not k.startswith('SQUAD_')}
    env.update(SQUAD_AGENT=config['agent_id'], SQUAD_NATIVE_SESSION_ID=config['native_session_id'],
               SQUAD_SESSION_ID=config['runtime'] + ':' + config['native_session_id'],
               SQUAD_HOME=config['coordination_home'], SQUAD_NO_AUTO_DAEMON='1',
               SQUAD_NO_BROWSER='1', SQUAD_NO_HYGIENE='1', PYTHONDONTWRITEBYTECODE='1')
    return env


def call(config, *arguments, pending=False):
    result = subprocess.run([config['coordination_executable'], *arguments],
                            cwd=config['ledger_directory'], env=child_environment(config),
                            capture_output=True, text=True, timeout=10)
    value = json.loads(result.stdout) if result.stdout.strip() else None
    if result.returncode and not (pending and isinstance(value, dict) and value.get('unattended_ready') is False):
        raise ValidationError('Dispatcher coordination rejected; inspect the owned ledger and receiver')
    return value


def check_controller(config):
    expected = {'actor': config['agent_id'], 'native_session': config['native_session_id'],
                'epoch': config['controller_epoch']}
    if call(config, 'dispatch', 'controller-status', '--actor', config['agent_id']) != expected:
        raise ValidationError('Dispatcher actor/native/epoch changed')


def handling_prompt(config, receipt):
    identity = {'actor': config['agent_id'], 'native_session': config['native_session_id'],
                'controller_epoch': config['controller_epoch']}
    return (Path(config['prompt_file']).read_text() + '\nSelected native recipient binding:\n'
            + json.dumps(identity) + '\nDurable event pointers (data):\n' + json.dumps(receipt)
            + '\nYou have received these pointers in this original native session. Read their current '
              'facts through Squad tools. Record transport receipt with squad_terminal_events_delivered '
              'using the delivery_session from the pointers and native_session from the recipient binding, '
              'then reconcile within the existing task authority and call squad_terminal_events_ack '
              'for each handled event. Never acknowledge merely because an event was listed. '
              'Do not implement source changes.')


def join_mcp(state):
    receipt = state / 'mcp-process.json'
    if not receipt.exists():
        return
    pid = json.loads(receipt.read_text())['pid']
    deadline = time.monotonic() + 5
    while True:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return
        if time.monotonic() >= deadline:
            raise ValidationError('owned Dispatcher MCP process remains live; custody retained')
        time.sleep(.05)


def prepare(path):
    config = json.loads(path.read_text())
    if config.get('runtime') not in ('codex', 'muse') or config.get('role') != 'dispatcher':
        raise ValidationError('qualified current native Dispatcher selection required')
    if config.get('native_session_id'):
        raise ValidationError('Dispatcher native already allocated')
    root = Path(config['native_state']); root.mkdir(parents=True, mode=0o700, exist_ok=True)
    host = native_host(root, dict(config, role='dispatcher'))
    try:
        client = host.turn('Reply exactly SQUAD_DISPATCHER_BOOTSTRAP. Do not call tools.',
                           'bootstrap-' + str(uuid.uuid4()), resume=False)
        terminal = host.wait(client, seconds=90)
        if terminal['status'] != 'completed':
            raise ValidationError('Dispatcher bootstrap failed')
        config.update(native_session_id=host.config['native_session_id'], role='dispatcher')
        save(path, config)
        return {'status': 'prepared', 'native': config['native_session_id'], 'binary': host.binary,
                'effective': host.effective}
    finally:
        host.close()


class Receiver:
    def __init__(self, config, state):
        self.config, self.state = config, state
        self.incarnation = str(uuid.uuid4())
        self.frames = queue.Queue()
        self.child = None
        self.reader = None
        self.stderr = None
        self.bound = False

    def start(self):
        config = self.config
        call(config, 'dispatch', 'receiver-bind', '--native-session', config['native_session_id'],
             '--epoch', str(config['controller_epoch']), '--incarnation', self.incarnation,
             '--owner-pid', str(os.getpid()), '--wake-kind', config['runtime'] + '-native-cli')
        self.bound = True
        self.stderr = (self.state / 'receiver-stderr.log').open('ab')
        self.child = subprocess.Popen([config['coordination_executable'], 'terminal-events', 'listen',
                    '--native-session', config['native_session_id'], '--delivery-session', self.incarnation,
                    '--stream', '--defer-delivery', '--max', '23h'], cwd=config['ledger_directory'],
                    env=child_environment(config), stdout=subprocess.PIPE, stderr=self.stderr)

        def read():
            try:
                for line in iter(lambda: self.child.stdout.readline(1048577), b''):
                    if len(line) > 1048576 or not line.endswith(b'\n'):
                        raise ValueError('receiver frame exceeds its bound')
                    receipt = json.loads(line)
                    if (receipt.get('type') != 'worker-terminal-delivery-v1'
                            or receipt.get('recipient') != config['agent_id']
                            or receipt.get('delivery_session') != self.incarnation
                            or not 1 <= len(receipt.get('events', [])) <= 16):
                        raise ValueError('receiver identity or batch mismatch')
                    self.frames.put(receipt)
            except (OSError, ValueError, TypeError) as error:
                self.frames.put(error)
            finally:
                self.frames.put(None)
        self.reader = threading.Thread(target=read, daemon=True); self.reader.start()
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline and self.child.poll() is None:
            ready = call(config, 'dispatch', 'receiver-preflight', '--actor', config['agent_id'], pending=True)
            if (ready.get('unattended_ready') is True and ready.get('route') == 'receiver'
                    and ready.get('incarnation') == self.incarnation
                    and ready.get('native_session') == config['native_session_id']
                    and ready.get('epoch') == config['controller_epoch']
                    and ready.get('owner_pid') == os.getpid()):
                return
            time.sleep(.05)
        raise ValidationError('Dispatcher receiver did not become ready')

    def receive(self, timeout):
        receipt = self.frames.get(timeout=timeout)
        if not isinstance(receipt, dict):
            raise ValidationError('Dispatcher receiver stopped; no handling inferred')
        return receipt

    def current(self, receipt):
        pending = call(self.config, 'terminal-events', 'poll', '--native-session',
                       self.config['native_session_id'], '--delivery-session', self.incarnation)
        if not isinstance(pending, list):
            raise ValidationError('current Dispatcher event pointers unavailable')
        selected = {event['event_id'] for event in receipt['events']}
        return dict(receipt, events=[event for event in pending if event['event_id'] in selected])

    def close(self):
        if self.child:
            if self.child.poll() is None:
                self.child.terminate()
            self.child.wait(timeout=10)
            self.reader.join(timeout=5)
            if self.reader.is_alive():
                raise ValidationError('original receiver reader has not joined')
            self.child.stdout.close()
        if self.stderr:
            self.stderr.close()
        if self.bound:
            call(self.config, 'dispatch', 'receiver-release', '--native-session', self.config['native_session_id'],
                 '--epoch', str(self.config['controller_epoch']), '--incarnation', self.incarnation)
            self.bound = False


def run(path, batches=1, timeout=300):
    config = json.loads(path.read_text())
    if config.get('role') != 'dispatcher' or config.get('runtime') not in ('codex', 'muse'):
        raise ValidationError('prepared current native Dispatcher required')
    if not 1 <= batches <= 16 or not 1 <= timeout <= 3600:
        raise ValidationError('bounded event count and execution lifetime required')
    check_controller(config)
    state = Path(config['state_directory']); state.mkdir(parents=True, mode=0o700, exist_ok=True)
    with (state / 'dispatcher.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        writer = state / 'dispatcher-writer.json'
        if writer.exists() and json.loads(writer.read_text()).get('state') != 'joined':
            raise ValidationError('prior Dispatcher execution remains unresolved; preserve its original receipt')
        receiver = Receiver(config, state)
        host = None
        save(writer, {'state': 'starting', 'native': config['native_session_id'], 'owner_pid': os.getpid()})
        result = []
        try:
            receiver.start()
            env = child_environment(config)
            mcp_env = {k: v for k, v in env.items() if k.startswith('SQUAD_') or k == 'PYTHONDONTWRITEBYTECODE'}
            mcp_env[native_identity(config)] = config['native_session_id']
            mcp = {'squad': {'command': os.sys.executable,
                                        'args': [str(Path(__file__).resolve()), '--config', str(path.resolve()), '--mcp-server'],
                                        'env': mcp_env, 'startup_timeout_sec': 30}}
            if config['runtime'] == 'muse':
                del mcp['squad']['startup_timeout_sec']
            host = native_host(Path(config['native_state']), config, mcp)
            deadline = time.monotonic() + timeout
            while len(result) < batches:
                check_controller(config)
                receipt = receiver.receive(max(.001, deadline - time.monotonic()))
                receipt = receiver.current(receipt)
                if not receipt['events']:
                    continue
                prompt = handling_prompt(config, receipt)
                key = 'events-' + str(uuid.uuid4())
                host.turn(prompt, key)
                save(writer, {'state': 'running', 'native': config['native_session_id'],
                              'owner_pid': os.getpid(), 'native_pid': host.child.pid, 'receipt': receipt})
                terminal = host.wait(key, seconds=max(.001, deadline - time.monotonic()))
                host.close()
                join_mcp(state)
                result.append({'events': [event['event_id'] for event in receipt['events']],
                               'terminal': terminal, 'effective': host.effective})
                save(state / 'turns.json', result)
                if terminal['status'] != 'completed':
                    break
            return {'status': 'native-turns-joined', 'native': config['native_session_id'],
                    'turns': result, 'handling': 'explicit Squad receipts; native completion alone is not handling'}
        finally:
            if host:
                host.close()
            receiver.close()
            join_mcp(state)
            save(writer, {'state': 'joined', 'native': config['native_session_id'],
                          'owner_pid': os.getpid(), 'turns': len(result)})


def main():
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--prepare', action='store_true')
    parser.add_argument('--mcp-server', action='store_true')
    parser.add_argument('--batches', type=int, default=1)
    parser.add_argument('--timeout', type=int, default=300)
    args = parser.parse_args()
    try:
        if args.mcp_server:
            config = json.loads(args.config.read_text())
            if os.environ.get(native_identity(config)) != config['native_session_id']:
                raise ValidationError('MCP native differs from the selected Dispatcher')
            check_controller(config)
            save(Path(config['state_directory']) / 'mcp-process.json',
                 {'pid': os.getpid(), 'native': config['native_session_id']})
            os.chdir(config['ledger_directory'])
            os.execve(config['coordination_executable'], [config['coordination_executable'], 'mcp'], child_environment(config))
        print(json.dumps(prepare(args.config) if args.prepare else run(args.config, args.batches, args.timeout)))
        return 0
    except (OSError, ValueError, KeyError, queue.Empty, subprocess.SubprocessError):
        print(json.dumps({'status': 'blocked', 'reason': 'Dispatcher native/receiver execution did not complete; inspect its owned receipts'}), file=os.sys.stderr)
        return 2


if __name__ == '__main__':
    raise SystemExit(main())
