import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT=Path(__file__).parents[1]
sys.path.insert(0,str(ROOT))
import muse_worker_tools as tools


class MuseToolsTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name);self.work=self.root/'work';self.work.mkdir()
        self.c={'workspace':str(self.work),'execution_binding':str(self.root/'binding.json'),
                'native_session_id':'native','execution_id':'pin'}
        (self.root/'startup-loaded.json').write_text('{}')
        (self.root/'binding.json').write_text(json.dumps({'item':'BUG-001','reservation':'D','generation':1,'native':'native'}))
        path=self.root/'config.json';path.write_text(json.dumps(self.c))
        with patch.dict(os.environ,{'MUSE_SESSION_ID':'native'}),patch('muse_worker_tools.Runtime'):
            self.bridge=tools.Bridge(path)
        self.addCleanup(self.bridge.lock.close)

    def test_unloaded_role_cannot_mutate(self):
        (self.root/'startup-loaded.json').unlink()
        with patch('muse_worker_tools.coordination') as cli:
            with self.assertRaisesRegex(ValueError,'startup loading'):
                self.bridge.write({'path':'marker','content':'BAD','expected_sha256':'absent'})
            cli.assert_not_called()
        self.assertFalse((self.work/'marker').exists())

    def test_failed_gate_prevents_file_write(self):
        with patch('muse_worker_tools.coordination',side_effect=ValueError('stale')):
            with self.assertRaisesRegex(ValueError,'stale'):
                self.bridge.write({'path':'marker','content':'BAD','expected_sha256':'absent'})
        self.assertFalse((self.work/'marker').exists())
        self.assertFalse(self.bridge.journal)

    def test_write_cas_and_durable_operation(self):
        with patch('muse_worker_tools.coordination') as check:
            self.bridge.write({'path':'marker','content':'OK','expected_sha256':'absent'})
            self.assertEqual(check.call_count,2)
            with self.assertRaisesRegex(ValueError,'changed'):
                self.bridge.write({'path':'marker','content':'BAD','expected_sha256':'absent'})
        self.assertEqual((self.work/'marker').read_text(),'OK')
        self.assertEqual(self.bridge.journal[0]['state'],'completed')
        self.assertEqual(self.bridge.journal_path.stat().st_mode&0o777,0o600)

    def test_local_closed_gate_prevents_commands_without_ledger(self):
        (self.root/'closed').touch()
        with patch('muse_worker_tools.coordination') as check:
            with self.assertRaisesRegex(ValueError,'closed'):
                self.bridge.shell({'command':'touch marker'})
            check.assert_not_called()

    def test_unresolved_reconnect_retains_original_journal(self):
        self.bridge.lock.close()
        self.bridge.journal_path.write_text(json.dumps([{'state':'running','container':'a'*64}]))
        with patch.dict(os.environ,{'MUSE_SESSION_ID':'native'}),patch('muse_worker_tools.Runtime'):
            with self.assertRaisesRegex(ValueError,'unresolved'):
                tools.Bridge(self.root/'config.json')

    def test_workspace_escape_is_rejected(self):
        with self.assertRaisesRegex(ValueError,'inside'):
            self.bridge.write({'path':'../outside','content':'BAD','expected_sha256':'absent'})

    def test_control_does_not_expose_execution_close(self):
        with patch('muse_worker_tools.coordination'):
            with self.assertRaisesRegex(ValueError,'only assignment'):
                self.bridge.control({'arguments':['worker-execution','close','--evidence','fake']})

    def test_suspended_custody_allows_only_handling_and_blocked_report(self):
        with patch('muse_worker_tools.coordination') as checked, patch('muse_worker_tools.child_environment',return_value={}), patch('muse_worker_tools.subprocess.run') as run:
            self.c.update(coordination_executable='/squad',ledger_directory=str(self.root))
            self.bridge.c.update(self.c)
            run.return_value.returncode=0;run.return_value.stdout='{}';run.return_value.stderr=''
            self.bridge.suspend()
            self.bridge.call('decision_get',{})
            self.bridge.call('acknowledge',{'event_id':'worker-terminal-v1/D/1/native/decision-resolved/1','note':'hold handled; tool joined'})
            self.bridge.call('report',{'status':'blocked','summary':'hold handled; source mutation stopped'})
            self.assertTrue((self.root/'report.json').exists())
            self.assertIn('check-read',[call.args[1] for call in checked.call_args_list])
            with self.assertRaisesRegex(ValueError,'closed'):
                self.bridge.call('report',{'status':'completed','summary':'BAD'})
            with self.assertRaisesRegex(ValueError,'closed'):
                self.bridge.write({'path':'marker','content':'BAD','expected_sha256':'absent'})
            with self.assertRaisesRegex(ValueError,'only decision'):
                self.bridge.control({'arguments':['review-request','BUG-001']})
        with patch('muse_worker_tools.coordination',side_effect=ValueError('changed custody')):
            with self.assertRaisesRegex(ValueError,'changed custody'):
                self.bridge.call('report',{'status':'blocked','summary':'BAD'})


if __name__=='__main__':unittest.main()
