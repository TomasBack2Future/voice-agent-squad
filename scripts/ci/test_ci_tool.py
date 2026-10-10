import os
import re
import subprocess
import sys
import unittest

sys.dont_write_bytecode = True

import ci_tool  # noqa: E402

WORKFLOW = os.path.join(ci_tool.ROOT, ".github", "workflows", "ci.yml")
ALL_BUT_DOC = sorted(f for f in ci_tool.FLAGS if f != "doc")


def on(flags):
    return sorted(k for k, v in flags.items() if v)


class ClassifyTest(unittest.TestCase):
    def test_docs_only_runs_only_doc_contracts(self):
        self.assertEqual(on(ci_tool.classify(["docs/reference/commands.md", "README.md"])), ["doc"])

    def test_spec_change_keeps_spec_parse_test(self):
        self.assertEqual(on(ci_tool.classify([".squad/specs/x.md"])), ["doc"])

    def test_skill_only_change_skips_go(self):
        flags = ci_tool.classify(["workspace/coordination-skills/studio-issue-worker/SKILL.md"])
        self.assertEqual(on(flags), ["doc", "node", "pyloop"])

    def test_go_test_only_change_skips_cross_build(self):
        self.assertEqual(on(ci_tool.classify(["internal/claims/claim_test.go"])), ["go", "remote"])

    def test_go_source_selects_cross_build_but_not_smoke(self):
        self.assertEqual(on(ci_tool.classify(["internal/store/store.go"])), ["cross", "go", "remote"])

    def test_sqlite_migration_asset_selects_go(self):
        self.assertTrue(ci_tool.classify(["internal/store/migrations/0042_x.sql"])["go"])

    def test_version_entrypoint_selects_release_smoke(self):
        self.assertTrue(ci_tool.classify(["cmd/squad/main.go"])["smoke"])

    def test_release_config_selects_distribution_checks_only(self):
        self.assertEqual(on(ci_tool.classify([".goreleaser.yaml"])), ["cross", "smoke"])

    def test_shared_dependency_selects_everything(self):
        self.assertEqual(on(ci_tool.classify(["go.mod"])), ALL_BUT_DOC)

    def test_unknown_path_selects_everything(self):
        self.assertEqual(on(ci_tool.classify(["docs/a.md", "totally/new/thing.bin"])), ALL_BUT_DOC)

    def test_workflow_or_ci_tool_change_selects_everything(self):
        for path in (".github/workflows/ci.yml", "scripts/ci/ci_tool.py"):
            self.assertEqual(on(ci_tool.classify([path])), ALL_BUT_DOC)

    def test_full_and_missing_diff_select_everything(self):
        self.assertEqual(on(ci_tool.classify(["docs/a.md"], full=True)), ALL_BUT_DOC)
        self.assertEqual(on(ci_tool.classify([])), ALL_BUT_DOC)

    def test_go_change_subsumes_doc_contracts(self):
        flags = ci_tool.classify(["internal/x/x.go", "README.md"])
        self.assertTrue(flags["go"])
        self.assertFalse(flags["doc"])


class GateTest(unittest.TestCase):
    def flags(self, *selected):
        return {f: "true" if f in selected else "false" for f in ci_tool.FLAGS}

    def needs(self, flags, results=None):
        needs = {"scope": {"result": "success"}}
        for job, flag in ci_tool.JOB_FLAGS.items():
            needs[job] = {"result": "success" if flags[flag] == "true" else "skipped"}
        needs.update({k: {"result": v} for k, v in (results or {}).items()})
        return needs

    def test_selected_success_and_unselected_skipped_pass(self):
        flags = self.flags("go")
        self.assertEqual(ci_tool.gate_errors(flags, self.needs(flags)), [])

    def test_selected_but_skipped_fails(self):
        flags = self.flags("go")
        errors = ci_tool.gate_errors(flags, self.needs(flags, {"race": "skipped"}))
        self.assertEqual(len(errors), 1)
        self.assertIn("race", errors[0])

    def test_failure_and_cancellation_fail(self):
        flags = self.flags("go", "node")
        for result in ("failure", "cancelled"):
            self.assertTrue(ci_tool.gate_errors(flags, self.needs(flags, {"node": result})))

    def test_unselected_job_that_failed_fails(self):
        flags = self.flags("go")
        self.assertTrue(ci_tool.gate_errors(flags, self.needs(flags, {"pyloop": "failure"})))

    def test_failed_scope_fails_even_if_everything_skipped(self):
        flags = self.flags()
        self.assertTrue(ci_tool.gate_errors(flags, self.needs(flags, {"scope": "failure"})))

    def test_job_missing_from_needs_fails(self):
        flags = self.flags("go")
        needs = self.needs(flags)
        del needs["lint"]
        self.assertTrue(ci_tool.gate_errors(flags, needs))


class ShardTest(unittest.TestCase):
    PKGS = ["m/cmd/squad", "m/internal/server", "m/internal/a", "m/internal/b"]
    NAMES = ["TestA", "TestB", "TestC", "ExampleD", "TestE"]

    def test_every_package_and_test_runs_exactly_once(self):
        self.assertEqual(ci_tool.shard_errors(self.PKGS, self.NAMES), [])

    def test_new_package_lands_in_rest(self):
        pkgs, run = ci_tool.shard_plan(self.PKGS + ["m/internal/new"], self.NAMES, "rest")
        self.assertIn("m/internal/new", pkgs)
        self.assertIsNone(run)

    def test_cli_halves_are_disjoint_and_complete(self):
        _, one = ci_tool.shard_plan(self.PKGS, self.NAMES, "cli-1")
        _, two = ci_tool.shard_plan(self.PKGS, self.NAMES, "cli-2")
        a, b = set(one[2:-2].split("|")), set(two[2:-2].split("|"))
        self.assertFalse(a & b)
        self.assertEqual(a | b, set(self.NAMES))


class InventoryTest(unittest.TestCase):
    def test_unmapped_python_and_node_tests_fail(self):
        errors = ci_tool.inventory_errors(set(), [], ["newdir/test_x.py"], ["elsewhere/a.test.mjs"])
        self.assertEqual(len(errors), 2)

    def test_mapped_tests_pass(self):
        errors = ci_tool.inventory_errors(
            {"internal/a"}, ["github.com/zsiec/squad/internal/a"],
            ["deploy/test_update.py", "workspace/agent-loop/tests/test_x.py"],
            ["workspace/coordination-skills/studio-issue-worker/scripts/a.test.mjs"])
        self.assertEqual(errors, [])

    def test_test_directory_without_package_fails(self):
        self.assertTrue(ci_tool.inventory_errors({"internal/ghost"}, ["github.com/zsiec/squad/internal/a"], [], []))


class RepositoryHygieneTest(unittest.TestCase):
    def test_no_python_bytecode_is_tracked(self):
        tracked = subprocess.run(["git", "ls-files"], cwd=ci_tool.ROOT, check=True,
                                 capture_output=True, text=True).stdout.splitlines()
        bytecode = [f for f in tracked if re.search(r"(^|/)__pycache__/|\.py[co]$", f)]
        self.assertEqual(bytecode, [])

    def test_gitignore_excludes_python_bytecode(self):
        with open(os.path.join(ci_tool.ROOT, ".gitignore"), encoding="utf-8") as fh:
            lines = {l.strip() for l in fh}
        self.assertLessEqual({"__pycache__/", "*.pyc"}, lines)


class RemoteAndUpdaterCoverageTest(unittest.TestCase):
    def test_every_input_of_remote_and_updater_tests_selects_remote(self):
        for path in ("deploy/update.py", "deploy/test_update.py", "deploy/squad-service.service",
                     "scripts/test_remote_service.py", "cmd/squad/service.go", "internal/remote/remote.go",
                     "internal/remote/remote_test.go", "go.mod"):
            self.assertTrue(ci_tool.classify([path])["remote"], path)

    def test_unrelated_documentation_and_skill_changes_skip_remote(self):
        self.assertFalse(ci_tool.classify(["docs/README.md"])["remote"])
        self.assertFalse(ci_tool.classify(["workspace/agent-loop/README.md"])["remote"])

    def test_selected_remote_job_that_is_skipped_fails_the_gate(self):
        flags = {f: "true" if f == "remote" else "false" for f in ci_tool.FLAGS}
        needs = {"scope": {"result": "success"}}
        for job, flag in ci_tool.JOB_FLAGS.items():
            needs[job] = {"result": "success" if flags[flag] == "true" else "skipped"}
        needs["remote"] = {"result": "skipped"}
        self.assertTrue(ci_tool.gate_errors(flags, needs))

    def test_remote_job_runs_receiver_acceptance_and_updater_tests(self):
        with open(WORKFLOW, encoding="utf-8") as fh:
            job = fh.read().split("\n  remote:\n", 1)[1].split("\n  # ", 1)[0]
        self.assertIn("python3 scripts/test_remote_service.py", job)
        self.assertIn("python3 -m unittest discover -s deploy", job)


class WorkflowContractTest(unittest.TestCase):
    def setUp(self):
        with open(WORKFLOW, encoding="utf-8") as fh:
            self.text = fh.read()
        self.gate = self.text.split("\n  gate:\n", 1)[1]

    def test_every_job_is_gated(self):
        body = self.text.split("\njobs:\n", 1)[1]
        jobs = set(re.findall(r"^  ([a-z][\w-]*):\n", body, re.M)) - {"gate"}
        self.assertEqual(jobs, set(ci_tool.JOB_FLAGS) | {"scope"})
        needs = re.search(r"needs: \[([^\]]*)\]", self.gate).group(1)
        self.assertEqual({n.strip() for n in needs.split(",")}, jobs)

    def test_race_matrix_matches_shards(self):
        matrix = re.search(r"shard: \[([^\]]*)\]", self.text).group(1)
        self.assertEqual(tuple(s.strip() for s in matrix.split(",")), ci_tool.RACE_SHARDS)

    def test_cancellation_is_limited_to_pull_requests(self):
        self.assertIn("cancel-in-progress: ${{ github.event_name == 'pull_request' }}", self.text)

    def test_lint_uses_the_pinned_verified_install_and_bounded_verify(self):
        lint = self.text.split("\n  lint:\n", 1)[1].split("\n  # ", 1)[0]
        self.assertNotIn("golangci-lint-action", self.text)
        self.assertIn("scripts/ci/golangci.py install", lint)
        self.assertIn("scripts/ci/golangci.py verify", lint)
        self.assertIn("hashFiles('scripts/ci/golangci-lint.pin')", lint)
        self.assertLess(lint.index("golangci.py verify"), lint.index("golangci-lint run"))

    def test_required_check_context_and_ruleset_draft(self):
        import json
        path = os.path.join(ci_tool.ROOT, "docs", "ci-main-ruleset-draft.json")
        with open(path, encoding="utf-8") as fh:
            draft = json.load(fh)
        checks = draft["rules"][0]["parameters"]["required_status_checks"]
        self.assertEqual(checks, [{"context": "gate", "integration_id": 15368}])
        self.assertEqual(draft["enforcement"], "active")
        self.assertIn("gate:", self.text)

    def test_gate_runs_even_when_needs_fail(self):
        self.assertIn("if: always()", self.gate)


if __name__ == "__main__":
    unittest.main()
