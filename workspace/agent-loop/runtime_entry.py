#!/usr/bin/env python3
"""Portable, explicit Squad identity and event entrypoint; never starts a scheduler."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

RUNTIMES = ('claude', 'codex', 'muse')
IDENTITIES = ('CODEX_THREAD_ID', 'CODEX_SESSION_ID', 'CLAUDE_SESSION_ID',
              'MUSE_SESSION_ID', 'SQUAD_SESSION_ID', 'SQUAD_NATIVE_SESSION_ID', 'SQUAD_AGENT')


def environment(runtime, native, agent, ledger):
    if runtime not in RUNTIMES or not re.fullmatch(r'[A-Za-z0-9_-]{1,160}', native):
        raise ValueError('explicit supported runtime and native session required')
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_-]{0,127}', agent):
        raise ValueError('explicit Squad actor required')
    env = {k: v for k, v in os.environ.items() if k not in IDENTITIES}
    suffix = hashlib.sha256(str(Path(ledger).resolve()).encode()).hexdigest()[:12]
    env.update(SQUAD_SESSION_ID=f'{runtime}:{native}:{suffix}', SQUAD_AGENT=agent,
               SQUAD_NATIVE_SESSION_ID=native, SQUAD_NO_AUTO_DAEMON='1',
               SQUAD_NO_BROWSER='1', SQUAD_NO_HYGIENE='1')
    return env


def capabilities(runtime):
    if runtime not in RUNTIMES:
        raise ValueError('unsupported runtime')
    return dict(schema_version='squad.runtime-capabilities.v1', runtime=runtime,
                native_qualified=False, coordination='cli-with-explicit-identity',
                managed_worker=dict(launcher={'claude': 'claude_worker_launcher.py',
                                              'codex': 'codex_worker_launcher.py',
                                              'muse': 'muse_worker_launcher.py'}[runtime],
                                    execution_fence='custody-pin-and-contained-tools' if runtime == 'muse' else 'unavailable',
                                    admission={'claude': 'legacy-supervision-only',
                                               'codex': 'blocked',
                                               'muse': 'source-test-handoff-only'}[runtime]),
                events=dict(listen='terminal-events', handling_ack='explicit-after-handling',
                            idle_wake={'claude': 'native-hooks-requires-adoption',
                                       'codex': 'qualified-cli-only',
                                       'muse': 'bounded-worker-receiver-requires-adoption'}[runtime]),
                review='bounded-native-cli; separate from Worker custody')


def handled_events(receipt, delivery, runtime, native, agent):
    if (not isinstance(receipt, dict) or not isinstance(delivery, dict)
            or delivery.get('type') != 'worker-terminal-delivery-v1'
            or delivery.get('recipient') != agent
            or receipt.get('schema_version') != 'squad.handled-events.v1'
            or receipt.get('runtime') != runtime or receipt.get('native_session') != native
            or receipt.get('agent') != agent
            or not delivery.get('delivery_session')
            or receipt.get('delivery_session') != delivery['delivery_session']):
        raise ValueError('handled receipt does not match recipient/native/delivery')
    events = delivery.get('events')
    handled = receipt.get('handled')
    if not isinstance(events, list) or not isinstance(handled, list) or not 1 <= len(handled) <= 16:
        raise ValueError('bounded explicit handled event list required')
    available = {event['event_id'] for event in events if isinstance(event, dict) and isinstance(event.get('event_id'), str)}
    seen = set()
    for event in handled:
        if (not isinstance(event, dict) or set(event) != {'event_id', 'note'}
                or not isinstance(event['event_id'], str) or event['event_id'] not in available
                or event['event_id'] in seen or not isinstance(event['note'], str)
                or not event['note'].strip() or len(event['note']) > 2000):
            raise ValueError('invalid, duplicate or undelivered handled event')
        seen.add(event['event_id'])
    return handled


def read_json(path):
    if path.stat().st_size > 65536:
        raise ValueError('receipt exceeds 64 KiB')
    return json.loads(path.read_text())


def main():
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('--runtime', choices=RUNTIMES, required=True)
    parser.add_argument('--native-session')
    parser.add_argument('--agent')
    parser.add_argument('--ledger', type=Path)
    parser.add_argument('--squad', type=Path)
    commands = parser.add_subparsers(dest='operation', required=True)
    commands.add_parser('capabilities')
    worker = commands.add_parser('worker')
    worker.add_argument('--assignment', type=Path, required=True)
    worker.add_argument('--config', type=Path, required=True)
    worker.add_argument('--check', action='store_true')
    execute = commands.add_parser('exec')
    execute.add_argument('argv', nargs=argparse.REMAINDER)
    listen = commands.add_parser('listen')
    listen.add_argument('--delivery-session', required=True)
    listen.add_argument('--max', default='23h')
    ack = commands.add_parser('handled')
    ack.add_argument('--delivery', type=Path, required=True)
    ack.add_argument('--receipt', type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.operation == 'capabilities':
            print(json.dumps(capabilities(args.runtime), sort_keys=True)); return 0
        if args.operation == 'worker':
            launch = capabilities(args.runtime)['managed_worker']['launcher']
            config = read_json(args.config)
            if args.runtime == 'claude' and any(not config.get(key) for key in ('model', 'effort', 'event_executable')):
                raise ValueError('portable Claude launch requires model, effort and event_executable')
            argv = [sys.executable, str(Path(__file__).with_name(launch)), '--assignment', str(args.assignment), '--config', str(args.config)]
            if args.check:
                argv.append('--check')
            return subprocess.run(argv).returncode
        if (not args.ledger or not args.ledger.is_absolute() or not (args.ledger / '.squad').is_dir()
                or not args.squad or not args.squad.is_absolute() or not os.access(args.squad, os.X_OK)):
            raise ValueError('absolute native Squad executable and initialized ledger required')
        env = environment(args.runtime, args.native_session or '', args.agent or '', args.ledger)
        def run(argv, quiet=False):
            # Inherited stdout/stderr; no credential-bearing environment is printed.
            return subprocess.run([str(args.squad), *argv], cwd=args.ledger, env=env,
                                  stdout=subprocess.DEVNULL if quiet else None,
                                  stderr=subprocess.DEVNULL if quiet else None).returncode
        if args.operation == 'exec':
            argv = args.argv[1:] if args.argv[:1] == ['--'] else args.argv
            if not argv:
                raise ValueError('Squad command required')
            return run(argv)
        if args.operation == 'listen':
            # Reading pointers is not native acceptance and must not mark delivery.
            return run(['terminal-events', 'listen', '--defer-delivery', '--native-session', args.native_session,
                        '--delivery-session', args.delivery_session, '--max', args.max])
        delivery = read_json(args.delivery)
        events = handled_events(read_json(args.receipt), delivery,
                                args.runtime, args.native_session, args.agent)
        for event in events:
            # The recipient's explicit handled proof implies native acceptance.
            # Mark it only now, never when the pointer was merely read.
            delivered = run(['terminal-events', 'delivered', event['event_id'], '--native-session',
                             args.native_session, '--delivery-session', delivery['delivery_session']], quiet=True)
            # Delivered rejects an already processed event. ACK is the authoritative
            # idempotent read/write: success proves the exact same handling note,
            # including after a partial batch or a lost command response. Never
            # infer success from a delivery error or classify its stderr.
            acknowledged = run(['terminal-events', 'ack', event['event_id'], '--native-session',
                                args.native_session, '--note', event['note']])
            if acknowledged:
                return delivered or acknowledged
        return 0
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(json.dumps({'status': 'blocked', 'reason': str(error) if isinstance(error, ValueError)
                          else 'runtime entry unavailable; no receipt inferred'}), file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
