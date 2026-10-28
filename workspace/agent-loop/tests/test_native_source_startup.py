"""Admission failures must join owned clients without losing pin custody."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).parents[1]
sys.path.insert(0, str(ROOT))
import codex_source_worker as source
from muse_session_host import atomic
from muse_worker_tools import child_environment


class SourceStartupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name); (self.root / 'prompt').write_text('isolated task')
        self.config = dict(runtime='codex', role='worker', native_session_id='own-native',
            workspace=str(self.root / 'work'), state_directory=str(self.root / 'execution'),
            native_state=str(self.root / 'native'), prompt_file=str(self.root / 'prompt'),
            coordination_home=str(self.root / 'home'), ledger_directory=str(self.root / 'ledger'),
            agent_id='own-worker', dispatcher_agent_id='own-controller', controller_native='own-controller-native',
            controller_epoch=1, claim_generation=1, tool_runtime={}, max_seconds=30)
        Path(self.config['native_state']).mkdir()
        self.assignment = dict(item='BUG-001', issue='example/repo#1', reservation={'key':'R-1','generation':1})
        self.args = self.root/'assignment.json', self.root/'config.json'
        self.calls = []; self.children = []

    def writer(self):
        return json.loads(next((self.root/'execution').glob('*.writer.json')).read_text())

    def execute(self, failure='turn', inspection='absent'):
        parent = self
        class Host:
            child = None
            def turn(self, *args):
                if failure == 'turn': raise OSError('owned startup refused')
                self.child = subprocess.Popen([sys.executable, '-c', 'import time;time.sleep(60)'])
                parent.children.append(self.child)
                return 'source-input'
            def close(self):
                if self.child:
                    self.child.terminate(); self.child.wait(timeout=5)
                    atomic(parent.root/'native/native-join.json',
                           {'pid':self.child.pid,'joined':True,'exit_code':self.child.returncode})
                    self.child = None
        def coordination(config, action, *args):
            self.calls.append(action)
            if action == 'acquire': raise ValueError('lost or refused acquire response')
            if action == 'inspect':
                if inspection == 'error': raise ValueError('status unavailable')
                return {'state':inspection}
            return {}
        factory = (lambda *a: (_ for _ in ()).throw(OSError('constructor refused'))) if failure == 'constructor' else lambda *a: Host()
        with patch.object(source,'source_config',return_value=dict(self.config)), \
             patch.object(source,'validate_file',return_value=self.assignment), \
             patch.object(source,'check_launch'), patch.object(source,'references',return_value={}), \
             patch.object(source,'native_host',side_effect=factory), \
             patch.object(source,'publish_failure') as failure_publish, \
             patch.object(source,'coordination',side_effect=coordination):
            return source.run(*self.args)

    def test_constructor_refusal_does_not_admit_writer(self):
        with self.assertRaisesRegex(OSError,'constructor refused'): self.execute('constructor')
        self.assertEqual(list((self.root/'execution').glob('*.writer.json')),[])

    def test_turn_failure_is_joined_and_does_not_wedge_the_next_admission(self):
        for _ in range(2):
            with self.assertRaisesRegex(OSError,'startup refused'): self.execute('turn')
            self.assertEqual(self.writer()['state'],'joined')
            self.assertIsNone(self.writer()['pid'])
        self.assertEqual(self.calls,[])

    def test_refused_acquire_proves_absent_and_joins_the_owned_child(self):
        with self.assertRaisesRegex(ValueError,'acquire response'): self.execute('acquire','absent')
        self.assertEqual(self.writer()['state'],'joined')
        self.assertEqual(self.calls,['acquire','inspect'])
        self.assertIsNotNone(self.children[0].poll())

    def test_lost_acquire_response_reconciles_the_exact_active_pin(self):
        with self.assertRaisesRegex(ValueError,'acquire response'): self.execute('acquire','active')
        self.assertEqual(self.writer()['state'],'joined')
        self.assertEqual(self.calls,['acquire','inspect','suspend','close'])
        self.assertIsNotNone(self.children[0].poll())

    def test_unavailable_acquire_status_retains_the_writer_after_join(self):
        with self.assertRaisesRegex(source.ValidationError,'unresolved'): self.execute('acquire','error')
        self.assertEqual(self.writer()['state'],'running')
        self.assertNotIn('close',self.calls)
        self.assertIsNotNone(self.children[0].poll())

    def test_unstarted_reconciliation_requires_exact_dead_bootstrap_and_no_source_tools(self):
        native=Path(self.config['native_state'])
        state=self.root/'refused';state.mkdir()
        config=dict(self.config,assignment=self.assignment)
        atomic(state/'bridge-config.json',config)
        atomic(self.args[1],self.config)
        writer=Path(self.config['state_directory'])/'own-native.writer.json'
        writer.parent.mkdir(exist_ok=True)
        atomic(writer,{'state':'intent','assignment':self.assignment,'evidence':str(state)})
        atomic(native/'native-owner.json',{'role':'worker','workspace':self.config['workspace'],'native':'own-native'})
        child=subprocess.Popen([sys.executable,'-c','pass']);child.wait(timeout=5)
        intent={'state':'terminal','client_id':'bootstrap-unique','pid':child.pid}
        atomic(native/'input-intent.json',intent)
        atomic(native/'native-join.json',{'pid':child.pid,'joined':True,'exit_code':0})
        with patch.object(source,'validate_file',return_value=self.assignment):
            atomic(native/'input-intent.json',dict(intent,state='submitted'))
            with self.assertRaisesRegex(source.ValidationError,'may have started'):
                source.reconcile(*self.args,state)
            atomic(native/'input-intent.json',intent)
            atomic(state/'operations.json',[])
            with self.assertRaisesRegex(source.ValidationError,'tool custody'):
                source.reconcile(*self.args,state)
            (state/'operations.json').unlink()
            result=source.reconcile(*self.args,state)
        self.assertEqual(result['status'],'reconciled-unstarted')
        self.assertEqual(json.loads(writer.read_text())['state'],'joined')
        self.assertTrue(json.loads((state/'join.json').read_text())['source_not_started'])

    def test_mutable_source_cannot_contain_runtime_credentials_or_custody(self):
        work=Path(self.config['workspace']);work.mkdir()
        config=dict(self.config,execution_mode='native-cli-source',client_executable='/bin/true',
            coordination_executable='/bin/true',model='fixture',effort='medium',provider='openai',
            approval_policy='never',sandbox='danger-full-access',tool_runtime={'executable':'/bin/true',
                'context':'fixture','daemon_id':'fixture','image':'sha256:'+'0'*64})
        atomic(self.args[1],config)
        self.assertEqual(source.source_config(self.args[1])['runtime'],'codex')
        for key in ('native_state','state_directory','coordination_home','ledger_directory','prompt_file','auth_file'):
            with self.subTest(key=key):
                atomic(self.args[1],dict(config,**{key:str(work/'untrusted')}))
                with self.assertRaisesRegex(source.ValidationError,'outside mutable source'):
                    source.source_config(self.args[1])

    def test_foreign_remote_and_identity_never_override_the_selected_source_ledger(self):
        with patch.dict(os.environ, {'SQUAD_REMOTE_URL':'https://foreign.invalid','SQUAD_REMOTE_TOKEN':'fixture',
                                    'SQUAD_REQUEST_ID':'foreign','CODEX_THREAD_ID':'foreign'}):
            for runtime in ('codex','muse','claude'):
                env=child_environment(dict(self.config,runtime=runtime))
                self.assertEqual(env['SQUAD_HOME'],self.config['coordination_home'])
                self.assertEqual(env['SQUAD_AGENT'],'own-worker')
                for key in ('SQUAD_REMOTE_URL','SQUAD_REMOTE_TOKEN','SQUAD_REQUEST_ID','CODEX_THREAD_ID'):
                    self.assertNotIn(key,env)


if __name__ == '__main__': unittest.main()
