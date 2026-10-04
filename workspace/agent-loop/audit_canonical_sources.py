#!/usr/bin/env python3
"""Deterministic audit for role-skill canonical sources (Issue #14).

Fails on:
- duplicate skill names with different content across the declared in-repo
  skill roots (discovery would depend on search order, not canonical revision);
- a manifest entry whose in-repo source path is missing, has no SKILL.md, or
  declares a different skill name;
- an installed skill directory entry that is neither a symlink into a declared
  canonical in-repo source nor byte-identical to it (unknown source revision).

Source + docs + audit only: this command never relinks or reinstalls live
skills. Standard library only.

Usage:
    python3 workspace/agent-loop/audit_canonical_sources.py
    python3 workspace/agent-loop/audit_canonical_sources.py --installed <dir>
    python3 workspace/agent-loop/audit_canonical_sources.py --manifest <path> --root <repo>
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parent.parent.parent
DEFAULT_MANIFEST = Path(__file__).resolve().parent / "canonical-sources.json"


def skill_name(skill_md: Path) -> str | None:
    try:
        text = skill_md.read_text(encoding="utf-8")
    except OSError:
        return None
    if not text.startswith("---\n"):
        return None
    front = text.split("---\n", 2)
    if len(front) < 3:
        return None
    for line in front[1].splitlines():
        if line.startswith("name:"):
            return line.split(":", 1)[1].strip()
    return None


def fingerprint(directory: Path) -> str:
    digest = hashlib.sha256()
    for path in sorted(directory.rglob("*")):
        if path.is_dir() or path.is_symlink():
            continue
        digest.update(path.relative_to(directory).as_posix().encode())
        digest.update(path.read_bytes())
    return digest.hexdigest()


def collect_skills(root: Path, roots: list[str]) -> dict[str, list[Path]]:
    found: dict[str, list[Path]] = {}
    for entry in roots:
        base = root / entry
        if not base.is_dir():
            continue
        for skill_md in sorted(base.rglob("SKILL.md")):
            name = skill_name(skill_md)
            if name:
                found.setdefault(name, []).append(skill_md.parent)
    return found


def check_duplicates(root: Path, manifest: dict) -> list[str]:
    errors: list[str] = []
    found = collect_skills(root, manifest.get("skill_roots", []))
    for name in sorted(found):
        dirs = found[name]
        if len(dirs) < 2:
            continue
        prints = {fingerprint(d) for d in dirs}
        if len(prints) > 1:
            locations = ", ".join(str(d.relative_to(root)) for d in dirs)
            errors.append(
                f"duplicate skill {name!r} with different content: {locations}")
    return errors


def check_manifest(manifest: dict, root: Path) -> list[str]:
    errors: list[str] = []
    for name in sorted(manifest.get("sources", {})):
        entry = manifest["sources"][name]
        path = entry.get("path")
        if entry.get("status") in ("canonical",) or (
                entry.get("kind") == "squad-repo" and path):
            if not path:
                errors.append(f"canonical skill {name!r} has no source path")
                continue
            source = root / path
            skill_md = source / "SKILL.md"
            if not skill_md.is_file():
                errors.append(
                    f"canonical skill {name!r} source missing: {path}/SKILL.md")
                continue
            actual = skill_name(skill_md)
            if actual != name:
                errors.append(
                    f"canonical skill {name!r} source declares {actual!r}: {path}")
    return errors


def canonical_targets(manifest: dict, root: Path) -> dict[str, Path]:
    targets: dict[str, Path] = {}
    for name, entry in manifest.get("sources", {}).items():
        path = entry.get("path")
        if entry.get("kind") == "squad-repo" and path:
            targets[name] = (root / path).resolve()
    return targets


def check_installed(installed: Path, manifest: dict, root: Path) -> list[str]:
    errors: list[str] = []
    if not installed.is_dir():
        return [f"installed skills directory missing: {installed}"]
    targets = canonical_targets(manifest, root)
    for entry in sorted(installed.iterdir(), key=lambda p: p.name):
        if entry.name.startswith("."):
            continue
        if entry.is_symlink():
            resolved = entry.resolve()
            for name, target in targets.items():
                if resolved == target or (
                        target.is_dir() and resolved.is_relative_to(target)):
                    break
            else:
                errors.append(
                    f"installed skill {entry.name!r} links outside canonical "
                    f"sources: {os.readlink(entry)}")
            continue
        name = skill_name(entry / "SKILL.md") if entry.is_dir() else None
        target = targets.get(name or entry.name)
        if target is None or not target.is_dir():
            errors.append(
                f"installed skill {entry.name!r} has unknown source revision "
                "(not a symlink into a declared canonical source)")
        elif fingerprint(entry) != fingerprint(target):
            errors.append(
                f"installed skill {entry.name!r} content differs from canonical "
                f"source {target}")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, default=DEFAULT_MANIFEST)
    parser.add_argument("--root", type=Path, default=ROOT)
    parser.add_argument("--installed", type=Path, default=None,
                        help="optional installed skills dir to audit; "
                             "never modified, only read")
    args = parser.parse_args()
    try:
        manifest = json.loads(args.manifest.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        print(f"canonical sources audit: {error}", file=sys.stderr)
        return 1
    errors = check_manifest(manifest, args.root)
    errors += check_duplicates(args.root, manifest)
    if args.installed is not None:
        errors += check_installed(args.installed, manifest, args.root)
    if errors:
        for error in errors:
            print(f"canonical sources audit: {error}", file=sys.stderr)
        return 1
    print("canonical sources audit: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
