import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).parents[1]
SCRIPT = ROOT / "validate_context_package.py"
ASSIGNMENT = ROOT / "examples" / "assignment.studio-worker.json"


def load_module():
    spec = importlib.util.spec_from_file_location("validate_context_package", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ContextPackageTests(unittest.TestCase):
    def test_default_package_is_valid_and_assignment_is_bounded(self):
        result = subprocess.run(
            [sys.executable, str(SCRIPT)], capture_output=True, text=True, check=False
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        receipt = json.loads(result.stdout)
        self.assertEqual(receipt["status"], "valid")
        self.assertLessEqual(receipt["assignment_bytes"], 2048)

    def test_assignment_rejects_unknown_evidence_kind(self):
        module = load_module()
        document = json.loads(ASSIGNMENT.read_text())
        document["evidence_required"].append("staging_acceptance")
        schema = json.loads(
            (ROOT / "schemas" / "assignment-envelope.schema.json").read_text()
        )
        with self.assertRaises(module.ValidationError):
            module.validate(document, schema)

    def test_assignment_rejects_implicit_production_or_extra_fields(self):
        module = load_module()
        document = json.loads(ASSIGNMENT.read_text())
        del document["authorization"]["production"]
        document["dispatcher_prompt"] = "copy of the complete conversation"
        schema = json.loads(
            (ROOT / "schemas" / "assignment-envelope.schema.json").read_text()
        )
        with self.assertRaises(module.ValidationError):
            module.validate(document, schema)

    def test_design_reference_is_bounded_and_requires_ready_revision(self):
        module = load_module()
        document = json.loads(ASSIGNMENT.read_text())
        schema = json.loads((ROOT / "schemas" / "assignment-envelope.schema.json").read_text())
        document["design_admission"] = {"section": "Design / admission", "revision": "d2", "status": "READY"}
        module.validate(document, schema)
        self.assertLessEqual(len(json.dumps(document).encode()), 2048)
        for invalid in (
            {"section": "Design / admission", "status": "READY"},
            {"section": "Design / admission", "revision": "d2", "status": "needs-decision"},
            {"section": "Design / admission", "revision": "", "status": "READY"},
        ):
            document["design_admission"] = invalid
            with self.assertRaises(module.ValidationError):
                module.validate(document, schema)

    def test_checkpoint_must_match_assignment(self):
        checkpoint = json.loads(
            (ROOT / "examples" / "checkpoint.studio-worker.json").read_text()
        )
        checkpoint["assignment_id"] = "different/attempt"
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "checkpoint.json"
            path.write_text(json.dumps(checkpoint))
            result = subprocess.run(
                [sys.executable, str(SCRIPT), "--checkpoint", str(path)],
                capture_output=True,
                text=True,
                check=False,
            )
        self.assertEqual(result.returncode, 1)
        self.assertIn("identity does not match", result.stderr)

    def test_worker_skill_has_no_legacy_workspace_dependency(self):
        skill = (ROOT / "roles" / "worker" / "SKILL.md").read_text()
        self.assertNotIn("/Users/", skill)
        self.assertNotIn("Data Analyze", skill)
        self.assertIn("Phase-scoped context", skill)

    def test_worker_review_contract_is_pr_wide_single_flight(self):
        skill = (ROOT / "roles" / "worker" / "SKILL.md").read_text()
        review = (
            ROOT / "roles" / "worker" / "references" / "review-readiness.md"
        ).read_text()
        profile = json.loads((ROOT / "projects" / "studio" / "profile.json").read_text())

        self.assertIn("references/review-readiness.md", skill)
        self.assertIn("complete `base...head` PR diff", skill)
        self.assertIn("at most one review invocation in flight for the PR", skill)
        self.assertIn("authoring history, not separate review units", review)
        self.assertIn("Allow at most one in-flight independent review", review)
        self.assertIn("write barrier", review)
        self.assertIn("final complete frozen diff", " ".join(profile["gates"]["review"]))


if __name__ == "__main__":
    unittest.main()
