"""Opt-in real Docker qualification; no foreign container cleanup or model call."""
import json
import os
from pathlib import Path
import shlex
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

ROOT=Path(__file__).parents[1]
sys.path.insert(0,str(ROOT))
import muse_worker_tools as tools


@unittest.skipUnless(os.environ.get('MUSE_CONTAINER_QUALIFICATION'), 'explicit isolated container qualification config required')
class MuseContainerTests(unittest.TestCase):
    def setUp(self):
        selected=json.loads(Path(os.environ['MUSE_CONTAINER_QUALIFICATION']).read_text())
        self.tmp=tempfile.TemporaryDirectory(dir=selected['state_directory'],prefix='container-regression-')
        self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name);self.work=self.root/'work';self.work.mkdir()
        self.c=dict(selected,workspace=str(self.work),execution_binding=str(self.root/'binding.json'),execution_id='isolated-container-'+self.root.name)
        (self.root/'startup-loaded.json').write_text('{}')
        path=self.root/'config.json';path.write_text(json.dumps(self.c))
        with patch.dict(os.environ,{'MUSE_SESSION_ID':self.c['native_session_id']}):self.bridge=tools.Bridge(path)
        self.addCleanup(self.bridge.lock.close)
        self.addCleanup(self.remove)

    def remove(self):
        for op in self.bridge.journal:
            if op.get('container'):
                self.bridge.runtime.inspect(op)
                if not self.bridge.runtime.joined(op):self.bridge.runtime.stop(op)
                self.bridge.runtime.call(['container','rm',op['container']])

    def test_env_clearing_setsid_descendant_is_contained(self):
        child='import time,pathlib; time.sleep(3); pathlib.Path("escaped-marker").write_text("BAD")'
        command='import subprocess; subprocess.Popen(["python3","-c",'+repr(child)+'],start_new_session=True,env={"PATH":"/usr/local/bin:/usr/bin:/bin"})'
        with patch('muse_worker_tools.coordination'):
            result=self.bridge.shell({'command':'python3 -c '+shlex.quote(command)})
        self.assertEqual(result['exit_code'],0)
        self.assertTrue(self.bridge.runtime.joined(self.bridge.journal[-1]))
        time.sleep(4)
        self.assertFalse((self.work/'escaped-marker').exists())
        self.assertEqual(self.bridge.journal[-1]['state'],'completed')

    def test_timeout_closes_gate_and_joins_all_owned_processes(self):
        with patch('muse_worker_tools.coordination'):
            with self.assertRaises(TimeoutError):self.bridge.shell({'command':'sleep 30','timeout_seconds':1})
        self.assertTrue((self.root/'closed').exists())
        self.assertEqual(self.bridge.journal[-1]['state'],'joined')
        self.assertTrue(self.bridge.runtime.joined(self.bridge.journal[-1]))
        with self.assertRaisesRegex(ValueError,'closed'):self.bridge.check()


if __name__=='__main__':unittest.main()
