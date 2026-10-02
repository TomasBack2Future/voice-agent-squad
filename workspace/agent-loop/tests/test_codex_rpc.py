import json
from pathlib import Path
import struct
import sys
import time
import unittest
from unittest.mock import Mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from codex_rpc import RPC, LIMIT
from validate_context_package import ValidationError


def frame(opcode, value, final=True):
    payload=value if isinstance(value,bytes) else json.dumps(value).encode()
    n=len(payload)
    header=bytes([(128 if final else 0)|opcode,n if n<126 else 126])
    if n>=126:header+=struct.pack('!H',n)
    return header+payload


class TransportTests(unittest.TestCase):
    def rpc(self, wire=b'', probe=False):
        rpc=RPC.__new__(RPC)
        rpc.sequence=0;rpc.notifications=[];rpc.buffer=wire;rpc.probe=probe
        rpc.socket=Mock()
        rpc.socket.recv.return_value=b''
        return rpc

    def test_fragmented_reply_with_interleaved_ping(self):
        value=json.dumps({'result':{'accepted':True}}).encode()
        rpc=self.rpc(frame(1,value[:5],False)+frame(9,b'health')+frame(0,value[5:]))
        self.assertEqual(rpc.receive(time.monotonic()+1),{'result':{'accepted':True}})
        self.assertEqual(rpc.socket.sendall.call_args[0][0][0],0x8A)

    def test_truncated_and_oversized_frames_fail_without_retry(self):
        rpc=self.rpc(frame(1,b'{')[:-1])
        with self.assertRaisesRegex(ValidationError,'closed'):rpc.receive(time.monotonic()+1)
        rpc=self.rpc(bytes([0x81,127])+struct.pack('!Q',LIMIT+1))
        with self.assertRaisesRegex(ValidationError,'bounded'):rpc.receive(time.monotonic()+1)
        rpc.socket.sendall.assert_not_called()

    def test_control_subscriber_does_not_answer_owner_tool_request(self):
        request={'id':42,'method':'item/commandExecution/requestApproval','params':{}}
        for probe,expected_sends in ((False,1),(True,2)):
            rpc=self.rpc(frame(1,request)+frame(1,{'id':1,'result':{'accepted':True}}),probe)
            self.assertEqual(rpc.call('thread/queue/add',{}),{'accepted':True})
            self.assertEqual(rpc.socket.sendall.call_count,expected_sends)

    def test_rpc_error_does_not_expose_native_private_details(self):
        rpc=self.rpc(frame(1,{'id':1,'error':{'message':'SECRET-PRIVATE-PATH'}}))
        with self.assertRaises(ValidationError) as error:rpc.call('thread/read',{})
        self.assertNotIn('SECRET',str(error.exception))


if __name__=='__main__':unittest.main()
