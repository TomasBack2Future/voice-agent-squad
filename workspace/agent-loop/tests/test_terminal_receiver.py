import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import terminal_receiver as receiver

class ReceiverTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.binary = self.root / 'squad'
        self.binary.write_text('#!/usr/bin/env python3\nimport json,time,pathlib,os\np=pathlib.Path("calls");p.write_text(p.read_text()+"call\\n" if p.exists() else "call\\n")\nwhile pathlib.Path("hold").exists(): time.sleep(.05)\nprint(json.dumps({"type":"worker-terminal-delivery-v1","events":[{"event_id":"test-event"}]}))\n')
        self.binary.chmod(0o700)
        self.config = self.root / 'config.json'
        self.data = dict(native_session_id='session-one', agent_id='dispatcher',
                         state_directory=str(self.root / 'state'), ledger_directory=str(self.root),
                         squad_executable=str(self.binary), incarnation='launch-one', owner_pid=os.getpid(), max_seconds=60)
        self.config.write_text(json.dumps(self.data))

    def start(self, session='session-one'):
        p = subprocess.Popen([sys.executable, receiver.__file__, '--config', str(self.config)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        p.stdin.write(json.dumps({'session_id': session}))
        p.stdin.close()
        p.stdin = None
        self.addCleanup(lambda: p.kill() if p.poll() is None else None)
        return p

    def test_native_reminder_and_identity_binding(self):
        wrong = self.start('other');out,err = wrong.communicate(timeout=5)
        self.assertEqual((wrong.returncode,out,err),(0,'',''))
        self.assertFalse((self.root/'calls').exists())
        p = self.start();out,err = p.communicate(timeout=5)
        self.assertEqual(p.returncode,2)
        self.assertEqual(out,'')
        self.assertIn('test-event',err)
        self.assertIn('terminal-events ack',err)

    def test_singleton_and_resume_cancels_old_receiver(self):
        (self.root/'hold').touch()
        first = self.start()
        deadline = time.time()+5
        while not (self.root/'calls').exists() and time.time()<deadline: time.sleep(.02)
        self.assertTrue((self.root/'calls').exists())
        duplicate = self.start();duplicate.communicate(timeout=5)
        self.assertEqual(duplicate.returncode,0)
        self.assertEqual((self.root/'calls').read_text(),'call\n')
        self.data['incarnation'] = 'launch-two'
        self.config.write_text(json.dumps(self.data))
        first.communicate(timeout=5)
        self.assertEqual(first.returncode,0)
        (self.root/'hold').unlink()
        resumed = self.start();resumed.communicate(timeout=5)
        self.assertEqual(resumed.returncode,2)
        self.assertEqual((self.root/'calls').read_text(),'call\ncall\n')

    def test_hook_settings_use_supported_events_and_no_shell(self):
        hooks = receiver.settings(self.config)['hooks']
        self.assertNotIn('asyncRewake',hooks)
        for event in ('SessionStart','PostToolUse','Stop'):
            h=hooks[event][0]['hooks'][0]
            self.assertTrue(h['asyncRewake'])
            self.assertEqual(h['command'],sys.executable)
            self.assertIn(str(self.config.resolve()),h['args'])

if __name__ == '__main__': unittest.main()
