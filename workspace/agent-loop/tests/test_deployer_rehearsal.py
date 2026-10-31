"""Isolated Deployer rehearsal: launch -> bind -> claim -> decision CAS -> wake -> readback.

Everything runs against a real squad binary built from this checkout, a
temporary fixture ledger (a fixture ENV-002 item and policy, no live ledger or
environment) and a scripted stand-in for the Claude client. The stand-in does
what the real client does with the generated settings: it fires the PreToolUse
fence and the asyncRewake receiver hooks as its own children. Nothing here
qualifies a native Claude model session or installs anything.
"""
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import time
import unittest

ROOT = Path(__file__).parents[1]
sys.path.insert(0, str(ROOT))
import claude_worker_launcher as launcher

SESSION = '00000000-0000-4000-8000-0000000000d1'
SKILL_PATH = '.agents/skills/studio-deploy-production/SKILL.md'
SKILL_TEXT = b'---\nname: studio-deploy-production\ndescription: fixture release skill\n---\nRelease only the frozen candidate.\n'

CLIENT = '''#!{python}
"""Scripted client: fires the generated hooks the way Claude would, then reads back its decision."""
import json, os, pathlib, shlex, subprocess, sys

state = pathlib.Path({state!r})
squad = {squad!r}
args = sys.argv[1:]
native = args[args.index('--session-id') + 1]
settings = json.loads(pathlib.Path(args[args.index('--settings') + 1]).read_text())
(state / 'argv.json').write_text(json.dumps(args[:-1]))


def argv(hook):
    return [hook['command'], *hook['args']] if 'args' in hook else shlex.split(hook['command'])


def start(event, payload):
    return [(h, subprocess.Popen(argv(h), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True))
            for group in settings['hooks'].get(event, []) for h in group['hooks']]


def feed(proc, payload):
    proc.stdin.write(json.dumps(payload)); proc.stdin.close()


def pre_tool(tool, command=''):
    proc = start('PreToolUse', None)
    codes = []
    for hook, p in proc:
        feed(p, dict(session_id=native, hook_event_name='PreToolUse', tool_name=tool, tool_input=dict(command=command)))
        p.communicate(timeout=30)
        codes.append(p.returncode)
    return max(codes)


# SessionStart arms the asyncRewake receiver; it exits 2 with the wake text when an event is delivered.
receivers = [p for hook, p in start('SessionStart', None) if hook.get('asyncRewake')]
for p in receivers:
    feed(p, dict(session_id=native, hook_event_name='SessionStart'))

result = dict(roles_seen=sorted(settings['hooks']))
result['edit_before_claim'] = pre_tool('Edit')
result['env_claim_before_item'] = pre_tool('Bash', squad + ' claim ENV-002 --intent release')
result['item_claim_fence'] = pre_tool('Bash', squad + ' claim REL-001 --intent release')
claim = subprocess.run([squad, 'claim', 'REL-001', '--intent', 'release'], capture_output=True, text=True, cwd={ledger!r})
result['item_claim'] = claim.returncode
result['item_claim_detail'] = claim.stderr[-300:]
result['env_claim_fence'] = pre_tool('Bash', squad + ' claim ENV-002 --intent release')
claim = subprocess.run([squad, 'claim', 'ENV-002', '--intent', 'release'], capture_output=True, text=True, cwd={ledger!r})
result['env_claim'] = claim.returncode
result['env_claim_detail'] = claim.stderr[-300:]
(state / 'claimed.json').write_text(json.dumps(result))

wake = receivers[0]
try:
    _, stderr = wake.communicate(timeout=60)
except subprocess.TimeoutExpired:
    wake.kill()
    _, stderr = wake.communicate()
result['wake_exit'] = wake.returncode
result['wake_text'] = stderr
event = json.loads(stderr.splitlines()[1])['events'][0]['event_id'] if wake.returncode == 2 else None
decision = subprocess.run([squad, 'terminal-events', 'decision-get', '--reservation', {reservation!r}, '--generation', '1',
                           '--worker-session', native], capture_output=True, text=True, cwd={ledger!r})
result['decision'] = json.loads(decision.stdout) if decision.returncode == 0 else None
if event:
    ack = subprocess.run([squad, 'terminal-events', 'ack', event, '--native-session', native, '--note', 'rehearsal readback'],
                         capture_output=True, text=True, cwd={ledger!r})
    result['ack_exit'] = ack.returncode
result['event_id'] = event
(state / 'result.json').write_text(json.dumps(result))
'''


class DeployerRehearsalTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build = tempfile.TemporaryDirectory()
        cls.binary = Path(cls.build.name) / 'squad'
        subprocess.run(['go', 'build', '-o', str(cls.binary), './cmd/squad'], cwd=ROOT.parents[1], check=True, capture_output=True)

    @classmethod
    def tearDownClass(cls):
        cls.build.cleanup()

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.env = {k: v for k, v in os.environ.items() if not k.startswith('SQUAD_') and k not in (
            'CODEX_THREAD_ID', 'CODEX_SESSION_ID', 'CLAUDE_SESSION_ID', 'MUSE_SESSION_ID')}
        self.env.update(SQUAD_HOME=str(self.root / 'home'), SQUAD_NO_AUTO_DAEMON='1', SQUAD_NO_BROWSER='1',
                        SQUAD_NO_HYGIENE='1', PYTHONDONTWRITEBYTECODE='1')
        self.ledger = self.root / 'ledger'
        self.ledger.mkdir()
        self.git(self.ledger, 'init', '-q')
        self.dispatcher('init', '--yes')
        self.dispatcher('register', '--as', 'dispatcher')
        config = self.ledger / '.squad/config.yaml'
        text = config.read_text().replace('default_worktree_per_claim: true', 'default_worktree_per_claim: false')
        text = text.replace('claim_concurrency: 1', 'claim_concurrency: 3').replace('  - CHORE\n', '  - CHORE\n  - ENV\n  - REL\n', 1)
        config.write_text(text)
        self.dispatcher('new', 'REL', 'Fixture release', '--ready')
        self.dispatcher('new', 'ENV', 'Fixture production environment', '--ready')
        items = self.ledger / '.squad/items'
        env_item = next(items.glob('ENV-001-*.md'))
        env_item.write_text(env_item.read_text().replace('id: ENV-001', 'id: ENV-002'))
        env_item.rename(items / env_item.name.replace('ENV-001', 'ENV-002'))
        policy = self.root / 'policy.json'
        policy.write_text(json.dumps([{'item_id': 'ENV-002', 'group': 'studio-production', 'default_scope': '*', 'service_scope': 'studio'}]))
        self.dispatcher('resources', 'define', str(policy))
        self.dispatcher('dispatch', 'reserve', 'REL-D1', '--source', 'release:rehearsal-1')
        self.dispatcher('dispatch', 'controller-bind', '--native-session', 'dispatcher-native')
        self.dispatcher('dispatch', 'attach', 'REL-D1', '--item', 'REL-001', '--generation', '1')

        self.work = self.root / 'release'
        self.work.mkdir()
        self.git(self.work, 'init', '-q', '-b', 'release')
        self.git(self.work, 'config', 'user.name', 'Test')
        self.git(self.work, 'config', 'user.email', 'test@example.invalid')
        (self.work / 'AGENTS.md').write_text('Isolated release checkout.\n')
        (self.work / SKILL_PATH).parent.mkdir(parents=True)
        (self.work / SKILL_PATH).write_bytes(SKILL_TEXT)
        self.git(self.work, 'add', '-A')
        self.git(self.work, 'commit', '-q', '-m', 'initial')
        self.git(self.work, 'remote', 'add', 'origin', 'git@github.com:TomasBack2Future/voice-agent-studio.git')
        sha = self.git(self.work, 'rev-parse', 'HEAD')
        assignment = json.loads((ROOT / 'examples/assignment.studio-deployer.json').read_text())
        assignment.update(worktree=str(self.work), branch='release', base_sha=sha, item='REL-001',
                          project_profile=dict(assignment['project_profile'], path=str(ROOT / 'projects/studio/profile.json')))
        assignment['reservation']['key'] = 'REL-D1'
        assignment['release'].update(id='rehearsal-1', repositories=[dict(repository='TomasBack2Future/voice-agent-studio', sha=sha)])
        self.assignment_path = self.root / 'assignment.json'
        self.assignment_path.write_text(json.dumps(assignment))

        self.state = self.root / 'client-state'
        self.state.mkdir()
        self.client = self.root / 'claude-stand-in'
        self.client.write_text(CLIENT.format(python=sys.executable, state=str(self.state), squad=str(self.binary),
                                             reservation='REL-D1', ledger=str(self.ledger)))
        self.client.chmod(0o700)
        prompt = self.root / 'prompt.txt'
        prompt.write_text('Release rehearsal only. No production action.')
        self.config = {'schema_version': 'agent-loop.claude-launch.v1', 'native_session_id': SESSION, 'agent_id': 'deployer',
                       'dispatcher_agent_id': 'dispatcher', 'coordination_executable': str(self.binary),
                       'event_executable': str(self.binary), 'coordination_mode': 'native', 'ledger_directory': str(self.ledger),
                       'client_executable': str(self.client), 'permission_mode': 'bypassPermissions',
                       'model': 'claude-sonnet-5-5', 'effort': 'high', 'prompt_file': str(prompt)}
        self.config_path = self.root / 'launch.json'
        self.config_path.write_text(json.dumps(self.config))
        self.deployer_env = launcher.child_environment(self.config)
        self.deployer_env.update({k: v for k, v in self.env.items() if k.startswith('SQUAD_') and k not in self.deployer_env})
        self.deployer_env['SQUAD_HOME'] = self.env['SQUAD_HOME']
        self.run_squad(self.deployer_env, 'register', '--as', 'deployer')

    def git(self, cwd, *args):
        return subprocess.run(['git', '-C', str(cwd), *args], check=True, capture_output=True, text=True).stdout.strip()

    def run_squad(self, env, *args):
        result = subprocess.run([str(self.binary), *args], cwd=self.ledger, env=env, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout

    def dispatcher(self, *args):
        return self.run_squad(dict(self.env, SQUAD_AGENT='dispatcher', SQUAD_SESSION_ID='test:dispatcher'), *args)

    def launcher_command(self, check=False):
        argv = [sys.executable, str(ROOT / 'claude_worker_launcher.py'), '--assignment', str(self.assignment_path),
                '--config', str(self.config_path), '--wait-seconds', '0']
        return argv + ['--check'] if check else argv

    def wait_for(self, name, process):
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            if (self.state / name).exists():
                return json.loads((self.state / name).read_text())
            if process.poll() is not None and not (self.state / name).exists():
                break
            time.sleep(.1)
        self.fail(f'{name} not produced; launcher exit={process.poll()}')

    def test_launch_bind_claim_decision_wake_readback_as_deployer(self):
        # Launch preflight before binding: pending, ENV read-only and ready, no client, no claim.
        before = subprocess.run(self.launcher_command(check=True), env=self.env, capture_output=True, text=True, timeout=30)
        self.assertEqual(before.returncode, 0, before.stderr)
        receipt = json.loads(before.stdout)
        self.assertEqual((receipt['role'], receipt['binding']), ('deployer', 'pending'))
        self.assertEqual([(r['item'], r['status']) for r in receipt['resources']], [('ENV-002', 'ready')])
        self.assertFalse((self.state / 'argv.json').exists())

        # An unbound reservation starts no client.
        unbound = subprocess.run(self.launcher_command(), env=self.env, capture_output=True, text=True, timeout=30)
        self.assertEqual(unbound.returncode, 2, unbound.stderr)
        self.assertFalse((self.state / 'argv.json').exists())

        self.dispatcher('dispatch', 'bind', 'REL-D1', '--thread-id', SESSION, '--generation', '1', '--supervised')
        bound = json.loads(subprocess.run(self.launcher_command(check=True), env=self.env, capture_output=True, text=True, timeout=30).stdout)
        self.assertEqual(bound['binding'], 'bound')

        process = subprocess.Popen(self.launcher_command(), env=self.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        def stop():
            if process.poll() is None:
                process.kill()
            process.communicate(timeout=10)
        self.addCleanup(stop)
        claimed = self.wait_for('claimed.json', process)

        # Claim fence: before the first claim only the canonical item may be claimed; ENV follows.
        self.assertEqual(claimed['edit_before_claim'], 2)
        self.assertEqual(claimed['env_claim_before_item'], 2)
        self.assertEqual((claimed['item_claim_fence'], claimed['item_claim']), (0, 0))
        self.assertEqual((claimed['env_claim_fence'], claimed['env_claim']), (0, 0))
        db = sqlite3.connect(self.root / 'home/global.db')
        self.addCleanup(db.close)
        held = db.execute("SELECT item_id,agent_id FROM claims WHERE state='held' ORDER BY item_id").fetchall()
        self.assertEqual(held, [('ENV-002', 'deployer'), ('REL-001', 'deployer')])
        observed = db.execute('SELECT actor FROM worker_native_hook_observations WHERE native_session=?', (SESSION,)).fetchall()
        self.assertEqual(observed, [('deployer',)])

        # Decision CAS by the Dispatcher; the receiver wakes the Deployer's session.
        self.dispatcher('milestone', '--to', 'REL-001', 'Dispatcher proceed; rehearsal fixture only.')
        outcome = db.execute("SELECT max(id) FROM messages WHERE agent_id='dispatcher' AND thread='REL-001'").fetchone()[0]
        self.dispatcher('terminal-events', 'decision-set', '--reservation', 'REL-D1', '--generation', '1', '--worker-session', SESSION,
                        '--expected-revision', '0', '--outcome', str(outcome), '--action', 'proceed')
        result = self.wait_for('result.json', process)
        _, launcher_stderr = process.communicate(timeout=30)
        self.assertEqual(process.returncode, 0, launcher_stderr)

        # Wake text is the Deployer's, readback matches the CAS, and the event is acknowledged by the owner.
        self.assertEqual(result['wake_exit'], 2)
        self.assertIn('execution binding', result['wake_text'])
        self.assertIn('not production authority', result['wake_text'])
        self.assertNotIn('Invoke $squad-dispatcher', result['wake_text'])
        self.assertEqual((result['decision']['revision'], result['decision']['action']), (1, 'proceed'))
        self.assertEqual(result['ack_exit'], 0)
        self.assertIn('decision-resolved', result['event_id'])

        # The launched client carried the explicit selection and the Deployer's own receiver configuration.
        args = json.loads((self.state / 'argv.json').read_text())
        self.assertEqual(args[:4], ['--session-id', SESSION, '--permission-mode', 'bypassPermissions'])
        self.assertEqual(args[4:8], ['--model', 'claude-sonnet-5-5', '--effort', 'high'])
        receiver = json.loads((self.root / f'receiver-{SESSION}/config.json').read_text())
        self.assertEqual((receiver['role'], receiver['runtime'], receiver['legacy_native_fence']), ('deployer', 'claude', True))


if __name__ == '__main__':
    unittest.main()
