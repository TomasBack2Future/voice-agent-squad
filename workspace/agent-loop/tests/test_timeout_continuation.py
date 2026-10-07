import sys
from pathlib import Path
import unittest

sys.path.insert(0, str(Path(__file__).parents[1]))
import timeout_continuation as continuation


def failure(**overrides):
    base = {'operation': 'canary-batch', 'revision': '78af0934', 'error_class': 'transient-timeout',
            'steps': [{'name': 'apply', 'state': 'completed'}, {'name': 'verify', 'state': 'pending'}]}
    base.update(overrides)
    return base


class TimeoutContinuationTests(unittest.TestCase):
    def test_transient_timeout_resumes_only_unfinished_steps(self):
        action, reason = continuation.continuation_attempt([failure()], set(), True)
        self.assertEqual(action, 'resume-unfinished')
        self.assertIn('1 unfinished', reason)

    def test_pause_decision_live_op_and_old_timeout_never_auto_retry(self):
        cases = [
            dict(paused=True),
            dict(pending_decision=True),
            dict(live_operation=True),
            dict(external_terminal=False),
        ]
        for kwargs in cases:
            with self.subTest(kwargs=kwargs):
                external = kwargs.pop('external_terminal', True)
                action, _ = continuation.continuation_attempt([failure()], set(), external, **kwargs)
                self.assertIn(action, ('stop', 'wait'))
        action, _ = continuation.continuation_attempt([failure(stale=True)], set(), True)
        self.assertEqual(action, 'stop')
        action, _ = continuation.continuation_attempt([failure(error_class='permission-denied')], set(), True)
        self.assertEqual(action, 'stop')

    def test_retry_cap_and_completed_steps_stop_blind_rerun(self):
        same = failure()
        action, _ = continuation.continuation_attempt([same, dict(same)], set(), True, max_attempts=2)
        self.assertEqual(action, 'stop')
        key = continuation.fingerprint(same)
        action, _ = continuation.continuation_attempt([same], {key}, True)
        self.assertEqual(action, 'stop')
        done = failure(steps=[{'name': 'apply', 'state': 'completed'}])
        action, _ = continuation.continuation_attempt([done], set(), True)
        self.assertEqual(action, 'stop')


if __name__ == '__main__':
    unittest.main()
