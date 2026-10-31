import os
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).parents[1]))
import assignment_contract as contract

WORKSPACE = Path(__file__).parents[2]
NORMATIVE = [
    WORKSPACE / 'coordination-skills/squad-dispatcher/SKILL.md',
    WORKSPACE / 'coordination-skills/squad-dispatcher/references/worker-task-prompt.md',
    WORKSPACE / 'coordination-skills/squad-dispatcher/references/session-continuity.md',
    WORKSPACE / 'coordination-skills/studio-issue-worker/SKILL.md',
    WORKSPACE / 'agent-loop/roles/worker/SKILL.md',
    WORKSPACE.parent / 'docs/reference/delivery-lifecycle.md',
]


class ScannerTests(unittest.TestCase):
    def test_flags_each_regression_and_accepts_negated_forms(self):
        bad = {
            'pre-close-approval': 'Submit a ready-to-close decision-request and wait.',
            'completed-session-reuse': 'Reuse the finished Worker session for the next Issue.',
            'handoff-to-completed': 'Hand off the follow-up repair to the completed Worker session.',
            'squad-repair-to-business-worker': 'Dispatch the Squad tool repair to a Worker.',
        }
        more = [('pre-close-approval', 'Wait for Dispatcher approval before closing the Issue.'),
                ('pre-close-approval', 'no routine approval; however submit ready-to-close and wait'),
                ('completed-session-reuse', 'Resume the completed Worker session for Issue #5.'),
                ('completed-session-reuse', '唤醒已完成的 Worker 会话处理下一个 Issue'),
                ('squad-repair-to-business-worker', 'Dispatch the Squad repair to a business Worker.'),
                ('squad-repair-to-business-worker', '派给 Worker 修复 Squad 工具问题')]
        for rule, text in more:
            self.assertIn(rule, [r for r, _ in contract.findings(text)], text)
        self.assertEqual(contract.findings('预关单审批已取消，不再需要'), [])
        for rule, text in bad.items():
            self.assertIn(rule, [r for r, _ in contract.findings(text)], text)
        for ok in ['Do not request ready-to-close approval.', '不要提交例行 ready-to-close 请求',
                   'never reuse a completed Worker session for another Issue',
                   'Do not dispatch a Squad tool repair to a business Worker.']:
            self.assertEqual(contract.findings(ok), [], ok)


class AssignmentScenarioTests(unittest.TestCase):
    CLOSEOUT = '自主结项，无例行预关单审批；不同 Issue 使用独立任务身份。'

    def test_studio_assignment_passes_and_squad_repair_needs_a_designation(self):
        studio = 'Issue github:o/voice-agent-studio#1801。' + self.CLOSEOUT
        self.assertEqual(contract.assignment_findings(studio), [])
        squad = 'Issue github:TomasBack2Future/voice-agent-squad#149，用户 2026-10-29 授权。' + self.CLOSEOUT
        self.assertIn('squad-repair-without-designation', [r for r, _ in contract.assignment_findings(squad)])
        self.assertIn('squad-repair-without-designation', [r for r, _ in contract.assignment_findings(squad + ' no maintenance agent designated.')])
        designated = squad + ' 用户本轮指定维护 agent 修复。'
        self.assertEqual(contract.assignment_findings(designated), [])

    def test_regressed_template_is_caught(self):
        old = 'Studio #1 结项：先不要关单，提交 ready-to-close decision-request，等待 Dispatcher 批准。'
        self.assertIn('pre-close-approval', [r for r, _ in contract.assignment_findings(old)])


class NormativeSourceTests(unittest.TestCase):
    def test_sources_contain_no_unnegated_regression_wording(self):
        for path in NORMATIVE:
            self.assertEqual(contract.findings(path.read_text()), [], str(path))

    def test_required_rules_are_present_where_assignments_come_from(self):
        text = '\n'.join(p.read_text() for p in NORMATIVE)
        self.assertEqual(contract.missing(text), [])
        dispatcher = (WORKSPACE / 'coordination-skills/squad-dispatcher/SKILL.md').read_text()
        self.assertRegex(dispatcher, r'Never derive that\s+assignment from business, staging or production authorization')
        self.assertNotRegex(dispatcher, r'#14[345]')

    def test_worker_skill_closes_itself_and_refuses_other_issues(self):
        worker = (WORKSPACE / 'agent-loop/roles/worker/SKILL.md').read_text()
        self.assertIn('close the\n  Issue yourself', worker)
        self.assertIn('different Issue', worker)


class GeneratedPromptTests(unittest.TestCase):
    @unittest.skipUnless(os.environ.get('ASSIGNMENT_PROMPT_FILES'), 'set to generated prompt paths (os.pathsep separated)')
    def test_real_generated_assignments(self):
        for name in os.environ['ASSIGNMENT_PROMPT_FILES'].split(os.pathsep):
            self.assertEqual(contract.assignment_findings(Path(name).read_text()), [], name)


if __name__ == '__main__':
    unittest.main()
