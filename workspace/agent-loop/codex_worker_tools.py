#!/usr/bin/env python3
"""Codex source tools reuse Squad execution custody and contained commands."""
import hashlib
import json
import subprocess
from pathlib import Path

from muse_session_host import atomic
from muse_worker_tools import Bridge, TOOLS, child_environment, main


class CodexBridge(Bridge):
    def read(self, args):
        self.check(readonly=True)
        path = Path(args['path']).resolve(strict=True)
        startup = self.c['startup_references']
        selected = [name for name, value in startup.items() if path == Path(value['path']).resolve(strict=True)]
        if not selected and not path.is_relative_to(Path(self.c['workspace']).resolve()):
            raise ValueError('read must target assigned source or an exact startup reference')
        if path.stat().st_size > 1048576:
            raise ValueError('source read exceeds bound')
        raw = path.read_bytes()
        sha = hashlib.sha256(raw).hexdigest()
        if selected:
            name = selected[0]
            if sha != startup[name]['sha256']:
                raise ValueError('startup reference changed')
            loaded = self.state / 'startup-loaded.json'
            observed = json.loads(loaded.read_text()) if loaded.exists() else {'loaded': {}}
            observed['loaded'][name] = {'path': str(path), 'sha256': sha}
            atomic(loaded, observed)
        return {'path': str(path), 'sha256': sha, 'content': raw.decode()}

    def check(self, mutation=False, readonly=False):
        super().check(mutation, readonly)
        if mutation:
            loaded = json.loads((self.state / 'startup-loaded.json').read_text())['loaded']
            if loaded != self.c['startup_references']:
                raise ValueError('read all exact startup references before source mutation')

    def events(self):
        self.check(readonly=True)
        result = subprocess.run([self.c['coordination_executable'], 'terminal-events', 'poll',
                    '--native-session', self.c['native_session_id'], '--delivery-session', self.c['delivery_incarnation']],
                    cwd=self.c['ledger_directory'], env=child_environment(self.c),
                    capture_output=True, text=True, timeout=10, check=True)
        events = json.loads(result.stdout)
        if not isinstance(events, list):
            raise ValueError('invalid durable event pointer list')
        receipt = {'type': 'worker-terminal-delivery-v1', 'recipient': self.c['agent_id'],
                   'delivery_session': self.c['delivery_incarnation'], 'events': events}
        atomic(self.state / 'native-received-events.json', receipt)
        return receipt

    def acknowledge(self, args):
        self.check(readonly=True)
        receipt = json.loads((self.state / 'native-received-events.json').read_text())
        if args['event_id'] not in [event['event_id'] for event in receipt['events']]:
            raise ValueError('read the exact durable event before acknowledging it')
        result = subprocess.run([self.c['coordination_executable'], 'terminal-events', 'delivered', args['event_id'],
                    '--native-session', self.c['native_session_id'], '--delivery-session', self.c['delivery_incarnation']],
                    cwd=self.c['ledger_directory'], env=child_environment(self.c),
                    capture_output=True, text=True, timeout=10)
        acknowledged = super().call('acknowledge', args)
        if result.returncode and acknowledged['exit_code']:
            raise ValueError('native event delivery or handling custody changed')
        return acknowledged

    def call(self, name, args):
        if name == 'read_file': return self.read(args)
        if name == 'events': return self.events()
        if name == 'acknowledge': return self.acknowledge(args)
        return super().call(name, args)


CODEX_TOOLS = [*TOOLS, {'name': 'events', 'description': 'Read current durable decision event pointers for this exact assignment. Read the current decision, handle it, then acknowledge each event.',
    'inputSchema': {'type': 'object', 'properties': {}, 'additionalProperties': False}}, {'name': 'read_file', 'description': 'Read an assigned source file or exact startup reference with its SHA-256.',
    'inputSchema': {'type': 'object', 'properties': {'path': {'type': 'string'}}, 'required': ['path'], 'additionalProperties': False}}]


if __name__ == '__main__':
    main(bridge_type=CodexBridge, tools=CODEX_TOOLS)
