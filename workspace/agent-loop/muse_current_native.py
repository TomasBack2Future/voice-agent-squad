"""Owned current Muse MSP turns with exact native selection and durable inputs."""
import hashlib
import json
import os
from pathlib import Path
import queue
import subprocess
import threading
import time
from urllib.parse import urlsplit

from muse_session_host import Host, atomic, uuid7, check_effective
from validate_context_package import ValidationError

MODEL = 'muse-spark-1.3-contributor'
VERSION = 'Muse Code 1.4.13 (1.4.13-R6996.1)'
BINARY_SHA256 = '9fd40ae9c6dfb847e3ef58b61405c004d0ebbf19ceb0297be966f750b3b84aa0'
FINGERPRINT = 'sha256:7fddaf646dbc317e5fcac0785e135eb04df14fbb0db1863089dfd6da21730e5b'


def validate_selection(config):
    expected = {'runtime': 'muse', 'model': MODEL, 'effort': 'max'}
    if any(config.get(k) != v for k, v in expected.items()):
        raise ValidationError('current Muse requires the qualified Contributor/max selection')
    if (config.get('role') not in ('worker', 'dispatcher')
            or config.get('provider', 'meta') != 'meta'
            or config.get('permission_mode', 'yolo') != 'yolo'
            or config.get('contributor_mode', 'on') != 'on'):
        raise ValidationError('current Muse role/provider/permission/contributor selection differs')
    path = Path(config['client_executable']).resolve(strict=True)
    sha = hashlib.sha256(path.read_bytes()).hexdigest()
    if sha != BINARY_SHA256 or not os.access(path, os.X_OK):
        raise ValidationError('current Muse native executable is not the qualified binary')
    return {'path': str(path), 'version': VERSION, 'sha256': sha}


def mcp_config(servers, native):
    result = {}
    for name, spec in servers.items():
        if set(spec) - {'command', 'args', 'env'}:
            raise ValidationError('current Muse requires a bounded stdio MCP source')
        env = {k: str(v) for k, v in spec.get('env', {}).items()
               if not k.startswith(('CODEX_', 'CLAUDE_', 'MUSE_'))}
        env.update(MUSE_SESSION_ID=native, PYTHONDONTWRITEBYTECODE='1')
        result[name] = dict(transport='stdio', command=spec['command'], args=spec.get('args', []),
                            env=env, framing='lineDelimitedJson', mode='required')
    return {'mcpServers': result}


def check_workspace_extensions(workspace):
    for name in ('.mcp.json', '.muse/hooks.json'):
        extension = Path(workspace) / name
        if extension.exists() or extension.is_symlink():
            raise ValidationError('unmediated project extensions require separate qualification: ' + name)


def native_settings(path, native):
    evidence = {'effort': None, 'permission': None, 'toolsets': []}

    def visit(value, own=True):
        if isinstance(value, dict):
            stream = value.get('stream')
            if isinstance(stream, dict):
                own = stream.get('kind') == 'session' and stream.get('id') == native
            if own:
                if value.get('payload_type') == 'runtime.reasoning_effort_reconfigure':
                    evidence['effort'] = value['payload']['record'].get('effective')
                if value.get('payload_type') == 'runtime.session.permission_profile_committed':
                    evidence['permission'] = value['payload'].get('resolved_snapshot')
                if 'active_tools' in value:
                    evidence['toolsets'].append(value['active_tools'])
            for key, child in value.items():
                if key == 'record_json' and isinstance(child, str):
                    visit(json.loads(child), own)
                elif isinstance(child, (dict, list)):
                    visit(child, own)
        elif isinstance(value, list):
            for child in value:
                visit(child, own)

    with path.open() as source:
        for line in source:
            visit(json.loads(line))
    permission = evidence['permission'] or {}
    if (evidence['effort'] != 'max' or permission.get('approval') != 'allow_all'
            or permission.get('filesystem', {}).get('mode') != 'unrestricted'
            or permission.get('local_command_network', {}).get('mode') != 'enabled'
            or permission.get('reviewer') != 'none' or not evidence['toolsets']):
        raise ValidationError('current Muse durable effort/permission/toolset proof unavailable')
    forbidden = {'shell', 'exec', 'shell_command', 'write_file', 'apply_patch', 'edit_file'}
    if any(forbidden.intersection(tools) for tools in evidence['toolsets']):
        raise ValidationError('current Muse direct shell/write tool boundary differs')
    return evidence


class CurrentHost(Host):
    def __init__(self, c, state, env):
        self.c, self.state, self.env = c, state, env
        self.events, self.responses = queue.Queue(), {}
        self.n, self.lock = 0, threading.Lock()
        self.err = (state / 'muse-stderr.log').open('a')
        self.p = self.reader = None
        argv = [c['client_executable'], 'serve', '--provider', 'meta', '--model', MODEL,
                '--contributor-mode', 'on', '--disable-sandbox', '--trust-workspace',
                '--disable-shell', '--disable-write', '--disable-request-user-input']
        try:
            self.p = subprocess.Popen(argv, cwd=c['workspace'], env=env, stdin=subprocess.PIPE,
                                      stdout=subprocess.PIPE, stderr=self.err, text=True, bufsize=1)
            atomic(state / 'native-process.json', dict(native=c['native_session_id'], pid=self.p.pid, state='started'))
            self.reader = threading.Thread(target=self.pump, daemon=True)
            self.reader.start()
        except BaseException as error:
            self.close()
            error.native_startup = dict(native=c['native_session_id'], command_id=c.get('input_command_id'),
                                        client_id=c.get('input_client_id'), created_process=self.p is not None)
            if self.p is None:
                error.native_startup['state'] = 'not-started'
            else:
                error.native_startup.update(pid=self.p.pid, exit_code=self.p.returncode, joined=True)
            raise

    def close(self):
        if self.p is None:
            self.err.close()
            return
        if self.p.poll() is None:
            try:
                self.p.stdin.close()
            except BrokenPipeError:
                pass
            try:
                self.p.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.p.terminate()
                try:
                    self.p.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.p.kill()
                    self.p.wait(timeout=5)
        if self.reader and self.reader.ident is not None:
            self.reader.join(timeout=5)
            if self.reader.is_alive():
                raise ValidationError('original Muse native reader has not joined')
        self.p.stdout.close()
        self.p.stdin.close()
        self.err.close()
        atomic(self.state / 'native-join.json', dict(pid=self.p.pid, exit_code=self.p.returncode, joined=True,
               native=self.c['native_session_id'], command_id=self.c.get('input_command_id'),
               client_id=self.c.get('input_client_id')))


class NativeCLI:
    def __init__(self, root, config, mcp=None):
        self.root, self.config = Path(root).resolve(), dict(config)
        self.binary = validate_selection(config)
        workspace = Path(config['workspace']).resolve(strict=True)
        check_workspace_extensions(workspace)
        self.root.mkdir(parents=True, mode=0o700, exist_ok=True)
        marker = self.root / 'native-owner.json'
        prior = json.loads(marker.read_text()) if marker.exists() else None
        native = config.get('native_session_id') or (prior or {}).get('native') or uuid7()
        self.config['native_session_id'] = native
        self.config['workspace'] = str(Path(config['workspace']).resolve(strict=True))
        owner = dict(role=config['role'], workspace=self.config['workspace'], native=native)
        if prior != owner and (prior is not None or any(self.root.iterdir())):
            raise ValidationError('native state belongs to another task/session')
        atomic(marker, owner)
        for path in self.root.glob('input-*.json'):
            self.reconcile_input(path)
        settings = json.loads(Path(config.get('settings_file', Path.home() / '.config/muse/settings.json')).read_text())
        endpoint = settings.get('endpoint_transport', {})
        url = urlsplit(endpoint.get('base_url', ''))
        if url.scheme != 'https' or not url.hostname or url.username or url.password:
            raise ValidationError('existing HTTPS Muse authentication route is required')
        private = {k: settings[k] for k in ('schema_version', 'endpoint_transport', 'model_catalog',
                   'contributor_mode_disclosure_acknowledged', 'permissions') if k in settings}
        private.update(provider='meta', model=MODEL, reasoning_effort='max', contributor_mode_enabled=True)
        self.config_home, self.data_home = self.root / 'config', self.root / 'data'
        (self.config_home / 'muse').mkdir(parents=True, mode=0o700, exist_ok=True)
        atomic(self.config_home / 'muse/settings.json', private)
        self.mcp = mcp_config(mcp or {}, native)
        self.host = self.child = self.reader = None
        self.effective = None
        self.active = None

    def input_path(self, client_id):
        return self.root / ('input-' + hashlib.sha256(client_id.encode()).hexdigest() + '.json')

    def reconcile_input(self, path):
        intent = json.loads(path.read_text())
        if intent.get('state') == 'terminal':
            return
        native = self.config['native_session_id']
        if intent.get('state') == 'not-started':
            startup = intent.get('startup', {})
            if (startup.get('state') != 'not-started' or startup.get('created_process') is not False
                    or startup.get('native') != native or 'pid' in intent
                    or startup.get('command_id') != intent.get('command_id')
                    or startup.get('client_id') != intent.get('client_id')):
                raise ValidationError('original native input has no exact no-process proof')
            return
        proof = intent.get('join')
        if proof is None:
            latest = self.root / 'native-join.json'
            proof = json.loads(latest.read_text()) if latest.exists() else {}
        pid = intent.get('pid')
        if (intent.get('state') not in ('intent', 'submitted', 'joined-unresolved')
                or not isinstance(pid, int) or pid <= 0 or proof.get('pid') != pid
                or proof.get('joined') is not True or not isinstance(proof.get('exit_code'), int)
                or intent.get('native', native) != native or proof.get('native', native) != native
                or proof.get('command_id', intent['command_id']) != intent['command_id']
                or proof.get('client_id', intent['client_id']) != intent['client_id']):
            raise ValidationError('original native input custody remains unresolved')
        if intent['state'] == 'joined-unresolved':
            if (proof.get('checked_dead') is not True or 'join' not in intent
                    or proof.get('native') != native or proof.get('command_id') != intent['command_id']
                    or proof.get('client_id') != intent['client_id']):
                raise ValidationError('original native input has no retained exact join')
            return
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            pass
        else:
            raise ValidationError('original native process is still present; retain its input custody')
        intent.update(original_state=intent['state'], state='joined-unresolved', native=native,
                      join=dict(proof, native=native, command_id=intent['command_id'],
                                client_id=intent['client_id'], checked_dead=True))
        atomic(path, intent)

    def existing_input(self, path, digest, client_id):
        try:
            old = json.loads(path.read_text())
        except ValueError:
            raise ValidationError('original native input reservation remains unresolved') from None
        if old.get('sha256') != digest or old.get('state') != 'terminal':
            raise ValidationError('original input differs or its native custody remains unresolved')
        return client_id

    def turn(self, text, client_id, resume=True):
        path = self.input_path(client_id)
        digest = hashlib.sha256(text.encode()).hexdigest()
        if path.exists():
            return self.existing_input(path, digest, client_id)
        if self.host is not None:
            raise ValidationError('join the original native turn before another input')
        check_workspace_extensions(self.config['workspace'])
        intent = dict(client_id=client_id, sha256=digest, state='intent', command_id=uuid7(),
                      native=self.config['native_session_id'])
        try:
            descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        except FileExistsError:
            return self.existing_input(path, digest, client_id)
        with os.fdopen(descriptor, 'w') as output:
            output.write(json.dumps(intent) + '\n')
            output.flush()
            os.fsync(output.fileno())
        directory = os.open(self.root, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
        env = {k: v for k, v in os.environ.items() if not k.startswith(('SQUAD_', 'CODEX_', 'CLAUDE_', 'MUSE_'))}
        env.update(XDG_CONFIG_HOME=str(self.config_home), XDG_DATA_HOME=str(self.data_home),
                   MUSE_SESSION_ID=self.config['native_session_id'], MUSE_NO_AUTO_UPDATE='1',
                   MUSE_DISABLE_AUTO_UPDATE='1', MUSE_LAUNCHER_INSTALL='0', MUSE_SYNC_UPDATE='0',
                   MUSE_LOGIN='0', PYTHONDONTWRITEBYTECODE='1')
        try:
            self.host = CurrentHost(dict(self.config, client_executable=self.binary['path'],
                   input_command_id=intent['command_id'], input_client_id=client_id), self.root, env)
        except BaseException as error:
            startup = getattr(error, 'native_startup', {})
            if (startup.get('native') == intent['native'] and startup.get('command_id') == intent['command_id']
                    and startup.get('client_id') == client_id):
                if startup.get('created_process') is False and startup.get('state') == 'not-started':
                    intent.update(state='not-started', startup=startup)
                    atomic(path, intent)
                elif startup.get('created_process') is True and startup.get('joined') is True:
                    intent.update(pid=startup['pid'], join=startup)
                    atomic(path, intent)
                    self.reconcile_input(path)
            raise
        self.child, self.reader = self.host.p, self.host.reader
        intent['pid'] = self.child.pid
        atomic(path, intent)
        self.active = client_id
        h, native = self.host, self.config['native_session_id']
        init = h.rpc('initialize', {'clientInfo': {'name': 'squad_current_muse', 'version': '1'},
                     'capabilities': {'requestedCapabilities': ['sessionMcp'], 'userInputDialogs': False}})
        if (init.get('serverInfo') != {'name': 'muse', 'version': '1.4.13'}
                or init.get('schema') != {'version': 1, 'fingerprint': FINGERPRINT}):
            raise ValidationError('current Muse MSP protocol differs from the qualified contract')
        if 'sessionMcp' not in init.get('grantedCapabilities', []):
            raise ValidationError('current Muse did not grant the native per-session MCP capability')
        h.rpc('initialized', {}, True)
        c = dict(self.config, provider='meta')
        started = (self.root / 'native-started.json').exists()
        if resume and started:
            check_effective(h.rpc('session/read', {'sessionId': native}), c)
            r = h.rpc('session/resume', dict(commandId=uuid7(), sessionId=native, excludeItems=True, config=self.mcp))
        elif started:
            raise ValidationError('existing native session requires explicit same-native resume')
        else:
            r = h.rpc('session/start', dict(commandId=uuid7(), sessionId=native,
                      workspaceRoot=c['workspace'], providerId='meta', modelId=MODEL,
                      approvalMode='allowAll', permissionProfile=':unrestricted', config=self.mcp))
        check_effective(r, c)
        self.session_path = Path(r['session']['path'])
        models = h.rpc('model/list', {'sessionId': native}).get('models', [])
        atomic(self.root / 'selected-catalog.json', [m for m in models if m.get('modelId') == MODEL])
        if not any(m.get('modelId') == MODEL and m.get('providerId') == 'meta' for m in models):
            raise ValidationError('selected current Muse model unavailable')
        effort = h.rpc('session/setReasoningEffort', dict(commandId=uuid7(), sessionId=native, reasoningEffort='max'))
        if effort.get('status') != 'accepted':
            raise ValidationError('native reasoning effort was not accepted')
        atomic(self.root / 'native-started.json', {'native': native, 'session_path': str(self.session_path), 'initialize': init})
        turn = h.rpc('turn/start', dict(commandId=intent['command_id'], sessionId=native,
                     reasoningEffort='max', input=[{'type': 'text', 'text': text}]))
        if turn.get('status') != 'accepted' or turn.get('startedNewTurn') is not True:
            raise ValidationError('native did not accept a distinct owned turn')
        intent.update(state='submitted', turn_id=turn['turnId'])
        atomic(path, intent)
        self.active = client_id
        return client_id

    def wait(self, client_id, seconds=180, interval=None):
        path = self.input_path(client_id)
        intent = json.loads(path.read_text())
        if intent.get('state') == 'terminal':
            return intent['terminal']
        if self.active != client_id or self.host is None:
            raise ValidationError('no owned native turn for this input')
        deadline = time.monotonic() + seconds
        frames, texts, terminal = [], [], None
        while time.monotonic() < deadline:
            if interval:
                interval()
            try:
                value = self.host.events.get(timeout=min(1, max(.001, deadline - time.monotonic())))
            except queue.Empty:
                continue
            frames.append(value)
            p = value.get('params', {})
            if p.get('sessionId') != self.config['native_session_id']:
                continue
            if value.get('method') == 'item/completed' and p.get('item', {}).get('kind') == 'agentMessage':
                texts.append(p['item'].get('text', ''))
            if value.get('method') == 'turn/completed' and p.get('turnId') == intent['turn_id']:
                terminal = p.get('terminal')
                break
        if terminal is None:
            raise ValidationError('native turn remains unresolved; retain original process and input custody')
        readback = self.host.rpc('session/read', {'sessionId': self.config['native_session_id']})
        check_effective(readback, dict(self.config, provider='meta'))
        self.effective = dict(model=MODEL, effort='max', contributor_mode='on', permission_mode='yolo',
                              provider='meta', session=readback['session'], session_path=str(self.session_path),
                              durable_settings=native_settings(self.session_path, self.config['native_session_id']))
        atomic(self.root / 'last-turn.json', {'effective': self.effective, 'frames': frames, 'assistant_texts': texts})
        self.close()
        joined = json.loads((self.root / 'native-join.json').read_text())
        result = dict(id=intent['turn_id'], status='completed' if terminal == 'completed' else 'failed',
                      native=self.config['native_session_id'], exit_code=joined['exit_code'])
        intent = json.loads(path.read_text())
        intent.update(state='terminal', terminal=result)
        atomic(path, intent)
        return result

    def close(self):
        if self.host is None:
            return
        host = self.host
        host.close()
        if host.reader.is_alive() or host.p.poll() is None:
            raise ValidationError('original native process and reader have not joined')
        proof = dict(pid=host.p.pid, exit_code=host.p.returncode, joined=True,
                     native=self.config['native_session_id'])
        if self.active is not None:
            path = self.input_path(self.active)
            intent = json.loads(path.read_text())
            proof.update(command_id=intent['command_id'], client_id=self.active)
        atomic(self.root / 'native-join.json', proof)
        if self.active is not None:
            self.reconcile_input(path)
        self.host = self.child = self.reader = None
        self.active = None
