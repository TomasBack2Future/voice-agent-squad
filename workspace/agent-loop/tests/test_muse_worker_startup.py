import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
ROOT=Path(__file__).parents[1];sys.path.insert(0,str(ROOT))
import muse_worker_launcher as launch


class MuseStartupTests(unittest.TestCase):
    def test_role_profile_without_lifecycle_never_opens_mutations(self):
        with tempfile.TemporaryDirectory() as tmp:
            state=Path(tmp);loaded={};c={'native_session_id':'native','workspace':tmp}
            startup={}
            for key in ('role','profile','lifecycle'):
                path=state/(key+'.txt');path.write_text(key+'\n')
                startup[key]={'path':str(path),'sha256':hashlib.sha256(path.read_bytes()).hexdigest()}
            (state/'startup.json').write_text(json.dumps(startup))
            for key in ('role','profile'):
                path=startup[key]['path']
                launch.record_context_read(c,state,{'method':'item/completed','params':{'sessionId':'native','item':{
                    'kind':'toolCall','tool':'read_file','status':'completed','itemId':key,'args':json.dumps({'path':path}),
                    'visibleOutput':'Read text file `'+path+'`.\n1|'+key}}},loaded)
            self.assertFalse((state/'startup-loaded.json').exists(),'lifecycle rules have not been read by the native')
    def test_partial_or_wrong_native_reads_never_open_mutations(self):
        with tempfile.TemporaryDirectory() as tmp:
            state=Path(tmp);role=state/'role.md';profile=state/'profile.json';lifecycle=state/'lifecycle.md'
            role.write_text('role\nfull contract\n');profile.write_text('{}\n');lifecycle.write_text('lifecycle contract\n')
            startup={key:{'path':str(path),'sha256':hashlib.sha256(path.read_bytes()).hexdigest()} for key,path in [('role',role),('profile',profile),('lifecycle',lifecycle)]}
            (state/'startup.json').write_text(json.dumps(startup));loaded={};c={'native_session_id':'native','workspace':tmp}
            def event(path,output,native='native'):
                return {'method':'item/completed','params':{'sessionId':native,'item':{'kind':'toolCall','tool':'read_file','status':'completed','itemId':'item','args':json.dumps({'path':str(path)}),'visibleOutput':'Read text file `'+str(path)+'`.\n'+output}}}
            launch.record_context_read(c,state,event(role,'1|role'),loaded)
            self.assertFalse(loaded)
            launch.record_context_read(c,state,event(role,'1|role\n2|full contract','foreign'),loaded)
            self.assertFalse(loaded)
            launch.record_context_read(c,state,event(role,'1|role\n2|full contract'),loaded)
            self.assertFalse((state/'startup-loaded.json').exists())
            launch.record_context_read(c,state,event(profile,'1|{}'),loaded)
            self.assertFalse((state/'startup-loaded.json').exists())
            launch.record_context_read(c,state,event(lifecycle,'1|lifecycle contract'),loaded)
            self.assertEqual(set(json.loads((state/'startup-loaded.json').read_text())['loaded']),{'role','profile','lifecycle'})


if __name__=='__main__':unittest.main()
