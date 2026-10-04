"""Regression tests for the role-skill canonical-source manifest and audit.

Issue #14: skill discovery must depend on one declared canonical revision per
skill, not on search order across global / Data Analyze / repository copies.
The audit fails on duplicate skill names with different content and on a
canonical entry whose in-repo source is missing or mismatched.
Only stdlib; discovered by `python3 -m unittest discover -s workspace/agent-loop/tests`.
"""
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2].joinpath("..").resolve()
AGENT_LOOP = ROOT / "workspace" / "agent-loop"
MANIFEST = AGENT_LOOP / "canonical-sources.json"

spec = importlib.util.spec_from_file_location(
    "audit_canonical_sources", AGENT_LOOP / "audit_canonical_sources.py"
)
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)


def write_skill(directory, name, body):
    directory.mkdir(parents=True, exist_ok=True)
    (directory / "SKILL.md").write_text(f"---\nname: {name}\ndescription: T\n---\n{body}\n")


class CanonicalSourcesTests(unittest.TestCase):
    def test_manifest_exists_and_declares_routed_skills(self):
        self.assertTrue(MANIFEST.is_file(), "canonical-sources.json must exist")
        manifest = json.loads(MANIFEST.read_text())
        self.assertEqual(manifest.get("schema_version"), "agent-loop.canonical-sources.v1")
        sources = manifest.get("sources", {})
        for name in ("squad-dispatcher", "studio-issue-worker", "cmux-sessions",
                      "squad-env-recovery", "transfer-docker-image",
                      "convoai-call-studio-import", "studio-sls-logs",
                      "studio-staging-sls-logs"):
            self.assertIn(name, sources, f"manifest must declare {name}")
            self.assertIn("kind", sources[name])
            self.assertIn("status", sources[name])

    def test_manifest_check_passes_on_current_tree(self):
        manifest = json.loads(MANIFEST.read_text())
        self.assertEqual(audit.check_manifest(manifest, ROOT), [])

    def test_duplicate_scan_passes_on_current_tree(self):
        manifest = json.loads(MANIFEST.read_text())
        self.assertEqual(audit.check_duplicates(ROOT, manifest), [])

    def test_duplicate_name_with_different_content_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_skill(root / "tree-a" / "dup", "dup", "version one")
            write_skill(root / "tree-b" / "dup", "dup", "version two")
            errors = audit.check_duplicates(
                root, {"skill_roots": ["tree-a", "tree-b"]})
            self.assertTrue(any("dup" in e for e in errors), errors)

    def test_duplicate_name_with_identical_content_passes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_skill(root / "tree-a" / "same", "same", "identical")
            write_skill(root / "tree-b" / "same", "same", "identical")
            self.assertEqual(
                audit.check_duplicates(root, {"skill_roots": ["tree-a", "tree-b"]}), [])

    def test_missing_in_repo_source_fails(self):
        manifest = {"sources": {
            "ghost": {"kind": "squad-repo", "status": "canonical",
                      "path": "workspace/no-such-dir"}}}
        errors = audit.check_manifest(manifest, ROOT)
        self.assertTrue(any("ghost" in e for e in errors), errors)

    def test_installed_entry_with_unknown_revision_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            installed = Path(tmp) / "skills"
            write_skill(installed / "mystery", "mystery", "untracked copy")
            manifest = {"sources": {}, "skill_roots": []}
            errors = audit.check_installed(installed, manifest, ROOT)
            self.assertTrue(any("mystery" in e for e in errors), errors)

    def test_installed_symlink_into_canonical_source_passes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_skill(root / "workspace" / "coordination-skills" / "demo", "demo", "v1")
            installed = root / "home-skills"
            installed.mkdir()
            (installed / "demo").symlink_to(
                root / "workspace" / "coordination-skills" / "demo")
            manifest = {"sources": {
                "demo": {"kind": "squad-repo", "status": "canonical",
                         "path": "workspace/coordination-skills/demo"}},
                        "skill_roots": []}
            self.assertEqual(audit.check_installed(installed, manifest, root), [])


if __name__ == "__main__":
    unittest.main()
