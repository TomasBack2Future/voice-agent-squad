#!/usr/bin/env python3
"""Install the pinned golangci-lint release and verify its configuration.

Standard library only. Only identified transient network failures are retried,
a bounded number of times; an exhausted retry budget, a checksum mismatch and
every unrecognised failure fail immediately and are never retried to green.
"""
import argparse
import hashlib
import os
import socket
import subprocess
import sys
import tarfile
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
PIN_FILE = os.path.join(HERE, "golangci-lint.pin")
ATTEMPTS = 3
BACKOFF_SECONDS = (2, 6)
DOWNLOAD_TIMEOUT = 60
RELEASE_URL = "https://github.com/golangci/golangci-lint/releases/download/v{v}/golangci-lint-{v}-linux-amd64.tar.gz"

SCHEMA_MARKER = 'failing loading "https://golangci-lint.run/jsonschema/'
NETWORK_MARKERS = (
    "context deadline exceeded",
    "timeout",
    "timed out",
    "connection reset",
    "connection refused",
    "unexpected eof",
    "no such host",
    "tls handshake",
    "temporary failure",
)

OK = "ok"
TRANSIENT_EXHAUSTED = "transient-exhausted"
PERMANENT = "permanent"


def read_pin(path=PIN_FILE):
    pin = {}
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line and not line.startswith("#"):
                key, _, value = line.partition("=")
                pin[key.strip()] = value.strip()
    return pin


def is_transient_verify_output(output):
    """A schema download failure with a network-class cause, and nothing else."""
    text = output.lower()
    return SCHEMA_MARKER.lower() in text and any(m in text for m in NETWORK_MARKERS)


def is_transient_download_error(err):
    if isinstance(err, urllib.error.HTTPError):
        return err.code >= 500
    if isinstance(err, urllib.error.URLError):
        return isinstance(err.reason, (socket.timeout, TimeoutError, ConnectionError, socket.gaierror))
    return isinstance(err, (socket.timeout, TimeoutError, ConnectionError))


def with_retries(attempt, is_transient, sleep=None):
    """Run attempt() -> (ok, detail, error). Returns (outcome, tries, detail)."""
    sleep = sleep or time.sleep
    detail = ""
    for tries in range(1, ATTEMPTS + 1):
        ok, detail, err = attempt()
        if ok:
            return OK, tries, detail
        if not is_transient(detail, err):
            return PERMANENT, tries, detail
        if tries < ATTEMPTS:
            sleep(BACKOFF_SECONDS[min(tries - 1, len(BACKOFF_SECONDS) - 1)])
    return TRANSIENT_EXHAUSTED, ATTEMPTS, detail


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def download(url, dest, fetch=None):
    def attempt():
        try:
            if fetch is not None:
                fetch(url, dest)
            else:
                with urllib.request.urlopen(url, timeout=DOWNLOAD_TIMEOUT) as resp, open(dest, "wb") as out:
                    out.write(resp.read())
            return True, "", None
        except (urllib.error.URLError, OSError) as err:
            return False, f"{type(err).__name__}: {err}", err
    return with_retries(attempt, lambda _detail, err: is_transient_download_error(err))


def install(dest_dir, pin, fetch=None):
    """Ensure dest_dir/bin/golangci-lint is the pinned, checksum-verified release."""
    os.makedirs(dest_dir, exist_ok=True)
    tarball = os.path.join(dest_dir, "golangci-lint.tar.gz")
    want = pin["sha256_linux_amd64"]
    if os.path.exists(tarball) and sha256_file(tarball) != want:
        os.remove(tarball)
    if not os.path.exists(tarball):
        outcome, tries, detail = download(RELEASE_URL.format(v=pin["version"]), tarball, fetch)
        if outcome != OK:
            return f"download {outcome} after {tries} attempt(s): {detail}"
        if sha256_file(tarball) != want:
            os.remove(tarball)
            return "downloaded archive does not match the pinned SHA-256"
    bin_dir = os.path.join(dest_dir, "bin")
    os.makedirs(bin_dir, exist_ok=True)
    member = f"golangci-lint-{pin['version']}-linux-amd64/golangci-lint"
    with tarfile.open(tarball) as tar:
        src = tar.extractfile(member)
        if src is None:
            return f"archive has no {member}"
        target = os.path.join(bin_dir, "golangci-lint")
        with open(target, "wb") as out:
            out.write(src.read())
    os.chmod(target, 0o755)
    return None


def verify_config(run=None, sleep=None):
    def default_run():
        proc = subprocess.run(["golangci-lint", "config", "verify"], capture_output=True, text=True)
        return proc.returncode, proc.stdout + proc.stderr
    runner = run or default_run

    def attempt():
        rc, output = runner()
        return rc == 0, output, None
    return with_retries(attempt, lambda detail, _err: is_transient_verify_output(detail), sleep)


def cmd_install(args):
    error = install(args.dest, read_pin())
    if error:
        sys.exit(f"golangci-lint install failed: {error}")
    bin_dir = os.path.join(os.path.abspath(args.dest), "bin")
    print(f"golangci-lint {read_pin()['version']} installed at {bin_dir}")
    target = os.environ.get("GITHUB_PATH")
    if target:
        with open(target, "a", encoding="utf-8") as fh:
            fh.write(bin_dir + "\n")


def cmd_verify(_args):
    outcome, tries, detail = verify_config()
    if outcome == OK:
        print(f"golangci-lint config verify ok (attempt {tries})")
        return
    print(detail, file=sys.stderr)
    if outcome == TRANSIENT_EXHAUSTED:
        sys.exit(f"config verify failed: external schema fetch kept failing after {tries} attempts; rerun the job (not a configuration error)")
    sys.exit("config verify failed: not a recognised transient failure; not retried")


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    sub = ap.add_subparsers(dest="cmd", required=True)
    i = sub.add_parser("install")
    i.add_argument("--dest", required=True)
    i.set_defaults(fn=cmd_install)
    sub.add_parser("verify").set_defaults(fn=cmd_verify)
    args = ap.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
