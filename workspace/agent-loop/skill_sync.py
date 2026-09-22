#!/usr/bin/env python3
"""Opt-in Git hooks for repository skills; standard library, no Squad ledger access."""
import argparse
import contextlib
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tempfile

CLIENTS = ('.agents', '.codex', '.claude')
HOOKS = ('post-merge', 'post-checkout', 'post-rewrite')
MARKER = '# squad-local-skill-sync-v1'


def git(repo, *args):
    return subprocess.check_output(['git', '-C', str(repo), *args], stderr=subprocess.PIPE)


def write_json(path, value):
    atomic_write(path, (json.dumps(value, indent=2) + '\n').encode())


def atomic_write(path, data, mode=0o644):
    fd, temp = tempfile.mkstemp(dir=path.parent, prefix='.sync-')
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
        os.chmod(temp, mode)
        os.replace(temp, path)
    finally:
        if os.path.lexists(temp):
            os.unlink(temp)


@contextlib.contextmanager
def locked(target):
    for client in CLIENTS:
        if (target / client).is_symlink() or (target / client / 'skills').is_symlink():
            raise ValueError('client directory is a symlink; refusing to follow it')
    state = target / '.agents' / 'squad-skill-sync'
    state.mkdir(parents=True, exist_ok=True)
    with (state / 'lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        yield state


def tracked_package(repo, source, sha):
    """Read only committed regular files, preserving the whole reference tree."""
    files = {}
    entries = git(repo, 'ls-tree', '-rz', sha, '--', source).split(b'\0')
    for entry in filter(None, entries):
        meta, raw_path = entry.split(b'\t', 1)
        mode, kind, oid = meta.decode().split()
        path = Path(raw_path.decode()).relative_to(source)
        if mode not in ('100644', '100755') or kind != 'blob':
            raise ValueError(f'package contains unsupported symlink/submodule: {path}')
        files[str(path)] = (git(repo, 'cat-file', 'blob', oid), mode)
    if not files:
        raise ValueError('source has no committed files')
    skills = {}
    for path, (data, _) in files.items():
        if Path(path).name != 'SKILL.md':
            continue
        front = data.decode().split('---', 2)
        match = re.search(r'^name:\s*([a-z0-9][a-z0-9-]*)\s*$', front[1], re.M) if len(front) == 3 and not front[0].strip() else None
        if not match:
            raise ValueError(f'invalid skill name/frontmatter: {path}')
        name = match[1]
        if name in skills:
            raise ValueError(f'duplicate skill name: {name}')
        skills[name] = str(Path(path).parent)
    return files, skills


def link_text(path):
    return os.readlink(path) if path.is_symlink() else None


def sync(config, check=False, hook=False):
    repo, target = Path(config['repo']), Path(config['target'])
    # A linked worktree shares hooks but must never publish its feature branch.
    if hook:
        actual = Path(git(Path.cwd(), 'rev-parse', '--show-toplevel').decode().strip()).resolve()
        if actual != repo:
            return {'status': 'skipped', 'reason': 'different worktree'}
    with locked(target) as state:
        branch = git(repo, 'rev-parse', '--abbrev-ref', 'HEAD').decode().strip()
        if branch != config['branch']:
            return {'status': 'skipped', 'reason': 'different branch', 'branch': branch}
        sha = git(repo, 'rev-parse', 'HEAD').decode().strip()
        files, skills = tracked_package(repo, config['source'], sha)
        owner = state / config['id']
        owner.mkdir(exist_ok=True)
        receipt = owner / 'receipt.json'
        previous = json.loads(receipt.read_text()) if receipt.exists() else {'links': {}}
        if 'commit' in previous:
            old_snapshot = owner / 'generations' / previous['commit']
            for relative, digest in previous.get('file_hashes', {}).items():
                old_file = old_snapshot / relative
                if old_file.is_symlink() or not old_file.is_file() or hashlib.sha256(old_file.read_bytes()).hexdigest() != digest:
                    raise ValueError('installed snapshot modified; refusing to overwrite')
        links = {}
        for client in CLIENTS:
            for name, relative in skills.items():
                dest = target / client / 'skills' / name
                links[str(dest)] = os.path.relpath(owner / 'current' / relative, dest.parent)
        # Check every destination before modifying any client entry. Never adopt
        # an existing unowned entry, even if its content happens to match.
        for dest in set(links) | set(previous['links']):
            path = Path(dest)
            for parent in (path.parent, path.parent.parent):
                if parent.is_symlink():
                    raise ValueError(f'client directory is a symlink: {parent}')
            if os.path.lexists(path):
                expected = {previous['links'].get(dest), previous.get('previous_links', {}).get(dest)} - {None}
                if link_text(path) not in expected:
                    raise ValueError(f'local skill conflict; left untouched: {path}')
        generation = owner / 'generations' / sha
        hashes = {p: hashlib.sha256(data).hexdigest() for p, (data, _) in files.items()}
        if generation.exists():
            existing = {str(p.relative_to(generation)) for p in generation.rglob('*') if not p.is_dir()}
            if existing != set(files) or any((generation / p).is_symlink() or hashlib.sha256((generation / p).read_bytes()).hexdigest() != digest for p, digest in hashes.items()):
                raise ValueError('installed snapshot modified; refusing to overwrite')
        result = {'status': 'checked' if check else 'synced', 'commit': sha, 'skills': sorted(skills), 'links': links, 'file_hashes': hashes}
        if check:
            return result
        generation.parent.mkdir(exist_ok=True)
        if not generation.exists():
            stage = Path(tempfile.mkdtemp(dir=generation.parent))
            try:
                for path, (data, mode) in files.items():
                    dest = stage / path
                    dest.parent.mkdir(parents=True, exist_ok=True)
                    dest.write_bytes(data)
                    dest.chmod(0o755 if mode == '100755' else 0o644)
                os.rename(stage, generation)
            finally:
                if stage.exists():
                    shutil.rmtree(stage)
        if git(repo, 'rev-parse', 'HEAD').decode().strip() != sha or git(repo, 'rev-parse', '--abbrev-ref', 'HEAD').decode().strip() != branch:
            raise ValueError('source changed during synchronization; retry')
        # Record intended ownership before link creation so interrupted runs can
        # be retried. Keep stale links in the journal until cleanup succeeds.
        journal = dict(previous, links={**previous['links'], **links}, previous_links=previous['links'])
        write_json(receipt, journal)
        for dest, relative in links.items():
            path = Path(dest)
            path.parent.mkdir(parents=True, exist_ok=True)
            if link_text(path) != relative:
                pending = path.parent / ('.' + path.name + '.squad-next')
                if os.path.lexists(pending):
                    raise ValueError(f'pending link already exists: {pending}')
                pending.symlink_to(relative)
                os.replace(pending, path)
        current = owner / 'current'
        temp = owner / 'current.next'
        if os.path.lexists(temp):
            temp.unlink()
        temp.symlink_to(Path('generations') / sha)
        os.replace(temp, current)
        for dest in previous['links'].keys() - links.keys():
            path = Path(dest)
            if path.is_symlink():
                path.unlink()
        write_json(receipt, result)
        return result


def install(repo, target, source, branch):
    repo, target = repo.resolve(), target.resolve()
    if Path(git(repo, 'rev-parse', '--show-toplevel').decode().strip()).resolve() != repo:
        raise ValueError('--repo must be a repository root')
    source_path = Path(source)
    if source_path.is_absolute() or '..' in source_path.parts or source in ('', '.'):
        raise ValueError('--source must be a relative package directory inside the repository')
    custom = subprocess.run(['git', '-C', str(repo), 'config', '--get', 'core.hooksPath'], capture_output=True)
    if custom.returncode == 0:
        raise ValueError('custom core.hooksPath detected; use run from your existing hook manager')
    hooks = Path(git(repo, 'rev-parse', '--path-format=absolute', '--git-path', 'hooks').decode().strip())
    hooks.mkdir(parents=True, exist_ok=True)
    config_path = hooks / 'squad-skill-sync.json'
    config = {'version': 1, 'repo': str(repo), 'target': str(target), 'source': str(source_path), 'branch': branch,
              'id': hashlib.sha256((str(repo) + '\0' + str(source_path)).encode()).hexdigest()[:16]}
    with (hooks / 'squad-skill-sync.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if config_path.exists() and json.loads(config_path.read_text()) != config:
            raise ValueError('hook already configured for another source/target; refusing to replace')
        for name in HOOKS:
            path, backup = hooks / name, hooks / (name + '.before-squad-skill-sync')
            if path.is_symlink():
                raise ValueError(f'hook is a symlink: {path}')
            managed = path.exists() and MARKER in path.read_text(errors='replace')
            if not managed and backup.exists():
                raise ValueError(f'hook backup already exists: {backup}')
        if sync(config, check=True)['status'] != 'checked':
            raise ValueError('checkout must be on the configured branch for installation')
        runner = hooks / 'squad-skill-sync.py'
        atomic_write(runner, Path(__file__).read_bytes())
        write_json(config_path, config)
        for name in HOOKS:
            path, backup = hooks / name, hooks / (name + '.before-squad-skill-sync')
            managed = path.exists() and MARKER in path.read_text(errors='replace')
            if path.exists() and not managed:
                path.rename(backup)
            body = f'#!/bin/sh\n{MARKER}\n'
            body += f'if [ -x {shlex.quote(str(backup))} ]; then\n  {shlex.quote(str(backup))} "$@" || exit $?\nfi\n'
            body += 'exec ' + ' '.join(shlex.quote(v) for v in (sys.executable, str(runner), 'run', '--config', str(config_path), '--hook')) + '\n'
            atomic_write(path, body.encode(), 0o755)
        return sync(config)


def uninstall(repo):
    hooks = Path(git(repo.resolve(), 'rev-parse', '--path-format=absolute', '--git-path', 'hooks').decode().strip())
    with (hooks / 'squad-skill-sync.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if not (hooks / 'squad-skill-sync.json').exists():
            raise ValueError('no skill sync hook installed')
        for name in HOOKS:
            path = hooks / name
            if path.exists() and (path.is_symlink() or MARKER not in path.read_text(errors='replace')):
                raise ValueError(f'hook changed locally; left untouched: {path}')
        for name in HOOKS:
            path, backup = hooks / name, hooks / (name + '.before-squad-skill-sync')
            if path.exists():
                path.unlink()
            if backup.exists():
                backup.rename(path)
        for name in ('squad-skill-sync.py', 'squad-skill-sync.json'):
            (hooks / name).unlink(missing_ok=True)
    return {'status': 'uninstalled', 'skills': 'retained at last installed snapshot'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    setup = sub.add_parser('install')
    setup.add_argument('--repo', type=Path, required=True)
    setup.add_argument('--target', type=Path, required=True)
    setup.add_argument('--source', required=True)
    setup.add_argument('--branch', default='main')
    remove = sub.add_parser('uninstall')
    remove.add_argument('--repo', type=Path, required=True)
    run = sub.add_parser('run')
    run.add_argument('--config', type=Path, required=True)
    run.add_argument('--check', action='store_true')
    run.add_argument('--hook', action='store_true')
    args = parser.parse_args()
    try:
        if args.command == 'install':
            result = install(args.repo, args.target, args.source, args.branch)
        elif args.command == 'uninstall':
            result = uninstall(args.repo)
        else:
            result = sync(json.loads(args.config.read_text()), args.check, args.hook)
        print(json.dumps({key: value for key, value in result.items() if key not in ('links', 'file_hashes')}, indent=2))
        return 0
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as error:
        print(f'squad skill sync: {error}', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
