"""Exact reviewed owned-stdio contract; no runtime discovery or allow-list fallback."""
import hashlib
import json
import os
import stat
from pathlib import Path
from validate_context_package import ValidationError, ROOT, validate

VERSION = 'codex-cli 0.159.0-alpha.12.1'
BINARY_SHA = '1180e2d56ea06ec583092acd933345685da3441cb1769a436d76dbf320613e75'
PROOF_SHA = '8ec9ce77c196d727045d2f90282169efe09cea657069eedc9d633e6afa41fc01'
SCHEMA_SHA = 'f36612f15cae53393ede193e0c65295b4d6b797a14bc2d10c06aeb047b3082ae'
SCHEMA_COUNT = 440
AGGREGATE_SHA = '7243ba241962af92ca60581f1a81808ebda4212a800f8b205f54703bcfd508c5'
V2_SHA = 'e77b7d1436a78f431a74b2cb263a862e92ae40d70411bc63835b47ab2168827c'
SELECTION = dict(model='gpt-6.1-sol', modelProvider='openai', reasoningEffort='medium',
                 serviceTier='priority', approvalPolicy='never', approvalsReviewer='user',
                 sandbox=dict(type='readOnly', networkAccess=False))
LIMIT = 8 * 1024 * 1024


def bounded(path):
    with Path(path).open('rb') as stream:
        raw = stream.read(LIMIT + 1)
    if len(raw) > LIMIT:
        raise ValidationError('stdio contract evidence exceeds bound')
    return raw


def file_sha(path):
    return hashlib.sha256(bounded(path)).hexdigest()


def executable_identity(path):
    """Fingerprint an opened regular executable without running caller code."""
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or not info.st_mode & 0o111 or info.st_size > 256 * 1024 * 1024:
            raise ValidationError('stdio executable type or size is unqualified')
        sha = hashlib.sha256()
        size = 0
        while block := stream.read(1024 * 1024):
            size += len(block)
            if size > 256 * 1024 * 1024:
                raise ValidationError('stdio executable exceeds bound')
            sha.update(block)
    return {'version': VERSION, 'executable_sha256': sha.hexdigest()}


def validate_contract(c, binary):
    """Historical transport qualification only. Caller still needs live owner readiness."""
    validate(c, json.loads((ROOT / 'schemas/codex-owned-stdio.schema.json').read_text()))
    expected = dict(client='cli', transport='owned-stdio', model='gpt-6.1-sol', provider='openai',
                    effort='medium', service_tier='priority', sandbox='read-only',
                    approval_policy='never', approvals_reviewer='user')
    if any(c.get(k) != v for k, v in expected.items()):
        raise ValidationError('stdio transport or selected policy is not qualified')
    if binary != {'version': VERSION, 'executable_sha256': BINARY_SHA}:
        raise ValidationError('stdio executable identity is not registered')
    if c.get('qualification_sha256') != PROOF_SHA:
        raise ValidationError('reviewed immutable stdio proof pin required')
    raw = bounded(c['qualification_file'])
    if hashlib.sha256(raw).hexdigest() != PROOF_SHA:
        raise ValidationError('stdio qualification proof changed')
    proof = json.loads(raw)
    protocol = proof.get('protocol', {})
    if (proof.get('schema_version') != 'squad.current-cli-transport-qualification.v1'
            or proof.get('binary', {}).get('version') != VERSION
            or proof.get('binary', {}).get('sha256') != BINARY_SHA
            or proof.get('actual_selection') != SELECTION
            or protocol.get('generated_schema_files') != 440
            or protocol.get('aggregate_schema_sha256') != AGGREGATE_SHA
            or protocol.get('v2_schema_sha256') != V2_SHA):
        raise ValidationError('stdio proof contract identity mismatch')
    root = Path(c['protocol_directory']).resolve(strict=True)
    files = sorted(root.rglob('*.json'))
    if len(files) != SCHEMA_COUNT:
        raise ValidationError('stdio protocol inventory incomplete')
    inventory = [{'path': str(f.relative_to(root)), 'sha256': file_sha(f)} for f in files]
    inventory_sha = hashlib.sha256(json.dumps(inventory, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    if inventory_sha != SCHEMA_SHA:
        raise ValidationError('stdio generated schema identity changed')
    associations = proof.get('immutable_associations', {})
    names = [Path(path).name for path in associations]
    if len(associations) != 8 or len(set(names)) != 8:
        raise ValidationError('stdio original qualification evidence incomplete')
    relocated = Path(c['evidence_directory']).resolve(strict=True) if c.get('evidence_directory') else None
    if any(file_sha(relocated / Path(path).name if relocated else path) != sha for path, sha in associations.items()):
        raise ValidationError('stdio original qualification evidence changed')
    thread = proof.get('probe_thread')
    intent = proof.get('queue_intent', {})
    queued = proof.get('queue_acceptance', {}).get('queuedSubmission', {})
    turn = proof.get('queued_turn', {})
    user = turn.get('user_item', {})
    content = [{'type': 'text', 'text': intent.get('literal'), 'text_elements': []}]
    if (not thread or intent.get('threadId') != thread or not intent.get('clientUserMessageId')
            or queued.get('clientUserMessageId') != intent['clientUserMessageId'] or not queued.get('id')
            or queued.get('input') != content or user.get('type') != 'userMessage'
            or user.get('clientId') != intent['clientUserMessageId'] or user.get('content') != content
            or not turn.get('turn_id') or turn['turn_id'] == proof.get('source_turn', {}).get('turn_id')
            or thread not in proof.get('loaded_owner', {}).get('data', [])
            or proof.get('loaded_owner', {}).get('nextCursor')
            or proof.get('before_queue_idle') != {'type': 'idle'}
            or proof.get('after_queue_idle') != {'type': 'idle'}
            or [proof.get(k) for k in ('actual_inference_turns','actual_queue_submissions','actual_turn_start_requests')] != [2,1,1]):
        raise ValidationError('stdio concrete acceptance/history association unavailable')
    cleanup = proof.get('cleanup', {})
    if (any(cleanup.get(k) is not True for k in ('all_hosts_waited_exit0', 'all_readers_joined',
                                                'both_native_turns_completed', 'own_thread_archived'))
            or cleanup.get('owned_process_groups_remaining') != []
            or not 0 < cleanup.get('total_from_original_start_seconds', 0) < 180):
        raise ValidationError('stdio original host/turn joins incomplete')
    hosts = proof.get('server_incarnations', [])
    if not hosts:
        raise ValidationError('stdio original host proof missing')
    expected_turns = [{'id': proof['source_turn']['turn_id'], 'status': 'completed'},
                      {'id': turn['turn_id'], 'status': 'completed'}]
    if hosts[-1].get('cleanup', {}).get('terminal_turns_joined') != expected_turns:
        raise ValidationError('stdio exact original terminal turn association missing')
    for host in hosts:
        join = host.get('cleanup', {})
        if (not host.get('incarnation') or not host.get('start') or not host.get('pid')
                or join.get('server_exit') != 0 or join.get('server_waited') is not True
                or join.get('stdout_reader_joined') is not True or join.get('stderr_reader_joined') is not True):
            raise ValidationError('stdio original child join unavailable')
    return dict(binary, transport='owned-stdio', contract_sha256=PROOF_SHA,
                schema_sha256=inventory_sha, live_owner_verified=False)
