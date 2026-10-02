"""Crash-durable native client custody; unresolved launch never permits reuse."""
import json
import os
from pathlib import Path
from validate_context_package import ValidationError


def writer_path(c):
    return Path(c['state_directory']) / (c['native_session_id'] + '.writer.json')


def check_writer_fence(c):
    path = writer_path(c)
    if not path.exists():
        return
    info = path.lstat()
    if path.is_symlink() or not path.is_file() or info.st_size > 65536 or info.st_mode & 0o077:
        raise ValidationError('native writer provenance unavailable; no replacement admitted')
    receipt = json.loads(path.read_text())
    if (receipt.get('schema_version') != 'agent-loop.native-writer.v1'
            or receipt.get('native') != c['native_session_id'] or receipt.get('actor') != c['agent_id']
            or receipt.get('state') not in ('joined', 'not-started')):
        raise ValidationError('original native writer is unjoined; reconcile its actual client/helper and external operations before reuse')
    if receipt['state'] == 'not-started':
        return
    for key in ('writer_pid', 'helper_pid'):
        pid = receipt.get(key)
        if not isinstance(pid, int) or pid <= 0:
            raise ValidationError('original joined process provenance unavailable')
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            continue
        raise ValidationError('original native client/helper is live or absence unverified')


def writer_intent(c):
    check_writer_fence(c)
    from codex_worker_launcher import save
    receipt = dict(schema_version='agent-loop.native-writer.v1', native=c['native_session_id'],
                   actor=c['agent_id'], state='launch-intent')
    save(writer_path(c), receipt)  # Before OS spawn: the unknown PID window fails closed.
    return receipt


def writer_record(c, receipt, **fields):
    from codex_worker_launcher import save
    receipt.update(fields)
    save(writer_path(c), receipt)
