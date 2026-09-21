import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).parents[1] / "codex_session.py"


class SessionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.config = self.root / "config.toml"
        self.config.write_text(
            'model="existing-model"\nmodel_provider="openai"\n'
            '[model_providers.sub2api]\nname="sub2api"\n'
            'base_url="https://gateway.example.test"\nwire_api="responses"\n'
            'env_key="SUB2API_API_KEY"\n'
        )
        self.before = self.config.read_bytes()
        self.env = {"PATH": os.environ["PATH"], "CODEX_HOME": str(self.root)}

    def run_entry(self, route="sub2api", extra=(), env=None):
        return subprocess.run(
            [sys.executable, str(SCRIPT), "--route", route, "--model", "test-model",
             "--effort", "high", "--cwd", str(self.root), *extra],
            env=env or self.env, capture_output=True, text=True, check=False,
        )

    def test_plan_is_local_and_does_not_modify_config(self):
        result = self.run_entry()
        self.assertEqual(result.returncode, 0, result.stderr)
        plan = json.loads(result.stdout)
        self.assertEqual(plan["provider"], "sub2api")
        self.assertIn('model_provider="sub2api"', plan["argv"])
        self.assertIn('model="test-model"', plan["argv"])
        self.assertNotIn("gateway.example.test", result.stdout)
        self.assertEqual(self.config.read_bytes(), self.before)
        self.assertEqual(sorted(p.name for p in self.root.iterdir()), ["config.toml"])

    def test_two_routes_are_independent(self):
        results = [self.run_entry(route) for route in ("openai", "sub2api")]
        for result, route in zip(results, ("openai", "sub2api")):
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout)["provider"], route)
        self.assertEqual(self.config.read_bytes(), self.before)

    def test_missing_key_prevents_launch(self):
        result = self.run_entry(extra=("--launch",))
        self.assertEqual(result.returncode, 2)
        self.assertIn("SUB2API_API_KEY", result.stderr)

    def test_no_generic_override_or_resume(self):
        for extra in (("-c", 'model_provider="other"'), ("resume", "abc"),
                      ("--dangerously-bypass-approvals-and-sandbox",)):
            self.assertEqual(self.run_entry(extra=extra).returncode, 2)

    def test_invalid_config_is_not_echoed(self):
        self.config.write_text('secret="SENTINEL\n')
        result = self.run_entry()
        self.assertEqual(result.returncode, 2)
        self.assertNotIn("SENTINEL", result.stdout + result.stderr)

    def test_insecure_or_embedded_credentials_are_rejected(self):
        for url in ("http://gateway.test", "https://user:SECRET@gateway.test",
                    "https://gateway.test?key=SECRET"):
            self.config.write_bytes(self.before.replace(b"https://gateway.example.test", url.encode()))
            result = self.run_entry()
            self.assertEqual(result.returncode, 2)
            self.assertNotIn("SECRET", result.stdout + result.stderr)

    def test_unqualified_auth_method_is_rejected(self):
        self.config.write_bytes(self.before + b'requires_openai_auth=true\n')
        self.assertEqual(self.run_entry().returncode, 2)

    def test_non_string_endpoint_is_rejected_without_traceback(self):
        for value in (b"123", b"true", b"3.14", b"[]", b"{}"):
            self.config.write_bytes(self.before.replace(b'"https://gateway.example.test"', value))
            result = self.run_entry()
            self.assertEqual(result.returncode, 2)
            self.assertIn("endpoint must be a string", result.stderr)
            self.assertNotIn("Traceback", result.stderr)

    def test_official_route_rejects_chatgpt_endpoint_override(self):
        self.config.write_bytes(b'chatgpt_base_url="https://other.test"\n' + self.before)
        self.assertEqual(self.run_entry("openai").returncode, 2)

    def test_official_route_rejects_endpoint_override(self):
        result = self.run_entry("openai", env={**self.env, "OPENAI_BASE_URL": "https://other.test"})
        self.assertEqual(result.returncode, 2)

    def test_launch_uses_argv_without_shell_and_preserves_parent(self):
        spec = importlib.util.spec_from_file_location("codex_session", SCRIPT)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        env = {**self.env, "SUB2API_API_KEY": "DO-NOT-PRINT"}
        with patch.dict(os.environ, env, clear=True), \
             patch.object(module.os, "execvpe") as execute, \
             patch.object(sys, "argv", [str(SCRIPT), "--route", "sub2api", "--model", "test-model",
                                       "--effort", "high", "--cwd", str(self.root), "--launch"]):
            module.main()
            execute.assert_called_once()
            binary, argv, child_env = execute.call_args.args
            self.assertEqual(binary, "codex")
            self.assertEqual(argv[0], "codex")
            self.assertIn('model_provider="sub2api"', argv)
            self.assertNotIn("DO-NOT-PRINT", " ".join(argv))
            self.assertEqual(child_env["SUB2API_API_KEY"], "DO-NOT-PRINT")
            self.assertEqual(os.environ, env)
        self.assertEqual(self.config.read_bytes(), self.before)


if __name__ == "__main__":
    unittest.main()
