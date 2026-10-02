#!/usr/bin/env python3
"""Bounded JSON-RPC over the installed Codex Unix WebSocket contract."""
from __future__ import annotations
import base64
import hashlib
import json
import os
from pathlib import Path
import socket
import stat
import struct
import time
import threading

from validate_context_package import ValidationError

LIMIT = 2 * 1024 * 1024


class RPC:
    def __init__(self, endpoint: str, timeout: float = 10, probe: bool = False):
        if not endpoint.startswith('unix:///'):
            raise ValidationError('only an explicit local Unix endpoint is supported')
        path = Path(endpoint.removeprefix('unix://'))
        info = path.lstat()
        # 0.159.2 shortens long Unix paths with an owned discovery symlink.
        # Resolve it, then fence the actual owned socket as well as the link.
        if info.st_uid != os.getuid():
            raise ValidationError('endpoint must belong to the current user')
        resolved = path.resolve(strict=True)
        target = resolved.lstat()
        if not stat.S_ISSOCK(target.st_mode) or target.st_uid != os.getuid():
            raise ValidationError('endpoint must resolve to an owned Unix socket')
        self.socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.socket.settimeout(timeout)
        self.probe = probe
        self.sequence = 0
        self.notifications = []
        self.buffer = b''
        try:
            self.socket.connect(str(resolved))
            key = base64.b64encode(os.urandom(16)).decode()
            self.socket.sendall(('GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\n'
                                 'Connection: Upgrade\r\nSec-WebSocket-Version: 13\r\n'
                                 f'Sec-WebSocket-Key: {key}\r\n\r\n').encode())
            header = b''
            while b'\r\n\r\n' not in header:
                chunk = self.socket.recv(4096)
                if not chunk:
                    raise ValidationError('native handshake closed')
                header += chunk
                if not header or len(header) > 16384:
                    raise ValidationError('invalid native transport handshake')
            raw, self.buffer = header.split(b'\r\n\r\n', 1)
            lines = raw.decode('ascii').split('\r\n')
            fields = {name.lower(): value.strip() for name, _, value in
                      (line.partition(':') for line in lines[1:]) if name}
            expected = base64.b64encode(hashlib.sha1((key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest()).decode()
            if ' 101 ' not in lines[0] or fields.get('sec-websocket-accept') != expected:
                raise ValidationError('native WebSocket handshake rejected')
            self.call('initialize', {'clientInfo': {'name': 'squad_control_plane', 'version': '1'},
                                     'capabilities': {'experimentalApi': True}})
            self.send({'method': 'initialized'})
        except Exception:
            self.close()
            raise

    def close(self):
        self.socket.close()

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.close()

    def exact(self, count):
        while len(self.buffer) < count:
            data = self.socket.recv(min(LIMIT, count - len(self.buffer)))
            if not data:
                raise ValidationError('native transport closed; delivery uncertain')
            self.buffer += data
        result, self.buffer = self.buffer[:count], self.buffer[count:]
        return result

    def frame(self, opcode, payload):
        if len(payload) > LIMIT:
            raise ValidationError('native message exceeds bounded input')
        mask = os.urandom(4)
        length = len(payload)
        header = bytes([0x80 | opcode, 0x80 | min(length, 126)])
        if length >= 126:
            if length > 65535:
                header = bytes([0x80 | opcode, 0x80 | 127]) + struct.pack('!Q', length)
            else:
                header += struct.pack('!H', length)
        self.socket.sendall(header + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(payload)))

    def send(self, value):
        self.frame(1, json.dumps(value, separators=(',', ':')).encode())

    def receive(self, deadline):
        parts = b''
        started = False
        while True:
            self.socket.settimeout(max(.001, deadline - time.monotonic()))
            first, second = self.exact(2)
            opcode, length = first & 15, second & 127
            if first & 0x70 or second & 0x80:
                raise ValidationError('unsupported native frame')
            if length == 126:
                length = struct.unpack('!H', self.exact(2))[0]
            elif length == 127:
                length = struct.unpack('!Q', self.exact(8))[0]
            if length + len(parts) > LIMIT:
                raise ValidationError('native response exceeds bounded output')
            payload = self.exact(length)
            if opcode == 8:
                raise ValidationError('native transport closed; delivery uncertain')
            if opcode in (9, 10):
                if not first & 0x80 or length > 125:
                    raise ValidationError('invalid native control frame')
                if opcode == 9:
                    self.frame(10, payload)
                continue
            if opcode == 1 and not started:
                started = True
            elif opcode != 0 or not started:
                raise ValidationError('unsupported native message frame')
            parts += payload
            if first & 0x80:
                value = json.loads(parts)
                if not isinstance(value, dict):
                    raise ValidationError('invalid native RPC envelope')
                return value

    def call(self, method, params, timeout=10):
        self.sequence += 1
        current = self.sequence
        self.send({'id': current, 'method': method, 'params': params})
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            value = self.receive(deadline)
            if value.get('id') == current:
                if 'error' in value:
                    # Native errors can include paths or private server details.
                    raise ValidationError('native RPC rejected ' + method)
                if not isinstance(value.get('result'), dict):
                    raise ValidationError('invalid native RPC result')
                return value['result']
            if 'id' in value and 'method' in value:
                # Only the isolated probe denies tool requests. A control-only
                # subscriber neither approves nor rejects the owner's requests.
                if self.probe:
                    self.send({'id': value['id'], 'error': {'code': -32601, 'message': 'probe tools disabled'}})
            elif len(self.notifications) < 256:
                self.notifications.append(value)
        raise ValidationError('native RPC deadline expired; delivery uncertain')


class OwnedStdioRPC(RPC):
    """Single parent/thread owns this child and all JSONL pipe IO; never attaches."""
    _owners = set()
    _owners_lock = threading.Lock()

    def __init__(self, child, config):
        import subprocess
        import threading
        import uuid
        from codex_worker_launcher import check_qualification
        check_qualification(config)  # Historical contract does not admit a live native.
        with self._owners_lock:
            if (not isinstance(child, subprocess.Popen) or child.poll() is not None
                    or child.stdin is None or child.stdout is None or child.stderr is None
                    or child in self._owners):
                raise ValidationError('exclusive live owned stdio child required')
            argv = child.args
            if (not isinstance(argv, list) or not argv or argv[0] != config['client_executable']
                    or argv[-3:] != ['app-server', '--listen', 'stdio://']):
                raise ValidationError('stdio child executable/transport owner mismatch')
            self.child = child
            self.owner_pid = os.getpid()
            self.owner_thread = threading.get_ident()
            self.incarnation = str(uuid.uuid4())
            self.pipe_identity = [os.fstat(pipe.fileno()) for pipe in (child.stdin, child.stdout, child.stderr)]
            self.pipe_identity = [(info.st_dev, info.st_ino) for info in self.pipe_identity]
            self.config = dict(config)
            self.sequence = 0
            self.notifications = []
            self.buffer = b''
            self.stderr_open = True
            self.reply_bytes = 0
            self.stderr_bytes = 0
            self.stderr_sha = hashlib.sha256()
            self.uncertain = False
            self.closed = False
            self.native_ready = False
            self.queue_uncertain = False
            self.pending_queue = None
            self._owners.add(child)
        for pipe in (child.stdin, child.stdout, child.stderr):
            os.set_blocking(pipe.fileno(), False)
        # Only this exclusive owner initializes and reads this connection.
        self.call('initialize', {'clientInfo': {'name': 'squad_owned_stdio', 'version': '1'},
                                'capabilities': {'experimentalApi': True}})
        self.send({'method': 'initialized'})

    def identity(self):
        return {'parent_pid': self.owner_pid, 'child_pid': self.child.pid,
                'incarnation': self.incarnation, 'pipes': self.pipe_identity}

    def check_owner(self, identity=None):
        import threading
        if (self.closed or os.getpid() != self.owner_pid or threading.get_ident() != self.owner_thread
                or self.child not in self._owners or self.child.poll() is not None):
            raise ValidationError('owned stdio child lifetime/IO owner changed')
        pipes = [(os.fstat(p.fileno()).st_dev, os.fstat(p.fileno()).st_ino)
                 for p in (self.child.stdin, self.child.stdout, self.child.stderr)]
        if pipes != self.pipe_identity or (identity is not None and identity != self.identity()):
            raise ValidationError('owned stdio child incarnation/pipes changed')

    def send(self, value):
        import select
        self.check_owner()
        raw = (json.dumps(value, separators=(',', ':')) + '\n').encode()
        if len(raw) > LIMIT:
            raise ValidationError('stdio request exceeds bounded input')
        deadline = time.monotonic() + 10
        offset = 0
        while offset < len(raw):
            _, ready, _ = select.select([], [self.child.stdin.fileno()], [], max(0, deadline-time.monotonic()))
            if not ready:
                self.uncertain = True
                raise ValidationError('stdio write deadline; delivery uncertain')
            try:
                written = os.write(self.child.stdin.fileno(), raw[offset:offset+4096])
            except (BlockingIOError, InterruptedError):
                continue
            except OSError:
                self.uncertain = True
                raise ValidationError('stdio write closed; delivery uncertain') from None
            offset += written

    def receive(self, deadline):
        import select
        self.check_owner()
        while True:
            if b'\n' in self.buffer:
                raw, self.buffer = self.buffer.split(b'\n', 1)
                if len(raw) > LIMIT:
                    raise ValidationError('stdio response exceeds bound')
                try:
                    value = json.loads(raw)
                except (ValueError, UnicodeError):
                    raise ValidationError('invalid stdio JSONL; delivery uncertain') from None
                if not isinstance(value, dict):
                    raise ValidationError('invalid stdio RPC envelope')
                return value
            if len(self.buffer) > LIMIT:
                raise ValidationError('stdio response exceeds bound')
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise ValidationError('stdio deadline; delivery uncertain')
            ready, _, _ = select.select([self.child.stdout.fileno()] + ([self.child.stderr.fileno()] if self.stderr_open else []), [], [], remaining)
            if not ready:
                raise ValidationError('stdio deadline; delivery uncertain')
            for fd in ready:
                data = os.read(fd, 4096)
                self.reply_bytes += len(data)
                if self.reply_bytes > LIMIT:
                    raise ValidationError('stdio response stream exceeds bound')
                if fd == self.child.stderr.fileno():
                    if not data:
                        self.stderr_open = False
                        continue
                    self.stderr_bytes += len(data)
                    self.stderr_sha.update(data)
                    if self.stderr_bytes > LIMIT:
                        raise ValidationError('stdio stderr exceeds bound; values withheld')
                else:
                    if not data:
                        raise ValidationError('stdio ended with incomplete reply; delivery uncertain')
                    self.buffer += data

    def call(self, method, params, timeout=10, before_send=None):
        self.check_owner()
        allowed = ('initialize', 'thread/loaded/list', 'thread/read', 'thread/resume', 'thread/queue/list', 'thread/queue/add')
        if method not in allowed:
            raise ValidationError('stdio method outside qualified transport contract')
        if method.startswith('thread/') and method != 'thread/loaded/list' and params.get('threadId') != self.config['native_session_id']:
            raise ValidationError('stdio request belongs to another native')
        if method == 'thread/resume':
            if params != {'threadId': self.config['native_session_id'], 'excludeTurns': True}:
                raise ValidationError('stdio selection overrides unavailable')
            loaded = self.call('thread/loaded/list', {})
            if self.config['native_session_id'] not in loaded.get('data', []):
                raise ValidationError('stdio cannot resume an unloaded native')
        if method == 'thread/queue/add':
            if self.queue_uncertain:
                raise ValidationError('stdio original queue outcome uncertain; no new write')
            if not self.native_ready:
                raise ValidationError('stdio queue requires fresh loaded-owner readiness')
            from codex_worker_launcher import live_target
            live_target(self, self.config, self.native_worktree)
            self.native_ready = False
        if self.uncertain and method not in ('thread/read', 'thread/queue/list', 'thread/loaded/list', 'thread/resume'):
            raise ValidationError('stdio uncertain; only exact readback allowed, no resubmission')
        self.reply_bytes = 0
        self.sequence += 1
        current = self.sequence
        try:
            if method == 'thread/queue/add':
                if before_send is not None:
                    before_send()  # Durable intent AFTER all readiness/read-only gates.
                import copy
                self.pending_queue = copy.deepcopy(params)
                self.queue_uncertain = True
            self.send({'id': current, 'method': method, 'params': params})
            deadline = time.monotonic() + timeout
            while True:
                value = self.receive(deadline)
                if value.get('id') == current and 'method' not in value:
                    if 'error' in value or not isinstance(value.get('result'), dict):
                        raise ValidationError('stdio RPC rejected ' + method)
                    result = value['result']
                    if method == 'thread/queue/add':
                        accepted = result.get('queuedSubmission')
                        if (not isinstance(accepted, dict) or not isinstance(accepted.get('id'), str)
                                or not accepted['id'] or accepted.get('clientUserMessageId') != params.get('clientUserMessageId')
                                or accepted.get('input') != params.get('input')):
                            raise ValidationError('stdio exact queue acceptance malformed')
                        self.queue_uncertain = False
                        self.pending_queue = None
                    return result
                if 'id' in value and 'method' in value:
                    raise ValidationError('stdio host request requires original owner; no automatic approval')
                if 'method' in value:
                    if len(self.notifications) >= 256:
                        raise ValidationError('stdio notification bound exceeded')
                    self.notifications.append(value)
        except (ValidationError, OSError):
            self.uncertain = True
            raise

    def close(self, timeout=5):
        """EOF and join only this child. Timeout retains handle; no kill/retry claim."""
        import threading
        if os.getpid() != self.owner_pid or threading.get_ident() != self.owner_thread:
            raise ValidationError('only original stdio parent may join child')
        if not self.child.stdin.closed:
            self.child.stdin.close()
        import subprocess
        try:
            exit_code = self.child.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            raise ValidationError('owned stdio child still in flight; retain original custody') from None
        for pipe in (self.child.stdout, self.child.stderr):
            pipe.close()
        self.closed = True
        with self._owners_lock:
            self._owners.discard(self.child)
        return {'child_pid': self.child.pid, 'incarnation': self.incarnation,
                'joined_exit': exit_code, 'observed_stderr_bytes': self.stderr_bytes,
                'observed_stderr_sha256': self.stderr_sha.hexdigest(),
                'pipes_closed': True, 'provider_join': 'not_inferred'}
