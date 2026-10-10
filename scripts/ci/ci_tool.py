#!/usr/bin/env python3
"""Scope selection, race sharding, test inventory and the aggregate gate for CI.

Standard library only. Every classification fails open: an unknown path, a
missing diff or a non-PR event selects all checks.
"""
import argparse
import fnmatch
import json
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

FLAGS = ("go", "pyloop", "observer", "node", "remote", "doc", "cross", "smoke")

# job id in ci.yml -> scope flag that selects it. The gate requires every
# selected job to succeed and every unselected job to be skipped.
JOB_FLAGS = {
    "static": "go",
    "lint": "go",
    "race": "go",
    "pyloop": "pyloop",
    "observer": "observer",
    "node": "node",
    "remote": "remote",
    "doc-contracts": "doc",
    "cross-build": "cross",
    "release-smoke": "smoke",
}

RACE_SHARDS = ("cli-1", "cli-2", "server", "rest")
CLI_PKG = "./cmd/squad"
SERVER_PKG = "./internal/server"

# Non-Go test suites and the scope flag that runs them.
PY_SUITES = {
    "scripts/ci": "scope",
    "workspace/agent-loop/tests": "pyloop",
    "scripts/squad-observer": "observer",
    "deploy": "remote",
    "scripts/test_remote_service.py": "remote",
}
NODE_SUITE_GLOB = "workspace/coordination-skills/*/scripts/*.test.mjs"


def _all(**over):
    flags = {f: True for f in FLAGS}
    flags["doc"] = False
    flags.update(over)
    return flags


def classify(paths, full=False):
    """Map changed repo-relative paths to scope flags."""
    if full or not paths:
        return _all()
    flags = {f: False for f in FLAGS}
    for path in paths:
        if path.startswith(".github/") or path.startswith("scripts/ci/") or path in ("go.mod", "go.sum"):
            return _all()
        if path == ".golangci.yml":
            flags["go"] = True
        elif path == ".goreleaser.yaml":
            flags["cross"] = flags["smoke"] = True
        elif path.endswith(".go") or path.split("/")[0] in ("internal", "cmd", "plugin", "reviewer", "templates"):
            flags["go"] = flags["remote"] = True
            if path.endswith(".go") and not path.endswith("_test.go"):
                flags["cross"] = True
            if path == "cmd/squad/main.go":
                flags["smoke"] = True
        elif path.startswith("workspace/"):
            flags["pyloop"] = flags["node"] = flags["doc"] = True
        elif path.startswith("scripts/squad-observer/"):
            flags["observer"] = True
        elif path == "scripts/test_remote_service.py" or path.startswith("deploy/"):
            flags["remote"] = True
        elif path.startswith("docs/") or path.startswith(".squad/") or (path.endswith(".md") and "/" not in path) or path == "LICENSE":
            # Go tests read README/AGENTS/CLAUDE, docs/ and .squad/specs.
            flags["doc"] = True
        else:
            return _all()
    if flags["go"]:
        flags["doc"] = False
    return flags


def changed_paths(event):
    """Return changed paths for a pull request, or None to broaden coverage."""
    if event != "pull_request":
        return None
    try:
        parents = subprocess.run(["git", "rev-list", "--parents", "-n", "1", "HEAD"], cwd=ROOT, check=True,
                                 capture_output=True, text=True).stdout.split()
        if len(parents) != 3:
            return None
        out = subprocess.run(["git", "diff", "--name-only", "--no-renames", parents[1], "HEAD"], cwd=ROOT, check=True,
                             capture_output=True, text=True).stdout
    except (OSError, subprocess.CalledProcessError):
        return None
    return [p for p in out.splitlines() if p]


def cmd_scope(args):
    paths = changed_paths(args.event)
    full = paths is None or args.full
    flags = classify(paths or [], full=full)
    lines = [f"{k}={'true' if v else 'false'}" for k, v in flags.items()]
    lines.append(f"mode={'full' if full else 'scoped'}")
    out = "\n".join(lines)
    print(out)
    target = os.environ.get("GITHUB_OUTPUT")
    if target:
        with open(target, "a", encoding="utf-8") as fh:
            fh.write(out + "\n")


def go_lines(*argv):
    return subprocess.run(["go", *argv], cwd=ROOT, check=True, capture_output=True, text=True).stdout.splitlines()


def cli_tests():
    names = sorted(l for l in go_lines("test", "-list", "^(Test|Example)", CLI_PKG) if re.match(r"^(Test|Example)\w*$", l))
    return names


def shard_plan(packages, cli_names, shard):
    """Return the (packages, run-regex or None) a shard executes."""
    server = SERVER_PKG
    cli = CLI_PKG
    if shard == "server":
        return [p for p in packages if p.endswith(server[1:])], None
    if shard == "rest":
        return [p for p in packages if not p.endswith(server[1:]) and not p.endswith(cli[1:])], None
    idx = {"cli-1": 0, "cli-2": 1}[shard]
    mine = [n for i, n in enumerate(cli_names) if i % 2 == idx]
    return [p for p in packages if p.endswith(cli[1:])], "^(" + "|".join(mine) + ")$"


def cmd_race(args):
    packages = go_lines("list", "./...")
    pkgs, run = shard_plan(packages, cli_tests() if args.shard.startswith("cli") else [], args.shard)
    if not pkgs:
        sys.exit(f"shard {args.shard} selects no packages")
    argv = ["go", "test", "-race", "-count=1"] + (["-run", run] if run else []) + pkgs
    env = dict(os.environ, CGO_ENABLED="1")
    sys.exit(subprocess.call(argv, cwd=ROOT, env=env))


def tracked(*patterns):
    files = subprocess.run(["git", "ls-files", *patterns], cwd=ROOT, check=True, capture_output=True, text=True).stdout.splitlines()
    return [f for f in files if not f.startswith(".squad/")]


def inventory_errors(go_test_dirs, packages, py_tests, node_tests):
    errors = []
    covered = {p.split("/", 3)[-1] if p.count("/") >= 3 else p for p in packages}
    for d in sorted(go_test_dirs):
        if d != "." and d not in covered:
            errors.append(f"Go test directory {d} is not a listed package")
    for t in sorted(py_tests):
        if not any(t == s or t.startswith(s + "/") or os.path.dirname(t) == s for s in PY_SUITES):
            errors.append(f"Python test {t} is not mapped to a CI job")
    for t in sorted(node_tests):
        if not fnmatch.fnmatch(t, NODE_SUITE_GLOB):
            errors.append(f"Node test {t} is not mapped to a CI job")
    return errors


def shard_errors(packages, cli_names):
    """Every package/test runs in exactly one race shard."""
    errors = []
    seen = {}
    for shard in RACE_SHARDS:
        pkgs, run = shard_plan(packages, cli_names, shard)
        for p in pkgs:
            if run is None:
                seen.setdefault((p, None), []).append(shard)
        if run is not None:
            for n in run[2:-2].split("|"):
                if n:
                    seen.setdefault((CLI_PKG, n), []).append(shard)
    for pkg in packages:
        if pkg.endswith(CLI_PKG[1:]):
            for n in cli_names:
                if len(seen.get((CLI_PKG, n), [])) != 1:
                    errors.append(f"cmd/squad test {n} runs in {len(seen.get((CLI_PKG, n), []))} shards")
        elif len(seen.get((pkg, None), [])) != 1:
            errors.append(f"package {pkg} runs in {len(seen.get((pkg, None), []))} shards")
    return errors


def cmd_inventory(_args):
    packages = go_lines("list", "./...")
    go_dirs = {os.path.dirname(f) or "." for f in tracked("*_test.go")}
    py = [f for f in tracked("*.py") if os.path.basename(f).startswith("test_")]
    node = tracked("*.test.mjs")
    errors = inventory_errors(go_dirs, packages, py, node) + shard_errors(packages, cli_tests())
    if errors:
        print("\n".join(errors), file=sys.stderr)
        sys.exit(1)
    print(f"inventory ok: {len(packages)} Go packages, {len(py)} Python tests, {len(node)} Node tests")


def gate_errors(flags, needs):
    """Selected jobs must succeed; unselected jobs must be skipped."""
    errors = []
    if needs.get("scope", {}).get("result") != "success":
        errors.append(f"scope: {needs.get('scope', {}).get('result', 'missing')}")
    for job, flag in sorted(JOB_FLAGS.items()):
        if job not in needs:
            errors.append(f"{job}: missing from gate needs")
            continue
        result = needs[job].get("result")
        if flags.get(flag) == "true":
            if result != "success":
                errors.append(f"{job}: selected by scope ({flag}) but result is {result}")
        elif result not in ("skipped", "success"):
            errors.append(f"{job}: not selected but result is {result}")
    return errors


def cmd_gate(_args):
    flags = json.loads(os.environ["SCOPE_JSON"])
    needs = json.loads(os.environ["NEEDS_JSON"])
    errors = gate_errors(flags, needs)
    if errors:
        print("\n".join(errors), file=sys.stderr)
        sys.exit(1)
    selected = sorted(j for j, f in JOB_FLAGS.items() if flags.get(f) == "true")
    print("gate ok; selected jobs:", ", ".join(selected) or "none")


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("scope")
    s.add_argument("--event", default=os.environ.get("GITHUB_EVENT_NAME", ""))
    s.add_argument("--full", action="store_true")
    s.set_defaults(fn=cmd_scope)
    r = sub.add_parser("race")
    r.add_argument("shard", choices=RACE_SHARDS)
    r.set_defaults(fn=cmd_race)
    sub.add_parser("inventory").set_defaults(fn=cmd_inventory)
    sub.add_parser("gate").set_defaults(fn=cmd_gate)
    args = ap.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
