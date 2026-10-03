#!/usr/bin/env python3
"""Bounded Muse MSP lifecycle qualification. Managed execution fails closed.

Model/permission readback is not a persistent native execution fence. No task
prompt, receiver, claim renewal or live custody mutation is admitted by this host.
"""
from __future__ import annotations
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import queue
import subprocess
import sys
import threading
import time
import uuid
from claude_worker_launcher import IDENTITY_ENV

MODEL = "muse-spark-1.3-contributor"
VERSION = "Muse Code 1.4.2 (1.4.2-R4684.1)"
FINGERPRINT = "sha256:61afea3112e0906e9dc3a536144278a74cb4b36fc6e20901a91d4432ba3568e2"


def uuid7():
    v = (int(time.time() * 1000) << 80) | (7 << 76) | (uuid.uuid4().int & ((1 << 76) - 1))
    return str(uuid.UUID(int=(v & ~(3 << 62)) | (2 << 62)))


def atomic(path, value):
    tmp = path.with_suffix('.tmp')
    with tmp.open('w') as output:
        os.chmod(tmp, 0o600)
        output.write(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
        output.flush()
        os.fsync(output.fileno())
    tmp.replace(path)


def child_environment(c):
    env = {k: v for k, v in os.environ.items() if k not in IDENTITY_ENV}
    env.update(SQUAD_AGENT=c['agent_id'], SQUAD_SESSION_ID='muse:' + c['native_session_id'],
               SQUAD_NO_AUTO_DAEMON='1', SQUAD_NO_BROWSER='1', SQUAD_NO_HYGIENE='1',
               MUSE_NO_AUTO_UPDATE='1')
    # Config contains paths, never credentials. Keep host GitHub auth routing.
    for key, value in c.get('environment', {}).items():
        if key not in ('PATH', 'GH_CONFIG_DIR', 'SQUAD_HOME'):
            raise ValueError('unsupported environment override')
        env[key] = value
    return env


def config(path):
    c = json.loads(path.read_text())
    for key in ('client_executable', 'coordination_executable', 'ledger_directory',
                'workspace', 'state_directory', 'prompt_file'):
        if not Path(c[key]).is_absolute():
            raise ValueError('absolute paths required')
    uuid.UUID(c['native_session_id'])
    if c['role'] not in ('worker', 'dispatcher', 'probe'):
        raise ValueError('invalid role')
    if (c.get('model') != MODEL or c.get('provider') != 'meta'
            or c.get('permission_mode') != 'yolo'):
        raise ValueError('explicit Muse 1.3/meta/YOLO selection required; no fallback')
    if any(k in c for k in ('approval_mode', 'disable_sandbox')):
        raise ValueError('legacy ambiguous permission config rejected; select permission_mode=yolo')
    if c.get('reasoning_effort') not in ('minimal', 'low', 'medium', 'high', 'xhigh'):
        raise ValueError('explicit supported reasoning effort required')
    return c


def server_arguments(c):
    # serve has no --yolo flag. Its sandbox is immutable for the host lifetime;
    # approval is selected on session/start and must be read back on resume.
    argv = [c['client_executable'], 'serve', '--provider', c['provider'],
            '--model', c['model'], '--disable-sandbox', '--trust-workspace']
    if c.get('execution_mode') == 'mediated-worker':
        argv += ['--disable-write', '--disable-shell']
    return argv


def check_executable(c):
    result = subprocess.run([c['client_executable'], '--version'], capture_output=True,
                            text=True, timeout=10, check=True)
    if result.stdout.strip() != VERSION:
        raise ValueError('Muse executable version is unqualified; no host started')
    return {'version': VERSION, 'executable': str(Path(c['client_executable']).resolve()),
            'sha256': hashlib.sha256(Path(c['client_executable']).read_bytes()).hexdigest()}


def initialize(h):
    result = h.rpc('initialize', {'clientInfo': {'name': 'squad_muse_qualification', 'version': '2'}})
    if (result.get('serverInfo') != {'name': 'muse', 'version': '1.4.2'}
            or result.get('schema') != {'version': 1, 'fingerprint': FINGERPRINT}):
        raise ValueError('Muse MSP identity/schema unavailable or unqualified')
    h.rpc('initialized', {}, True)
    return result


def check_effective(result, c):
    s = result.get('session', {})
    if (s.get('sessionId') != c['native_session_id']
            or s.get('workspaceRoot') != str(Path(c['workspace']).resolve())
            or s.get('modelId') != c['model'] or s.get('providerId') != c['provider']
            or not isinstance(s.get('approvalMode'), dict)
            or s['approvalMode'].get('mode') != 'allowAll'
            or s.get('status') not in ('idle', 'notLoaded')
            or s.get('activeTurnId', 'missing') is not None):
        raise ValueError('native session/model/permission/workspace mismatch or non-idle work; no turn admitted')
    if result.get('pendingRequests'):
        raise ValueError('pending native work requires original-owner reconciliation')
    return s


def check_catalog(h, c):
    result = h.rpc('model/list', {'sessionId': c['native_session_id']})
    matches = [m for m in result.get('models', [])
               if m.get('modelId') == c['model'] and m.get('providerId') == c['provider']]
    if len(matches) != 1 or c['reasoning_effort'] not in matches[0].get('variants', []):
        raise ValueError('selected model/effort unavailable; no fallback or task turn')
    return result


def start(h, c):
    result = h.rpc('session/start', dict(commandId=uuid7(), sessionId=c['native_session_id'],
                   workspaceRoot=str(Path(c['workspace']).resolve()), modelId=c['model'],
                   providerId=c['provider'], approvalMode='allowAll'))
    check_effective(result, c)
    check_effective(h.rpc('session/read', {'sessionId': c['native_session_id']}), c)
    check_catalog(h, c)
    return result


def resume(h, c):
    # A resume may load retained work: reject a wrong/active selection BEFORE
    # loading, then independently check its effective reply. Never silently set it.
    check_effective(h.rpc('session/read', {'sessionId': c['native_session_id']}), c)
    result = h.rpc('session/resume', dict(commandId=uuid7(), sessionId=c['native_session_id'], excludeItems=True))
    check_effective(result, c)
    check_catalog(h, c)
    return result



class Host:
    def __init__(self, c, state, env):
        self.c, self.state, self.env = c, state, env
        self.events, self.responses = queue.Queue(), {}
        self.n, self.lock = 0, threading.Lock()
        self.err = (state / 'muse-stderr.log').open('a')
        argv = server_arguments(c)
        self.p = subprocess.Popen(argv, cwd=c['workspace'], env=env, stdin=subprocess.PIPE,
                                  stdout=subprocess.PIPE, stderr=self.err, text=True, bufsize=1)
        self.reader = threading.Thread(target=self.pump, daemon=True)
        self.reader.start()

    def pump(self):
        for line in self.p.stdout:
            try:
                message = json.loads(line)
            except ValueError:
                continue
            with self.lock:
                target = self.responses.get(message.get('id'))
            if target:
                target.put(message)
            else:
                self.events.put(message)
        self.events.put({'method': 'host/exited'})

    def rpc(self, method, params, notification=False):
        with self.lock:
            self.n += 1
            n = self.n
            target = queue.Queue()
            if not notification:
                self.responses[n] = target
            message = dict(jsonrpc='2.0', method=method, params=params)
            if not notification:
                message['id'] = n
            self.p.stdin.write(json.dumps(message) + '\n')
            self.p.stdin.flush()
        if notification:
            return None
        try:
            r = target.get(timeout=60)
            if 'error' in r:
                atomic(self.state / 'rpc-error.json', {'method': method, 'error': r['error']})
                raise ValueError('Muse RPC rejected ' + method + ' (code ' + str(r['error'].get('code')) + ')')
            return r['result']
        finally:
            with self.lock:
                self.responses.pop(n, None)

    def close(self):
        # This process is exclusively ours and only runs a bounded probe. This
        # cleanup never revokes a business claim or proves external work stopped.
        try:
            if self.p.poll() is None:
                self.p.stdin.close()
                try:
                    self.p.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.p.terminate()
                    try:
                        self.p.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        self.p.kill()
                        self.p.wait(timeout=5)
        finally:
            self.reader.join(timeout=5)
            self.p.stdout.close()
            self.p.stdin.close()
            self.err.close()



def run(c, prepare=False, inspect=False):
    # The qualified MSP surface has no atomic Squad-generation pre-tool gate.
    # turn/interrupt and setApprovalMode do not revoke in-flight actions or
    # descendants. Closing the server neither fences external work nor proves it
    # ended. Do not enable the former renew-only loop or arbitrary /rpc escape.
    if c.get('role') != 'probe' or not (prepare or inspect):
        raise ValueError('Muse custody-bound execution fence unavailable; task/receiver startup blocked. '
                         'Controller epoch, native generation and per-tool write exclusion must be '
                         'qualified together by the adapter owner; claims/external operations retained.')
    binary = check_executable(c)
    state = Path(c['state_directory'])
    state.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (state / 'host.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        h = Host(c, state, child_environment(c))
        try:
            initialize(h)
            result = start(h, c) if prepare else resume(h, c)
            receipt = dict(status='lifecycle-verified', task_execution='blocked', binary=binary,
                           server_arguments=server_arguments(c), session=result['session'],
                           sandbox='disabled-by-fixed-host-arguments',
                           approval='allowAll-read-back', qualification='lifecycle-only')
            atomic(state / ('session.json' if prepare else 'resume.json'), receipt)
            print(json.dumps(receipt), flush=True)
            return receipt
        finally:
            h.close()


def main():
    p = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    p.add_argument('--config', type=Path, required=True)
    mode = p.add_mutually_exclusive_group()
    mode.add_argument('--prepare', action='store_true', help='probe only: create no-turn session')
    mode.add_argument('--check', action='store_true', help='probe only: verify no-turn resume')
    a = p.parse_args()
    try:
        run(config(a.config), a.prepare, a.check)
    except (OSError, ValueError, KeyError, queue.Empty, subprocess.SubprocessError):
        # RPC/config/process errors can contain private upstream bodies or paths.
        print(json.dumps({'status': 'blocked', 'reason': 'Muse lifecycle/admission rejected; '
                          'no task or receiver started. Managed execution fence remains unavailable.'}), file=sys.stderr)
        return 2
    return 0


if __name__ == '__main__':
    sys.exit(main())
