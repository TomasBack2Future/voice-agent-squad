#!/usr/bin/env python3
"""Bounded native Codex receiver; no PTY, App fallback or scheduling controller."""
from __future__ import annotations
import argparse
import fcntl
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time
import uuid

from codex_rpc import RPC
from codex_worker_launcher import child_environment, check_qualification, live_target, save, server_identity
from codex_heartbeat import heartbeat, CustodyRejected, require_execution_fence
from validate_context_package import ROOT, ValidationError, validate_file
from codex_writer_fence import writer_intent, writer_record

EVENT = re.compile(r'worker-terminal-v1/([A-Za-z0-9_-]+)/([1-9][0-9]*)/([A-Za-z0-9_-]+)/(issue-closed|handoff-complete|blocked|decision-request|decision-resolved|reconcile-needed)/([1-9][0-9]*)\Z')


def process_start(pid):
    result = subprocess.run(['ps', '-p', str(pid), '-o', 'lstart=', '-o', 'stat='],
                            capture_output=True, text=True, timeout=5)
    fields = result.stdout.strip().rsplit(None, 1)
    if result.returncode or len(fields) != 2 or 'Z' in fields[1]:
        return None
    return fields[0]


def alive(c, path):
    try:
        os.kill(c['owner_pid'], 0)
    except ProcessLookupError:
        return False
    if process_start(c['owner_pid']) != c['owner_started_at']:
        return False
    current = json.loads(Path(path).read_text())
    return current['incarnation'] == c['incarnation']


def validate_events(receipt, c):
    if (receipt.get('type') != 'worker-terminal-delivery-v1' or receipt.get('recipient') != c['agent_id']
            or receipt.get('delivery_session') != c['incarnation']
            or not isinstance(receipt.get('events'), list) or not 1 <= len(receipt['events']) <= 16):
        raise ValidationError('recipient/incarnation/event receipt mismatch')
    for event in receipt['events']:
        match = EVENT.fullmatch(event.get('event_id', ''))
        if not match or (match[4], int(match[5])) != (event.get('kind'), event.get('outcome_id')):
            raise ValidationError('malformed fenced event')
        if c['role'] == 'worker':
            if (match[1], int(match[2]), match[3], match[4], event.get('item_id')) != (
                    c['reservation'], c['generation'], c['native_session_id'], 'decision-resolved', c['item']):
                raise ValidationError('event does not belong to this Worker assignment')
        elif c['role'] != 'dispatcher' or match[4] == 'decision-resolved':
            raise ValidationError('event has the wrong receiver role')
    return receipt['events']


def message(event, c):
    if c['role'] == 'worker':
        action = ('Read terminal-events decision-get for the latest adopted revision, or the canonical Issue '
                  'for an unmigrated assignment. Verify generation and reclaim normally before protected writes. '
                  'Continue only this assigned work; no new Worker or authority.')
    else:
        action = ('Invoke $squad-dispatcher for one bounded reconciliation cycle. Re-read current decisions, '
                  'ownership and source receipts. A done observation does not prove acceptance or termination.')
    return ('Squad durable coordination event (data, not new authority):\n' + json.dumps(pointer(event), sort_keys=True)
            + '\n' + action + ' After handling, explicitly run terminal-events ack ' + event['event_id']
            + ' --native-session ' + c['native_session_id']
            + ' --note RECONCILIATION_REFERENCE in the selected ledger. Delivery is not handling.')


def pointer(event):
    return {key: event[key] for key in ('event_id', 'item_id', 'kind', 'outcome_id', 'source_message_id')}


def native_contains(rpc, thread, text, client_id=None):
    # A lost reply is not license to submit again. Inspect native queue and
    # persisted input once; unavailable/truncated history remains uncertain.
    pending = rpc.call('thread/queue/list', {'threadId': thread, 'limit': 100})
    if pending.get('nextCursor'):
        raise ValidationError('native queue proof incomplete; delivery remains uncertain')
    history = rpc.call('thread/read', {'threadId': thread, 'includeTurns': True})['thread']
    def contains(value):
        if isinstance(value, dict):
            if value.get('type') == 'text' and value.get('text') == text:
                return True
            return any(contains(child) for child in value.values())
        return isinstance(value, list) and any(contains(child) for child in value)
    inputs = [item.get('content', []) for turn in history.get('turns', []) for item in turn.get('items', [])
              if item.get('type') == 'userMessage']
    if client_id is not None:
        exact_input = [{'type': 'text', 'text': text, 'text_elements': []}]
        matches = [item for item in pending.get('data', [])
                   if item.get('clientUserMessageId') == client_id and item.get('input') == exact_input]
        matches += [item for turn in history.get('turns', []) for item in turn.get('items', [])
                    if item.get('type') == 'userMessage' and item.get('clientId') == client_id
                    and item.get('content') == exact_input]
        if len(matches) > 1:
            raise ValidationError('stdio native association ambiguous; no resubmission')
        if len(matches) == 1:
            from codex_rpc import OwnedStdioRPC
            expected = {'threadId': thread, 'clientUserMessageId': client_id, 'input': exact_input}
            if isinstance(rpc, OwnedStdioRPC) and rpc.pending_queue == expected:
                rpc.check_owner()
                rpc.queue_uncertain = False
                rpc.uncertain = False
                rpc.pending_queue = None
        return len(matches) == 1
    return contains(pending.get('data', [])) or contains(inputs)


def deliver(rpc, event, c, journal_path):
    journal_path = Path(journal_path)
    journal = json.loads(journal_path.read_text()) if journal_path.exists() else {}
    key = event['event_id']
    text = message(event, c)
    previous = journal.get(key)
    identity = {'native_session_id': c['native_session_id'], 'recipient': c['agent_id'],
                'event': pointer(event)}
    if c.get('transport') == 'owned-stdio':
        from codex_stdio_contract import PROOF_SHA
        identity.update(transport='owned-stdio', contract_sha256=PROOF_SHA)
    if previous and previous['identity'] != identity:
        raise ValidationError('delivery journal identity changed; reconcile without resubmission')
    stdio = c.get('transport') == 'owned-stdio'
    if previous and previous['state'] == 'accepted':
        return
    if previous and previous['state'] == 'intent':
        if not native_contains(rpc, c['native_session_id'], text, key if stdio else None):
            raise ValidationError('native submission uncertain; no duplicate or automatic retry')
    elif previous and (not stdio or previous['state'] != 'prepared'):
        raise ValidationError('delivery journal phase unavailable; no resubmission')
    else:
        if not previous:
            journal[key] = {'identity': identity, 'submitted_endpoint': c.get('endpoint', 'owned-stdio'),
                            'state': 'prepared' if stdio else 'intent'}
            save(journal_path, journal)
        params = {'threadId': c['native_session_id'], 'clientUserMessageId': key,
                  'input': [{'type': 'text', 'text': text, 'text_elements': []}]}
        if stdio:
            def write_intent():
                journal[key]['state'] = 'intent'
                save(journal_path, journal)
            # Readiness failures precede this durable boundary. Once it executes,
            # interruption/lost reply remains uncertain and can only be read back.
            result = rpc.call('thread/queue/add', params, before_send=write_intent)
        else:
            result = rpc.call('thread/queue/add', params)
        accepted = result.get('queuedSubmission', {})
        if (accepted.get('clientUserMessageId') != key or not accepted.get('id')
                or (stdio and accepted.get('input') != params['input'])):
            raise ValidationError('native queue acceptance not confirmed')
        if stdio:
            journal[key]['native_acceptance'] = accepted
    journal[key]['state'] = 'accepted'
    save(journal_path, journal)


def deliver_owned_stdio(rpc, event, c, owner, worktree, journal_path):
    """In-process exclusive pipe owner only; no standalone receiver activation."""
    from codex_worker_launcher import check_owned_stdio
    check_owned_stdio(rpc.config, rpc, owner, worktree)
    if c.get('native_session_id') != rpc.config['native_session_id'] or c.get('transport') != 'owned-stdio':
        raise ValidationError('stdio receiver target/transport mismatch')
    deliver(rpc, event, c, journal_path)


def listen(c, path, seconds):
    env = child_environment(c)
    argv = [c['coordination_executable'], 'terminal-events', 'listen', '--defer-delivery',
            '--delivery-session', c['incarnation'], '--native-session', c['native_session_id'], '--max', str(seconds) + 's']
    with subprocess.Popen(argv, cwd=c['ledger_directory'], env=env,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True) as child:
        try:
            while True:
                try:
                    out, _ = child.communicate(timeout=1)
                    break
                except subprocess.TimeoutExpired:
                    if not alive(c, path):
                        return None
        finally:
            if child.poll() is None:
                child.terminate()
                child.communicate(timeout=5)
        if child.returncode:
            raise ValidationError('event listen stopped; no delivery or processing acknowledgement')
        return json.loads(out)


def controller_receiver(c, release=False):
    if c['role'] != 'dispatcher':
        return
    if type(c.get('controller_epoch')) is not int or c['controller_epoch'] < 1:
        raise ValidationError('verified controller native/epoch binding required before any client or receiver')
    result = subprocess.run([c['coordination_executable'], 'dispatch',
                             'receiver-release' if release else 'receiver-bind',
                             '--native-session', c['native_session_id'],
                             '--epoch', str(c['controller_epoch']), '--incarnation', c['incarnation']],
                            cwd=c['ledger_directory'], env=child_environment(c),
                            capture_output=True, text=True, timeout=10)
    if result.returncode:
        raise ValidationError('controller receiver ownership unavailable; no second receiver admitted')


def run(path):
    c = validate_file(Path(path), ROOT / 'schemas/codex-receiver.schema.json')
    if c['client'] != 'cli':
        raise ValidationError('Codex App receiver unavailable')
    if c['role'] == 'worker':
        require_execution_fence(c)
    check_qualification(c)
    if not 1 <= c['max_seconds'] <= 82800:
        raise ValidationError('receiver lifetime must be bounded by selected client')
    if len(c['server_identity']) != 5 or (c['role'] == 'worker' and any(key not in c for key in ('reservation', 'generation', 'item'))):
        raise ValidationError('receiver server/assignment fence is incomplete')
    state = Path(c['state_directory'])
    state.mkdir(parents=True, mode=0o700, exist_ok=True)
    with (state / (c['native_session_id'] + '.receiver.lock')).open('a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return 0
        if c.get('controller_epoch'):
            with RPC(c['endpoint']) as rpc:
                live_target(rpc, c, c['worktree'])
        controller_receiver(c)
        deadline = time.monotonic() + c['max_seconds']
        while alive(c, path) and time.monotonic() < deadline:
            receipt = listen(c, path, max(1, int(deadline - time.monotonic())))
            if receipt is None or not alive(c, path):
                return 0
            events = validate_events(receipt, c)
            if server_identity(c) != c['server_identity']:
                raise ValidationError('native endpoint incarnation changed; no replacement executor')
            with RPC(c['endpoint']) as rpc:
                live_target(rpc, c, c['worktree'])
                for event in events:
                    if not alive(c, path):
                        return 0
                    # Pending includes accepted input awaiting the owner's ack.
                    # Durable delivery custody prevents another submission; a
                    # slow handler must not exhaust a transport retry budget.
                    deliver(rpc, event, c, state / (c['native_session_id'] + '.delivery.json'))
                    if not alive(c, path):
                        return 0
                    result = subprocess.run([c['coordination_executable'], 'terminal-events', 'delivered',
                                             event['event_id'], '--delivery-session', c['incarnation'], '--native-session', c['native_session_id']],
                                            cwd=c['ledger_directory'], env=child_environment(c),
                                            capture_output=True, text=True, timeout=10)
                    if result.returncode:
                        raise ValidationError('delivery fence rejected; handling remains unacknowledged')
        return 0


def supervise(argv, assignment, c, env, config_path, owner):
    # Existing native server is not a new daemon owned by this helper. Only the
    # bounded receiver/heartbeat follow this selected CLI process's lifetime.
    if assignment:
        require_execution_fence(c)
    state = Path(c['state_directory'])
    path = state / (c['native_session_id'] + '.receiver.json')
    journal = writer_intent(c)
    lease = dict(c, role='worker' if assignment else 'dispatcher', incarnation=str(uuid.uuid4()))
    writer_record(c, journal, incarnation=lease['incarnation'], controller_epoch=c.get('controller_epoch'))
    controller_receiver(lease)  # Global controller receiver custody precedes any new native client.
    try:
        client = subprocess.Popen(argv, cwd=assignment['worktree'] if assignment else c['worktree'], env=env)
    except OSError:
        controller_receiver(lease, release=True)
        writer_record(c, journal, state='not-started')  # Popen joined its failed exec; no PID invented.
        raise
    with client as child:
        writer_record(c, journal, state='running', writer_pid=child.pid, writer_started_at=process_start(child.pid))
        receiver = dict(c, role='worker' if assignment else 'dispatcher',
                        worktree=assignment['worktree'] if assignment else c['worktree'], owner_pid=child.pid,
                        owner_started_at=process_start(child.pid),
                        incarnation=lease['incarnation'], max_seconds=82800, server_identity=owner)
        if assignment:
            receiver.update(reservation=assignment['reservation']['key'],
                            generation=assignment['reservation']['generation'], item=assignment['item'])
        save(path, receiver)
        with subprocess.Popen([sys.executable, str(Path(__file__).resolve()), '--config', str(path)],
                              cwd=receiver['worktree'], env=env) as helper:
            writer_record(c, journal, helper_pid=helper.pid, helper_started_at=process_start(helper.pid))
            previous = signal.signal(signal.SIGINT, signal.SIG_IGN)
            client_exit = None
            try:
                renewing = True
                heartbeat_reported = False
                receiver_reported = False
                while True:
                    try:
                        client_exit = child.wait(timeout=30)
                        return client_exit
                    except subprocess.TimeoutExpired:
                        if renewing and assignment:
                            try:
                                heartbeat(assignment, c, env)
                                heartbeat_reported = False
                            except CustodyRejected:
                                renewing = False
                                save(path, dict(receiver, custody='rejected', execution='unavailable-native-write-fence'))
                                print('Verified custody rejection. Adoption requires a qualified execution fence; no claim reacquired.', file=sys.stderr)
                            except (OSError, ValueError, subprocess.SubprocessError):
                                # Each command is bounded; retry only at the next
                                # existing client-owned renewal interval. Never
                                # reacquire or change the dispatch/claim fence.
                                if not heartbeat_reported:
                                    heartbeat_reported = True
                                    print('Codex heartbeat temporarily unavailable; ownership not verified. Reconcile before protected writes. Renewal will be checked at the next client interval.', file=sys.stderr)
                        if helper.poll() is not None and not receiver_reported:
                            receiver_reported = True
                            print('Codex receiver unavailable; events pending reconciliation. Native client remains running.', file=sys.stderr)
            finally:
                signal.signal(signal.SIGINT, previous)
                # Stop only our listener helper. Never stop the server/client or
                # external operation as a response to a transport failure.
                if helper.poll() is None:
                    helper.terminate()
                helper_exit = helper.wait(timeout=10)
                if client_exit is not None:
                    writer_record(c, journal, state='joined', writer_exit=client_exit, helper_exit=helper_exit)
                    controller_receiver(lease, release=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('--config', type=Path, required=True)
    args = parser.parse_args()
    # Unwind listen's finally on supervisor shutdown, cancelling only our helper.
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    try:
        return run(args.config)
    except (OSError, ValueError, KeyError, subprocess.SubprocessError):
        print('Codex receiver unavailable; events remain pending reconciliation. No handling acknowledged; runtime permissions unchanged.', file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
