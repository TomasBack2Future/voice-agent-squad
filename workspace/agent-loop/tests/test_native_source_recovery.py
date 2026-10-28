"""Continue an owned joined failure; full native qualification owns initial Dispatcher handling."""
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import time
import unittest

import sys
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from codex_source_worker import prepare, run
from muse_session_host import atomic
from muse_worker_tools import child_environment


@unittest.skipUnless(os.environ.get('SOURCE_HANDOFF_QUALIFICATION'), 'explicit isolated cross-runtime qualification required')
class NativeSourceRecoveryTests(unittest.TestCase):
    def test_failed_codex_transfers_to_muse_without_final_old_model_reply(self):
        selected = json.loads(Path(os.environ['SOURCE_HANDOFF_QUALIFICATION']).read_text())
        retained = os.environ.get('SOURCE_HANDOFF_FIXTURE')
        if not retained:
            self.skipTest('fresh failure qualification is owned by test_native_failure_dispatch_recovery.py')
        root = Path(retained).resolve(strict=True)
        self.assertTrue(root.is_relative_to(Path(selected['codex']['state_directory']).resolve()))
        config = json.loads((root / 'config.json').read_text())
        assignment = json.loads((root / 'assignment.json').read_text())
        work, home = Path(config['workspace']), Path(config['coordination_home'])
        def squad(actor, *args):
            env = child_environment(dict(config, agent_id=actor,
                native_session_id=config['controller_native'] if actor == config['dispatcher_agent_id'] else config['native_session_id']))
            return subprocess.check_output([config['coordination_executable'], *args], cwd=config['ledger_directory'],
                env=env, text=True, stderr=subprocess.PIPE, timeout=15)
        database = home / 'global.db'
        def rows(sql, *args):
            with sqlite3.connect(database) as db:
                db.row_factory = sqlite3.Row
                return [dict(row) for row in db.execute(sql, args)]
        writer_path = Path(config['state_directory']) / (config['native_session_id'] + '.writer.json')
        writer = json.loads(writer_path.read_text()); self.assertEqual(writer['state'], 'joined')
        state = Path(writer['evidence']); join = json.loads((state / 'join.json').read_text())
        self.assertTrue(join['joined']); self.assertFalse((state / 'report.json').exists())
        self.assertNotEqual(join['native_exit'], 0)
        preserved = (work / 'retained.py').read_bytes()
        self.assertEqual(preserved.strip(), b'value = 11')
        frozen = root / 'failed-original-writer.json'; frozen.write_bytes(writer_path.read_bytes())
        successor = dict(config, **selected['muse'])
        successor.pop('native_session_id', None)
        successor.update(agent_id='qualification-muse-replacement', native_state=str(root / 'muse-native'),
                         state_directory=str(root / 'muse-execution'), prompt_file=str(root / 'muse-prompt.txt'))
        successor_path = root / 'muse-config.json'
        if successor_path.exists(): successor = json.loads(successor_path.read_text())
        else: atomic(successor_path, successor); prepare(successor_path); successor = json.loads(successor_path.read_text())
        controller = config['dispatcher_agent_id']
        squad(successor['agent_id'], 'register', '--as', successor['agent_id'])
        pending = rows("SELECT event_id,recipient,worker_session,kind,processed_at FROM terminal_event_receipts WHERE generation=1 AND processed_at=0 ORDER BY event_id")
        self.assertTrue(pending)
        self.assertTrue(all(row['recipient'] == config['agent_id'] and row['kind'] == 'decision-resolved' for row in pending))
        authority = Path(selected['authority_file']).resolve(strict=True)
        request_path = root / 'source-recovery-request.json'
        if request_path.exists():
            request = json.loads(request_path.read_text())
        else:
            expected = json.loads(squad(controller, 'dispatch', 'list', '--json'))
            if isinstance(expected, dict): expected = expected.get('reservations', expected.get('items'))
            claim = rows("SELECT item_id AS item,agent_id AS actor,generation,claimed_at,last_touch FROM claims WHERE item_id='BUG-001'")[0]
            request = dict(request_id='codex-muse-failure-recovery', controller=dict(actor=controller,
                native_session=config['controller_native'], epoch=config['controller_epoch']), expected=expected[0], claim=claim,
                execution_id=join['binding']['id'], new_actor=successor['agent_id'], new_native=successor['native_session_id'],
                decision_revision=2, consent_outcome_id=0,
                custody_evidence='Isolated actual interrupted model and source tools joined; original report absent; retained source SHA256 ' + hashlib.sha256(preserved).hexdigest() + '; evidence ' + str(state),
                source_recovery=dict(human_authority_sha256=hashlib.sha256(authority.read_bytes()).hexdigest(),
                    expires_at=int(time.time()) + 1800, pending_event_ids=[row['event_id'] for row in pending], attestation_outcome_id=0))
            atomic(request_path, request)
            digest = json.loads(squad(controller, 'dispatch', 'worker-handoff-digest', '--request', str(request_path)))['request_sha256']
            proof = dict(schema_version='squad.source-worker-recovery-attestation.v1', request_sha256=digest,
                         human_authority_sha256=request['source_recovery']['human_authority_sha256'], expires_at=request['source_recovery']['expires_at'])
            body = json.dumps(proof, sort_keys=True)
            squad(controller, 'say', '--to', 'BUG-001', body)
            request['source_recovery']['attestation_outcome_id'] = rows("SELECT id FROM messages WHERE agent_id=? AND thread='BUG-001' AND body=?", controller, body)[0]['id']
            atomic(request_path, request)
        receipt = json.loads(squad(controller, 'dispatch', 'worker-handoff', '--request', str(request_path)))
        self.assertEqual(receipt['previous_actor'], config['agent_id'])
        self.assertEqual(receipt['reservation']['generation'], 2)
        self.assertEqual(rows("SELECT event_id,recipient,worker_session,kind,processed_at FROM terminal_event_receipts WHERE generation=1 AND processed_at=0 ORDER BY event_id"), pending)
        successor.update(claim_generation=receipt['claim_generation'], handoff=dict(request_id=request['request_id'],
            prior_writer=str(frozen), prior_writer_sha256=hashlib.sha256(frozen.read_bytes()).hexdigest()))
        replacement_assignment = dict(assignment, reservation=dict(assignment['reservation'], generation=2))
        atomic(successor_path, successor); atomic(root / 'muse-assignment.json', replacement_assignment)
        Path(successor['prompt_file']).write_text('You are a fresh Muse replacement for this same isolated source assignment. '
            'The old Codex model was stopped and cannot reply. Read startup references, events and decision_get. '
            'Read retained.py and preserve it exactly. Handle and acknowledge the actual transferred HOLD event. '
            'No source writes or commands while held. Report blocked with actual preserved source and HOLD evidence.')
        held = run(root / 'muse-assignment.json', successor_path)
        self.assertEqual(held['status'], 'blocked'); self.assertEqual((work / 'retained.py').read_bytes(), preserved)
        self.assertTrue(all(row['processed_at'] > 0 for row in rows("SELECT processed_at FROM terminal_event_receipts WHERE recipient=? AND kind='decision-resolved'", successor['agent_id'])))
        squad(controller, 'milestone', '--to', 'BUG-001', 'Owned isolated replacement HOLD handled; release source test only')
        outcome = rows("SELECT max(id) AS id FROM messages WHERE agent_id=? AND thread='BUG-001'", controller)[0]['id']
        squad(controller, 'terminal-events', 'decision-set', '--reservation', assignment['reservation']['key'], '--generation', '2',
            '--worker-session', successor['native_session_id'], '--expected-revision', '2', '--outcome', str(outcome), '--action', 'proceed')
        successor.pop('handoff'); atomic(successor_path, successor)
        Path(successor['prompt_file']).write_text('Same Muse replacement, preserve retained.py. Read events and current decision, '
            'acknowledge the actual proceed event. Run python3 -c \'from retained import value; assert value == 11; print("CROSS_RUNTIME_RECOVERY_OK")\' '
            'with run_command and report actual verification. Do not rerun interrupted sleep or release custody.')
        completed = run(root / 'muse-assignment.json', successor_path, resume=True)
        self.assertEqual(completed['status'], 'completed'); self.assertEqual((work / 'retained.py').read_bytes(), preserved)
        self.assertEqual(rows("SELECT count(*) AS n FROM execution_authorizations WHERE state='active'")[0]['n'], 0)
        self.assertEqual(rows("SELECT event_id,recipient,worker_session,kind,processed_at FROM terminal_event_receipts WHERE generation=1 AND processed_at=0 ORDER BY event_id"), pending)
        atomic(root / 'cross-runtime-recovery.json', {'status': 'passed', 'failed_native':config['native_session_id'],
            'failed_execution':str(state), 'old_model_final_reply':False, 'old_receipts_unmodified':pending,
            'authority_file':str(authority), 'receipt':receipt, 'replacement_hold':held, 'replacement_completed':completed,
            'retained_source_sha256':hashlib.sha256(preserved).hexdigest()})
        print(json.dumps({'evidence':str(root / 'cross-runtime-recovery.json'), 'status':'passed'}), flush=True)


if __name__ == '__main__': unittest.main()
