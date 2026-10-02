#!/usr/bin/env python3
"""Read-only cold-start checks. No model, ownership, installation or release calls."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import re
from urllib.parse import urlsplit
from datetime import datetime, timezone

from validate_context_package import ROOT, ValidationError, validate_file


def git(worktree: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", "-C", str(worktree), *args], capture_output=True,
        text=True, timeout=15, check=False,
    )
    if result.returncode:
        # Git stderr can contain credential-bearing remote URLs.
        raise ValidationError("git identity check failed")
    return result.stdout.strip()


def repository_name(remote: str, expected_host: str = "github.com", clone_layout: str = "plain") -> str:
    # Match the explicit host as well as namespace/name. Never accept a local
    # path, credential-bearing URL, encoded path or a same-name foreign remote.
    if clone_layout not in ("plain", "bitbucket-server") or (expected_host == "github.com" and clone_layout != "plain"):
        raise ValidationError("unsupported host/clone layout")
    if not re.fullmatch(r"[a-z0-9]+(?:[.-][a-z0-9]+)*", expected_host):
        raise ValidationError("invalid assigned repository host")
    if remote.startswith("git@") and ":" in remote and "://" not in remote:
        host, path = remote[4:].split(":", 1)
    else:
        parsed = urlsplit(remote)
        if (parsed.scheme not in ("ssh", "https") or parsed.password is not None
                or parsed.query or parsed.fragment
                or parsed.username not in (None, "git")):
            raise ValidationError("unsupported repository remote")
        host, path = parsed.hostname, parsed.path.removeprefix("/")
        if parsed.scheme == "https" and clone_layout == "bitbucket-server":
            if not path.startswith("scm/"):
                raise ValidationError("Bitbucket HTTPS remote requires the declared scm layout")
            path = path[4:]
    path = path.removesuffix(".git")
    if host != expected_host or not re.fullmatch(r"[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+", path) or path.split("/")[1] in (".", ".."):
        raise ValidationError("assignment repository mismatch")
    return path


def check_profile(assignment: dict, profile_path: Path | None = None) -> dict:
    selected = assignment["project_profile"]
    declared = Path(selected["path"])
    if not declared.is_absolute():
        declared = Path(assignment["worktree"]) / declared
    if profile_path is not None and declared.resolve() != profile_path.resolve():
        raise ValidationError("profile path differs from the assignment")
    profile = validate_file(declared, ROOT / "schemas/project-profile.schema.json")
    if (selected["id"], selected["version"], assignment["repository"], assignment.get("repository_host", "github.com"), assignment.get("clone_layout", "plain")) != (
        profile["id"], profile["version"], profile["repository"], profile.get("repository_host", "github.com"), profile.get("clone_layout", "plain")
    ):
        raise ValidationError("assignment/profile identity mismatch")
    if profile.get("delivery_mode") == "human-pr" and any(assignment["authorization"][key] for key in ("pull_request", "merge", "staging", "production", "issue_close")):
        raise ValidationError("human-PR profile allows source/test/local-review handoff only")
    return profile


def check_worktree(assignment: dict) -> None:
    worktree = Path(assignment["worktree"]).resolve(strict=True)
    if Path(git(worktree, "rev-parse", "--show-toplevel")).resolve() != worktree:
        raise ValidationError("assignment worktree must be the Git root")
    if git(worktree, "branch", "--show-current") != assignment["branch"]:
        raise ValidationError("assignment branch mismatch")
    if git(worktree, "rev-parse", "HEAD") != assignment["base_sha"]:
        raise ValidationError("cold-start base changed; refresh the assignment before launch")
    if repository_name(git(worktree, "remote", "get-url", "origin"), assignment.get("repository_host", "github.com"), assignment.get("clone_layout", "plain")) != assignment["repository"]:
        raise ValidationError("assignment repository mismatch")
    if git(worktree, "status", "--porcelain"):
        raise ValidationError("cold-start worktree is dirty")
    if not (worktree / "AGENTS.md").is_file():
        raise ValidationError("repository AGENTS.md is missing")


def check(assignment_path: Path, profile_path: Path, runtime: str,
          skills: list[Path], tools: list[str], launch_config: Path | None = None,
          *, context_only: bool = False) -> dict:
    if runtime not in ('claude', 'codex', 'muse'):
        raise ValidationError('unsupported runtime')
    if context_only and launch_config is not None:
        raise ValidationError('context-only cannot be combined with a launch config')
    if not context_only:
        if runtime == 'muse':
            raise ValidationError('Muse launch adapter unavailable in this package; context-only is not execution readiness')
        if launch_config is None:
            raise ValidationError(f'{runtime.capitalize()} launch config required; executable presence is not readiness')
    assignment = validate_file(
        assignment_path, ROOT / "schemas/assignment-envelope.schema.json"
    )
    profile = check_profile(assignment, profile_path)
    if len(json.dumps(assignment, separators=(",", ":")).encode()) > 2048:
        raise ValidationError("assignment exceeds 2048-byte cold-start budget")
    if runtime != 'codex' or launch_config is None:
        check_worktree(assignment)

    # The launcher supplies the actual client-visible entries, not just canonical
    # source paths. Resolving a source file alone does not prove client discovery.
    client_directory = ".claude" if runtime == "claude" else ".agents"
    receipts = []
    has_worker = False
    for path in skills:
        if not path.is_absolute() or client_directory not in path.parts:
            raise ValidationError("skill entry must be an absolute client-visible path")
        if path.name != "SKILL.md" or not path.is_file():
            raise ValidationError("required skill entry is missing")
        content = path.read_bytes()
        if path.parent.name == assignment["role"]["skill"]:
            if b"name: agent-loop-worker\n" not in content:
                raise ValidationError("Worker skill identity mismatch")
            if content != (ROOT / "roles/worker/SKILL.md").read_bytes():
                raise ValidationError("client Worker skill differs from the selected package")
            has_worker = True
        receipts.append({"skill": path.parent.name,
                         "sha256": hashlib.sha256(content).hexdigest()})
    if not has_worker:
        raise ValidationError("client-visible Worker skill is required")
    available = []
    for tool in sorted(set([runtime, "git", "gh", *tools])):
        if not shutil.which(tool):
            raise ValidationError("a required executable is unavailable")
        available.append(Path(tool).name)
    launch_receipt = {"status": "not_checked"}
    if launch_config is not None:
        if runtime == "codex":
            from codex_worker_launcher import check_launch
        else:
            from claude_worker_launcher import check_launch
        launch_receipt = check_launch(assignment, launch_config)
    return {
        "schema_version": "agent-loop.startup-receipt.v1",
        "status": "context-checked" if context_only else "ready",
        "checked_at": datetime.now(timezone.utc).isoformat(),
        "assignment_id": assignment["assignment_id"],
        "assignment_sha256": hashlib.sha256(assignment_path.read_bytes()).hexdigest(),
        "base_sha": assignment["base_sha"], "runtime": runtime,
        "profile_sha256": hashlib.sha256(profile_path.read_bytes()).hexdigest(),
        "skills": receipts, "executables": available,
        "ownership": launch_receipt.get("primary_ownership", "not_checked"), "repository_api_access": "not_checked",
        "runtime_approval": launch_receipt.get("runtime_approval", "not_checked"),
        "environment": launch_receipt.get("environment", "not_checked"),
        "launcher": launch_receipt,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--assignment", type=Path, required=True)
    parser.add_argument("--profile", type=Path, required=True)
    parser.add_argument("--runtime", choices=("claude", "codex", "muse"), required=True)
    parser.add_argument("--skill", type=Path, action="append", required=True)
    parser.add_argument("--tool", action="append", default=[])
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--launch-config", type=Path, help="check the selected runtime's canonical launcher")
    mode.add_argument("--context-only", action="store_true", help="check portable inputs only; never reports execution ready")
    args = parser.parse_args()
    try:
        receipt = check(args.assignment, args.profile, args.runtime, args.skill, args.tool,
                        args.launch_config, context_only=args.context_only)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        # Do not echo input documents, OS errors or command output into receipts.
        reason = str(error) if isinstance(error, ValidationError) else "preflight input unavailable"
        print(json.dumps({"status": "blocked", "reason": reason}))
        return 1
    print(json.dumps(receipt, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
