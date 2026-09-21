#!/usr/bin/env python3
"""Plan or start one new Codex CLI session without changing shared configuration."""

import argparse
import json
import os
from pathlib import Path
import re
import sys
import tomllib
from urllib.parse import urlsplit


def main():
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument("--route", choices=("openai", "sub2api"), required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--effort", choices=("low", "medium", "high", "xhigh"), required=True)
    parser.add_argument("--cwd", type=Path, required=True)
    parser.add_argument("--sandbox", choices=("read-only", "workspace-write"), default="read-only")
    parser.add_argument("--launch", action="store_true", help="start Codex; default only prints a safe plan")
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}", args.model):
        parser.error("model must be an explicit model identifier")
    if not args.cwd.is_dir():
        parser.error("cwd must be an existing directory")
    codex_home = Path(os.environ.get("CODEX_HOME", str(Path.home() / ".codex")))
    try:
        config = tomllib.loads((codex_home / "config.toml").read_text())
    except (OSError, UnicodeError, tomllib.TOMLDecodeError):
        parser.error("cannot read valid user-level Codex configuration; values withheld")
    if args.route == "openai":
        if (config.get("openai_base_url") or config.get("chatgpt_base_url")
                or os.environ.get("OPENAI_BASE_URL")):
            parser.error("openai route has an endpoint override; qualify it separately")
    else:
        providers = config.get("model_providers", {})
        provider = providers.get("sub2api", {}) if isinstance(providers, dict) else {}
        if not isinstance(provider, dict):
            parser.error("sub2api provider must be a table")
        endpoint = provider.get("base_url", "")
        if not isinstance(endpoint, str):
            parser.error("sub2api endpoint must be a string; value withheld")
        try:
            url = urlsplit(endpoint)
            valid_url = (url.scheme == "https" and bool(url.hostname) and not url.username
                         and not url.password and not url.query and not url.fragment)
        except (TypeError, ValueError):
            valid_url = False
        if not valid_url:
            parser.error("sub2api requires an HTTPS endpoint without embedded credentials or query")
        if provider.get("wire_api") != "responses" or provider.get("env_key") != "SUB2API_API_KEY":
            parser.error("sub2api requires responses and the SUB2API_API_KEY environment reference")
        if any(provider.get(key) for key in ("auth", "requires_openai_auth", "experimental_bearer_token",
                                              "http_headers", "env_http_headers")):
            parser.error("sub2api has additional authentication settings; qualify them separately")
        if args.launch and not os.environ.get("SUB2API_API_KEY", "").strip():
            parser.error("SUB2API_API_KEY is unavailable; no launch or fallback performed")
    argv = [
        "codex", "--config", f"model_provider={json.dumps(args.route)}",
        "--config", f"model={json.dumps(args.model)}",
        "--config", f"model_reasoning_effort={json.dumps(args.effort)}",
        "--sandbox", args.sandbox, "--ask-for-approval", "on-request",
        "--cd", str(args.cwd.resolve()),
    ]
    if not args.launch:
        print(json.dumps({"schema_version": "codex.session-plan.v1", "provider": args.route,
                          "model": args.model, "effort": args.effort, "argv": argv,
                          "model_availability": "unverified", "quota": "unverified",
                          "launch": False}, indent=2))
        return
    child_env = dict(os.environ)
    if args.route == "openai":
        child_env.pop("SUB2API_API_KEY", None)
    try:
        os.execvpe(argv[0], argv, child_env)
    except OSError:
        parser.error("could not execute Codex; no alternate route attempted")


if __name__ == "__main__":
    main()
