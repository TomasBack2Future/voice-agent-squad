#!/usr/bin/env python3
"""Launch one assigned source Muse Worker through a custody-bound MCP bridge."""
from __future__ import annotations

import argparse
import fcntl
import json
import hashlib
import os
from pathlib import Path
import queue
import re
import subprocess
import sys
import time
import uuid

from claude_worker_launcher import authorization, binding, check_heartbeat_runtime, diagnostic, heartbeat
import muse_session_host as native
from muse_worker_tools import child_environment, coordination
from validate_context_package import ROOT, ValidationError, validate_file
from worker_preflight import check_profile, check_worktree, git, repository_name
from muse_worker_receiver import Receiver
from muse_worker_container import Runtime
from muse_worker_view import LiveView
from muse_worker_handoff import check_handoff
from muse_worker_input import TerminalInput

FINGERPRINT = native.FINGERPRINT


def read_decision(c, env, pin):
    """Read the currently adopted decision for this assignment, if any.

    Returns the decision object or None for legacy unadopted assignments.
    The caller passes the observed revision back unchanged; a changed
    revision surfaces as stale at submit time for Worker reconciliation.
    Never auto-proceeds: holds are returned as-is for the caller to honor.
    """
    result = subprocess.run([c['coordination_executable'], 'terminal-events', 'decision-get',
                             '--reservation', pin['reservation'], '--generation', str(pin['generation']),
                             '--worker-session', pin['native']],
                            cwd=c['ledger_directory'], env=env, capture_output=True, text=True, timeout=10, check=False)
    if result.returncode:
        if 'no rows' in (result.stderr or ''):
            return None
        raise ValidationError('decision read failed: ' + diagnostic(result.stderr or result.stdout))
    try:
        decision = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise ValidationError('decision read returned invalid JSON') from error
    if decision == {'revision': 0, 'outcome_id': 0, 'action': '', 'condition': '', 'worker_agent': ''}:
        return None
    if not isinstance(decision, dict) or not decision.get('revision'):
        raise ValidationError('decision read returned no revision')
    return decision


def submit_outcome(c, env, pin, kind, body_file, decision_revision=None, request_key=None):
    """Submit one Worker outcome atomically via terminal-events submit.

    decision_revision is the actually observed revision (or None for legacy
    unadopted assignments); it is passed unchanged, never refreshed. request_key
    is the stable request identity: retries reuse it, distinct requests use
    distinct keys (None keeps the legacy single default slot). Returns the
    stored message/event IDs. A rejection carries a precise condition plus
    repair action instead of the legacy generic error.
    """
    argv = [c['coordination_executable'], 'terminal-events', 'submit',
           '--reservation', pin['reservation'], '--generation', str(pin['generation']),
           '--worker-session', pin['native'], '--kind', kind,
           '--body-file', str(body_file)]
    if request_key:
        argv += ['--request-key', str(request_key)]
    if decision_revision:
        argv += ['--expected-decision', str(decision_revision)]
    result = subprocess.run(argv, cwd=c['ledger_directory'], env=env,
                            capture_output=True, text=True, timeout=10, check=False)
    if result.returncode:
        raise ValidationError('outcome submit rejected: ' + diagnostic(result.stderr or result.stdout))
    try:
        submitted = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise ValidationError('outcome submit returned invalid JSON') from error
    if not isinstance(submitted, dict) or not submitted.get('message_id') or not submitted.get('event_id'):
        raise ValidationError('outcome submit returned no message/event IDs')
    return submitted


def publish_report(c, env, pin, state):
    """Publish a joined execution's report with a retry-stable execution key."""
    report = json.loads((state / 'report.json').read_text())
    kind = 'handoff-complete' if report['status'] == 'completed' else 'blocked'
    decision = read_decision(c, env, pin)
    # Atomic submit distinguishes a new completion under hold from an identical
    # committed replay after the controller advances to a new held phase.
    body_file = state / 'outcome-body.txt'
    body_file.write_text(report.get('summary', '') or report['status'])
    return submit_outcome(c, env, pin, kind, body_file,
                          decision_revision=decision['revision'] if decision else None,
                          request_key='report-' + pin['id'])


def resolve_effort(c):
    if 'reasoning_effort' in c:
        effort, source = c['reasoning_effort'], 'launch-config'
    else:
        preferences = json.loads(Path(c['settings_file']).read_text())
        if not isinstance(preferences, dict):
            raise ValidationError('Muse settings must be an object; no model started')
        effort = preferences.get('reasoning_effort', 'max')
        source = 'settings-file' if 'reasoning_effort' in preferences else 'default'
    if effort not in ('minimal', 'low', 'medium', 'high', 'xhigh', 'max'):
        raise ValidationError('unsupported Muse reasoning preference; no model started')
    return effort, source


def config_file(path):
    c = validate_file(path, ROOT / 'schemas/muse-launch.schema.json')
    if str(uuid.UUID(c['native_session_id'])) != c['native_session_id'] or uuid.UUID(c['native_session_id']).version != 7:
        raise ValidationError('native session must be a canonical UUIDv7')
    for key in ('client_executable', 'coordination_executable'):
        if not Path(c[key]).is_file() or not os.access(c[key], os.X_OK):
            raise ValidationError(key + ' is unavailable')
    workspace = Path(c['workspace']).resolve(strict=True)
    for owned in (ROOT, Path(c['state_directory']), Path(c['coordination_home']),
                  Path(c['client_executable']), Path(c['coordination_executable']),
                  Path(c['tool_runtime']['executable']), Path(c['settings_file'])):
        if owned.resolve().is_relative_to(workspace):
            raise ValidationError('runtime, credentials, coordination state and custody evidence must remain outside the mutable workspace')
    for name in ('.mcp.json', '.muse/hooks.json'):
        if (workspace / name).exists():
            raise ValidationError('unmediated project extensions require separate qualification: ' + name)
    c['reasoning_effort'], c['reasoning_effort_source'] = resolve_effort(c)
    c['ui_mode'], c['ui_mode_source'] = resolve_ui_mode(c)
    return c


def resolve_ui_mode(c):
    """Resolve explicit ui_mode with provenance; default stays live-view.

    live-view is a passive display on the mediated headless host, never an
    interactive TUI. An explicit interactive request keeps its label so the
    caller must verify a visible selected surface; it never silently
    becomes a log view.
    """
    from launch_selection import resolve_launch
    task = {'ui_mode': c['ui_mode']} if c.get('ui_mode') else {}
    resolved = resolve_launch(task, c['settings_file'], 'muse')
    ui_mode, source = resolved['ui_mode']
    if source == 'default':
        return 'live-view', 'default'
    return ui_mode, ('launch-config' if source == 'task' else source)


def check_launch(assignment, config_path, resume=False):
    c = config_file(config_path)
    if Path(c['workspace']).resolve() != Path(assignment['worktree']).resolve():
        raise ValidationError('Muse workspace differs from assignment')
    check_profile(assignment)
    if resume or c.get('handoff'):
        if c.get('handoff'):
            if resume:
                raise ValidationError('fresh source handoff and same-native resume are separate operations')
            check_handoff(assignment, c)
        work = Path(c['workspace']).resolve()
        if (Path(git(work, 'rev-parse', '--show-toplevel')).resolve() != work
                or git(work, 'branch', '--show-current') != assignment['branch']
                or repository_name(git(work, 'remote', 'get-url', 'origin'), assignment.get('repository_host', 'github.com'), assignment.get('clone_layout', 'plain')) != assignment['repository']):
            raise ValidationError('retained worktree identity changed')
        git(work, 'merge-base', '--is-ancestor', assignment['base_sha'], 'HEAD')
    else:
        check_worktree(assignment)
    if not assignment['authorization']['source_mutation']:
        raise ValidationError('source Worker requires explicit source mutation authority')
    if assignment['authorization']['staging'] or assignment['authorization']['production']:
        raise ValidationError('this Muse launcher qualifies source Workers only')
    authorization(c, assignment)
    env = child_environment(c)
    if binding(assignment, c, env) != 'bound':
        raise ValidationError('Worker must be bound before native startup')
    heartbeat(assignment, c, env, check=True)
    inspected = subprocess.run([c['coordination_executable'], 'claim-inspect', assignment['item']],
                               cwd=c['ledger_directory'], env=env, capture_output=True, text=True, timeout=10, check=True)
    claim = json.loads(inspected.stdout)['env_claim']
    if (claim is None or claim['holder'] != c['agent_id'] or claim['state'] != 'held'
            or claim['generation'] != c['claim_generation']):
        raise ValidationError('exact original primary claim changed; no task admitted')
    selected = subprocess.run([c['coordination_executable'], 'dispatch', 'controller-status', '--actor', c['dispatcher_agent_id']],
                               cwd=c['ledger_directory'], env=env, capture_output=True, text=True, timeout=10, check=True)
    controller = json.loads(selected.stdout)
    if controller != {'actor': c['dispatcher_agent_id'], 'native_session': c['controller_native'], 'epoch': c['controller_epoch']}:
        raise ValidationError('controller native or epoch changed; no task admitted')
    check_heartbeat_runtime(c, env)
    result = subprocess.run([c['coordination_executable'], 'worker-execution', 'check', '--help'],
                            cwd=c['ledger_directory'], env=env, capture_output=True, text=True, timeout=10)
    if result.returncode or '--binding' not in result.stdout:
        raise ValidationError('coordination runtime lacks source Worker execution gating')
    native.check_executable(c)
    Runtime(c)
    return {'reasoning_effort': c['reasoning_effort'], 'reasoning_effort_source': c['reasoning_effort_source'],
            'progress_view': c.get('progress_view', 'live'), 'status': 'ready', 'runtime_approval': 'explicit-meta-1.3-yolo', 'primary_ownership': 'verified',
            'environment': 'identity-sanitized', 'execution': 'mediated-source-tools',
            'authorization': authorization(c, assignment)}


def initialize(h):
    result = h.rpc('initialize', {'clientInfo': {'name': 'squad_muse_worker', 'version': '1'},
                  'capabilities': {'experimentalApi': True, 'requestedCapabilities': ['sessionMcp']}})
    if (result.get('serverInfo') != {'name': 'muse', 'version': '1.4.2'}
            or result.get('schema') != {'version': 1, 'fingerprint': FINGERPRINT}
            or result.get('experimentalApi') is not True
            or 'sessionMcp' not in result.get('grantedCapabilities', [])):
        raise ValidationError('Muse native schema or session MCP capability is unqualified')
    h.rpc('initialized', {}, True)
    return result


def settings(c, state):
    x = json.loads(Path(c['settings_file']).read_text())
    selected = {k: x[k] for k in ('schema_version', 'endpoint_transport', 'model_catalog') if k in x}
    selected.update(provider='meta', model=native.MODEL, reasoning_effort=c['reasoning_effort'])
    target = state / 'config' / 'muse'
    target.mkdir(parents=True, mode=0o700)
    native.atomic(target / 'settings.json', selected)
    os.chmod(target / 'settings.json', 0o600)
    return str(target.parent)


def record_context_read(c, state, message, loaded):
    params = message.get('params', {})
    item = params.get('item', {})
    if (message.get('method') != 'item/completed' or params.get('sessionId') != c['native_session_id']
            or item.get('kind') != 'toolCall' or item.get('tool') != 'read_file' or item.get('status') != 'completed'):
        return
    try:
        args = json.loads(item.get('args', '{}'))
        path = Path(args['path'])
        if not path.is_absolute():
            path = Path(c['workspace']) / path
        path = path.resolve()
    except (KeyError, ValueError):
        return
    startup = json.loads((state / 'startup.json').read_text())
    required = ('role', 'profile', 'lifecycle')
    if any(key not in startup for key in required):
        return
    for key in required:
        expected = startup[key]
        output = item.get('visibleOutput', '')
        if path == Path(expected['path']).resolve() and output.startswith('Read text file `'):
            lines = []
            numbers = []
            for line in output.splitlines():
                match = re.fullmatch(r'(\d+)\|(.*)', line)
                if match:
                    numbers.append(int(match[1])); lines.append(match[2])
            source = path.read_bytes()
            if (hashlib.sha256(source).hexdigest() == expected['sha256']
                    and lines == source.decode().splitlines() and numbers == list(range(1,len(lines)+1))):
                loaded[key] = {'path': str(path), 'sha256': expected['sha256'], 'native_item': item['itemId']}
    if set(loaded) == set(required):
        native.atomic(state / 'startup-loaded.json', {'native': c['native_session_id'], 'loaded': loaded})


def submit_text(h, c, command, text, pending):
    h.rpc('turn/start', {'commandId': command, 'sessionId': c['native_session_id'],
          'reasoningEffort': c['reasoning_effort'], 'input': [{'type': 'text', 'text': text}]})
    # Only native acceptance consumes pending human input. Later model/tool or
    # publication failures must not turn accepted input into an implicit replay.
    pending['accepted'] = True


def drive_turns(h, c, state, assignment, env, pin, receiver, view, ui=None):
    deadline = time.monotonic() + c.get('max_seconds', 82800)
    renewed = time.monotonic()
    native_events = []
    loaded = {}
    terminal = None
    busy = True
    while time.monotonic() < deadline:
        if ui:
            action = ui.read(busy)
            if action:
                kind, text = action
                if kind in ('exit', 'resume', 'interrupt'):
                    ui.action = 'restart-wait' if kind == 'interrupt' else kind
                    # Revoke admission BEFORE interrupting or closing the host.
                    (state / 'closed').touch()
                    coordination(c, 'suspend')
                    if busy:
                        h.rpc('turn/interrupt', {'commandId': native.uuid7(), 'sessionId': c['native_session_id']})
                        return 'interrupted'
                    return terminal
                if kind == 'text' and not busy:
                    try:
                        decision = read_decision(c, env, pin)
                        if decision and decision['action'] != 'proceed':
                            ui.restore(text)
                            ui.status('HOLD — task retained; source turns wait for the controller decision')
                            continue
                        if (state / 'closed').exists():
                            ui.restore(text)
                            ui.status('execution suspended — /resume joins and reconnects the same native')
                            continue
                        # A previous turn's report must never describe a new turn.
                        (state / 'report.json').unlink(missing_ok=True)
                        submit_text(h, c, native.uuid7(), text, {})
                    except (OSError, ValueError, subprocess.SubprocessError, queue.Empty):
                        ui.restore(text)
                        raise
                    busy = True
                    terminal = None
                    ui.status('running — Escape interrupts; your draft stays local')
        if time.monotonic() - renewed >= 20:
            heartbeat(assignment, c, env)
            renewed = time.monotonic()
            if not ui:
                view.status('[Muse] running; claim renewed, execution pin retained')
        try:
            m = h.events.get(timeout=0.1 if ui else 1)
        except queue.Empty:
            if ui and not busy:
                receipt = receiver.pending()
                if receipt:
                    (state / 'report.json').unlink(missing_ok=True)
                    receiver.submit(h, receipt)
                    busy = True
                    terminal = None
                    ui.status('handling current durable decision')
            continue
        params = m.get('params', {})
        if m.get('method') != 'host/exited' and params.get('sessionId') != c['native_session_id']:
            continue
        if ui:
            ui.erase()
        view.observe(m)
        if ui:
            ui.redraw()
        record_context_read(c, state, m, loaded)
        with (state / 'native-events.jsonl').open('a') as journal:
            os.chmod(journal.name, 0o600)
            journal.write(json.dumps(m, ensure_ascii=False) + '\n')
        native_events.append(m)
        # Keep full evidence on disk without retaining and rewriting a day's
        # worth of deltas in a persistent UI's memory.
        native_events = native_events[-256:]
        native.atomic(state / 'native-events.json', native_events)
        if m.get('method') == 'host/exited':
            raise ValidationError('native host exited; original custody retained')
        if m.get('method') == 'turn/completed':
            terminal = params.get('terminal')
            if terminal == 'completed':
                if ui and (state / 'report.json').exists():
                    # A reported execution must join and publish before another
                    # turn can replace its result. The frontend stays alive.
                    ui.action = 'reported'
                    return terminal
                receipt = receiver.pending()
                if receipt:
                    if ui:
                        (state / 'report.json').unlink(missing_ok=True)
                    receiver.submit(h, receipt)
                    terminal = None
                    continue
                if ui:
                    busy = False
                    decision = read_decision(c, env, pin)
                    if decision and decision['action'] != 'proceed':
                        ui.status('HOLD — visible input retained; source writes require the controller decision')
                    elif (state / 'closed').exists():
                        ui.status('execution suspended — /resume reconnects after verified join')
                    else:
                        ui.status('waiting — editable input ready; same native and mediated tools retained')
                    continue
            if ui:
                ui.action = 'restart-wait'
            break
    if ui and terminal is None:
        ui.action = 'restart-wait'
    return terminal


def run_execution(assignment_path, config_path, resume=False, ui=None, owned_lock=None, continuation=None):
    assignment = validate_file(assignment_path, ROOT / 'schemas/assignment-envelope.schema.json')
    check_launch(assignment, config_path, resume)
    c = config_file(config_path)
    state = Path(c['state_directory']) / str(uuid.uuid4())
    state.mkdir(parents=True, mode=0o700)
    lock = owned_lock or (Path(c['state_directory']) / (c['native_session_id'] + '.lock')).open('a')
    try:
        if not owned_lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        c.update(execution_mode='mediated-worker', execution_binding=str(state / 'binding.json'))
        env = child_environment(c)
        env['XDG_CONFIG_HOME'] = settings(c, state)
        writer = Path(c['state_directory']) / (c['native_session_id'] + '.writer.json')
        custody = {k: c[k] for k in ('native_session_id', 'agent_id', 'claim_generation', 'dispatcher_agent_id', 'controller_native', 'controller_epoch')}
        if writer.exists():
            old = json.loads(writer.read_text())
            if old.get('state') != 'joined' or old.get('assignment') != assignment or old.get('custody') != custody:
                raise ValidationError('original Worker writer is unjoined or assignment changed; reconcile original custody')
        native.atomic(writer, {'state': 'intent', 'custody': custody, 'assignment': assignment, 'evidence': str(state)})
        view = LiveView(c['native_session_id'], enabled=bool(ui) or c.get('progress_view', 'live') == 'live')
        view.status('[Muse] ' + ('Resuming ' if resume else 'Starting ') + c['native_session_id']
                    + ' | effort=' + c['reasoning_effort'] + ' (' + c['reasoning_effort_source'] + ')'
                    + ' | mediated source tools | evidence=' + str(state))
        h = native.Host(c, state, env)
    except BaseException:
        if not owned_lock:
            lock.close()
        raise
    pinned = False
    receiver = None
    terminal = None
    try:
        native.atomic(writer, {'state': 'running', 'custody': custody, 'assignment': assignment, 'evidence': str(state), 'pid': h.p.pid})
        initialize(h)
        claim = subprocess.run([c['coordination_executable'], 'claim-inspect', assignment['item']],
                               cwd=c['ledger_directory'], env=env, capture_output=True, text=True, timeout=10, check=True)
        claim = json.loads(claim.stdout)['env_claim']
        if claim is None or claim['holder'] != c['agent_id'] or claim['state'] != 'held' or claim['generation'] != c['claim_generation']:
            raise ValidationError('primary claim changed')
        pin = dict(kind='worker', id='muse-' + state.name, item=assignment['item'], actor=c['agent_id'],
                   native=c['native_session_id'], claim_generation=claim['generation'], reservation=assignment['reservation']['key'],
                   source_ref='github:' + assignment['issue'], generation=assignment['reservation']['generation'],
                   controller=c['dispatcher_agent_id'], controller_native=c['controller_native'], epoch=c['controller_epoch'],
                   expires_at=int(time.time()) + c.get('max_seconds', 82800), client_pid=h.p.pid, tool_runtime=c['tool_runtime'])
        c['execution_id'] = pin['id']
        c['assignment'] = assignment
        native.atomic(state / 'binding.json', pin)
        coordination(c, 'acquire')
        pinned = True
        bridge_config = state / 'bridge-config.json'
        native.atomic(bridge_config, c)
        mcp = {'mcpServers': {'squad_worker': {'transport': 'stdio', 'command': sys.executable,
               'args': [str(ROOT / 'muse_worker_tools.py'), '--config', str(bridge_config)],
               'framing': 'lineDelimitedJson', 'mode': 'required', 'env': {'SQUAD_HOME': c['coordination_home']}}}}
        params = dict(commandId=native.uuid7(), sessionId=c['native_session_id'], config=mcp)
        if resume:
            native.check_effective(h.rpc('session/read', {'sessionId': c['native_session_id']}), c)
            params['excludeItems'] = True
            started = h.rpc('session/resume', params)
        else:
            params.update(workspaceRoot=c['workspace'], modelId=native.MODEL, providerId='meta', approvalMode='allowAll')
            started = h.rpc('session/start', params)
        native.check_effective(started, c)
        native.atomic(state / 'native.json', {'session': started['session'], 'server_arguments': h.p.args})
        profile_path = Path(assignment['project_profile']['path'])
        if not profile_path.is_absolute():
            profile_path = Path(assignment['worktree']) / profile_path
        native.atomic(state / 'startup.json', {'native': c['native_session_id'], 'actor': c['agent_id'],
                      'model': native.MODEL, 'permission_mode': 'yolo', 'reasoning_effort': c['reasoning_effort'],
                      'reasoning_effort_source': c['reasoning_effort_source'], 'progress_view': c.get('progress_view', 'live'),
                      'ui_mode': c['ui_mode'], 'ui_mode_source': c['ui_mode_source'],
                      'role': {'path': str(ROOT / 'roles/worker/SKILL.md'), 'sha256': hashlib.sha256((ROOT / 'roles/worker/SKILL.md').read_bytes()).hexdigest()},
                      'profile': {'path': str(profile_path.resolve()), 'sha256': hashlib.sha256(profile_path.read_bytes()).hexdigest()},
                      'lifecycle': {'path': str(ROOT / 'lifecycle-continuity.md'), 'sha256': hashlib.sha256((ROOT / 'lifecycle-continuity.md').read_bytes()).hexdigest()},
                      'assignment_sha256': hashlib.sha256(Path(assignment_path).read_bytes()).hexdigest()})
        continuation = continuation or {}
        receiver = continuation.get('receiver') or Receiver(c, state)
        receiver.c = c
        task_text = continuation.get('text')
        if task_text is None:
            task_text = ('Handle the current durable decision and preserve already completed work. Continue only the current authorized phase.'
                         if continuation.get('receipt') else Path(c['prompt_file']).read_text())
        prompt = 'Exact assignment envelope (scope and identity):\n' + json.dumps(assignment) + '\n' + ('First read the entire Worker role skill at ' + str(ROOT / 'roles/worker/SKILL.md') + ', assigned project profile at ' + str(profile_path.resolve()) + ', and lifecycle contract at ' + str(ROOT / 'lifecycle-continuity.md') + '. Record their verified identities in your startup evidence. All three exact native reads are required before source mutations.\n') + task_text + '\nUse squad_worker run_command/write_file for mutations. For coordination use decision_get({}), claim_read({}), mailbox({}), acknowledge({event_id,note}) and post_message({kind,text}); do not guess Squad CLI arguments. Native shell/write are disabled; approvals remain YOLO. Do not detach processes or spawn subagents. Call squad_worker report with actual completion or blocker evidence, then reply; the supervisor retains task custody until tools are joined. Do not release the primary claim or mark it done inside this run.'
        command = native.uuid7()
        native.atomic(state / 'initial-turn.json', {'command_id': command, 'native': c['native_session_id'],
                      'reasoning_effort': c['reasoning_effort'], 'state': 'prepared'})
        if continuation.get('receipt'):
            receiver.submit(h, continuation['receipt'], prefix=prompt, command=command)
        else:
            submit_text(h, c, command, prompt, continuation)
        native.atomic(state / 'initial-turn.json', {'command_id': command, 'native': c['native_session_id'],
                      'reasoning_effort': c['reasoning_effort'], 'state': 'accepted'})
        if ui:
            ui.action = None
            ui.status('running — Escape interrupts; /exit joins owned tools')
        terminal = drive_turns(h, c, state, assignment, env, pin, receiver, view, ui)
        report_path = state / 'report.json'
        if not ui and terminal == 'completed' and not report_path.exists():
            raise ValidationError('native task ended without a durable report; custody retained for same-session follow-up')
        if not ui and terminal != 'completed':
            raise ValidationError('Muse task did not complete within its bounded run')
        return {'status': json.loads(report_path.read_text())['status'] if report_path.exists() else 'retained',
                'native': c['native_session_id'], 'evidence': str(state), 'terminal': terminal,
                'interactive_action': ui.action if ui else None}
    finally:
        # Local closure prevents admission even if the ledger is temporarily
        # unavailable. Failure to persist suspension never skips owned cleanup.
        cleanup_error = None
        if pinned:
            (state / 'closed').touch()
            try:
                coordination(c, 'suspend')
            except (OSError, ValueError, subprocess.SubprocessError) as error:
                cleanup_error = error
        try:
            if receiver:
                receiver.close()
            h.close()
            operations = json.loads((state / 'operations.json').read_text()) if (state / 'operations.json').exists() else []
            bridge = json.loads((state / 'bridge.json').read_text()) if (state / 'bridge.json').exists() else None
            bridge_live = False
            if bridge:
                deadline = time.monotonic() + 5
                while True:
                    try:
                        os.kill(bridge['pid'], 0)
                        bridge_live = True
                    except ProcessLookupError:
                        bridge_live = False
                        break
                    if time.monotonic() >= deadline:
                        break
                    time.sleep(0.1)
            pending = any(op['state'] not in ('completed', 'joined') for op in operations)
            for op in operations:
                if op.get('kind') == 'container' and 'container' in op:
                    runtime = Runtime(c)
                    if not runtime.joined(op):
                        runtime.stop(op)
                    if not bridge_live and runtime.joined(op):
                        op['state'] = 'joined'
                        native.atomic(state / 'operations.json', operations)
            pending = any(op['state'] not in ('completed', 'joined') for op in operations)
            for op in operations:
                if op.get('kind') == 'container':
                    pending = pending or 'container' not in op or not Runtime(c).joined(op)
            joined = not bridge_live and not pending and h.p.returncode is not None
            native.atomic(state / 'join.json', {'native_pid': h.p.pid, 'native_exit': h.p.returncode,
                          'bridge_live': bridge_live, 'bridge_pid': bridge['pid'] if bridge else None, 'binding': pin if pinned else None, 'operations': operations, 'joined': joined, 'terminal': terminal})
            if pinned and joined and cleanup_error is None:
                report_path = state / 'report.json'
                if terminal == 'completed' and report_path.exists():
                    submitted = publish_report(c, env, pin, state)
                    native.atomic(state / 'outcome.json', submitted)
                coordination(c, 'close', str(state / 'join.json'))
                for op in operations:
                    if op.get('kind') == 'container':
                        Runtime(c).call(['container', 'rm', op['container']])
            elif pinned:
                raise ValidationError('owned tools or ledger closure unverified; closed execution pin retained for reconciliation')
            native.atomic(writer, {'state': 'joined', 'custody': custody, 'assignment': assignment, 'evidence': str(state), 'pid': h.p.pid})
        finally:
            if not owned_lock:
                lock.close()


def interactive_loop(assignment_path, config_path, c, ui, lock, frontend_state, resume=False):
    result = {'status': 'retained', 'native': c['native_session_id']}
    continuation = None
    idle_receiver = None
    try:
        while True:
            allow_text = False
            try:
                result = run_execution(assignment_path, config_path, resume, ui, lock, continuation)
                continuation = None
                if result['interactive_action'] == 'exit':
                    return result
                if result['interactive_action'] == 'resume':
                    resume = True
                    continue
                if result['interactive_action'] == 'reported':
                    allow_text = True
                    assignment = validate_file(assignment_path, ROOT / 'schemas/assignment-envelope.schema.json')
                    pin = dict(reservation=assignment['reservation']['key'], generation=assignment['reservation']['generation'], native=c['native_session_id'])
                    decision = read_decision(c, child_environment(c), pin)
                    ui.status('HOLD — result published; editable draft retained, source turns wait for the controller'
                              if decision and decision['action'] != 'proceed' else
                              'waiting — result published after owned join; Enter continues this same native, /exit exits')
                    idle_receiver = Receiver(dict(c, assignment=assignment), frontend_state)
                else:
                    ui.status('interrupted or failed — task retained; /resume reconnects after verified join, /exit exits')
            except (OSError, ValueError, subprocess.SubprocessError, queue.Empty) as error:
                allow_text = False
                result = {'status': 'retained', 'native': c['native_session_id']}
                if continuation and continuation.get('text') and not continuation.get('accepted'):
                    ui.restore(continuation['text'])
                continuation = None
                ui.status('blocked — ' + diagnostic(str(error)) + '; custody retained, no automatic retry')
            renewed = time.monotonic()
            while True:
                action = None
                try:
                    action = ui.read(False)
                    if action and action[0] == 'exit':
                        return result
                    if action and action[0] == 'resume':
                        if idle_receiver:
                            idle_receiver.close()
                            idle_receiver = None
                        resume = True
                        break
                    if action and action[0] == 'text' and allow_text:
                        assignment = validate_file(assignment_path, ROOT / 'schemas/assignment-envelope.schema.json')
                        pin = dict(reservation=assignment['reservation']['key'], generation=assignment['reservation']['generation'], native=c['native_session_id'])
                        decision = read_decision(c, child_environment(c), pin)
                        if decision and decision['action'] != 'proceed':
                            ui.restore(action[1])
                            ui.status('HOLD — draft retained locally; source turns wait for the controller')
                            continue
                        if idle_receiver:
                            idle_receiver.close()
                            idle_receiver = None
                        continuation = {'text': action[1]}
                        resume = True
                        break
                    if action:
                        if action[0] == 'text':
                            ui.restore(action[1])
                        ui.status('no active execution — /resume reconnects this same native; /exit exits')
                    if idle_receiver:
                        receipt = idle_receiver.pending()
                        if receipt:
                            idle_receiver.close()
                            continuation = {'receipt': receipt, 'receiver': idle_receiver}
                            idle_receiver = None
                            resume = True
                            break
                    if time.monotonic() - renewed >= 20:
                        assignment = validate_file(assignment_path, ROOT / 'schemas/assignment-envelope.schema.json')
                        heartbeat(assignment, c, child_environment(c))
                        renewed = time.monotonic()
                except (OSError, ValueError, subprocess.SubprocessError, queue.Empty) as error:
                    if action and action[0] == 'text':
                        ui.restore(action[1])
                    if idle_receiver:
                        idle_receiver.close()
                        idle_receiver = None
                    allow_text = False
                    renewed = time.monotonic()
                    ui.status('blocked waiting — ' + diagnostic(str(error)) + '; input and custody retained, /resume rechecks ownership')
                time.sleep(0.1)
    finally:
        if idle_receiver:
            idle_receiver.close()


def run(assignment_path, config_path, resume=False):
    c = config_file(config_path)
    if c['ui_mode'] != 'interactive':
        return run_execution(assignment_path, config_path, resume)
    root = Path(c['state_directory'])
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    # The frontend retains the SAME native admission lock even while its host
    # is joined. A bounded launcher or second UI cannot become another writer.
    with (root / (c['native_session_id'] + '.lock')).open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        with TerminalInput() as ui:
            frontend_state = root / ('frontend-' + str(uuid.uuid4()))
            frontend_state.mkdir(mode=0o700)
            native.atomic(frontend_state / 'frontend.json', {'native': c['native_session_id'], 'pid': os.getpid(), 'ui_mode': 'interactive'})
            return interactive_loop(assignment_path, config_path, c, ui, lock, frontend_state, resume)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--assignment', type=Path, required=True)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--check', action='store_true')
    parser.add_argument('--resume', action='store_true')
    args = parser.parse_args()
    try:
        if args.check:
            result = check_launch(validate_file(args.assignment, ROOT / 'schemas/assignment-envelope.schema.json'), args.config, args.resume)
        else:
            result = run(args.assignment, args.config, args.resume)
        print(json.dumps(result), flush=True)
        return 0 if result['status'] in ('ready', 'completed', 'retained') else 1
    except (OSError, ValueError, subprocess.SubprocessError, queue.Empty) as error:
        print(json.dumps({'status': 'blocked', 'reason': diagnostic(str(error))}), file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
