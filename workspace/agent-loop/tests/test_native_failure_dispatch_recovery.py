"""Real native failure publication, Dispatcher handling and cross-runtime source recovery."""
import json
import os
from pathlib import Path
import sqlite3
import sys
import threading
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parents[1]))
from codex_native_host import NativeCLI
from codex_source_worker import run as run_worker
from codex_dispatcher_session import prepare as prepare_dispatcher, run as run_dispatcher
from muse_session_host import atomic
from test_codex_source_native import fixture
from test_native_dispatch_matrix import call
import test_native_source_recovery as source_recovery_fixture
from validate_context_package import ValidationError


@unittest.skipUnless(os.environ.get('SOURCE_HANDOFF_QUALIFICATION'), 'explicit isolated cross-runtime qualification required')
class FailureDispatcherRecoveryTests(unittest.TestCase):
    def test_actual_dispatcher_handles_joined_failure_before_muse_recovery(self):
        selected_path = Path(os.environ['SOURCE_HANDOFF_QUALIFICATION'])
        selected = json.loads(selected_path.read_text())
        retained = os.environ.get('FAILURE_DISPATCH_FIXTURE')
        if retained:
            root = Path(retained).resolve(strict=True)
            self.assertTrue(root.is_relative_to(Path(selected['codex']['state_directory']).resolve()))
            worker = json.loads((root / 'config.json').read_text()); assignment = json.loads((root / 'assignment.json').read_text())
            work, home = Path(worker['workspace']), Path(worker['coordination_home'])
            for writer in Path(worker['state_directory']).glob('*.writer.json'):
                self.assertEqual(json.loads(writer.read_text())['state'], 'joined', 'original source custody unresolved')
        else:
            root, work, home, worker, assignment, _, _ = fixture(selected['codex'])
        state = root / 'failure-dispatcher'; state.mkdir(exist_ok=True)
        existing = state / 'config.json'
        if existing.exists() and json.loads(existing.read_text()).get('native_session_id'):
            dispatcher = json.loads(existing.read_text())
        else:
            dispatcher = dict(worker, role='dispatcher', workspace=worker['workspace'],
                native_state=str(state / 'native'), state_directory=str(state / 'state'),
                ledger_directory=worker['ledger_directory'], coordination_home=worker['coordination_home'],
                agent_id='actual-failure-dispatcher', controller_epoch=2, prompt_file=str(state / 'prompt.txt'))
            dispatcher.pop('native_session_id', None); dispatcher.pop('tool_runtime', None)
            atomic(state / 'config.json', dispatcher); prepare_dispatcher(state / 'config.json')
            dispatcher = json.loads((state / 'config.json').read_text())
        if worker['dispatcher_agent_id'] != dispatcher['agent_id']:
            old_actor, old_native = worker['dispatcher_agent_id'], worker['controller_native']
            def old_call(*args): return call(worker, old_actor, old_native, *args)
            old_call('register', '--as', dispatcher['agent_id'])
            reservations = old_call('dispatch', 'list', '--json')
            if isinstance(reservations, dict): reservations = reservations['reservations']
            handoff = dict(request_id='before-source-controller-handoff', expected_epoch=1, old_native=old_native,
                new_actor=dispatcher['agent_id'], new_native=dispatcher['native_session_id'], reservations=reservations)
            atomic(state / 'handoff.json', handoff); old_call('dispatch', 'handoff', '--request', str(state / 'handoff.json'))
            worker.update(dispatcher_agent_id=dispatcher['agent_id'], controller_native=dispatcher['native_session_id'], controller_epoch=2)
            atomic(root / 'config.json', worker)
        Path(dispatcher['prompt_file']).write_text('You are the actual only Dispatcher of an isolated source qualification ledger. '
            'The supervised source runtime publishes runtime-failure only after its exact native and owned tools join. '
            'For received runtime-failure, inspect BUG-001 using squad_get_item and squad_dispatch_list; inspect its canonical thread if needed. '
            'It is an unknown stopped native failure; do not retry or complete its work. Your actual native_session is '
            + dispatcher['native_session_id'] + ' and actor is ' + dispatcher['agent_id'] + '. Use that native_session with the exact received delivery_session. '
            'Record delivered for the exact event. Post a controller-authored milestone on BUG-001 using squad_say with verb milestone, '
            'explaining the joined unknown failure and retained source. Use its actual returned canonical message ID for the HOLD outcome_id. '
            'Set HOLD through squad_terminal_decision_set using reservation QUALIFY-CODEX generation1, worker_session '
            + worker['native_session_id'] + ', expected_revision1 and the controller milestone outcome_id, '
            'action hold and condition "Joined native failure; source custody retained for authorized recovery". '
            'Read back the current decision with squad_terminal_decision_get. Then ACK the actual runtime-failure event, '
            'explaining that its unknown failure is held and source custody retained. No source changes, replacement, claim release or external operations.')
        result = {}; failures = []; done = threading.Event(); children = []
        def dispatch():
            try: result['failure_dispatcher'] = run_dispatcher(state / 'config.json', batches=1, timeout=600)
            except BaseException as error: failures.append(repr(error))
        dispatcher_thread = threading.Thread(target=dispatch); dispatcher_thread.start()
        observer = None
        if not list(Path(worker['state_directory']).glob('*.writer.json')):
            Path(worker['prompt_file']).write_text('Read startup references, events and decision_get, acknowledge the actual proceed event. '
                'Create retained.py containing value = 11 with write_file. Run exactly sleep 60 with run_command and timeout_seconds120. '
                'An isolated fixture will stop your owned model during this command. No background or external work.')
            original_turn = NativeCLI.turn
            def capture(host, *args, **kwargs):
                value = original_turn(host, *args, **kwargs)
                if host.config['role'] == 'worker': children.append(host.child)
                return value
            def interrupt():
                try:
                    deadline = time.monotonic() + 180
                    while not done.is_set() and time.monotonic() < deadline:
                        for path in Path(worker['state_directory']).glob('*/operations.json'):
                            if children and any(op['state'] == 'running' for op in json.loads(path.read_text())):
                                children[0].terminate(); return
                        time.sleep(.1)
                    raise AssertionError('owned source container did not run')
                except BaseException as error: failures.append(repr(error))
            observer = threading.Thread(target=interrupt); observer.start()
            try:
                with patch('codex_native_host.NativeCLI.turn', side_effect=capture, autospec=True):
                    with self.assertRaises(ValidationError): run_worker(root / 'assignment.json', root / 'config.json')
            finally: done.set(); observer.join(timeout=5)
        while dispatcher_thread.is_alive(): dispatcher_thread.join(timeout=1)
        self.assertEqual(failures, [])
        if observer: self.assertFalse(observer.is_alive())
        with sqlite3.connect(home / 'global.db') as db:
            failure = db.execute("SELECT event_id,outcome_id,delivered_at,processed_at FROM terminal_event_receipts WHERE kind='runtime-failure'").fetchone()
            self.assertIsNotNone(failure); self.assertGreater(failure[2], 0); self.assertGreater(failure[3], 0)
            self.assertEqual(db.execute("SELECT action,revision FROM dispatch_decisions WHERE generation=1").fetchone(), ('hold',2))
            self.assertEqual(db.execute("SELECT count(*) FROM execution_authorizations WHERE state='active'").fetchone()[0], 0)
        atomic(root / 'failure-dispatcher-proof.json', {'runtime_failure_receipt':failure, 'dispatcher':result['failure_dispatcher'],
            'status':'passed', 'controller_handoff_before_source':True})
        atomic(root / 'failure-phase.json', {'status':'joined-failed', 'native':worker['native_session_id']})
        print('Actual failure Dispatcher proof: ' + str(root), flush=True)
        with patch.dict(os.environ, {'SOURCE_HANDOFF_FIXTURE':str(root)}):
            source_recovery_fixture.NativeSourceRecoveryTests('test_failed_codex_transfers_to_muse_without_final_old_model_reply').test_failed_codex_transfers_to_muse_without_final_old_model_reply()
        Path(dispatcher['prompt_file']).write_text('Reconcile the received source replacement outcomes in this isolated ledger. '
            'Read BUG-001 and dispatch list; verify same canonical source item now generation2 and its replacement binding. '
            'The earlier blocked outcome reflects transferred HOLD; the later completed outcome verifies retained.py after proceed. '
            'Record delivered and ACK each actually reconciled outcome. Do not mutate decisions, claims, source or external systems.')
        result['replacement_dispatcher'] = run_dispatcher(state / 'config.json', batches=1, timeout=600)
        with sqlite3.connect(home / 'global.db') as db:
            receipts = db.execute("SELECT event_id,delivered_at,processed_at FROM terminal_event_receipts WHERE recipient=?", (dispatcher['agent_id'],)).fetchall()
        self.assertTrue(all(row[1] and row[2] for row in receipts), receipts)
        atomic(root / 'native-failure-dispatch-recovery.json', {'status':'passed', 'failure_receipt':failure,
            'dispatcher_native':dispatcher['native_session_id'], 'dispatcher_results':result,
            'source_recovery':json.loads((root / 'cross-runtime-recovery.json').read_text()), 'handled_receipts':receipts})
        print(json.dumps({'evidence':str(root / 'native-failure-dispatch-recovery.json'), 'status':'passed'}), flush=True)


if __name__ == '__main__': unittest.main()
