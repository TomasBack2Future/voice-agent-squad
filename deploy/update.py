#!/usr/bin/env python3
"""Linux single-host adoption. Install deliberately; never runs on source checkout.

The root-owned updater only adopts reviewed main with successful exact-SHA CI.
Build/test run as the service user. Schema changes require operator admission.
"""
import fcntl
import json
from pathlib import Path
import shutil
import subprocess
import tarfile
import time
import tempfile
import urllib.request

ROOT = Path('/opt/squad-service')
SOURCE = ROOT / 'source'
STATE = Path('/var/lib/squad-service')
REPO = 'TomasBack2Future/voice-agent-squad'


def run(*args, **kwargs):
    return subprocess.check_output(args, text=True, timeout=600, **kwargs).strip()


def build_run(*args, cwd=SOURCE):
    return run('runuser', '-u', 'squad-service', '--', 'env',
               'HOME=/var/lib/squad-service/build-home', 'GOTOOLCHAIN=auto',
               'CGO_ENABLED=0', 'PATH=/usr/local/go/bin:/usr/bin:/bin',
               *args, cwd=cwd)


def replace_link(target):
    temporary = ROOT / 'next'
    temporary.unlink(missing_ok=True)
    temporary.symlink_to(target)
    temporary.replace(ROOT / 'current')


def export_commit(source, sha, destination, prefix=('runuser', '-u', 'squad-service', '--')):
    # git archive ignores modified/untracked working files; extraction runs as
    # the unprivileged builder, never root. The archive lives outside its tree.
    with tempfile.TemporaryFile() as archive:
        subprocess.run([*prefix, 'git', '-C', str(source), 'archive', sha],
                       stdout=archive, check=True, timeout=120)
        archive.seek(0)
        subprocess.run([*prefix, 'tar', '-x', '-C', str(destination)],
                       stdin=archive, check=True, timeout=120)


def main():
    with open(ROOT / 'update.lock', 'w') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        adopt()


def stage_release(target, binary, manifest):
    target.mkdir(parents=True, exist_ok=True)
    if (target / 'manifest.json').exists():
        raise RuntimeError('Candidate already attempted; operator reconciliation required')
    shutil.copy2(binary, target / 'squad')
    # The root updater runs with UMask=0077, while the runtime is unprivileged.
    # Only immutable executable artifacts are public; state/manifests stay private.
    target.chmod(0o755)
    (target / 'squad').chmod(0o755)
    (target / 'manifest.json').write_text(json.dumps(manifest))


def adopt():
    build_run('git', 'fetch', '--prune', 'origin', 'main')
    sha = build_run('git', 'rev-parse', 'origin/main')
    current = (ROOT / 'current').resolve(strict=True)
    old = json.loads((current / 'manifest.json').read_text())['sha']
    if sha == old:
        print('Already at main HEAD', sha)
        return
    runs = json.loads(run('gh', 'run', 'list', '--repo', REPO, '--workflow', 'ci.yml',
                         '--branch', 'main', '--commit', sha, '--event', 'push',
                         '--json', 'databaseId,headSha,conclusion,status', '--limit', '10'))
    qualified = [r for r in runs if r['headSha'] == sha and r['status'] == 'completed'
                 and r['conclusion'] == 'success']
    if not qualified:
        print('Waiting for exact-HEAD CI', sha)
        return
    # Never downgrade or run a changed migrator unattended, including the code
    # applying migrations. Compatibility requires a separate operator review.
    build_run('git', 'merge-base', '--is-ancestor', old, sha)
    changed = build_run('git', 'diff', '--name-only', old, sha, '--', 'internal/store')
    if changed:
        raise RuntimeError('Store implementation changed; operator compatibility admission required')
    candidate = STATE / 'candidate'
    candidate.mkdir(exist_ok=True)
    shutil.chown(candidate, user='squad-service', group='squad-service')
    # Export exactly the CI-qualified commit. A dirty source checkout (including
    # untracked Go files) must never affect deployed inputs.
    with tempfile.TemporaryDirectory(prefix='squad-build-', dir=STATE) as tmp:
        clean = Path(tmp)
        shutil.chown(clean, user='squad-service', group='squad-service')
        export_commit(SOURCE, sha, clean)
        build_run('go', 'build', '-trimpath', '-ldflags=-X main.versionString='+sha,
                  '-o', str(candidate / 'squad'), './cmd/squad', cwd=clean)
        build_run('python3', 'scripts/test_remote_service.py', str(candidate / 'squad'), cwd=clean)
    target = ROOT / 'releases' / sha
    stage_release(target, candidate / 'squad',
                  {'sha': sha, 'ci_run': qualified[0]['databaseId'],
                   'previous': old, 'created_at': time.time()})
    run('systemctl', 'stop', 'squad-service')
    activated = False
    try:
        backups = ROOT / 'backups'
        backups.mkdir(mode=0o700, exist_ok=True)
        with tarfile.open(backups / (str(int(time.time()))+'-'+old+'.tar.gz'), 'w:gz') as archive:
            for name in ('ledger', 'home', 'receipts'):
                archive.add(STATE / name, arcname=name)
        replace_link(target)
        run('runuser', '-u', 'squad-service', '--', 'env',
            'HOME='+str(STATE / 'home'), 'SQUAD_HOME='+str(STATE / 'home'),
            'SQUAD_NO_HYGIENE=1', 'SQUAD_NO_AUTO_DAEMON=1',
            str(target / 'squad'), 'status', cwd=STATE / 'ledger')
        run('systemctl', 'start', 'squad-service')
        for _ in range(30):
            try:
                with urllib.request.urlopen('http://127.0.0.1:7788/healthz', timeout=2) as response:
                    if json.load(response).get('version') == sha:
                        activated = True
                        break
            except OSError:
                pass
            time.sleep(1)
        if not activated:
            raise RuntimeError('Candidate failed startup check')
    finally:
        if not activated:
            # Same store implementation only. Never rewind ledger/receipts after
            # the endpoint may have accepted writes from a client.
            replace_link(current)
            run('systemctl', 'restart', 'squad-service')
    print('Adopted', sha, 'CI', qualified[0]['databaseId'])


if __name__ == '__main__':
    main()
