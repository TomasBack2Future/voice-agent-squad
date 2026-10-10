import hashlib
import io
import os
import sys
import tarfile
import tempfile
import unittest
import urllib.error

sys.dont_write_bytecode = True

import golangci  # noqa: E402

SCHEMA_TIMEOUT = ('The command is terminated due to an error: [.golangci.yml] validate: compile schema: failing loading '
                  '"https://golangci-lint.run/jsonschema/golangci.v2.11.jsonschema.json": Get "...": context deadline exceeded')


def no_sleep(_seconds):
    pass


def archive(version, payload=b"binary"):
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        info = tarfile.TarInfo(f"golangci-lint-{version}-linux-amd64/golangci-lint")
        info.size = len(payload)
        tar.addfile(info, io.BytesIO(payload))
    return buf.getvalue()


class VerifyRetryTest(unittest.TestCase):
    def run_sequence(self, outputs):
        calls = []

        def run():
            calls.append(1)
            return outputs[min(len(calls), len(outputs)) - 1]
        return golangci.verify_config(run=run, sleep=no_sleep), len(calls)

    def test_schema_timeout_is_retried_then_succeeds(self):
        (outcome, tries, _), calls = self.run_sequence([(3, SCHEMA_TIMEOUT), (0, "")])
        self.assertEqual((outcome, tries, calls), (golangci.OK, 2, 2))

    def test_schema_timeout_exhaustion_fails(self):
        (outcome, tries, _), calls = self.run_sequence([(3, SCHEMA_TIMEOUT)])
        self.assertEqual((outcome, tries, calls), (golangci.TRANSIENT_EXHAUSTED, golangci.ATTEMPTS, golangci.ATTEMPTS))

    def test_invalid_configuration_is_not_retried(self):
        (outcome, _, _), calls = self.run_sequence([(3, "[.golangci.yml] validate: jsonschema: additional properties 'bogus' not allowed")])
        self.assertEqual((outcome, calls), (golangci.PERMANENT, 1))

    def test_unknown_failure_is_not_retried(self):
        (outcome, _, _), calls = self.run_sequence([(1, "something unexpected")])
        self.assertEqual((outcome, calls), (golangci.PERMANENT, 1))

    def test_network_words_without_schema_marker_are_not_transient(self):
        (outcome, _, _), calls = self.run_sequence([(1, "linter crashed: context deadline exceeded")])
        self.assertEqual((outcome, calls), (golangci.PERMANENT, 1))

    def test_success_runs_once(self):
        (outcome, tries, _), calls = self.run_sequence([(0, "")])
        self.assertEqual((outcome, tries, calls), (golangci.OK, 1, 1))


class InstallTest(unittest.TestCase):
    def pin(self, data):
        return {"version": "9.9.9", "sha256_linux_amd64": hashlib.sha256(data).hexdigest()}

    def fetcher(self, data, failures=()):
        state = {"calls": 0}

        def fetch(_url, dest):
            state["calls"] += 1
            if state["calls"] <= len(failures):
                raise failures[state["calls"] - 1]
            with open(dest, "wb") as fh:
                fh.write(data)
        return fetch, state

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dest = os.path.join(self.tmp.name, "gl")
        self._sleep = golangci.time.sleep
        golangci.time.sleep = no_sleep
        self.addCleanup(setattr, golangci.time, "sleep", self._sleep)

    def test_downloads_verifies_and_extracts(self):
        data = archive("9.9.9")
        fetch, _ = self.fetcher(data)
        self.assertIsNone(golangci.install(self.dest, self.pin(data), fetch=fetch))
        self.assertTrue(os.access(os.path.join(self.dest, "bin", "golangci-lint"), os.X_OK))

    def test_checksum_mismatch_fails_without_retry(self):
        data = archive("9.9.9")
        fetch, state = self.fetcher(b"tampered")
        error = golangci.install(self.dest, self.pin(data), fetch=fetch)
        self.assertIn("SHA-256", error)
        self.assertEqual(state["calls"], 1)

    def test_transient_download_errors_are_retried(self):
        data = archive("9.9.9")
        fetch, state = self.fetcher(data, failures=[urllib.error.URLError(TimeoutError("timed out")),
                                                    urllib.error.HTTPError("u", 503, "x", {}, None)])
        self.assertIsNone(golangci.install(self.dest, self.pin(data), fetch=fetch))
        self.assertEqual(state["calls"], 3)

    def test_download_retry_budget_is_bounded(self):
        data = archive("9.9.9")
        fetch, state = self.fetcher(data, failures=[urllib.error.URLError(TimeoutError("t"))] * 10)
        error = golangci.install(self.dest, self.pin(data), fetch=fetch)
        self.assertIn("transient-exhausted", error)
        self.assertEqual(state["calls"], golangci.ATTEMPTS)

    def test_client_errors_are_not_retried(self):
        data = archive("9.9.9")
        fetch, state = self.fetcher(data, failures=[urllib.error.HTTPError("u", 404, "nf", {}, None)] * 3)
        self.assertIn("permanent", golangci.install(self.dest, self.pin(data), fetch=fetch))
        self.assertEqual(state["calls"], 1)

    def test_valid_cached_archive_skips_download(self):
        data = archive("9.9.9")
        fetch, state = self.fetcher(data)
        self.assertIsNone(golangci.install(self.dest, self.pin(data), fetch=fetch))
        self.assertIsNone(golangci.install(self.dest, self.pin(data), fetch=fetch))
        self.assertEqual(state["calls"], 1)

    def test_corrupt_cached_archive_is_replaced(self):
        data = archive("9.9.9")
        os.makedirs(self.dest)
        with open(os.path.join(self.dest, "golangci-lint.tar.gz"), "wb") as fh:
            fh.write(b"corrupt")
        fetch, state = self.fetcher(data)
        self.assertIsNone(golangci.install(self.dest, self.pin(data), fetch=fetch))
        self.assertEqual(state["calls"], 1)


class PinTest(unittest.TestCase):
    def test_pin_file_is_complete(self):
        pin = golangci.read_pin()
        self.assertRegex(pin["version"], r"^\d+\.\d+\.\d+$")
        self.assertRegex(pin["sha256_linux_amd64"], r"^[0-9a-f]{64}$")


if __name__ == "__main__":
    unittest.main()
