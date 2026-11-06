#!/usr/bin/env python3
"""Run one command tree with an owner, a wall-clock limit and a CPU limit.

Use it for any background, load or stress reproduction started by a Worker or
an independent review agent. The command and every descendant run in one
process group whose ID is pinned by an anchor shell for the whole run:

* normal exit, non-zero exit, timeout, SIGINT/SIGTERM of this runner: the whole
  group is killed before the runner exits;
* this runner killed with SIGKILL: the anchor reads EOF on its stdin, which only
  this runner holds, and kills its own group;
* every process inherits RLIMIT_CPU, so even a descendant that leaves the group
  (setsid) stops after its CPU budget.

Cleanup signals only the owned group ID, never processes chosen by name or
port. Exit status is the command's, 124 on timeout, 125 for a runner error.
Python 3.9 compatible; no third-party modules.
"""

import argparse
import datetime
import json
import os
import resource
import signal
import subprocess
import sys
import time

ANCHOR = "trap '' INT TERM HUP QUIT; read _; kill -KILL 0"
MAX_TIMEOUT = 4 * 3600
MAX_CPU = 4 * 3600


class Interrupted(Exception):
    def __init__(self, signum):
        super().__init__(signum)
        self.signum = signum


def group_members(pgid):
    out = subprocess.run(["/bin/ps", "-A", "-o", "pid=", "-o", "pgid="], capture_output=True, text=True, check=False).stdout
    members = []
    for line in out.splitlines():
        parts = line.split()
        if len(parts) == 2 and parts[1] == str(pgid) and parts[0] != str(pgid):
            members.append(int(parts[0]))
    return members


def kill_group(pgid):
    try:
        os.killpg(pgid, signal.SIGKILL)
    except (ProcessLookupError, PermissionError):
        # ESRCH: no member left. macOS reports EPERM when only the unreaped
        # anchor zombie remains; every live member is ours, so nothing is left.
        pass


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def run(argv, timeout, cpu_seconds, owner, record):
    anchor = subprocess.Popen(["/bin/sh", "-c", ANCHOR], stdin=subprocess.PIPE, preexec_fn=lambda: os.setpgid(0, 0))
    pgid = anchor.pid

    def child_setup():
        os.setpgid(0, pgid)
        resource.setrlimit(resource.RLIMIT_CPU, (cpu_seconds, cpu_seconds + 5))

    report = {"schema": "agent-loop.bounded_run.v1", "owner": owner, "runner_pid": os.getpid(), "pgid": pgid,
              "command": os.path.basename(argv[0]), "timeout_seconds": timeout, "cpu_seconds": cpu_seconds,
              "started_at": now(), "exit_code": None, "timed_out": False, "interrupted": None, "descendants_reaped": 0}

    def on_signal(signum, _frame):
        raise Interrupted(signum)

    previous = {s: signal.signal(s, on_signal) for s in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP)}
    code = 125
    child = None
    try:
        child = subprocess.Popen(argv, preexec_fn=child_setup)
        report["pid"] = child.pid
        try:
            code = child.wait(timeout=timeout)
            report["exit_code"] = code
        except subprocess.TimeoutExpired:
            report["timed_out"] = True
            kill_group(pgid)
            child.wait()
            report["exit_code"] = child.returncode
            code = 124
    except Interrupted as exc:
        report["interrupted"] = signal.Signals(exc.signum).name
        code = 128 + exc.signum
    finally:
        for s, h in previous.items():
            signal.signal(s, h)
        report["descendants_reaped"] = len(group_members(pgid))
        kill_group(pgid)
        if child is not None and child.poll() is None:
            child.wait()
        try:
            anchor.stdin.close()
        except OSError:
            pass
        anchor.wait()
        report["finished_at"] = now()
        if record:
            with open(record, "w") as f:
                json.dump(report, f, indent=1)
                f.write("\n")
        print(json.dumps(report), file=sys.stderr)
    return code


def main(args=None):
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--timeout", type=int, required=True, help="wall-clock seconds (1..%d)" % MAX_TIMEOUT)
    p.add_argument("--cpu-seconds", type=int, required=True, help="CPU seconds per process (1..%d)" % MAX_CPU)
    p.add_argument("--owner", required=True, help="owning session or reservation, recorded in the report")
    p.add_argument("--record", help="write the JSON report here as well as to stderr")
    p.add_argument("command", nargs=argparse.REMAINDER, help="-- COMMAND [ARG...]")
    a = p.parse_args(args)
    command = a.command[1:] if a.command[:1] == ["--"] else a.command
    if not command:
        p.error("a command after -- is required")
    if not 1 <= a.timeout <= MAX_TIMEOUT or not 1 <= a.cpu_seconds <= MAX_CPU:
        p.error("timeout and cpu-seconds must be positive and bounded")
    return run(command, a.timeout, a.cpu_seconds, a.owner, a.record)


if __name__ == "__main__":
    sys.exit(main())
