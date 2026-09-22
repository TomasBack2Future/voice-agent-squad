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


def repository_name(remote: str) -> str:
    for prefix in ("git@github.com:", "https://github.com/", "ssh://git@github.com/"):
        if remote.startswith(prefix):
            return remote[len(prefix):].removesuffix(".git")
    raise ValidationError("origin must identify the assigned GitHub repository")


def check_worktree(assignment: dict) -> None:
    worktree = Path(assignment["worktree"]).resolve(strict=True)
    if Path(git(worktree, "rev-parse", "--show-toplevel")).resolve() != worktree:
        raise ValidationError("assignment worktree must be the Git root")
    if git(worktree, "branch", "--show-current") != assignment["branch"]:
        raise ValidationError("assignment branch mismatch")
    if git(worktree, "rev-parse", "HEAD") != assignment["base_sha"]:
        raise ValidationError("cold-start base changed; refresh the assignment before launch")
    if repository_name(git(worktree, "remote", "get-url", "origin")) != assignment["repository"]:
        raise ValidationError("assignment repository mismatch")
    if git(worktree, "status", "--porcelain"):
        raise ValidationError("cold-start worktree is dirty")
    if not (worktree / "AGENTS.md").is_file():
        raise ValidationError("repository AGENTS.md is missing")


def check(assignment_path: Path, profile_path: Path, runtime: str,
          skills: list[Path], tools: list[str], launch_config: Path | None = None) -> dict:
    assignment = validate_file(
        assignment_path, ROOT / "schemas/assignment-envelope.schema.json"
    )
    profile = validate_file(profile_path, ROOT / "schemas/project-profile.schema.json")
    selected = assignment["project_profile"]
    declared_profile = Path(selected["path"])
    if not declared_profile.is_absolute():
        declared_profile = Path(assignment["worktree"]) / declared_profile
    if declared_profile.resolve() != profile_path.resolve():
        raise ValidationError("profile path differs from the assignment")
    if (selected["id"], selected["version"], assignment["repository"]) != (
        profile["id"], profile["version"], profile["repository"]
    ):
        raise ValidationError("assignment/profile identity mismatch")
    if len(json.dumps(assignment, separators=(",", ":")).encode()) > 2048:
        raise ValidationError("assignment exceeds 2048-byte cold-start budget")
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
        if runtime != "claude":
            raise ValidationError("launch-config currently supports Claude only")
        from claude_worker_launcher import check_launch
        launch_receipt = check_launch(assignment, launch_config)
    return {
        "schema_version": "agent-loop.startup-receipt.v1", "status": "ready",
        "checked_at": datetime.now(timezone.utc).isoformat(),
        "assignment_id": assignment["assignment_id"],
        "assignment_sha256": hashlib.sha256(assignment_path.read_bytes()).hexdigest(),
        "base_sha": assignment["base_sha"], "runtime": runtime,
        "profile_sha256": hashlib.sha256(profile_path.read_bytes()).hexdigest(),
        "skills": receipts, "executables": available,
        "ownership": "not_checked", "repository_api_access": "not_checked",
        "runtime_approval": "not_checked", "environment": "not_checked",
        "launcher": launch_receipt,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--assignment", type=Path, required=True)
    parser.add_argument("--profile", type=Path, required=True)
    parser.add_argument("--runtime", choices=("claude", "codex"), required=True)
    parser.add_argument("--skill", type=Path, action="append", required=True)
    parser.add_argument("--tool", action="append", default=[])
    parser.add_argument("--launch-config", type=Path, help="check the canonical Claude launcher with child identity")
    args = parser.parse_args()
    try:
        receipt = check(args.assignment, args.profile, args.runtime, args.skill, args.tool, args.launch_config)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        # Do not echo input documents, OS errors or command output into receipts.
        reason = str(error) if isinstance(error, ValidationError) else "preflight input unavailable"
        print(json.dumps({"status": "blocked", "reason": reason}))
        return 1
    print(json.dumps(receipt, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
