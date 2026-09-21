#!/usr/bin/env python3
"""Validate the portable Agent Loop context package without third-party modules."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import sys
from typing import Any


ROOT = Path(__file__).resolve().parent


class ValidationError(ValueError):
    pass


def _matches_type(value: Any, expected: str) -> bool:
    if expected == "object":
        return isinstance(value, dict)
    if expected == "array":
        return isinstance(value, list)
    if expected == "string":
        return isinstance(value, str)
    if expected == "integer":
        return isinstance(value, int) and not isinstance(value, bool)
    if expected == "boolean":
        return isinstance(value, bool)
    raise ValidationError(f"unsupported schema type {expected!r}")


def validate(value: Any, schema: dict[str, Any], path: str = "$") -> None:
    expected = schema.get("type")
    if expected and not _matches_type(value, expected):
        raise ValidationError(f"{path}: expected {expected}")
    if "const" in schema and value != schema["const"]:
        raise ValidationError(f"{path}: expected constant {schema['const']!r}")
    if "enum" in schema and value not in schema["enum"]:
        raise ValidationError(f"{path}: value is not in the allowed enum")

    if isinstance(value, dict):
        required = schema.get("required", [])
        missing = [key for key in required if key not in value]
        if missing:
            raise ValidationError(f"{path}: missing required keys {missing}")
        properties = schema.get("properties", {})
        if schema.get("additionalProperties") is False:
            extra = sorted(set(value) - set(properties))
            if extra:
                raise ValidationError(f"{path}: unexpected keys {extra}")
        for key, child in value.items():
            if key in properties:
                validate(child, properties[key], f"{path}.{key}")

    if isinstance(value, list) and "items" in schema:
        for index, child in enumerate(value):
            validate(child, schema["items"], f"{path}[{index}]")

    if isinstance(value, str):
        if len(value) < schema.get("minLength", 0):
            raise ValidationError(f"{path}: string is too short")
        pattern = schema.get("pattern")
        if pattern and re.search(pattern, value) is None:
            raise ValidationError(f"{path}: string does not match {pattern!r}")

    if isinstance(value, int) and not isinstance(value, bool):
        if value < schema.get("minimum", value):
            raise ValidationError(f"{path}: number is below minimum")


def load_json(path: Path) -> Any:
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def validate_file(document: Path, schema: Path) -> dict[str, Any]:
    value = load_json(document)
    validate(value, load_json(schema))
    return value


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--assignment",
        type=Path,
        default=ROOT / "examples" / "assignment.studio-worker.json",
    )
    parser.add_argument(
        "--checkpoint",
        type=Path,
        default=ROOT / "examples" / "checkpoint.studio-worker.json",
    )
    parser.add_argument(
        "--profile",
        type=Path,
        default=ROOT / "projects" / "studio" / "profile.json",
    )
    args = parser.parse_args()

    try:
        assignment = validate_file(
            args.assignment, ROOT / "schemas" / "assignment-envelope.schema.json"
        )
        checkpoint = validate_file(
            args.checkpoint, ROOT / "schemas" / "checkpoint.schema.json"
        )
        profile = validate_file(
            args.profile, ROOT / "schemas" / "project-profile.schema.json"
        )
        compact_size = len(
            json.dumps(assignment, separators=(",", ":"), ensure_ascii=False).encode()
        )
        if compact_size > 2048:
            raise ValidationError(
                f"assignment envelope is {compact_size} bytes; maximum is 2048"
            )
        selected = assignment["project_profile"]
        if selected["id"] != profile["id"] or selected["version"] != profile["version"]:
            raise ValidationError("assignment project profile identity does not match")
        if checkpoint["assignment_id"] != assignment["assignment_id"]:
            raise ValidationError("checkpoint assignment identity does not match")
    except (OSError, json.JSONDecodeError, ValidationError) as error:
        print(f"context package invalid: {error}", file=sys.stderr)
        return 1

    print(
        json.dumps(
            {
                "status": "valid",
                "assignment_bytes": compact_size,
                "assignment_schema": assignment["schema_version"],
                "checkpoint_schema": checkpoint["schema_version"],
                "project_profile": f"{profile['id']}@{profile['version']}",
            },
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

