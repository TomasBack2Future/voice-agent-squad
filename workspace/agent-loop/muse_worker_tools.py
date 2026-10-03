#!/usr/bin/env python3
"""One native-owned MCP bridge for mediated source Worker writes."""
from __future__ import annotations

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import uuid

from claude_worker_launcher import child_environment as claude_environment, diagnostic
from muse_session_host import atomic
from muse_worker_container import Runtime

LIMIT = 16384


def child_environment(c):
    env = claude_environment(dict(c, coordination_mode='native'))
    suffix = hashlib.sha256(str(Path(c['ledger_directory']).resolve()).encode()).hexdigest()[:12]
    env.update(SQUAD_SESSION_ID=f"muse:{c['native_session_id']}:{suffix}",
               SQUAD_NO_HYGIENE='1', MUSE_NO_AUTO_UPDATE='1', SQUAD_HOME=c['coordination_home'])
    return env


def coordination(c, action, evidence=None, report=None):
    argv = [c['coordination_executable'], 'worker-execution', action, '--binding', c['execution_binding']]
    if evidence is not None:
        argv += ['--evidence', evidence]
    if report is not None:
        argv += ['--report', report]
    result = subprocess.run(argv, cwd=c['ledger_directory'], env=child_environment(c),
                            capture_output=True, text=True, timeout=10, check=False)
    if result.returncode:
        raise ValueError('execution ' + action + ' rejected: ' + diagnostic(result.stderr))
    receipt = json.loads(result.stdout)
    if receipt.get('status') != 'ok' or receipt.get('action') != action:
        raise ValueError('invalid execution receipt')
    return receipt



class Bridge:
    def __init__(self, config_path):
        self.c = json.loads(config_path.read_text())
        if os.environ.get('MUSE_SESSION_ID') != self.c['native_session_id']:
            raise ValueError('MCP native identity mismatch')
        self.state = Path(self.c['execution_binding']).parent
        self.lock = (self.state / 'bridge.lock').open('a')
        fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        self.closed = False
        self.journal_path = self.state / 'operations.json'
        self.journal = json.loads(self.journal_path.read_text()) if self.journal_path.exists() else []
        if any(op['state'] not in ('completed', 'joined') for op in self.journal):
            self.lock.close()
            raise ValueError('unresolved prior tool custody; no automatic reconnect')
        atomic(self.state / 'bridge.json', {'pid': os.getpid(), 'native': self.c['native_session_id']})
        self.runtime = Runtime(self.c)

    def check(self, mutation=False, readonly=False):
        if not readonly and (self.closed or (self.state / 'closed').exists()):
            raise ValueError('Worker execution is closed; no new tool admitted')
        if mutation and not (self.state / 'startup-loaded.json').is_file():
            raise ValueError('Read the exact canonical Worker role and assigned project profile before mutations; startup loading is not confirmed yet')
        coordination(self.c, 'check-read' if readonly else ('check-write' if mutation else 'check'))

    def suspend(self):
        self.closed = True
        (self.state / 'closed').touch()
        coordination(self.c, 'suspend')

    def save(self):
        atomic(self.journal_path, self.journal)

    def shell(self, args):
        command = args['command']
        timeout = args.get('timeout_seconds', 300)
        if not isinstance(command, str) or not command.strip() or len(command.encode()) > 65536:
            raise ValueError('bounded non-empty command required')
        if not isinstance(timeout, int) or not 1 <= timeout <= 600:
            raise ValueError('timeout must be between 1 and 600 seconds')
        cwd = Path(args.get('cwd', self.c['workspace'])).resolve(strict=True)
        self.check(mutation=True)
        op = {'id': str(uuid.uuid4()), 'state': 'admitting', 'kind': 'container',
              'command_sha256': hashlib.sha256(command.encode()).hexdigest()}
        op['name'] = 'squad-muse-' + op['id']
        self.journal.append(op)
        self.save()
        child = None
        try:
            self.runtime.create(op, command, cwd)
            op['state'] = 'created'
            self.save()
            self.check(mutation=True)
            with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
                child = subprocess.Popen([*self.runtime.argv, 'container', 'start', '--attach', op['container']],
                                         stdout=stdout, stderr=stderr)
                op.update(pid=child.pid, state='running')
                self.save()
                deadline = time.monotonic() + timeout
                while child.poll() is None:
                    self.check(mutation=True)
                    if time.monotonic() >= deadline:
                        raise TimeoutError('command exceeded its bounded lifetime')
                    time.sleep(1)
                child.wait()
                if not self.runtime.joined(op):
                    raise ValueError('container process custody is unresolved')
                value = self.runtime.inspect(op)
                op.update(state='completed', exit_code=value['State']['ExitCode'])
                self.save()
                stdout.seek(0); stderr.seek(0)
                return {'exit_code': op['exit_code'],
                        'stdout': stdout.read(LIMIT).decode(errors='replace'),
                        'stderr': stderr.read(LIMIT).decode(errors='replace')}
        except BaseException:
            try:
                self.suspend()
            finally:
                if 'container' in op and self.runtime.stop(op):
                    if child:
                        child.wait(timeout=5)
                    op['state'] = 'joined'
                    self.save()
            raise

    def write(self, args):
        path = Path(args['path'])
        if not path.is_absolute():
            path = Path(self.c['workspace']) / path
        path = path.resolve()
        if not path.is_relative_to(Path(self.c['workspace']).resolve()):
            raise ValueError('file must be inside the assigned workspace')
        content = args['content']
        if not isinstance(content, str) or len(content.encode()) > 1048576:
            raise ValueError('bounded UTF-8 content required')
        self.check(mutation=True)
        current = hashlib.sha256(path.read_bytes()).hexdigest() if path.exists() else 'absent'
        if args['expected_sha256'] != current:
            raise ValueError('file changed; read its current contents before retrying')
        op = {'id': str(uuid.uuid4()), 'state': 'admitting', 'path': str(path), 'kind': 'write'}
        self.journal.append(op)
        self.save()
        path.parent.mkdir(parents=True, exist_ok=True)
        fd, name = tempfile.mkstemp(dir=path.parent)
        try:
            if path.exists():
                os.fchmod(fd, path.stat().st_mode & 0o777)
            with os.fdopen(fd, 'w') as f:
                f.write(content)
                f.flush()
                os.fsync(f.fileno())
            self.check(mutation=True)
            os.replace(name, path)
            op['state'] = 'completed'
            self.save()
        except BaseException:
            self.suspend()
            raise
        finally:
            if os.path.exists(name):
                os.unlink(name)
        return {'path': str(path), 'sha256': hashlib.sha256(content.encode()).hexdigest()}

    def control(self, args):
        self.check(readonly=True)
        argv = args['arguments']
        if (not isinstance(argv, list) or not all(isinstance(x, str) for x in argv)
                or len(argv) > 20 or sum(map(len, argv)) > 8192 or not argv):
            raise ValueError('bounded assignment coordination arguments required')
        binding = json.loads(Path(self.c['execution_binding']).read_text())
        if (self.closed or (self.state / 'closed').exists()) and not (
                argv[0] in ('tick', 'claim-inspect', 'show', 'stuck') or argv[:2] in (
                    ['terminal-events', 'decision-get'], ['terminal-events', 'ack'])):
            raise ValueError('suspended Worker admits only decision handling and blocked reporting')
        if argv[0] in ('thinking', 'milestone', 'fyi', 'stuck'):
            if len(argv)!=2 or not argv[1].strip():
                raise ValueError('one bounded message for this assignment required')
            argv += ['--to', binding['item']]
        elif argv[0] in ('tick', 'claim-inspect', 'show', 'review-request'):
            expected = [argv[0]] if argv[0]=='tick' else [argv[0], binding['item']]
            if argv!=expected:
                raise ValueError('coordination command must target this assignment')
        elif len(argv)<2 or argv[0]!='terminal-events' or argv[1] not in ('decision-get','ack'):
            raise ValueError('only assignment coordination and recipient acknowledgements admitted')
        if argv[0] == 'terminal-events' and argv[1] == 'decision-get':
            flags = {}
            rest = argv[2:]
            if len(rest)%2:
                raise ValueError('decision flags must be name/value pairs')
            for name,value in zip(rest[::2],rest[1::2]):
                if name in flags:
                    raise ValueError('duplicate decision flag')
                flags[name]=value
            expected = {'--reservation': binding['reservation'], '--generation': str(binding['generation']), '--worker-session': binding['native']}
            if any(k not in (*expected,'--expected-revision') for k in flags) or any(flags[k]!=v for k,v in expected.items() if k in flags):
                raise ValueError('decision identity differs from this exact assignment')
            if '--expected-revision' in flags and not flags['--expected-revision'].isdigit():
                raise ValueError('bounded decision revision required')
            for key,value in expected.items():
                if key not in flags:
                    argv += [key,value]
        elif argv[0] == 'terminal-events':
            prefix = 'worker-terminal-v1/' + binding['reservation'] + '/' + str(binding['generation']) + '/' + binding['native'] + '/'
            if len(argv) != 5 or not argv[2].startswith(prefix) or argv[3] != '--note' or not argv[4].strip():
                raise ValueError('exact assignment event and handling note required')
        result = subprocess.run([self.c['coordination_executable'], *argv], cwd=self.c['ledger_directory'],
                                env=child_environment(self.c), capture_output=True, text=True, timeout=10)
        return {'exit_code': result.returncode, 'stdout': result.stdout[:LIMIT], 'stderr': diagnostic(result.stderr)}

    def report(self, args):
        self.check(readonly=True)
        if args.get('status') not in ('completed', 'blocked') or not isinstance(args.get('summary'), str) or not 1 <= len(args['summary'].encode()) <= 16384:
            raise ValueError('bounded actual completion or blocked summary required')
        if args['status'] == 'completed':
            self.check(mutation=True)
        atomic(self.state / 'report.json', {'status': args['status'], 'summary': args['summary']})
        return {'status': 'recorded', 'custody': 'retained-until-native-and-tools-join'}

    def call(self, name, args):
        if name == 'run_command':
            return self.shell(args)
        if name == 'decision_get':
            return self.control({'arguments':['terminal-events','decision-get']})
        if name == 'acknowledge':
            return self.control({'arguments':['terminal-events','ack',args['event_id'],'--note',args['note']]})
        if name == 'mailbox':
            return self.control({'arguments':['tick']})
        if name == 'claim_read':
            binding=json.loads(Path(self.c['execution_binding']).read_text())
            return self.control({'arguments':['claim-inspect',binding['item']]})
        if name == 'post_message':
            return self.control({'arguments':[args['kind'],args['text']]})
        if name == 'report':
            return self.report(args)
        if name == 'coordination':
            return self.control(args)
        if name == 'write_file':
            return self.write(args)
        raise ValueError('unknown mediated tool')


TOOLS = [
    {'name': 'report', 'description': 'Record the actual task completion or blocker and verification evidence. The supervisor joins tools and publishes the durable outcome; do not release claims.',
     'inputSchema': {'type': 'object', 'properties': {'status': {'type':'string','enum': ['completed', 'blocked']}, 'summary': {'type': 'string'}}, 'required': ['status','summary']}},
    {'name':'decision_get','description':'Read this exact assignment decision and its current revision. No arguments or identity flags required.',
     'inputSchema':{'type':'object','properties':{},'additionalProperties':False}},
    {'name':'acknowledge','description':'Acknowledge a delivered event only after handling its current decision. Use the exact event_id from the delivery and a concrete handling note.',
     'inputSchema':{'type':'object','properties':{'event_id':{'type':'string'},'note':{'type':'string'}},'required':['event_id','note'],'additionalProperties':False}},
    {'name':'mailbox','description':'Read addressed Squad mailbox updates under this Worker identity.',
     'inputSchema':{'type':'object','properties':{},'additionalProperties':False}},
    {'name':'claim_read','description':'Read the original primary claim, including holder and generation. No arguments required.',
     'inputSchema':{'type':'object','properties':{},'additionalProperties':False}},
    {'name':'post_message','description':'Post one bounded progress message to this assignment item.',
     'inputSchema':{'type':'object','properties':{'kind':{'type':'string','enum':['thinking','milestone','fyi','stuck']},'text':{'type':'string'}},'required':['kind','text'],'additionalProperties':False}},
    {'name': 'run_command', 'description': 'Run a synchronous shell command under this Worker custody. All descendants stay in an owned Linux container; completion requires that container to exit. Use the typed coordination tools for Squad operations.',
     'inputSchema': {'type': 'object', 'properties': {'command': {'type': 'string'}, 'cwd': {'type': 'string'}, 'timeout_seconds': {'type': 'integer'}}, 'required': ['command']}},
    {'name': 'write_file', 'description': 'Write a workspace UTF-8 file under Worker custody. Supply its current SHA-256, or absent for a new file.',
     'inputSchema': {'type': 'object', 'properties': {'path': {'type': 'string'}, 'content': {'type': 'string'}, 'expected_sha256': {'type': 'string'}}, 'required': ['path', 'content', 'expected_sha256']}},
]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    args = parser.parse_args()
    bridge = Bridge(args.config)
    for line in iter(lambda: sys.stdin.readline(2097153), ''):
        if len(line.encode()) > 2097152:
            bridge.suspend()
            raise ValueError('MCP frame exceeded its bound')
        request = json.loads(line)
        if 'id' not in request:
            continue
        try:
            method = request['method']
            if method == 'initialize':
                result = {'protocolVersion': '2024-11-05', 'capabilities': {'tools': {}}, 'serverInfo': {'name': 'squad-worker', 'version': '1'}}
            elif method == 'tools/list':
                result = {'tools': TOOLS}
            elif method == 'tools/call':
                q = request['params']
                result = {'content': [{'type': 'text', 'text': json.dumps(bridge.call(q['name'], q.get('arguments', {})))}]}
            elif method == 'ping':
                result = {}
            else:
                raise ValueError('unsupported MCP method')
        except (OSError, ValueError, KeyError, TimeoutError, subprocess.SubprocessError) as error:
            result = {'isError': True, 'content': [{'type': 'text', 'text': diagnostic(str(error))}]}
        print(json.dumps({'jsonrpc': '2.0', 'id': request['id'], 'result': result}), flush=True)


if __name__ == '__main__':
    main()
