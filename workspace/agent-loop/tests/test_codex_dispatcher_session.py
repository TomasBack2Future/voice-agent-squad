import json
import os
from pathlib import Path
import sqlite3
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parents[1]))
import codex_dispatcher_session as dispatcher
import test_terminal_receiver_ledger as fixtures


class DispatcherSessionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        fixtures.ReceiverLedgerTests.setUpClass()

    @classmethod
    def tearDownClass(cls):
        fixtures.ReceiverLedgerTests.tearDownClass()

    def setUp(self):
        self.fixture = fixtures.ReceiverLedgerTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.config = dict(self.fixture.c, runtime='codex',
                           coordination_executable=str(self.fixture.binary),
                           coordination_home=str(self.fixture.root / 'home'),
                           state_directory=str(self.fixture.root / 'stream'))
        self.state = Path(self.config['state_directory']); self.state.mkdir()

    def test_persistent_receiver_admits_next_worker_before_first_event_ack(self):
        f = self.fixture
        settings = f.root / '.squad/config.yaml'
        settings.write_text(settings.read_text().replace('default_worktree_per_claim: true',
                                                        'default_worktree_per_claim: false'))
        receiver = dispatcher.Receiver(self.config, self.state)
        self.addCleanup(receiver.close)
        receiver.start()
        pid = receiver.child.pid
        for number in (1, 2):
            reservation, item, native, actor = f'D-{number}', f'BUG-{number:03}', f'worker-{number}', f'actor-{number}'
            f.squad('new', 'new', 'BUG', 'Isolated streaming source task', '--ready')
            if number == 2:
                f.squad('new', 'dispatch', 'reserve', reservation, '--source', f'github:example/repo#{number}')
            f.squad('new', 'dispatch', 'attach', reservation, '--item', item, '--generation', '1')
            f.squad('new', 'dispatch', 'bind', reservation, '--thread-id', native, '--generation', '1')
            f.squad(actor, 'register', '--as', actor)
            f.squad(actor, 'claim', item, '--long')
            f.squad(actor, 'milestone', '--to', item, 'Actual isolated result')
            with sqlite3.connect(f.root / 'home/global.db') as db:
                outcome = db.execute('SELECT max(id) FROM messages WHERE agent_id=? AND thread=?', (actor, item)).fetchone()[0]
            published = json.loads(f.squad(actor, 'terminal-events', 'publish', '--reservation', reservation,
                                   '--generation', '1', '--worker-session', native, '--kind', 'blocked', '--outcome', str(outcome)))
            receipt = receiver.receive(3)
            self.assertEqual([event['event_id'] for event in receipt['events']], [published['event_id']])
            self.assertEqual(receiver.child.pid, pid)
            self.assertIsNone(receiver.child.poll())
            with sqlite3.connect(f.root / 'home/global.db') as db:
                row = db.execute('SELECT delivered_at,processed_at FROM terminal_event_receipts WHERE event_id=?',
                                 (published['event_id'],)).fetchone()
            self.assertEqual(row, (0, 0))
            self.assertEqual([event['event_id'] for event in receiver.current(receipt)['events']], [published['event_id']])
            f.squad('new', 'terminal-events', 'delivered', published['event_id'],
                    '--native-session', self.config['native_session_id'], '--delivery-session', receiver.incarnation)
            f.squad('new', 'terminal-events', 'ack', published['event_id'],
                    '--native-session', self.config['native_session_id'], '--note', 'fixture handling complete')
            self.assertEqual(receiver.current(receipt)['events'], [])
        receiver.close()
        self.assertEqual(f.rows(), [])

    def test_stale_controller_and_unjoined_writer_reject_before_native_launch(self):
        dispatcher.check_controller(self.config)
        changed = dict(self.config, controller_epoch=1)
        with self.assertRaisesRegex(dispatcher.ValidationError, 'changed'):
            dispatcher.check_controller(changed)
        (self.state / 'dispatcher-writer.json').write_text(json.dumps({'state': 'running', 'pid': os.getpid()}))
        path = self.fixture.root / 'dispatcher.json'; path.write_text(json.dumps(self.config))
        with self.assertRaisesRegex(dispatcher.ValidationError, 'unresolved'):
            dispatcher.run(path)
        self.assertEqual(self.fixture.rows(), [])

    def test_handling_prompt_distinguishes_native_identity_from_delivery_incarnation(self):
        path=self.fixture.root/'prompt.txt';path.write_text('Isolated authorized handling')
        config=dict(self.config,prompt_file=str(path))
        receipt={'recipient':config['agent_id'],'delivery_session':'distinct-incarnation','events':[]}
        prompt=dispatcher.handling_prompt(config,receipt)
        self.assertIn(json.dumps({'actor':config['agent_id'],'native_session':config['native_session_id'],
                                  'controller_epoch':config['controller_epoch']}),prompt)
        self.assertIn('distinct-incarnation',prompt)
        self.assertNotEqual(config['native_session_id'],receipt['delivery_session'])

    def test_deferred_runtime_cannot_bind_or_launch_a_dispatcher_receiver(self):
        path=self.fixture.root/'deferred.json'
        path.write_text(json.dumps(dict(self.config,runtime='claude')))
        with patch.object(dispatcher,'check_controller') as controller, patch.object(dispatcher,'Receiver') as receiver:
            with self.assertRaisesRegex(dispatcher.ValidationError,'native Dispatcher'):
                dispatcher.run(path)
            with self.assertRaisesRegex(dispatcher.ValidationError,'native Dispatcher'):
                dispatcher.prepare(path)
        controller.assert_not_called();receiver.assert_not_called()
        self.assertEqual(self.fixture.rows(),[])

    def test_preparation_preserves_allocated_native(self):
        path = self.fixture.root / 'dispatcher.json'; path.write_text(json.dumps(self.config))
        with self.assertRaisesRegex(dispatcher.ValidationError, 'already allocated'):
            dispatcher.prepare(path)


if __name__ == '__main__':
    unittest.main()
