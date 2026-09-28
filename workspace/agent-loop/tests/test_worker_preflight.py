import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).parents[1]
sys.path.insert(0, str(ROOT))
import worker_preflight as preflight


class WorkerPreflightTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.work = self.root / "work"
        self.work.mkdir()
        self.git("init", "-b", "work")
        self.git("config", "user.name", "Test")
        self.git("config", "user.email", "test@example.invalid")
        (self.work / "AGENTS.md").write_text("Isolated test repository.\n")
        self.git("add", "AGENTS.md")
        self.git("commit", "-m", "initial")
        self.git("remote", "add", "origin", "git@github.com:TomasBack2Future/voice-agent-studio.git")
        self.assignment = json.loads((ROOT / "examples/assignment.studio-worker.json").read_text())
        self.assignment.update(worktree=str(self.work), branch="work", base_sha=self.git("rev-parse", "HEAD"))
        self.profile = ROOT / "projects/studio/profile.json"
        self.assignment["project_profile"]["path"] = str(self.profile)
        self.path = self.root / "assignment.json"
        self.skill = self.root / ".claude/skills/agent-loop-worker/SKILL.md"
        self.skill.parent.mkdir(parents=True)
        self.skill.symlink_to(ROOT / "roles/worker/SKILL.md")

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.work), *args], stderr=subprocess.DEVNULL, text=True).strip()

    def check(self):
        self.path.write_text(json.dumps(self.assignment))
        with patch("worker_preflight.shutil.which", return_value="/qualified/tool"):
            return preflight.check(self.path, self.profile, "claude", [self.skill], ["squad"])

    def test_isolated_pilot_reads_identity_without_mutation(self):
        before = self.git("status", "--porcelain")
        receipt = self.check()
        self.assertEqual(receipt["status"], "ready")
        self.assertEqual(receipt["ownership"], "not_checked")
        self.assertEqual(receipt["runtime_approval"], "not_checked")
        self.assertEqual(before, self.git("status", "--porcelain"))
        self.assertEqual(receipt["base_sha"], self.assignment["base_sha"])

    def test_missing_client_skill_fails_before_launch(self):
        self.skill.unlink()
        with self.assertRaises(preflight.ValidationError):
            self.check()

    def select_importer(self):
        repository = "TomasBack2Future/convoai-studio-importer"
        self.git("remote", "set-url", "origin", f"git@github.com:{repository}.git")
        self.profile = ROOT / "projects/importer/profile.json"
        self.assignment.update(
            assignment_id="importer/1/1", issue=f"{repository}#1",
            repository=repository, item="IMPORTER-001",
            reservation={"key": "DISPATCH-IMPORTER-1", "generation": 1},
            project_profile={"id": "importer", "version": 1, "path": str(self.profile)},
        )

    def test_importer_cold_start_uses_its_repository_and_profile(self):
        self.select_importer()
        before = self.git("status", "--porcelain")
        receipt = self.check()
        self.assertEqual(receipt["status"], "ready")
        self.assertEqual(receipt["ownership"], "not_checked")
        self.assertEqual(before, self.git("status", "--porcelain"))
        self.assertLessEqual(len(json.dumps(self.assignment, separators=(",", ":")).encode()), 2048)

    def test_importer_cannot_borrow_studio_profile(self):
        self.select_importer()
        self.profile = ROOT / "projects/studio/profile.json"
        self.assignment["project_profile"] = {
            "id": "studio", "version": 1, "path": str(self.profile),
        }
        with self.assertRaisesRegex(preflight.ValidationError, "assignment/profile identity mismatch"):
            self.check()

    def test_importer_profile_rejects_studio_checkout(self):
        self.select_importer()
        self.git("remote", "set-url", "origin", "git@github.com:TomasBack2Future/voice-agent-studio.git")
        with self.assertRaisesRegex(preflight.ValidationError, "assignment repository mismatch"):
            self.check()

    def select_interceptor(self):
        self.git("remote", "set-url", "origin", "ssh://git@git.agoralab.co/ipt/interceptor.git")
        self.profile = ROOT / "projects/interceptor/profile.json"
        self.assignment.update(
            assignment_id="interceptor/1154/1",
            issue="TomasBack2Future/voice-agent-studio#1154",
            repository="git.agoralab.co/ipt/interceptor",
            item="INTERCEPTOR-1154",
            reservation={"key": "DISPATCH-STUDIO-1154", "generation": 1},
            project_profile={"id": "interceptor", "version": 1, "path": str(self.profile)},
            authorization=dict(source_mutation=True, branch_push=True, pull_request=False,
                               merge=False, staging=False, production=False, issue_close=False),
        )

    def test_interceptor_branch_handoff_with_github_tracking_issue(self):
        self.select_interceptor()
        self.assertEqual(self.check()["status"], "ready")
        self.git("remote", "set-url", "origin", "git@github.com:ipt/interceptor.git")
        with self.assertRaisesRegex(preflight.ValidationError, "assignment repository mismatch"):
            self.check()

    def test_branch_only_requires_explicit_push_authorization(self):
        self.select_interceptor()
        del self.assignment["authorization"]["branch_push"]
        with self.assertRaisesRegex(preflight.ValidationError, "branch_push"):
            self.check()

    def test_repository_remote_identity_formats_and_rejections(self):
        accepted = {
            "git@github.com:TomasBack2Future/voice-agent-squad.git": "TomasBack2Future/voice-agent-squad",
            "https://github.com/TomasBack2Future/convoai-studio-importer.git": "TomasBack2Future/convoai-studio-importer",
            "ssh://git@git.agoralab.co/ipt/interceptor.git": "git.agoralab.co/ipt/interceptor",
            "git@git.agoralab.co:ipt/interceptor.git": "git.agoralab.co/ipt/interceptor",
        }
        for remote, identity in accepted.items():
            self.assertEqual(preflight.repository_name(remote), identity)
        for remote in ("file:///ipt/interceptor.git", "https://user:secret@git.agoralab.co/ipt/interceptor.git",
                       "ssh://git@git.agoralab.co/ipt/interceptor/extra.git", "git@git.agoralab.co:ipt/../interceptor.git"):
            with self.assertRaises(preflight.ValidationError):
                preflight.repository_name(remote)

    def test_wrong_runtime_discovery_path_fails(self):
        self.path.write_text(json.dumps(self.assignment))
        with self.assertRaises(preflight.ValidationError):
            preflight.check(self.path, self.profile, "codex", [self.skill], [])

    def test_changed_base_branch_repository_and_dirty_tree_fail(self):
        for field, value in [("base_sha", "0" * 40), ("branch", "wrong")]:
            original = self.assignment[field]
            self.assignment[field] = value
            with self.assertRaises(preflight.ValidationError):
                self.check()
            self.assignment[field] = original
        self.git("remote", "set-url", "origin", "git@github.com:other/repo.git")
        with self.assertRaises(preflight.ValidationError):
            self.check()
        self.git("remote", "set-url", "origin", "git@github.com:TomasBack2Future/voice-agent-studio.git")
        (self.work / "AGENTS.md").write_text("unrelated work\n")
        with self.assertRaises(preflight.ValidationError):
            self.check()

    def test_missing_executable_fails(self):
        self.path.write_text(json.dumps(self.assignment))
        with patch("worker_preflight.shutil.which", return_value=None):
            with self.assertRaises(preflight.ValidationError):
                preflight.check(self.path, self.profile, "claude", [self.skill], [])

    def test_cli_does_not_echo_invalid_assignment_data(self):
        self.path.write_text('{"private":"DO_NOT_ECHO"}')
        result = subprocess.run([sys.executable, str(ROOT / "worker_preflight.py"),
                                 "--assignment", str(self.path), "--profile", str(self.profile),
                                 "--runtime", "claude", "--skill", str(self.skill)],
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 1)
        self.assertNotIn("DO_NOT_ECHO", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
