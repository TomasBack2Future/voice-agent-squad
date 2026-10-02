import json
import subprocess
import sys
from pathlib import Path
import unittest
from unittest.mock import Mock, patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from codex_heartbeat import heartbeat, CustodyRejected
from validate_context_package import ValidationError


class HeartbeatTests(unittest.TestCase):
    def setUp(self):
        self.a={'reservation':{'key':'D-1','generation':1}}
        self.c={'coordination_executable':'/fixture/squad','ledger_directory':'/fixture/ledger','agent_id':'worker','native_session_id':'native'}
        self.receipt={'schema_version':'squad.worker-heartbeat.v1','outcome':'renewed','agent':'worker','reservation':'D-1','generation':1,'worker_session':'native','require_primary':True}

    def call(self,receipt,code=0,stderr=''):
        with patch('codex_heartbeat.subprocess.run',return_value=Mock(returncode=code,stdout=json.dumps(receipt),stderr=stderr)):
            heartbeat(self.a,self.c,{})

    def test_only_structured_exact_atomic_negative_is_custody_rejection(self):
        self.call(self.receipt)
        with self.assertRaises(CustodyRejected):self.call(dict(self.receipt,outcome='custody-rejected'),1)
        for receipt in (dict(self.receipt,outcome='unavailable'),dict(self.receipt,outcome='custody-rejected',generation=2),dict(self.receipt,outcome='custody-rejected',agent='other'),dict(self.receipt,outcome='custody-rejected',generation=True),{}):
            try:self.call(receipt,1,'heartbeat dispatch fence rejected')
            except CustodyRejected:self.fail('stderr or mismatched data invented custody rejection')
            except ValidationError:pass
            else:self.fail('unverified heartbeat succeeded')

    def test_plain_cli_nonzero_and_malformed_receipt_remain_retryable_unknown(self):
        for stdout in ('','not json','[]'):
            with patch('codex_heartbeat.subprocess.run',return_value=Mock(returncode=1,stdout=stdout,stderr='canonical claim belongs to another agent')):
                try:heartbeat(self.a,self.c,{})
                except CustodyRejected:self.fail('stderr became authority')
                except ValidationError:pass
                else:self.fail('unavailable heartbeat succeeded')
