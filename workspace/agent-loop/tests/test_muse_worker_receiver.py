import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock,patch
ROOT=Path(__file__).parents[1];sys.path.insert(0,str(ROOT))
import muse_worker_receiver as receiver


class MuseReceiverTests(unittest.TestCase):
    def test_native_rejection_neither_marks_delivery_nor_acknowledges(self):
        with tempfile.TemporaryDirectory() as tmp:
            r=object.__new__(receiver.Receiver)
            r.c={'native_session_id':'native','reasoning_effort':'max','assignment':{'item':'BUG-001'}}
            r.journal=Path(tmp)/'deliveries.json';r.deliveries=[];r.incarnation='incarnation'
            host=Mock();host.rpc.side_effect=ValueError('transport unavailable')
            with patch('muse_worker_receiver.subprocess.run') as cli:
                with self.assertRaisesRegex(ValueError,'transport unavailable'):
                    r.submit(host,{'events':[{'event_id':'event'}]})
                cli.assert_not_called()
            self.assertEqual(json.loads(r.journal.read_text())[0]['state'],'prepared')

    def test_native_acceptance_marks_delivery_without_handling_ack(self):
        with tempfile.TemporaryDirectory() as tmp:
            r=object.__new__(receiver.Receiver)
            r.c={'native_session_id':'native','reasoning_effort':'max','assignment':{'item':'BUG-001'},'coordination_executable':'/squad','ledger_directory':'/ledger'}
            r.journal=Path(tmp)/'deliveries.json';r.deliveries=[];r.incarnation='incarnation';r.start=Mock()
            with patch('muse_worker_receiver.subprocess.run') as cli,patch('muse_worker_receiver.child_environment',return_value={}):
                r.submit(Mock(),{'events':[{'event_id':'event'}]})
            self.assertEqual(json.loads(r.journal.read_text())[0]['state'],'accepted')
            self.assertIn('delivered',cli.call_args.args[0]);self.assertNotIn('ack',cli.call_args.args[0])
            r.start.assert_called_once()


if __name__=='__main__':unittest.main()
