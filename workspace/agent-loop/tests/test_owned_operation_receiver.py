"""Owned command exits use the original Worker receipt route."""
import copy
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from codex_receiver import validate_events
from validate_context_package import ValidationError


class OwnedOperationReceiverTests(unittest.TestCase):
    def setUp(self):
        self.config = dict(agent_id='worker', incarnation='inc', role='worker',
                           reservation='D', generation=1,
                           native_session_id='native', item='T')
        self.receipt = dict(
            type='worker-terminal-delivery-v1', recipient='worker',
            delivery_session='inc', events=[dict(
                event_id='worker-terminal-v1/D/1/native/operation-exit/2',
                kind='operation-exit', outcome_id=2, item_id='T')])

    def test_original_worker_accepts_exit(self):
        self.assertEqual(len(validate_events(self.receipt, self.config)), 1)

    def test_wrong_assignment_rejected(self):
        for field, value in [('reservation', 'other'), ('generation', 2),
                             ('native_session_id', 'other'), ('item', 'other')]:
            config = copy.deepcopy(self.config)
            config[field] = value
            with self.subTest(field=field), self.assertRaises(ValidationError):
                validate_events(self.receipt, config)

    def test_dispatcher_cannot_consume_worker_exit(self):
        config = dict(self.config, role='dispatcher')
        with self.assertRaises(ValidationError):
            validate_events(self.receipt, config)

    def test_existing_decision_route_kept(self):
        receipt = copy.deepcopy(self.receipt)
        receipt['events'][0].update(
            event_id='worker-terminal-v1/D/1/native/decision-resolved/2',
            kind='decision-resolved')
        self.assertEqual(len(validate_events(receipt, self.config)), 1)

    def test_worker_cannot_consume_dispatcher_completion(self):
        receipt = copy.deepcopy(self.receipt)
        receipt['events'][0].update(
            event_id='worker-terminal-v1/D/1/native/issue-closed/2',
            kind='issue-closed')
        with self.assertRaises(ValidationError):
            validate_events(receipt, self.config)


if __name__ == '__main__':
    unittest.main()
