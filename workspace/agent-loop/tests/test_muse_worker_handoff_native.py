"""Opt-in real same-controller source handoff; no business/ENV custody."""
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import unittest
import uuid

from test_muse_worker_native import native_fixture, native, launcher
from muse_worker_tools import child_environment


@unittest.skipUnless(os.environ.get('MUSE_WORKER_QUALIFICATION'), 'explicit isolated native Worker qualification config required')
class MuseHandoffNativeTests(unittest.TestCase):
    def test_joined_native_consent_replacement_hold_and_resume(self):
        selected = json.loads(Path(os.environ['MUSE_WORKER_QUALIFICATION']).read_text())
        retained = os.environ.get('MUSE_HANDOFF_FIXTURE')
        if retained:
            root = Path(retained).resolve()
            if not root.is_relative_to(Path(selected['state_directory']).resolve()):
                self.fail('retained qualification fixture must belong to this owned evidence directory')
            c = json.loads((root / 'config.json').read_text())
            a = json.loads((root / 'assignment.json').read_text())
            work, home = Path(c['workspace']), Path(c['coordination_home'])
            def squad(actor, *args):
                env = child_environment(dict(c, agent_id=actor, native_session_id=c['controller_native'] if actor == c['dispatcher_agent_id'] else c['native_session_id']))
                return subprocess.check_output([c['coordination_executable'], *args], cwd=c['ledger_directory'], env=env, text=True, stderr=subprocess.PIPE, timeout=15).strip()
        else:
            root, work, home, c, a, squad = native_fixture(selected)
        controller = c['dispatcher_agent_id']
        database = home / 'global.db'
        with sqlite3.connect(database) as db:
            previous_receiver = db.execute('SELECT incarnation,owner_pid FROM dispatch_controller_receivers WHERE actor=?', (controller,)).fetchone()
        if previous_receiver:
            with self.assertRaises(ProcessLookupError, msg='previous fixture receiver owner must be joined'):
                os.kill(previous_receiver[1], 0)
            squad(controller, 'dispatch', 'receiver-release', '--native-session', c['controller_native'], '--epoch', '1', '--incarnation', previous_receiver[0])
        receiver_incarnation = 'fixture-controller-' + str(uuid.uuid4())
        receiver_args = ['--native-session', c['controller_native'], '--epoch', '1', '--incarnation', receiver_incarnation, '--owner-pid', str(os.getpid())]
        squad(controller, 'dispatch', 'receiver-bind', *receiver_args, '--wake-kind', 'asyncRewake')
        self.addCleanup(squad, controller, 'dispatch', 'receiver-release', '--native-session', c['controller_native'], '--epoch', '1', '--incarnation', receiver_incarnation)

        def rows(query, *args):
            with sqlite3.connect(database) as db:
                db.row_factory = sqlite3.Row
                return [dict(row) for row in db.execute(query, args)]

        def decide(revision, action):
            squad(controller, 'milestone', '--to', 'BUG-001', 'Isolated native handoff: ' + action)
            outcome = rows("SELECT max(id) AS id FROM messages WHERE agent_id=? AND thread='BUG-001'", controller)[0]['id']
            args = ['terminal-events', 'decision-set', '--reservation', 'QUALIFY-MUSE', '--generation', str(a['reservation']['generation']),
                    '--worker-session', c['native_session_id'], '--expected-revision', str(revision), '--outcome', str(outcome), '--action', action]
            if action == 'hold':
                args += ['--condition', 'isolated source replacement; preserve hold until qualified receiver handles it']
            squad(controller, *args)

        def handle_results():
            for event in rows("SELECT event_id FROM terminal_event_receipts WHERE recipient=? AND processed_at=0", controller):
                incarnation = receiver_incarnation
                receipt = json.loads(squad(controller, 'terminal-events', 'listen', '--native-session', c['controller_native'],
                                          '--delivery-session', incarnation, '--defer-delivery', '--max', '1s'))
                self.assertIn(event['event_id'], [x['event_id'] for x in receipt['events']])
                squad(controller, 'terminal-events', 'delivered', event['event_id'], '--native-session', c['controller_native'], '--delivery-session', incarnation)
                squad(controller, 'terminal-events', 'ack', event['event_id'], '--native-session', c['controller_native'],
                      '--note', 'Fixture owner inspected actual native report, owned joins and preserved source; result handled')

        Path(c['prompt_file']).write_text('Isolated source handoff fixture only. Use write_file to create proof.py containing def add(a,b): return a+b. '
            'Run python3 -c \'from proof import add; assert add(2,3)==5; print("SOURCE_OK")\' with run_command. Report actual completed results and end the turn. '
            'Do not guess event IDs. The supervisor delivers pending decisions in a follow-up turn after this turn ends. No external operations.')
        if retained:
            writer = json.loads((Path(c['state_directory']) / (c['native_session_id'] + '.writer.json')).read_text())
            self.assertEqual(writer['state'], 'joined')
            state = Path(writer['evidence'])
            self.assertTrue(json.loads((state / 'join.json').read_text())['joined'])
            first = dict(status=json.loads((state / 'report.json').read_text())['status'], native=c['native_session_id'], evidence=str(state))
        else:
            first = launcher.run(root / 'assignment.json', root / 'config.json')
        self.assertEqual(first['status'], 'completed')
        handle_results()
        writer = Path(c['state_directory']) / (c['native_session_id'] + '.writer.json')
        frozen = root / 'original-joined-writer.json'
        frozen.write_bytes(writer.read_bytes())
        join = json.loads((Path(first['evidence']) / 'join.json').read_text())
        decide(1, 'hold')
        claim = rows("SELECT item_id AS item,agent_id AS actor,generation,claimed_at,last_touch FROM claims WHERE item_id='BUG-001'")[0]
        expected = json.loads(squad(controller, 'dispatch', 'list', '--json'))
        if isinstance(expected, dict):
            expected = expected.get('reservations', expected.get('items'))
        new_actor, new_native = 'qualification-replacement', native.uuid7()
        request = dict(request_id='native-source-handoff', controller=dict(actor=controller, native_session=c['controller_native'], epoch=1),
                       expected=expected[0], claim=claim, execution_id=join['binding']['id'], new_actor=new_actor, new_native=new_native,
                       decision_revision=2, consent_outcome_id=0, custody_evidence='Isolated preserved source, joined original native and tools; retained actual fixture receipts')
        path = root / 'handoff.json'
        native.atomic(path, request)
        consent = json.loads(squad(controller, 'dispatch', 'worker-handoff-digest', '--request', str(path)))
        Path(c['prompt_file']).write_text('The current fixture is on hold. Do not mutate source or run commands. Read decision_get. '
            'Authorize exactly the prepared isolated source handoff by calling post_message with kind stuck and text exactly this JSON: ' + json.dumps(consent) +
            '. Use report with status blocked and the actual preserved source/hold evidence, then end the turn. Handle exact delivered decision receipts; never guess IDs.')
        stopped = launcher.run(root / 'assignment.json', root / 'config.json', resume=True)
        self.assertEqual(stopped['status'], 'blocked')
        handle_results()
        consent_rows = rows("SELECT id,body FROM messages WHERE agent_id=? AND thread='BUG-001' ORDER BY id DESC", c['agent_id'])
        matching = [x for x in consent_rows if x['body'].strip() == json.dumps(consent) or x['body'].strip() == json.dumps(consent, separators=(',', ':'))]
        if not matching:
            matching = [x for x in consent_rows if x['body'].startswith('{') and json.loads(x['body']) == consent]
        self.assertEqual(len(matching), 1, 'original native did not publish the exact consent')
        request['consent_outcome_id'] = matching[0]['id']
        native.atomic(path, request)
        original_native = c['native_session_id']
        c['native_session_id'] = new_native
        squad(new_actor, 'register', '--as', new_actor)
        receipt = json.loads(squad(controller, 'dispatch', 'worker-handoff', '--request', str(path)))
        c.update(agent_id=new_actor, claim_generation=receipt['claim_generation'],
                 handoff=dict(request_id=request['request_id'], prior_writer=str(frozen), prior_writer_sha256=hashlib.sha256(frozen.read_bytes()).hexdigest()))
        a['reservation']['generation'] = receipt['reservation']['generation']
        native.atomic(root / 'config.json', c)
        native.atomic(root / 'assignment.json', a)
        preserved = (work / 'proof.py').read_bytes()
        Path(c['prompt_file']).write_text('Fresh native replacement in an isolated source handoff. Preserve proof.py exactly. '
            'Read decision_get and handle the actual delivered hold. Do not mutate source or run commands while held. '
            'Report blocked with verified preserved source and hold evidence; end the turn. Never guess event IDs.')
        replacement = launcher.run(root / 'assignment.json', root / 'config.json')
        self.assertEqual(replacement['status'], 'blocked')
        self.assertNotEqual(replacement['native'], original_native)
        self.assertEqual((work / 'proof.py').read_bytes(), preserved)
        self.assertGreater(rows("SELECT processed_at FROM terminal_event_receipts WHERE recipient=? AND kind='decision-resolved'", new_actor)[0]['processed_at'], 0)
        handle_results()
        decide(2, 'proceed')
        c.pop('handoff')
        native.atomic(root / 'config.json', c)
        Path(c['prompt_file']).write_text('Same isolated replacement native, now eligible to proceed. Preserve proof.py. '
            'Run python3 -c \'from proof import add; assert add(2,3)==5; print("REPLACEMENT_OK")\' with run_command. '
            'Report actual completed verification, then end the turn. Handle only exact delivered event IDs; no external operations or claim release.')
        resumed = launcher.run(root / 'assignment.json', root / 'config.json', resume=True)
        self.assertEqual(resumed['status'], 'completed')
        self.assertEqual(resumed['native'], new_native)
        handle_results()
        self.assertEqual(rows("SELECT count(*) AS count FROM execution_authorizations WHERE state='active'")[0]['count'], 0)
        squad(new_actor, 'release', 'BUG-001')
        native.atomic(root / 'handoff-qualification.json', dict(new=first, consent=stopped, replacement_hold=replacement,
                      replacement_resume=resumed, receipt=receipt, preserved_source_sha256=hashlib.sha256(preserved).hexdigest(),
                      actual_original_consent=True, actual_replacement_handling=True, joined=True))
        print('Native source handoff proof retained at ' + str(root), flush=True)
