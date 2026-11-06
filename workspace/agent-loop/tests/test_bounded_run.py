import json
import os
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
RUNNER = ROOT / "bounded_run.py"


def gone(pid, seconds=5.0):
    deadline = time.time() + seconds
    while time.time() < deadline:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return True
        time.sleep(0.02)
    return False


def read_pid(path, seconds=5.0):
    deadline = time.time() + seconds
    while time.time() < deadline:
        if path.exists() and path.read_text().endswith("\n"):
            return int(path.read_text())
        time.sleep(0.02)
    raise AssertionError("no pid written to %s" % path)


class BoundedRunTest(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp())
        self.pid_file = self.dir / "descendant.pid"
        self.record = self.dir / "report.json"
        self.env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")

    def run_runner(self, script, timeout=30, cpu=30, wait=True):
        argv = [sys.executable, "-I", str(RUNNER), "--timeout", str(timeout), "--cpu-seconds", str(cpu),
                "--owner", "test-owner", "--record", str(self.record), "--", "/bin/sh", "-c", script, "sh", str(self.pid_file)]
        if not wait:
            return subprocess.Popen(argv, env=self.env, stderr=subprocess.DEVNULL)
        return subprocess.run(argv, env=self.env, capture_output=True, text=True, timeout=60)

    def test_normal_exit_reaps_a_background_descendant(self):
        # The #1965 shape: start a background load process and exit 0.
        r = self.run_runner('sleep 300 & echo $! > "$1"; exit 0')
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue(gone(read_pid(self.pid_file)))
        report = json.loads(self.record.read_text())
        self.assertEqual(report["owner"], "test-owner")
        self.assertEqual(report["descendants_reaped"], 1)
        self.assertFalse(report["timed_out"])

    def test_failure_exit_code_is_kept_and_tree_cleaned(self):
        r = self.run_runner('sleep 300 & echo $! > "$1"; exit 3')
        self.assertEqual(r.returncode, 3)
        self.assertTrue(gone(read_pid(self.pid_file)))

    def test_timeout_kills_the_whole_tree(self):
        started = time.time()
        r = self.run_runner('sleep 300 & echo $! > "$1"; exec sleep 300', timeout=1)
        self.assertEqual(r.returncode, 124)
        self.assertLess(time.time() - started, 15)
        self.assertTrue(gone(read_pid(self.pid_file)))
        self.assertTrue(json.loads(self.record.read_text())["timed_out"])

    def test_cpu_budget_stops_a_burner(self):
        # One CPU-second budget: a bounded, isolated check, not host pressure.
        started = time.time()
        r = self.run_runner('echo $$ > "$1"; while :; do :; done', timeout=60, cpu=1)
        self.assertNotEqual(r.returncode, 0)
        self.assertLess(time.time() - started, 30)

    def test_interrupt_cleans_up(self):
        p = self.run_runner('sleep 300 & echo $! > "$1"; exec sleep 300', wait=False)
        descendant = read_pid(self.pid_file)
        p.send_signal(signal.SIGTERM)
        self.assertEqual(p.wait(timeout=30), 128 + signal.SIGTERM)
        self.assertTrue(gone(descendant))
        self.assertEqual(json.loads(self.record.read_text())["interrupted"], "SIGTERM")

    def test_runner_crash_is_cleaned_by_the_anchor(self):
        p = self.run_runner('sleep 300 & echo $! > "$1"; exec sleep 300', wait=False)
        descendant = read_pid(self.pid_file)
        p.send_signal(signal.SIGKILL)
        p.wait(timeout=30)
        self.assertTrue(gone(descendant), "descendant survived a SIGKILLed runner")

    def test_other_owners_are_untouched(self):
        other = subprocess.Popen(["/bin/sleep", "300"], preexec_fn=lambda: os.setpgid(0, 0))
        try:
            r = self.run_runner('sleep 300 & echo $! > "$1"; exit 0')
            self.assertEqual(r.returncode, 0)
            self.assertIsNone(other.poll(), "cleanup reached another owner's process")
        finally:
            other.kill()
            other.wait()

    def test_limits_are_required_and_bounded(self):
        for args in (["--timeout", "0", "--cpu-seconds", "1"], ["--timeout", "1", "--cpu-seconds", "99999"]):
            r = subprocess.run([sys.executable, "-I", str(RUNNER), *args, "--owner", "x", "--", "true"], env=self.env, capture_output=True)
            self.assertEqual(r.returncode, 2)


if __name__ == "__main__":
    unittest.main()
