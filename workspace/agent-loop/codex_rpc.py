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
