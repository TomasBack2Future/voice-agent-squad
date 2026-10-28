"""Owned original Codex CLI turns; serial same-native resume and exact readback."""
import hashlib
import json
import os
from pathlib import Path
import queue
import shlex
import subprocess
import sys
import threading
import time

from muse_session_host import atomic
from validate_context_package import ValidationError

VERSION = 'codex-cli 0.164.0-alpha.11.1'
BINARY_SHA256 = '50ab38ba21d0d9f8346f32f41848382f15b556190f3c7a07e885a4fb73e379c8'


def binary_identity(path):
    path = Path(path).resolve(strict=True)
    sha = hashlib.sha256(path.read_bytes()).hexdigest()
    if sha != BINARY_SHA256 or not os.access(path, os.X_OK):
        raise ValidationError('current native executable is not the qualified one')
    return {'path': str(path), 'version': VERSION, 'sha256': sha}


def toml(value):
    if isinstance(value, dict):
        return '{' + ', '.join(json.dumps(k) + ' = ' + toml(v) for k, v in value.items()) + '}'
    if isinstance(value, list):
        return '[' + ', '.join(map(toml, value)) + ']'
    return json.dumps(value)


class NativeCLI:
    def __init__(self, root, config, mcp=None):
        self.root, self.config = Path(root).resolve(), dict(config)
        self.root.mkdir(parents=True, mode=0o700, exist_ok=True)
        marker = self.root / 'native-owner.json'
        owner = {'role': config['role'], 'workspace': config['workspace'], 'native': config.get('native_session_id')}
        if marker.exists():
            if json.loads(marker.read_text()) != owner:
                raise ValidationError('native state belongs to a different task/session')
        elif any(self.root.iterdir()):
            raise ValidationError('native state must start in an empty owned directory')
        atomic(marker, owner)
        prior = self.root / 'input-intent.json'
        if prior.exists():
            intent = json.loads(prior.read_text())
            joined = self.root / 'native-join.json'
            if intent.get('state') in ('intent', 'submitted'):
                proof = json.loads(joined.read_text()) if joined.exists() else {}
                if not intent.get('pid') or proof.get('pid') != intent['pid'] or proof.get('joined') is not True:
                    raise ValidationError('original native input/process custody remains unresolved')
                try:
                    os.kill(intent['pid'], 0)
                except ProcessLookupError:
                    pass
                else:
                    raise ValidationError('original native process is still present')
        self.binary = binary_identity(config['client_executable'])
        self.home = self.root / 'codex-home'
        self.home.mkdir(mode=0o700, parents=True, exist_ok=True)
        auth = Path(config.get('auth_file', str(Path.home() / '.codex/auth.json'))).resolve(strict=True)
        link = self.home / 'auth.json'
        if not link.exists(): link.symlink_to(auth)
        if not link.is_symlink() or link.resolve(strict=True) != auth:
            raise ValidationError('native authentication reference changed')
        self.gate = self.root / 'native-tool-gate.json'
        atomic(self.gate, {'role': config['role'], 'native_session_id': config.get('native_session_id')})
        handler = {'type': 'command', 'command': shlex.join([sys.executable,
                   str(Path(__file__).with_name('codex_native_tool_gate.py').resolve()), '--config', str(self.gate)]),
                   'timeout': 10}
        atomic(self.home / 'hooks.json', {'hooks': {'PreToolUse': [{'matcher': '.*', 'hooks': [handler]}]}})
        settings = dict(model=config['model'], model_provider=config.get('provider', 'openai'),
                        model_reasoning_effort=config['effort'], approval_policy='never',
                        sandbox_mode='danger-full-access',
                        features=dict(hooks=True, multi_agent=False, unified_exec=False, code_mode=False),
                        mcp_servers=mcp or {})
        (self.home / 'config.toml').write_text('\n'.join(key + ' = ' + toml(value) for key, value in settings.items()) + '\n')
        self.child = None
        self.reader = None
        self.stderr = None
        self.events = queue.Queue()
        self.effective = None
        self.rollout_baseline = {}

    def input_path(self, client_id):
        return self.root / ('input-client-' + hashlib.sha256(client_id.encode()).hexdigest() + '.json')

    def turn(self, text, client_id, resume=True):
        if self.child is not None:
            raise ValidationError('join the original native turn before another input')
        intent = self.root / 'input-intent.json'
        receipt = {'native': self.config.get('native_session_id'), 'client_id': client_id,
                   'sha256': hashlib.sha256(text.encode()).hexdigest(), 'state': 'intent'}
        client_path = self.input_path(client_id)
        try:
            descriptor = os.open(client_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        except FileExistsError:
            raise ValidationError('native client input already reserved; retain its original receipt and use a new input identity') from None
        with os.fdopen(descriptor, 'w') as output:
            output.write(json.dumps(receipt) + '\n')
            output.flush()
            os.fsync(output.fileno())
        directory = os.open(self.root, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
        self.rollout_baseline = {}
        for path in (self.home / 'sessions').rglob('*.jsonl'):
            info = path.stat()
            self.rollout_baseline[str(path)] = {'bytes': info.st_size, 'device': info.st_dev, 'inode': info.st_ino}
        receipt['rollout_baseline'] = self.rollout_baseline
        atomic(intent, receipt)
        argv = [self.binary['path'], '--dangerously-bypass-hook-trust', '--cd', self.config['workspace'],
                '--sandbox', 'danger-full-access', '--ask-for-approval', 'never',
                '--model', self.config['model'], '-c', 'model_reasoning_effort=' + json.dumps(self.config['effort']),
                'exec', '--json', '--skip-git-repo-check']
        if resume:
            argv += ['resume', self.config['native_session_id']]
        argv += ['-']
        env = {k: v for k, v in os.environ.items() if not k.startswith(('SQUAD_', 'CODEX_', 'CLAUDE_', 'MUSE_'))}
        env.update(CODEX_HOME=str(self.home), PYTHONDONTWRITEBYTECODE='1')
        def read():
            try:
                with (self.root / 'native-stdout.jsonl').open('ab') as log:
                    while True:
                        line = self.child.stdout.readline(2097153)
                        if not line: break
                        if len(line) > 2097152 or not line.endswith(b'\n'):
                            raise ValueError('native JSONL exceeds bound or is partial')
                        log.write(line); log.flush()
                        value = json.loads(line)
                        if not isinstance(value, dict): raise ValueError('invalid native event')
                        self.events.put(value)
            except BaseException as error:
                self.events.put(error)
            finally:
                self.events.put(None)
        self.events = queue.Queue()
        try:
            self.stderr = (self.root / 'native-stderr.log').open('ab')
            self.child = subprocess.Popen(argv, cwd=self.config['workspace'], env=env,
                                          stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=self.stderr)
            receipt.update(state='submitted', pid=self.child.pid)
            atomic(intent, receipt)
            atomic(client_path, receipt)
            self.child.stdin.write(text.encode()); self.child.stdin.close()
            self.reader = threading.Thread(target=read, daemon=True); self.reader.start()
        except BaseException:
            if self.child is None:
                atomic(intent, dict(receipt, state='not-started'))
                atomic(client_path, dict(receipt, state='not-started'))
            self.close()
            raise
        return client_id

    def readback(self):
        native = self.config['native_session_id']
        files = list((self.home / 'sessions').rglob('*' + native + '*.jsonl'))
        if len(files) != 1: raise ValidationError('exact original native rollout unavailable')
        context = None
        baseline = self.rollout_baseline.get(str(files[0]))
        info = files[0].stat()
        if baseline and (baseline['device'] != info.st_dev or baseline['inode'] != info.st_ino or baseline['bytes'] > info.st_size):
            raise ValidationError('original native rollout changed instead of appending the current turn')
        with files[0].open('rb') as stream:
            if baseline:
                stream.seek(baseline['bytes'])
            for line in stream:
                value = json.loads(line)
                if value.get('type') == 'turn_context': context = value['payload']
        expected = {'cwd': self.config['workspace'], 'model': self.config['model'], 'effort': self.config['effort'],
                    'approval_policy': 'never', 'sandbox_policy': {'type': 'danger-full-access'}}
        if context is None:
            raise ValidationError('current native rollout context unavailable; previous turn is not current proof')
        if any(context.get(k) != v for k, v in expected.items()):
            raise ValidationError('actual native model or permission context changed')
        self.effective = expected
        return context

    def wait(self, client_id, seconds=180, interval=None):
        deadline = time.monotonic() + seconds
        terminal = None; native_seen = False; frames = []
        while time.monotonic() < deadline:
            if interval: interval()
            try: value = self.events.get(timeout=min(1, max(.001, deadline - time.monotonic())))
            except queue.Empty: continue
            if value is None: break
            if isinstance(value, BaseException): raise ValidationError('native event stream failed') from value
            frames.append(value)
            if value.get('type') == 'thread.started':
                native = value['thread_id']
                if self.config.get('native_session_id') not in (None, native):
                    raise ValidationError('native resume changed session identity')
                self.config['native_session_id'] = native; native_seen = True
                atomic(self.root / 'native-owner.json', {'role': self.config['role'], 'workspace': self.config['workspace'], 'native': native})
                atomic(self.gate, {'role': self.config['role'], 'native_session_id': native})
            elif value.get('type') in ('turn.completed', 'turn.failed'):
                terminal = 'completed' if value['type'] == 'turn.completed' else 'failed'
        try:
            self.child.wait(timeout=min(5, max(.001, deadline - time.monotonic())))
        except subprocess.TimeoutExpired:
            raise ValidationError('native turn remains live; retain original process and input custody') from None
        if not native_seen or terminal is None:
            raise ValidationError('native terminal proof missing; no automatic input replay')
        context = self.readback()
        result = {'id': context.get('turn_id', client_id), 'status': terminal, 'client_id': client_id,
                  'native': self.config['native_session_id'], 'exit_code': self.child.returncode}
        atomic(self.root / 'last-turn.json', {'native': self.config['native_session_id'], 'turn': result, 'frames': frames})
        intent = json.loads((self.root / 'input-intent.json').read_text())
        intent.update(state='terminal', native=self.config['native_session_id'], terminal=result)
        atomic(self.root / 'input-intent.json', intent)
        atomic(self.input_path(intent['client_id']), intent)
        return result

    def close(self):
        if self.child is None:
            if self.stderr:
                self.stderr.close(); self.stderr = None
            return
        if self.child.poll() is None:
            self.child.terminate()
        result = self.child.wait(timeout=10)
        if self.reader and self.reader.ident is not None:
            self.reader.join(timeout=5)
            if self.reader.is_alive(): raise ValidationError('original native event reader has not joined')
        if not self.child.stdin.closed:
            try:
                self.child.stdin.close()
            except BrokenPipeError:
                pass
        self.child.stdout.close(); self.stderr.close()
        atomic(self.root / 'native-join.json', {'pid': self.child.pid, 'exit_code': result, 'joined': True})
        self.child = None; self.reader = None; self.stderr = None
