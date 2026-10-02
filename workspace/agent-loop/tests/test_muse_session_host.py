import copy
import json
import os
from pathlib import Path
import sys
import tempfile
import subprocess
import threading
import unittest
from unittest.mock import patch, MagicMock
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import muse_session_host as host


def configuration(root):
    return dict(role='worker', agent_id='worker', native_session_id='01900000-0000-7000-8000-000000000001',
                client_executable='/bin/echo', coordination_executable='/bin/echo', ledger_directory=str(root),
                workspace=str(root), state_directory=str(root/'state'), prompt_file=str(root/'prompt'),
                model='muse-spark-1.3-contributor', provider='meta', permission_mode='yolo',
                reasoning_effort='high', assignment_file=str(root/'assignment.json'), dispatcher_agent_id='dispatcher')


def session(c):
    return dict(sessionId=c['native_session_id'], workspaceRoot=c['workspace'], modelId=c['model'],
                providerId=c['provider'], approvalMode={'mode': 'allowAll'}, status='idle', activeTurnId=None)


class MuseHostTests(unittest.TestCase):
    def setUp(self):
        self.environment = patch.dict(os.environ, {"PATH": os.environ["PATH"]}, clear=True)
        self.environment.start()
        self.addCleanup(self.environment.stop)
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name).resolve()
        self.c = configuration(self.root)
        (self.root/'prompt').write_text('TASK MUST NOT RUN')
    def tearDown(self):
        self.tmp.cleanup()
    def test_identity_never_inherits_claude_or_codex(self):
        with patch.dict(os.environ, {'SQUAD_AGENT':'old','SQUAD_SESSION_ID':'old','CODEX_THREAD_ID':'old','CLAUDE_SESSION_ID':'old'}):
            env=host.child_environment(self.c)
        self.assertEqual(env['SQUAD_AGENT'],'worker')
        self.assertEqual(env['SQUAD_SESSION_ID'],'muse:'+self.c['native_session_id'])
        self.assertNotIn('CODEX_THREAD_ID',env)
        self.assertNotIn('CLAUDE_SESSION_ID',env)
    def test_unknown_environment_override_rejected(self):
        self.c['environment']={'CODEX_THREAD_ID':'old'}
        with self.assertRaises(ValueError): host.child_environment(self.c)
    def test_missing_or_different_selection_rejected(self):
        for key,value in [('model',None),('model','muse-1.2'),('provider','echo'),('permission_mode','onRequest')]:
            c=dict(self.c);c[key]=value
            p=self.root/'config.json';p.write_text(json.dumps(c))
            with self.subTest(key=key,value=value),self.assertRaises(ValueError):host.config(p)
    def test_explicit_serve_selection(self):
        argv=host.server_arguments(self.c)
        self.assertEqual(argv[argv.index('--model')+1],self.c['model'])
        self.assertEqual(argv[argv.index('--provider')+1],'meta')
        self.assertIn('--disable-sandbox',argv)
        self.assertIn('--trust-workspace',argv)
        self.assertNotIn('--yolo',argv)  # Not an MSP serve option.
    def test_effective_selection_fail_closed(self):
        host.check_effective({'session':session(self.c)},self.c)
        for key,value in [('modelId','muse-1.2'),('providerId','echo'),('approvalMode',{'mode':'onRequest'}),('approvalMode',None),('workspaceRoot','/wrong'),('sessionId','wrong'),('status','running'),('activeTurnId','old-turn')]:
            r=session(self.c);r[key]=value
            with self.subTest(key=key),self.assertRaises(ValueError):host.check_effective({'session':r},self.c)
    def test_resume_checks_read_before_resuming_and_after(self):
        rpc=MagicMock();r=session(self.c);r['approvalMode']={'mode':'onRequest'}
        rpc.rpc.return_value={'session':r}
        with self.assertRaises(ValueError):host.resume(rpc,self.c)
        self.assertEqual([c.args[0] for c in rpc.rpc.call_args_list],['session/read'])
        rpc.reset_mock();good={'session':session(self.c)}
        rpc.rpc.side_effect=[good,{'session':r}]
        with self.assertRaises(ValueError):host.resume(rpc,self.c)
        self.assertEqual([c.args[0] for c in rpc.rpc.call_args_list],['session/read','session/resume'])
    def test_unavailable_model_or_effort_rejected(self):
        rpc=MagicMock()
        for models in [[],[{'modelId':'muse-1.2','providerId':'meta','variants':['high']}],
                       [{'modelId':self.c['model'],'providerId':'meta','variants':['low']}]]:
            rpc.rpc.return_value={'models':models}
            with self.assertRaises(ValueError):host.check_catalog(rpc,self.c)
    def test_bound_role_cannot_start_server_without_execution_fence(self):
        # No actor/lease/native operation, even with an asserted capability flag.
        for role in ['worker','dispatcher','deployer','reviewer','investigator','probe']:
            c=dict(self.c,role=role,execution_fence=True)
            with self.subTest(role=role),patch.object(host,'Host') as h,self.assertRaises(ValueError):
                host.run(c)
            h.assert_not_called()
    def test_unresponsive_owned_probe_is_killed_and_reaped(self):
        h=host.Host.__new__(host.Host)
        h.err=(self.root/'probe-error.log').open('w')
        h.p=subprocess.Popen([sys.executable,'-c',
            "import signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); print('ready',flush=True); time.sleep(60)"],
            stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=h.err,text=True)
        self.assertEqual(h.p.stdout.readline().strip(),'ready')
        h.reader=threading.Thread(target=h.p.stdout.read,daemon=True);h.reader.start()
        wait=h.p.wait
        try:
            with patch.object(h.p,'wait',side_effect=lambda timeout:wait(timeout=min(timeout,0.1))):
                h.close()
            self.assertIsNotNone(h.p.poll())
            self.assertLess(h.p.returncode,0)
            self.assertTrue(h.p.stdout.closed)
            self.assertTrue(h.err.closed)
        finally:
            if h.p.poll() is None:h.p.kill();h.p.wait(timeout=5)
            h.reader.join(timeout=5)
            h.p.stdout.close();h.p.stdin.close();h.err.close()
    def test_uuid7(self):
        import uuid
        self.assertEqual(uuid.UUID(host.uuid7()).version,7)

if __name__=='__main__': unittest.main()
