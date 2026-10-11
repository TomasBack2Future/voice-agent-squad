#!/usr/bin/env python3
"""Run recovery contract regressions or explicitly selected native qualification.

A contract PASS is never native compatibility. Native suites are opt-in, operate
on isolated fixtures, and fail qualification when even one test is skipped.
This command does not install clients, migrate assignments or grant custody.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import unittest
from unittest.mock import patch

from runtime_entry import IDENTITIES

ROOT = Path(__file__).resolve().parent
RUNTIMES = ('claude', 'codex', 'muse')
ROLES = ('dispatcher', 'worker', 'deployer', 'reviewer', 'investigator')
# These are executable test modules, not assertions that every runtime has an
# implementation. The matrix tests use the actual CLI/store and portable entry.
CONTRACT_SUITES = (
    'test_recovery_matrix', 'test_runtime_entry', 'test_timeout_continuation',
    'test_codex_writer_fence', 'test_codex_stdio', 'test_delivery_readiness',
    'test_terminal_receiver_ledger', 'test_outcome_submit_decision',
)
NATIVE_SUITES = {('muse', 'worker'): ('test_muse_native', 'test_muse_worker_native')}
NATIVE_SCOPE = {('muse', 'worker'): 'source-worker-new-resume-hold'}


def native_coverage():
    return [dict(runtime=runtime, role=role,
                 status='not_run' if (runtime, role) in NATIVE_SUITES else 'unavailable',
                 native_qualified=False,
                 scope=NATIVE_SCOPE.get((runtime, role)),
                 reason='explicit native execution required' if (runtime, role) in NATIVE_SUITES
                 else 'no complete native role recovery suite; contract tests are insufficient')
            for runtime in RUNTIMES for role in ROLES]


def execute(suite, stream=None):
    started = time.monotonic()
    result = unittest.TextTestRunner(stream=stream or sys.stderr, verbosity=2).run(suite)
    passed = (result.wasSuccessful() and result.testsRun > 0 and not result.skipped
              and not result.expectedFailures)
    return dict(status='passed' if passed else 'failed', tests_run=result.testsRun,
                failures=len(result.failures), errors=len(result.errors),
                skipped=len(result.skipped), expected_failures=len(result.expectedFailures),
                unexpected_successes=len(result.unexpectedSuccesses),
                duration_seconds=round(time.monotonic() - started, 3))


def run_suites(names):
    sys.path.insert(0, str(ROOT / 'tests'))
    rows = []
    # Older suites set SQUAD_HOME but do not remove remote routing. Sanitize
    # before loading modules too: decorators/imports can inspect environment.
    env = {k: v for k, v in os.environ.items()
           if not k.startswith('SQUAD_') and k not in IDENTITIES}
    env['PYTHONDONTWRITEBYTECODE'] = '1'
    with patch.dict(os.environ, env, clear=True):
        for name in names:
            loader = unittest.TestLoader()
            suite = loader.loadTestsFromName(name)
            rows.append(dict(suite=name, **execute(suite)))
    return rows


def qualify(runtime, role):
    names = NATIVE_SUITES.get((runtime, role))
    if names is None:
        return dict(status='blocked', native_qualified=False, runtime=runtime, role=role,
                    reason='no native recovery suite for this runtime/role', suites=[])
    rows = run_suites(names)
    passed = bool(rows) and all(row['status'] == 'passed' for row in rows)
    return dict(status='passed' if passed else 'failed', runtime=runtime, role=role,
                scope=NATIVE_SCOPE[(runtime, role)], native_qualified=passed,
                cross_runtime_migration_qualified=False, suites=rows)


def source_identity():
    def git(*args):
        return subprocess.run(['git', *args], cwd=ROOT, capture_output=True,
                              text=True, check=True, timeout=10).stdout.strip()
    return dict(revision=git('rev-parse', 'HEAD'), dirty=bool(git('status', '--porcelain')))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('mode', choices=('contract', 'native', 'coverage'))
    parser.add_argument('--runtime', choices=RUNTIMES)
    parser.add_argument('--role', choices=ROLES)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args(argv)
    if args.mode == 'native' and (not args.runtime or not args.role):
        parser.error('native mode requires --runtime and --role')
    if args.mode != 'native' and (args.runtime or args.role):
        parser.error('runtime and role selections require native mode')
    # Applies to descendants as well, including tests that build client helpers.
    os.environ['PYTHONDONTWRITEBYTECODE'] = '1'
    sys.dont_write_bytecode = True
    report = dict(schema_version='squad.recovery-smoke.v1', mode=args.mode,
                  source=source_identity(), native_coverage=native_coverage(),
                  purpose='test evidence only; not an execution admission receipt')
    if args.mode == 'contract':
        rows = run_suites(CONTRACT_SUITES)
        report.update(status='passed' if rows and all(r['status'] == 'passed' for r in rows) else 'failed',
                      native_qualified=False, suites=rows)
    elif args.mode == 'native':
        report.update(qualify(args.runtime, args.role))
        for cell in report['native_coverage']:
            if (cell['runtime'], cell['role']) == (args.runtime, args.role):
                cell.update(status=report['status'], native_qualified=report['native_qualified'],
                            reason='see suite results; qualification applies only to the stated scope')
    else:
        report.update(status='not_run', native_qualified=False, suites=[])
    args.output.write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps({k: report[k] for k in ('mode', 'status', 'native_qualified')}))
    return 0 if report['status'] in ('passed', 'not_run') else 1


if __name__ == '__main__':
    sys.exit(main())
