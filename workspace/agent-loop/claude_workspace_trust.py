"""Claude's documented exact-project trust entry; no permission-mode changes."""
from contextlib import contextmanager
import threading
import time
import json
import os
from pathlib import Path
import tempfile


@contextmanager
def native_config_lock(path):
    # Claude 2.1.288 uses a mkdir lock at <config>.lock, refreshed by mtime.
    # Join that protocol; a separate flock does not exclude native writers.
    lock = Path(str(path) + '.lock')
    deadline = time.monotonic() + 3
    while True:
        try:
            lock.mkdir(mode=0o700)
            break
        except FileExistsError:
            if time.monotonic() >= deadline:
                raise ValueError('Claude configuration busy; no trust change or client launch')
            time.sleep(.05)
    identity = lock.stat()
    stopped = threading.Event()
    compromised = threading.Event()
    def owned():
        try:
            value = lock.lstat()
            return not lock.is_symlink() and (value.st_dev, value.st_ino) == (identity.st_dev, identity.st_ino)
        except OSError:
            return False
    def refresh():
        while not stopped.wait(1):
            try:
                if not owned():
                    compromised.set(); return
                os.utime(lock, None)
            except OSError:
                compromised.set(); return
    thread = threading.Thread(target=refresh, daemon=True)
    thread.start()
    def verify():
        if compromised.is_set() or not owned():
            raise ValueError('Claude configuration lock changed; no trust write admitted')
    try:
        yield verify
    finally:
        stopped.set(); thread.join()
        if owned(): lock.rmdir()


def config_path(env):
    if env.get('CLAUDE_CONFIG_DIR'):
        return Path(env['CLAUDE_CONFIG_DIR']).expanduser() / '.claude.json'
    return Path(env.get('HOME', str(Path.home()))) / '.claude.json'


def read(path):
    if path.is_symlink():
        raise ValueError('Claude trust configuration must not be a symlink')
    raw = path.read_bytes() if path.exists() else None
    data = json.loads(raw) if raw is not None else {}
    if not isinstance(data, dict) or not isinstance(data.get('projects', {}), dict):
        raise ValueError('invalid Claude trust configuration')
    return raw, data


def workspace_trust(worktree, env, establish=False):
    work = Path(worktree).resolve(strict=True)
    if work == Path(work.anchor) or work == Path(env.get('HOME', str(Path.home()))).resolve():
        raise ValueError('cannot grant root or home workspace trust')
    path = config_path(env)
    def inspect():
        raw, data = read(path)
        project = data.get('projects', {}).get(str(work), {})
        if not isinstance(project, dict):
            raise ValueError('invalid Claude project trust entry')
        return raw, data, project
    raw, data, project = inspect()
    status = 'established' if project.get('hasTrustDialogAccepted') is True else 'will-establish'
    if establish and status != 'established':
        path.parent.mkdir(parents=True, exist_ok=True)
        with native_config_lock(path) as verify_lock:
            raw, data, project = inspect()
            data.setdefault('projects', {})[str(work)] = dict(project, hasTrustDialogAccepted=True)
            fd, name = tempfile.mkstemp(prefix='.squad-trust-', dir=path.parent)
            try:
                with os.fdopen(fd, 'w') as out:
                    json.dump(data, out, indent=2)
                    out.write('\n'); out.flush(); os.fsync(out.fileno())
                if read(path)[0] != raw:
                    raise ValueError('Claude configuration changed during trust setup; retry before launch')
                verify_lock()
                os.replace(name, path)
            finally:
                if os.path.exists(name): os.unlink(name)
            if inspect()[2].get('hasTrustDialogAccepted') is not True:
                raise ValueError('Claude workspace trust readback failed')
            status = 'established'
    return {'status': status, 'workspace': str(work), 'scope': 'exact-project', 'config_file': str(path)}
