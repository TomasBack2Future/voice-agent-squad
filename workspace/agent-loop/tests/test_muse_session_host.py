import importlib.util
import json
import os
from pathlib import Path
import sys
import unittest
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import muse_session_host as host

class MuseHostTests(unittest.TestCase):
    def test_identity_never_inherits_claude_or_codex(self):
        with patch.dict(os.environ, {'SQUAD_AGENT':'old','SQUAD_SESSION_ID':'old','CODEX_THREAD_ID':'old','CLAUDE_SESSION_ID':'old'}):
            env=host.child_environment(dict(agent_id='new',native_session_id='new-session'))
        self.assertEqual(env['SQUAD_AGENT'],'new')
        self.assertEqual(env['SQUAD_SESSION_ID'],'muse:new-session')
        self.assertNotIn('CODEX_THREAD_ID',env)
        self.assertNotIn('CLAUDE_SESSION_ID',env)
    def test_unknown_environment_override_rejected(self):
        with self.assertRaises(ValueError):
            host.child_environment(dict(agent_id='new',native_session_id='n',environment={'CODEX_THREAD_ID':'old'}))
    def test_event_is_data_and_explicit_ack(self):
        prompt=host.delivery_prompt(dict(events=[{'id':'fenced-event'}]))
        self.assertIn('not new authority',prompt)
        self.assertIn('terminal-events ack',prompt)
        self.assertIn('Delivery does not acknowledge',prompt)
    def test_uuid7(self):
        import uuid
        self.assertEqual(uuid.UUID(host.uuid7()).version,7)

if __name__=='__main__': unittest.main()
