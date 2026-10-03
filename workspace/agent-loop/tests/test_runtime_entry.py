import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).parents[1]
sys.path.insert(0, str(ROOT))
import runtime_entry as entry


class RuntimeEntryTests(unittest.TestCase):
    def test_all_clients_get_own_identity_without_inherited_codex_identity(self):
        for runtime in ('muse', 'claude', 'codex'):
            with self.subTest(runtime=runtime), patch.dict(os.environ, {
                    'CODEX_THREAD_ID': 'parent', 'MUSE_SESSION_ID': 'parent',
                    'CLAUDE_SESSION_ID': 'parent', 'SQUAD_AGENT': 'parent'}):
                env = entry.environment(runtime, 'child', 'worker', '/ledger')
                self.assertEqual(env['SQUAD_AGENT'], 'worker')
                self.assertTrue(env['SQUAD_SESSION_ID'].startswith(runtime + ':child:'))
                for key in ('CODEX_THREAD_ID', 'MUSE_SESSION_ID', 'CLAUDE_SESSION_ID'):
                    self.assertNotIn(key, env)

    def test_unknown_runtime_and_missing_identity_rejected(self):
        for args in [('unknown', 'child', 'worker'), ('muse', '', 'worker'), ('codex', 'child', '')]:
            with self.assertRaises(ValueError):
                entry.environment(*args, '/ledger')

    def test_native_entry_routes_exact_argv_and_ledger_without_wrapper(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); (root / '.squad').mkdir()
            binary = root / 'squad'
            binary.write_text('#!' + sys.executable + '\nimport json,os,sys\n'
                              'print(json.dumps([sys.argv[1:],os.getcwd(),os.environ["SQUAD_SESSION_ID"]]))\n')
            binary.chmod(0o700)
            command = [sys.executable, str(ROOT / 'runtime_entry.py'), '--runtime', 'muse',
                       '--native-session', 'child', '--agent', 'worker', '--ledger', str(root),
                       '--squad', str(binary), 'exec', '--', 'dispatch', 'list', '--json']
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            args, cwd, session = json.loads(result.stdout)
            self.assertEqual(args, ['dispatch', 'list', '--json'])
            self.assertEqual(Path(cwd).resolve(), root.resolve())
            self.assertTrue(session.startswith('muse:child:'))

    def test_capabilities_never_claim_binary_presence_is_qualification(self):
        for runtime in ('claude', 'codex', 'muse'):
            result = entry.capabilities(runtime)
            self.assertFalse(result['native_qualified'])
            self.assertEqual(result['managed_worker']['execution_fence'],
                             'custody-pin-and-contained-tools' if runtime == 'muse' else 'unavailable')
            self.assertEqual(result['events']['handling_ack'], 'explicit-after-handling')

    def test_muse_worker_routes_to_custody_launcher_and_preserves_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory) / 'launch.json'
            config.write_text('{}')
            argv = ['runtime_entry.py', '--runtime', 'muse', 'worker',
                    '--assignment', '/assignment.json', '--config', str(config), '--check']
            with patch.object(sys, 'argv', argv), patch.object(entry.subprocess, 'run') as run:
                run.return_value.returncode = 17
                self.assertEqual(entry.main(), 17)
                self.assertEqual(run.call_args.args[0], [sys.executable,
                    str(ROOT / 'muse_worker_launcher.py'), '--assignment', '/assignment.json',
                    '--config', str(config), '--check'])
            capability = entry.capabilities('muse')
            self.assertEqual(capability['managed_worker']['admission'], 'source-test-handoff-only')
            self.assertFalse(capability['native_qualified'])

    def test_handled_receipt_cannot_ack_other_native_or_delivery_or_duplicate(self):
        receipt = {'schema_version': 'squad.handled-events.v1', 'runtime': 'codex',
                   'native_session': 'native', 'agent': 'actor', 'delivery_session': 'delivery',
                   'handled': [{'event_id': 'event42', 'note': 'decision/2'}]}
        delivery = {'type': 'worker-terminal-delivery-v1', 'delivery_session': 'delivery',
                    'recipient': 'actor', 'events': [{'event_id': 'event42'}]}
        self.assertEqual(entry.handled_events(receipt, delivery, 'codex', 'native', 'actor'), receipt['handled'])
        for change in ({'native_session': 'foreign'}, {'agent': 'foreign'},
                       {'delivery_session': 'old'}, {'runtime': 'muse'},
                       {'handled': [{'event_id': 'event43', 'note': 'not-delivered'}]},
                       {'handled': [{'event_id': 'event42', 'note': ''}]},
                       {'handled': receipt['handled'] * 2}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                entry.handled_events(dict(receipt, **change), delivery, 'codex', 'native', 'actor')

    def test_handled_records_delivery_then_ack_and_stops_on_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); (root / '.squad').mkdir()
            script = root / 'squad'
            log = root / 'calls.jsonl'
            script.write_text('#!' + sys.executable + '\nimport json,sys,pathlib\n'
                              + f'p=pathlib.Path({str(log)!r})\n'
                              + 'with p.open("a") as f:f.write(json.dumps(sys.argv[1:])+"\\n")\n'
                              + 'sys.exit(7 if sys.argv[2]=="ack" else 0)\n')
            script.chmod(0o700)
            delivery = root / 'delivery.json'; receipt = root / 'handled.json'
            delivery.write_text(json.dumps({'type': 'worker-terminal-delivery-v1', 'recipient': 'actor',
                'delivery_session': 'incarnation', 'events': [{'event_id': 'one'}, {'event_id': 'two'}]}))
            receipt.write_text(json.dumps({'schema_version': 'squad.handled-events.v1',
                'runtime': 'muse', 'native_session': 'native', 'agent': 'actor',
                'delivery_session': 'incarnation', 'handled': [{'event_id': 'one', 'note': 'checked/1'},
                                                               {'event_id': 'two', 'note': 'checked/2'}]}))
            args = [sys.executable, str(ROOT / 'runtime_entry.py'), '--runtime', 'muse',
                '--native-session', 'native', '--agent', 'actor', '--ledger', str(root), '--squad', str(script),
                'handled', '--delivery', str(delivery), '--receipt', str(receipt)]
            result = subprocess.run(args, capture_output=True, text=True)
            self.assertEqual(result.returncode, 7, result.stderr)
            calls = [json.loads(line) for line in log.read_text().splitlines()]
            self.assertEqual([call[1:3] for call in calls], [['delivered', 'one'], ['ack', 'one']])
            self.assertTrue(all('--native-session' in call for call in calls))

    def test_partial_batch_replay_advances_past_already_processed_event(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); (root / '.squad').mkdir()
            script = root / 'squad'
            script.write_text('#!' + sys.executable + '\n' + '''import json,sys,pathlib
p=pathlib.Path.cwd()/'state.json'
s=json.loads(p.read_text()) if p.exists() else {'delivered':[], 'processed':[], 'failed':False}
op,event=sys.argv[2:4]; code=0
if op=='ack':
 if event not in s['delivered']: code=7
 elif event not in s['processed']: s['processed'].append(event)
elif op=='delivered':
 if event in s['processed']: code=8
 elif event=='two' and not s['failed']: s['failed']=True;code=9
 elif event not in s['delivered']:s['delivered'].append(event)
p.write_text(json.dumps(s));sys.exit(code)
''')
            script.chmod(0o700)
            delivery = root / 'delivery.json'; receipt = root / 'handled.json'
            delivery.write_text(json.dumps({'type': 'worker-terminal-delivery-v1', 'recipient': 'actor',
                'delivery_session': 'incarnation', 'events': [{'event_id': 'one'}, {'event_id': 'two'}]}))
            receipt.write_text(json.dumps({'schema_version': 'squad.handled-events.v1',
                'runtime': 'muse', 'native_session': 'native', 'agent': 'actor',
                'delivery_session': 'incarnation', 'handled': [{'event_id': 'one', 'note': 'checked/1'},
                                                               {'event_id': 'two', 'note': 'checked/2'}]}))
            args = [sys.executable, str(ROOT / 'runtime_entry.py'), '--runtime', 'muse',
                '--native-session', 'native', '--agent', 'actor', '--ledger', str(root), '--squad', str(script),
                'handled', '--delivery', str(delivery), '--receipt', str(receipt)]
            self.assertEqual(subprocess.run(args, capture_output=True).returncode, 9)
            self.assertEqual(json.loads((root / 'state.json').read_text())['processed'], ['one'])
            self.assertEqual(subprocess.run(args, capture_output=True).returncode, 0)
            self.assertEqual(json.loads((root / 'state.json').read_text())['processed'], ['one', 'two'])
