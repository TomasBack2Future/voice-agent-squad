"""Kernel-contained source commands; only exact retained Docker handles may join."""
import json
from pathlib import Path
import subprocess
from claude_worker_launcher import diagnostic


class Runtime:
    def __init__(self, config):
        self.c = config
        self.runtime = config['tool_runtime']
        self.argv = [self.runtime['executable'], '--context', self.runtime['context']]
        result = self.call(['info', '--format', '{{json .}}'])
        info = json.loads(result)
        if (info.get('ID') != self.runtime['daemon_id'] or info.get('OSType') != 'linux'
                or info.get('CgroupVersion') != '2'):
            raise ValueError('qualified Linux container daemon identity changed')
        image = json.loads(self.call(['image', 'inspect', self.runtime['image']]))[0]
        if image['Id'] != self.runtime['image']:
            raise ValueError('immutable tool image changed')

    def call(self, args, timeout=15):
        result = subprocess.run([*self.argv, *args], capture_output=True, text=True, timeout=timeout)
        if result.returncode:
            raise ValueError('owned container operation failed: ' + diagnostic(result.stderr))
        return result.stdout

    def inspect(self, op):
        value = json.loads(self.call(['container', 'inspect', op['container']]))[0]
        labels = value.get('Config', {}).get('Labels', {})
        if value['Id'] != op['container'] or labels.get('squad.execution') != self.c['execution_id']:
            raise ValueError('container custody identity changed')
        return value

    def joined(self, op):
        value = self.inspect(op)
        return value['State']['Status'] in ('exited', 'created') and not value['State']['Running'] and value['State']['Pid'] == 0

    def stop(self, op):
        self.inspect(op)
        self.call(['container', 'stop', '--time', '5', op['container']], timeout=15)
        return self.joined(op)

    def create(self, op, command, cwd):
        workspace = str(Path(self.c['workspace']).resolve())
        cwd = Path(cwd).resolve(strict=True)
        if not cwd.is_relative_to(Path(workspace)):
            raise ValueError('source commands must stay in the assigned workspace')
        # No engine socket, host PID namespace, privilege, credential home or
        # ledger mount. setsid/reparent/env-clearing cannot leave this cgroup.
        args = ['container', 'create', '--name', op['name'], '--label', 'squad.execution=' + self.c['execution_id'],
                '--init', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--pids-limit', '512',
                '--mount', 'type=bind,source=' + workspace + ',target=' + workspace,
                '--workdir', str(cwd), '--env', 'HOME=/tmp', '--entrypoint', '/bin/sh', self.runtime['image'], '-c', command]
        result = self.call(args).strip()
        if len(result) != 64 or any(ch not in '0123456789abcdef' for ch in result):
            raise ValueError('container creation receipt unavailable; custody retained')
        op['container'] = result
        self.inspect(op)
