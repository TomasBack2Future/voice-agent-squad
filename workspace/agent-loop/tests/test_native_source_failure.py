import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from native_source_failure import publish_failure
from validate_context_package import ValidationError


class SourceFailureTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name); self.native = self.root / 'native'; self.native.mkdir()
        self.state = self.root / 'execution'; self.state.mkdir()
        self.pin = {'id': 'codex-closed-execution', 'native': 'native', 'client_pid': 123, 'actor': 'worker'}
        self.c = {'native_state': str(self.native), 'provider': 'openai', 'runtime': 'codex'}
        self.join = {'binding': self.pin, 'joined': True, 'native_pid': 123, 'native_exit': -15,
                     'bridge_pid': None, 'bridge_live': False, 'operations': [], 'terminal': None}
        self.save()
    def save(self):
        (self.state / 'join.json').write_text(json.dumps(self.join))
        (self.native / 'native-join.json').write_text(json.dumps({'pid':123, 'exit_code':-15, 'joined':True}))
    def test_joined_supervisor_failure_uses_fenced_observation_without_model_report(self):
        receipt = {'message_id': 7, 'event_id': 'actual-event'}
        with patch('native_source_failure.os.kill', side_effect=ProcessLookupError), patch('native_source_failure.read_decision', return_value={'revision': 3}), patch('native_source_failure.submit_outcome', return_value=receipt) as submit:
            result = publish_failure(self.c, {}, self.pin, self.state)
        self.assertEqual(result, receipt); self.assertFalse((self.state / 'report.json').exists())
        args, kwargs = submit.call_args
        self.assertEqual(args[3], 'runtime-failure'); self.assertEqual(kwargs['decision_revision'], 3)
        self.assertEqual(kwargs['request_key'], 'failure-codex-closed-execution')
        self.assertEqual(Path(args[4]).read_text(), 'runtime-failure ep-1 unknown turn=unresolved request=codex-closed-execution attempt=0 provider=openai')
    def test_lost_response_keeps_exact_original_decision_and_payload(self):
        with patch('native_source_failure.os.kill', side_effect=ProcessLookupError), patch('native_source_failure.read_decision', return_value={'revision': 3}) as decision, patch('native_source_failure.submit_outcome', side_effect=[ValidationError('response lost'), {'message_id':7, 'event_id':'actual-event'}]) as submit:
            with self.assertRaises(ValidationError): publish_failure(self.c, {}, self.pin, self.state)
            first = submit.call_args
            publish_failure(self.c, {}, self.pin, self.state)
            self.assertEqual(submit.call_args, first); self.assertEqual(decision.call_count, 1)
    def test_a_live_container_cannot_emit_a_joined_failure(self):
        self.join['operations'] = [{'kind':'container', 'state':'joined', 'container':'a' * 64}]
        self.save()
        with patch('native_source_failure.os.kill', side_effect=ProcessLookupError), patch('native_source_failure.Runtime') as runtime, patch('native_source_failure.submit_outcome') as submit:
            runtime.return_value.joined.return_value = False
            with self.assertRaises(ValidationError): publish_failure(self.c, {}, self.pin, self.state)
            submit.assert_not_called()

    def test_unjoined_live_changed_or_completed_native_cannot_publish(self):
        original = dict(self.join)
        for change in ({'joined':False}, {'binding':{}}, {'native_pid':456}, {'native_exit':None}, {'bridge_live':True}, {'operations':[{'state':'running'}]}):
            self.join = dict(original, **change); self.save()
            with patch('native_source_failure.os.kill', side_effect=ProcessLookupError), patch('native_source_failure.submit_outcome') as submit:
                with self.assertRaises(ValidationError): publish_failure(self.c, {}, self.pin, self.state)
                submit.assert_not_called()
        self.join = dict(original); self.save()
        with patch('native_source_failure.os.kill'), patch('native_source_failure.submit_outcome') as submit:
            with self.assertRaises(ValidationError): publish_failure(self.c, {}, self.pin, self.state)
            submit.assert_not_called()
        self.join['terminal'] = {'status':'completed'}; self.save(); (self.state / 'report.json').write_text('{}')
        with patch('native_source_failure.os.kill', side_effect=ProcessLookupError):
            with self.assertRaises(ValidationError): publish_failure(self.c, {}, self.pin, self.state)


if __name__ == '__main__': unittest.main()
