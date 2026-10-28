"""Publish an owned supervisor's joined-source failure through the existing event contract."""
import json
import os
from pathlib import Path
import re

from muse_session_host import atomic
from muse_worker_container import Runtime
from muse_worker_launcher import read_decision, submit_outcome
from validate_context_package import ValidationError

TOKEN = re.compile(r'[A-Za-z0-9_.-]{1,128}\Z')
CLASSES = ('exhausted', 'connection', 'auth', 'quota', 'config', 'unknown')


def absent(pid):
    if not isinstance(pid, int) or isinstance(pid, bool) or pid < 1:
        raise ValidationError('source failure process identity unavailable')
    try: os.kill(pid, 0)
    except ProcessLookupError: return
    raise ValidationError('source failure process remains live')


def publish_failure(config, env, pin, state, error_class='unknown'):
    state = Path(state)
    join = json.loads((state / 'join.json').read_text())
    native = json.loads((Path(config['native_state']) / 'native-join.json').read_text())
    terminal = join.get('terminal') or {}
    if (join.get('joined') is not True or join.get('binding') != pin or join.get('native_pid') != pin['client_pid']
            or not isinstance(join.get('native_exit'), int) or isinstance(join.get('native_exit'), bool)
            or join.get('bridge_live') is not False or native.get('joined') is not True
            or native.get('pid') != pin['client_pid'] or native.get('exit_code') != join['native_exit']
            or (terminal.get('status') == 'completed' and (state / 'report.json').exists())):
        raise ValidationError('source failure requires its exact unsuccessful native/tool join')
    absent(pin['client_pid'])
    if join.get('bridge_pid') is not None: absent(join['bridge_pid'])
    for operation in join['operations']:
        if operation.get('state') not in ('completed', 'joined'):
            raise ValidationError('source failure tool custody remains unresolved')
        if operation.get('pid'): absent(operation['pid'])
        if operation.get('kind') == 'container':
            runtime = Runtime(config)
            if 'container' in operation:
                if not runtime.joined(operation): raise ValidationError('source failure container remains live')
            elif (operation.get('not_started') is not True or operation.get('create_refusal') != 'invalid-bind-mount'
                  or not re.fullmatch(r'[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}', operation.get('id', ''))
                  or operation.get('name') != 'squad-muse-' + operation['id'] or runtime.call(['container', 'ls', '--all', '--format', '{{.Names}}',
                                                              '--filter', 'name=' + operation['name']]).strip()):
                raise ValidationError('source failure container allocation remains unresolved')
    provider = config.get('provider', config['runtime'])
    turn = terminal.get('id', 'unresolved')
    if (error_class not in CLASSES or not isinstance(pin['id'], str) or not isinstance(turn, str) or not TOKEN.fullmatch(pin['id']) or len(pin['id']) > 120
            or not TOKEN.fullmatch(turn) or not isinstance(provider, str)
            or not re.fullmatch(r'[A-Za-z0-9_.-]{1,64}', provider)):
        raise ValidationError('bounded typed source failure pointers required')
    body = f"runtime-failure ep-1 {error_class} turn={turn} request={pin['id']} attempt=0 provider={provider}"
    key = 'failure-' + pin['id']
    intent_path = state / 'failure-intent.json'
    if intent_path.exists():
        intent = json.loads(intent_path.read_text())
        if intent.get('binding') != pin or intent.get('body') != body or intent.get('request_key') != key:
            raise ValidationError('source failure publication input changed')
    else:
        decision = read_decision(config, env, pin)
        intent = {'binding': pin, 'body': body, 'request_key': key,
                  'decision_revision': decision['revision'] if decision else None}
        atomic(intent_path, intent)
    body_file = state / 'failure-body.txt'; body_file.write_text(intent['body'])
    receipt = submit_outcome(config, env, pin, 'runtime-failure', body_file,
                             decision_revision=intent['decision_revision'], request_key=intent['request_key'])
    atomic(state / 'failure-outcome.json', receipt)
    return receipt
