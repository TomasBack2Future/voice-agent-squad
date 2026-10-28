"""Opt-in real native Dispatcher/Worker cross-product; never touches a live ledger."""
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import threading
import time
import unittest

ROOT = Path(__file__).parents[1]
sys.path.insert(0, str(ROOT))
from codex_source_worker import prepare as prepare_worker, run as run_worker
from codex_dispatcher_session import prepare as prepare_dispatcher, run as run_dispatcher
from muse_session_host import atomic
from muse_worker_tools import child_environment
from test_codex_source_native import fixture


def call(config, actor, native, *args):
    env = child_environment(dict(config, agent_id=actor, native_session_id=native))
    result = subprocess.run([config['coordination_executable'], *args], cwd=config['ledger_directory'],
                            env=env, capture_output=True, text=True, timeout=15)
    if result.returncode:
        raise AssertionError('isolated Squad operation rejected: ' + result.stderr[:2000])
    try: return json.loads(result.stdout) if result.stdout.strip() else None
    except json.JSONDecodeError: return result.stdout


def qualify_pair(worker_selected, dispatcher_selected, reused=None):
    if reused:
        root = Path(reused).resolve(strict=True)
        worker = json.loads((root / 'config.json').read_text())
        assignment = json.loads((root / 'assignment.json').read_text())
        home = Path(worker['coordination_home'])
    else:
        root, _, home, worker, assignment, _, _ = fixture(worker_selected)
        Path(worker['prompt_file']).write_text('Create first.py containing value = 11 with write_file; run '
            'python3 -c \'from first import value; assert value==11; print("NATIVE_FIRST_OK")\' with run_command. '
            'Record the actual test and report completed. No external operations.')
        result = run_worker(root / 'assignment.json', root / 'config.json')
        if result['status'] != 'completed': raise AssertionError('first real source result failed')
    state = root / ('dispatcher-' + dispatcher_selected['runtime'])
    prepared = root / 'actual-dispatcher'
    if reused and dispatcher_selected['runtime'] == 'codex' and (prepared / 'config.json').is_file():
        state = prepared
    state.mkdir(mode=0o700, exist_ok=True)
    dispatcher = dict(dispatcher_selected, role='dispatcher', workspace=worker['workspace'],
        native_state=str(state / 'native'), state_directory=str(state / 'state'),
        coordination_executable=worker_selected['coordination_executable'], ledger_directory=worker['ledger_directory'],
        coordination_home=worker['coordination_home'], agent_id='native-dispatcher', controller_epoch=2,
        prompt_file=str(state / 'prompt.txt'))
    dispatcher.pop('tool_runtime', None)
    if not (state / 'config.json').is_file():
        atomic(state / 'config.json', dispatcher)
        prepare_dispatcher(state / 'config.json')
    dispatcher = json.loads((state / 'config.json').read_text())
    old = worker['dispatcher_agent_id']; old_native = worker['controller_native']
    def old_call(*args): return call(worker, old, old_native, *args)
    current = call(dispatcher, dispatcher['agent_id'], dispatcher['native_session_id'],
                   'dispatch', 'controller-status', '--actor', dispatcher['agent_id']) if (state / 'handoff.json').exists() else None
    if current != {'actor': dispatcher['agent_id'], 'native_session': dispatcher['native_session_id'], 'epoch': 2}:
        old_call('register', '--as', dispatcher['agent_id'])
        rows = old_call('dispatch', 'list', '--json')
        if isinstance(rows, dict): rows = rows['reservations']
        handoff = dict(request_id='native-dispatcher-handoff', expected_epoch=1, old_native=old_native,
                       new_actor=dispatcher['agent_id'], new_native=dispatcher['native_session_id'], reservations=rows)
        atomic(state / 'handoff.json', handoff)
        old_call('dispatch', 'handoff', '--request', str(state / 'handoff.json'))
    def control(*args): return call(dispatcher, dispatcher['agent_id'], dispatcher['native_session_id'], *args)
    # The successor is prepared, but only the real Dispatcher model may reserve and bind it.
    successor = dict(worker)
    successor.update({key: worker_selected[key] for key in ('runtime','model','effort','provider',
        'approval_policy','sandbox','permission_mode','contributor_mode') if key in worker_selected})
    for key in ('native_session_id',): successor.pop(key, None)
    successor.update(agent_id='next-worker', dispatcher_agent_id=dispatcher['agent_id'],
        controller_native=dispatcher['native_session_id'], controller_epoch=2,
        coordination_executable=worker_selected['coordination_executable'], native_state=str(root / 'next-native'),
        state_directory=str(root / 'next-execution'), prompt_file=str(root / 'next-prompt.txt'))
    # A different isolated source directory preserves one-writer ownership per item.
    next_work = root / 'next-work'
    if not next_work.exists():
        subprocess.run(['git', '-c', 'core.hooksPath=/dev/null', 'clone', '--local', worker['workspace'], str(next_work)],
                       capture_output=True, check=True)
    subprocess.run(['git', '-C', str(next_work), 'remote', 'set-url', 'origin',
                    'git@github.com:TomasBack2Future/voice-agent-squad.git'], check=True)
    successor['workspace'] = str(next_work)
    if not (root / 'next-config.json').exists() or not json.loads((root / 'next-config.json').read_text()).get('native_session_id'):
        atomic(root / 'next-config.json', successor)
        prepare_worker(root / 'next-config.json')
    successor = json.loads((root / 'next-config.json').read_text())
    control('new', 'BUG', 'Second isolated native source task', '--ready')
    next_assignment = dict(assignment, assignment_id='matrix/next/1', item='BUG-002',
        issue='TomasBack2Future/voice-agent-squad#79',
        reservation={'key': 'NATIVE-NEXT', 'generation': 1}, worktree=str(next_work))
    atomic(root / 'next-assignment.json', next_assignment)
    Path(successor['prompt_file']).write_text('Create next.py containing value = 23 with write_file. Run '
        'python3 -c \'from next import value; assert value==23; print("NATIVE_NEXT_OK")\' with run_command, '
        'and report actual successful test. No external operations.')
    Path(dispatcher['prompt_file']).write_text('You are the only Dispatcher in an isolated qualification ledger. '
        'For every received outcome, use squad_get_item and squad_dispatch_list to reconcile its exact item and reservation, '
        'then record transport delivered and ACK the reconciled event. When handling BUG-001 only: reserve NATIVE-NEXT '
        'with source github:TomasBack2Future/voice-agent-squad#79, attach BUG-002 generation1, and bind worker_session '
        + successor['native_session_id'] + ' using squad_dispatch_bind. This is the authorized independent next task. '
        'On BUG-002 outcome only verify its item and binding and ACK. Do not release claims, close Issues, alter decisions, '
        'implement source or use any external system. Never use supervised bind. The persistent receiver must stay ready.')
    result = {}; errors = []
    def dispatch():
        try: result['dispatcher'] = run_dispatcher(state / 'config.json', batches=2, timeout=900)
        except BaseException as error: errors.append(repr(error))
    thread = threading.Thread(target=dispatch); thread.start()
    try:
        deadline = time.monotonic() + 600
        bound = False
        while time.monotonic() < deadline and thread.is_alive():
            rows = control('dispatch', 'list', '--json')
            if isinstance(rows, dict): rows = rows['reservations']
            bound = any(row.get('reservation_key') == 'NATIVE-NEXT' and row.get('worker_thread_id') == successor['native_session_id']
                        and row.get('state') == 'dispatched' for row in rows)
            if bound: break
            time.sleep(.25)
        if not bound: raise AssertionError('real Dispatcher did not bind the second Worker: ' + repr(errors))
        call(successor, successor['agent_id'], successor['native_session_id'], 'register', '--as', successor['agent_id'])
        call(successor, successor['agent_id'], successor['native_session_id'], 'claim', 'BUG-002', '--long')
        result['worker'] = run_worker(root / 'next-assignment.json', root / 'next-config.json')
    finally:
        thread.join(timeout=910)
    if thread.is_alive() or errors: raise AssertionError('native Dispatcher did not join: ' + repr(errors))
    if result['worker']['status'] != 'completed': raise AssertionError('second source result not complete')
    with sqlite3.connect(home / 'global.db') as db:
        receipts = db.execute("SELECT event_id,delivered_at,processed_at FROM terminal_event_receipts WHERE recipient=?",
                              (dispatcher['agent_id'],)).fetchall()
        if len(receipts) < 2 or any(not delivered or not handled for _, delivered, handled in receipts):
            raise AssertionError('real native handling receipts missing: ' + repr(receipts))
        if db.execute("SELECT count(*) FROM execution_authorizations WHERE state='active'").fetchone()[0]:
            raise AssertionError('source execution custody remains active')
    proof = dict(status='passed', dispatcher_runtime=dispatcher['runtime'], worker_runtime=worker['runtime'],
        dispatcher_native=dispatcher['native_session_id'], first_worker_native=worker['native_session_id'],
        next_worker_native=successor['native_session_id'], results=result, receipts=receipts,
        real_model_reservation_and_binding=True, same_receiver_during_handling=True)
    atomic(root / 'matrix-proof.json', proof)
    return root, proof


@unittest.skipUnless(os.environ.get('NATIVE_MATRIX_QUALIFICATION'), 'explicit real native matrix selection required')
class NativeMatrixTests(unittest.TestCase):
    def test_selected_native_pairs(self):
        selection = json.loads(Path(os.environ['NATIVE_MATRIX_QUALIFICATION']).read_text())
        for dispatcher, worker in selection['pairs']:
            with self.subTest(dispatcher=dispatcher, worker=worker):
                root, _ = qualify_pair(selection['runtimes'][worker], selection['runtimes'][dispatcher],
                                       selection.get('reuse', {}).get(dispatcher + '/' + worker))
                print(json.dumps({'pair': [dispatcher, worker], 'evidence': str(root)}), flush=True)


if __name__ == '__main__': unittest.main()
