"""Opt-in pinned native lifecycle evidence; never uses a live Squad ledger.

MUSE_NATIVE_EXECUTABLE=/absolute/muse python3 -m unittest discover ... -p test_muse_native.py
Temporary sessions have no business assignment. This does not qualify task execution.
"""
import json
import os
from pathlib import Path
import queue
import sys
import tempfile
import time
import unittest
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import muse_session_host as host


@unittest.skipUnless(os.environ.get('MUSE_NATIVE_EXECUTABLE'), 'explicit native qualification only')
class NativeMuseTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='squad-muse-native-')
        self.root = Path(self.tmp.name).resolve()
        self.c = dict(role='probe', agent_id='qualification', native_session_id=host.uuid7(),
                      client_executable=os.environ['MUSE_NATIVE_EXECUTABLE'], model=host.MODEL,
                      provider='meta', permission_mode='yolo', reasoning_effort='high', workspace=str(self.root))
        self.binary = host.check_executable(self.c)
        self.hosts = []
        self.evidence = {'binary': self.binary, 'session_id': self.c['native_session_id'], 'test': self.id()}
    def open(self):
        h = host.Host(self.c, self.root, host.child_environment(self.c))
        self.hosts.append(h)
        self.evidence['initialize'] = host.initialize(h)
        return h
    def tearDown(self):
        for h in self.hosts:
            h.close()
        output = os.environ.get('MUSE_NATIVE_EVIDENCE')
        if output:
            directory=Path(output);directory.mkdir(parents=True,exist_ok=True)
            # Metadata only; never credentials, arbitrary tool output or provider errors.
            (directory/(self._testMethodName+'.json')).write_text(json.dumps(self.evidence,indent=2))
        self.tmp.cleanup()
    def test_start_resume_and_text_response(self):
        h=self.open();started=host.start(h,self.c);self.evidence['start']=started['session']
        # Native retained permission evidence, distinct from the public MSP
        # approval projection. This is a qualification assertion, not a general
        # runtime log-based enforcement adapter.
        snapshots=[]
        for line in Path(started['session']['path']).read_text().splitlines():
            frame=json.loads(line)
            for child in frame.get('children', []):
                record=json.loads(child['record_json'])
                if (record.get('stream',{}).get('id')==self.c['native_session_id']
                        and record.get('payload_type')=='runtime.session.permission_profile_committed'):
                    snapshots.append(record['payload']['resolved_snapshot'])
        self.assertTrue(snapshots)
        effective=snapshots[-1]
        self.assertEqual(effective['filesystem']['mode'],'unrestricted')
        self.assertEqual(effective['local_command_network']['mode'],'enabled')
        self.assertEqual(effective['approval'],'allow_all')
        self.assertEqual(effective['reviewer'],'none')
        self.evidence['native_permission_snapshot']=effective
        h.close();self.hosts.remove(h)
        h=self.open();self.evidence['resume']=host.resume(h,self.c)['session']
        h.rpc('turn/start',dict(commandId=host.uuid7(),sessionId=self.c['native_session_id'],reasoningEffort='high',
                              input=[{'type':'text','text':'Reply with exactly READY. Do not use tools.'}]))
        deadline=time.monotonic()+45;messages=[]
        while time.monotonic()<deadline:
            try: m=h.events.get(timeout=min(5,deadline-time.monotonic()))
            except queue.Empty: continue
            p=m.get('params',{})
            if m.get('method')=='item/completed' and p.get('item',{}).get('kind')=='agentMessage':
                messages.append(p['item'].get('text',''))
            if m.get('method')=='turn/completed':
                self.evidence['turn_terminal']=p.get('terminal')
                self.evidence['ready_response']=any('READY' in s for s in messages)
                self.assertEqual(p.get('terminal'),'completed','native route failed; no task readiness claimed')
                self.assertTrue(self.evidence['ready_response']);return
        self.fail('bounded native response timeout; no task readiness claimed')
    def test_wrong_permission_resume_rejected_before_load(self):
        h=self.open();host.start(h,self.c)
        h.rpc('session/setApprovalMode',dict(commandId=host.uuid7(),sessionId=self.c['native_session_id'],mode='onRequest'))
        h.close();self.hosts.remove(h)
        h=self.open()
        with self.assertRaisesRegex(ValueError,'mismatch'):host.resume(h,self.c)
        self.evidence['rejected_before_resume']=True
        self.assertEqual(h.rpc('session/read',{'sessionId':self.c['native_session_id']})['session']['turnCount'],0)
    def test_unknown_model_and_duplicate_start(self):
        h=self.open();host.start(h,self.c)
        with self.assertRaises(ValueError):host.start(h,self.c)
        self.evidence['duplicate_start_rejected']=True
        c=dict(self.c,model='not-a-real-muse-model')
        with self.assertRaises(ValueError):host.check_catalog(h,c)
        with self.assertRaises(ValueError):host.resume(h,c)
        self.evidence['unknown_model_rejected']=True
        self.assertEqual(h.rpc('session/read',{'sessionId':self.c['native_session_id']})['session']['turnCount'],0)
    def test_duplicate_native_owner_is_rejected(self):
        h=self.open();host.start(h,self.c)
        second=self.open()
        with self.assertRaises(ValueError):host.resume(second,self.c)
        self.evidence['second_host_resume_rejected']=True
        self.assertEqual(h.rpc('session/read',{'sessionId':self.c['native_session_id']})['session']['turnCount'],0)

if __name__=='__main__':unittest.main()
