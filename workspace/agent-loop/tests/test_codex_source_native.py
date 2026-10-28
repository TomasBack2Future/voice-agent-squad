"""Opt-in original CLI source/new/resume/hold proof on isolated Squad state."""
import json
import os
from pathlib import Path
import shlex
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

ROOT = Path(__file__).parents[1]
sys.path.insert(0, str(ROOT))
from codex_source_worker import prepare, run
from codex_native_host import NativeCLI
from validate_context_package import ValidationError
from muse_session_host import atomic
from muse_worker_tools import child_environment


def fixture(selected):
    root = Path(tempfile.mkdtemp(prefix=selected.get('runtime', 'codex') + '-source-', dir=selected['state_directory'])).resolve()
    work = root / 'work'; ledger = root / 'ledger'; home = root / 'coordination-home'
    for path in (work, ledger, home): path.mkdir(mode=0o700)
    def git(*args):
        return subprocess.check_output(['git', '-c', 'core.hooksPath=/dev/null', *args], cwd=work,
                                       text=True, stderr=subprocess.DEVNULL).strip()
    git('init', '-b', 'qualification'); git('config', 'user.name', 'Qualification')
    git('config', 'user.email', 'qualification@example.invalid')
    git('remote', 'add', 'origin', 'git@github.com:TomasBack2Future/voice-agent-squad.git')
    (work / 'AGENTS.md').write_text('Isolated Squad source fixture; no external or business operations.\n')
    git('add', 'AGENTS.md'); git('commit', '-m', 'fixture')
    subprocess.run(['git', '-c', 'core.hooksPath=/dev/null', 'init', str(ledger)], capture_output=True, check=True)
    config = dict(execution_mode='native-cli-source', runtime='codex', role='worker',
        client_executable=selected['client_executable'], coordination_executable=selected['coordination_executable'],
        tool_runtime=selected['tool_runtime'], ledger_directory=str(ledger), coordination_home=str(home),
        workspace=str(work), state_directory=str(root / 'execution'), native_state=str(root / 'native'),
        model='gpt-6.1-sol', effort='medium', provider='openai', approval_policy='never', sandbox='danger-full-access',
        agent_id='qualification-worker', dispatcher_agent_id='qualification-controller',
        controller_native='qualification-controller-native', controller_epoch=1, claim_generation=1,
        prompt_file=str(root / 'prompt.txt'), max_seconds=360)
    config.update({key: selected[key] for key in ('runtime', 'model', 'effort', 'provider',
        'approval_policy', 'sandbox', 'permission_mode', 'contributor_mode') if key in selected})
    atomic(root / 'config.json', config); prepare(root / 'config.json')
    config = json.loads((root / 'config.json').read_text())
    def squad(actor, *args):
        env = child_environment(dict(config, agent_id=actor,
            native_session_id=config['controller_native'] if actor == config['dispatcher_agent_id'] else config['native_session_id']))
        result = subprocess.run([config['coordination_executable'], *args], cwd=ledger, env=env,
                                capture_output=True, text=True, timeout=15)
        if result.returncode: raise RuntimeError(result.stderr)
        return result.stdout
    squad(config['dispatcher_agent_id'], 'init', '--yes')
    path = ledger / '.squad/config.yaml'
    path.write_text(path.read_text().replace('default_worktree_per_claim: true', 'default_worktree_per_claim: false'))
    for actor in (config['dispatcher_agent_id'], config['agent_id']): squad(actor, 'register', '--as', actor)
    squad(config['dispatcher_agent_id'], 'dispatch', 'reserve', 'QUALIFY-CODEX', '--source', 'github:TomasBack2Future/voice-agent-squad#109')
    squad(config['dispatcher_agent_id'], 'dispatch', 'controller-bind', '--native-session', config['controller_native'])
    squad(config['dispatcher_agent_id'], 'new', 'BUG', 'Isolated native source fixture', '--ready')
    squad(config['dispatcher_agent_id'], 'dispatch', 'attach', 'QUALIFY-CODEX', '--item', 'BUG-001', '--generation', '1')
    squad(config['dispatcher_agent_id'], 'dispatch', 'bind', 'QUALIFY-CODEX', '--thread-id', config['native_session_id'], '--generation', '1', '--supervised')
    squad(config['agent_id'], 'claim', 'BUG-001', '--long')
    profile = dict(schema_version='agent-loop.project-profile.v1', id='codex-qualification', version=1,
                   repository='TomasBack2Future/voice-agent-squad', context_by_phase={}, gates={}, resources={}, risk_probes=[])
    atomic(root / 'profile.json', profile)
    assignment = dict(schema_version='agent-loop.assignment.v1', assignment_id='codex/qualification/1',
        issue='TomasBack2Future/voice-agent-squad#109', item='BUG-001', reservation={'key': 'QUALIFY-CODEX', 'generation': 1},
        repository=profile['repository'], worktree=str(work), branch='qualification', base_sha=git('rev-parse', 'HEAD'),
        role={'id': 'worker', 'skill': 'agent-loop-worker', 'version': 1},
        project_profile={'id': profile['id'], 'version': 1, 'path': str(root / 'profile.json')},
        authorization={'source_mutation': True, 'pull_request': False, 'merge': False, 'staging': False,
                       'production': False, 'issue_close': False}, evidence_required=['test', 'manual'])
    atomic(root / 'assignment.json', assignment)
    def decide(action, revision):
        squad(config['dispatcher_agent_id'], 'milestone', '--to', 'BUG-001', 'Isolated qualification ' + action)
        with sqlite3.connect(home / 'global.db') as db:
            outcome = db.execute("SELECT max(id) FROM messages WHERE agent_id=? AND thread='BUG-001'", (config['dispatcher_agent_id'],)).fetchone()[0]
        argv = ['terminal-events', 'decision-set', '--reservation', 'QUALIFY-CODEX', '--generation', '1',
                '--worker-session', config['native_session_id'], '--expected-revision', str(revision),
                '--outcome', str(outcome), '--action', action]
        if action == 'hold': argv += ['--condition', 'Isolated hold assertion']
        return json.loads(squad(config['dispatcher_agent_id'], *argv))
    decide('proceed', 0)
    return root, work, home, config, assignment, squad, decide


@unittest.skipUnless(os.environ.get('CODEX_SOURCE_QUALIFICATION'), 'explicit isolated current Codex source qualification required')
class CodexSourceNativeTests(unittest.TestCase):
    def test_source_resume_decision_and_inflight_hold(self):
        selected = json.loads(Path(os.environ['CODEX_SOURCE_QUALIFICATION']).read_text())
        root, work, home, config, assignment, squad, decide = fixture(selected)
        results = []
        atomic(root / 'qualification.json', {'status': 'running', 'native': config['native_session_id']})
        Path(config['prompt_file']).write_text('Create proof.py containing def add(a,b): return a+b with write_file. '
            'Run python3 -c \'from proof import add; assert add(2,3)==5; print("NATIVE_CODEX_SOURCE_OK")\' '
            'with run_command. Record actual results in report. No external operations.')
        first = run(root / 'assignment.json', root / 'config.json'); results.append(first)
        self.assertEqual(first['status'], 'completed'); self.assertIn('return a+b', (work / 'proof.py').read_text())
        before = (work / 'proof.py').read_bytes()
        Path(config['prompt_file']).write_text('Resume the same task. Preserve proof.py, rerun its add(2,3)==5 assertion '
            'with run_command and record actual verification in report. No source rewrites or external operations.')
        second = run(root / 'assignment.json', root / 'config.json', resume=True); results.append(second)
        self.assertEqual(second['status'], 'completed'); self.assertEqual(second['native'], first['native'])
        self.assertEqual((work / 'proof.py').read_bytes(), before)
        previous = {p.parent for p in Path(config['state_directory']).glob('*/operations.json')}
        stopped = threading.Event(); errors = []
        def hold():
            try:
                deadline = time.monotonic() + 180
                while not stopped.is_set() and time.monotonic() < deadline:
                    for path in Path(config['state_directory']).glob('*/operations.json'):
                        if path.parent not in previous and any(op['state'] == 'running' for op in json.loads(path.read_text())):
                            decide('hold', 1); return
                    time.sleep(.1)
                raise AssertionError('no owned running command observed')
            except BaseException as error: errors.append(str(error))
        observer = threading.Thread(target=hold); observer.start()
        Path(config['prompt_file']).write_text('Run exactly sleep 60 with run_command and timeout_seconds 120 as an in-flight hold test. '
            'An external fixture will HOLD this command. After rejection, use events and decision_get, acknowledge the hold and report blocked. '
            'Do not retry the command, mutate source, or work around the hold.')
        try: held = run(root / 'assignment.json', root / 'config.json', resume=True); results.append(held)
        finally: stopped.set(); observer.join(timeout=5)
        self.assertFalse(observer.is_alive()); self.assertEqual(errors, [])
        self.assertEqual(held['status'], 'blocked')
        self.assertEqual((work / 'proof.py').read_bytes(), before)
        for result in results:
            state = Path(result['evidence'])
            self.assertTrue(json.loads((state / 'join.json').read_text())['joined'])
            self.assertEqual(set(json.loads((state / 'startup-loaded.json').read_text())['loaded']), {'role', 'profile', 'lifecycle'})
        with sqlite3.connect(home / 'global.db') as db:
            self.assertEqual(db.execute("SELECT count(*) FROM execution_authorizations WHERE state='active'").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT count(*) FROM terminal_event_receipts WHERE kind='decision-resolved' AND processed_at=0").fetchone()[0], 0)
        atomic(root / 'qualification.json', {'status': 'passed', 'native': config['native_session_id'], 'runs': results,
               'source_test': True, 'same_native_resume': True, 'hold_handled': True, 'owned_tools_joined': True})
        print(json.dumps({'evidence': str(root), 'status': 'passed'}))

    def test_interrupted_native_recovers_without_an_old_model_handoff(self):
        selected = json.loads(Path(os.environ['CODEX_SOURCE_QUALIFICATION']).read_text())
        root, work, home, config, assignment, squad, decide = fixture(selected)
        Path(config['prompt_file']).write_text('Use run_command to execute exactly sleep 60 with timeout_seconds 120. '
            'This isolated test will stop the model process while the owned command runs. Do not launch background work.')
        original_turn = NativeCLI.turn
        children = []; failures = []; stopped = threading.Event()
        def capture(host, *args, **kwargs):
            result = original_turn(host, *args, **kwargs)
            children.append(host.child)
            return result
        def interrupt():
            try:
                deadline = time.monotonic() + 180
                while time.monotonic() < deadline and not stopped.is_set():
                    for path in Path(config['state_directory']).glob('*/operations.json'):
                        if children and any(op['state'] == 'running' for op in json.loads(path.read_text())):
                            children[0].terminate(); return
                    time.sleep(.1)
                raise AssertionError('no running source command observed before interruption')
            except BaseException as error: failures.append(str(error))
        observer = threading.Thread(target=interrupt); observer.start()
        try:
            with patch('codex_source_worker.NativeCLI.turn', side_effect=capture, autospec=True):
                with self.assertRaises(ValidationError):
                    run(root / 'assignment.json', root / 'config.json')
        finally: stopped.set(); observer.join(timeout=5)
        self.assertFalse(observer.is_alive()); self.assertEqual(failures, [])
        self.assertIsNotNone(children[0].poll())
        original_state = next(Path(config['state_directory']).glob('*/join.json')).parent
        self.assertTrue(json.loads((original_state / 'join.json').read_text())['joined'])
        self.assertFalse((original_state / 'report.json').exists())
        Path(config['prompt_file']).write_text('Resume this same native after the interrupted source command. '
            'Do not rerun sleep. Create recovery.py containing value = 7 with write_file. '
            'Run python3 -c \'from recovery import value; assert value == 7; print("NATIVE_RECOVERY_OK")\' '
            'with run_command and report actual verification. No old-model handoff is available or required.')
        recovered = run(root / 'assignment.json', root / 'config.json', resume=True)
        self.assertEqual(recovered['native'], config['native_session_id'])
        self.assertEqual(recovered['status'], 'completed')
        self.assertEqual((work / 'recovery.py').read_text().strip(), 'value = 7')
        with sqlite3.connect(home / 'global.db') as db:
            self.assertEqual(db.execute("SELECT count(*) FROM execution_authorizations WHERE state='active'").fetchone()[0], 0)
        atomic(root / 'recovery-qualification.json', {'status': 'passed', 'native': config['native_session_id'],
               'interrupted_execution': str(original_state), 'recovered': recovered, 'old_model_handoff': False})
        print(json.dumps({'evidence': str(root), 'recovery': 'passed'}))


if __name__ == '__main__': unittest.main()
