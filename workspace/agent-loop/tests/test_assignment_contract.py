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
        gated = [('gated-follow-on', 'After the assessment, request approval before executing the retry.'),
                 ('gated-follow-on', 'Once the qualification passes, wait for a Dispatcher decision to run the Simulation.'),
                 ('gated-follow-on', 'After the dry-run, submit a decision-request and run the migration only after proceed.'),
                 ('gated-follow-on', 'Assess the fixture; then hold until the Dispatcher confirms the run.'),
                 ('gated-follow-on', 'No customer data in fixtures, and after the dry-run wait for a Dispatcher decision.'),
                 ('gated-follow-on', '资格核定完成后等待 Dispatcher 决定再执行'),
                 ('gated-follow-on', '評估後提交 decision-request 再执行'),
                 ('gated-follow-on', 'After the assessment, request Dispatcher approval before executing; never touch production.'),
                 ('gated-follow-on', 'After the evaluation of the user flow, wait for a Dispatcher decision before running it.'),
                 ('gated-follow-on', 'After the assessment, request a Dispatcher decision before running the CNY 2 Simulation.'),
                 ('gated-follow-on', 'After the assessment, ask the Dispatcher for approval before executing.'),
                 ('gated-follow-on', 'After the assessment, request approval of the user story from the Dispatcher before executing.'),
                 ('per-step-approval', 'Ask for a decision before each assertion.'),
                 ('per-step-approval', 'Request approval before running every assertion.'),
                 ('per-step-approval', 'Each acceptance assertion needs a Dispatcher decision first.'),
                 ('per-step-approval', '每一步都要申请 Dispatcher 审批')]
        for rule, text in gated:
            self.assertIn(rule, [r for r, _ in contract.findings(text)], text)
        for ok in ['Assess X; if condition C holds, execute Y within this scope.',
                   'Do not request approval for each assertion.',
                   'After the assessment, never wait for another approval before executing.',
                   '评估完成后无需等待批准，满足条件直接执行',
                   'Once the preflight passes, request a merge-release decision for the exact head.',
                   "After the assessment, wait for the user's authorization before any production mutation.",
                   '调查后如需新增生产权限，请求用户授权。',
                   'After the dry-run, request a Dispatcher decision only if the result exceeds the CNY 10 budget.',
                   'Request a merge-release decision for each phase of the window.',
                   'When the investigation shows production access is needed, request authorization from the user.']:
            self.assertEqual(contract.findings(ok), [], ok)
        for rule, text in bad.items():
            self.assertIn(rule, [r for r, _ in contract.findings(text)], text)
        for ok in ['Do not request ready-to-close approval.', '不要提交例行 ready-to-close 请求',
                   'never reuse a completed Worker session for another Issue',
                   'Do not dispatch a Squad tool repair to a business Worker.']:
            self.assertEqual(contract.findings(ok), [], ok)


class AssignmentScenarioTests(unittest.TestCase):
    CLOSEOUT = '自主结项，无例行预关单审批；不同 Issue 使用独立任务身份。验证标准 v1。'

    def test_studio_assignment_passes_and_squad_repair_needs_a_designation(self):
        studio = 'Issue github:o/voice-agent-studio#1801。' + self.CLOSEOUT
        self.assertEqual(contract.assignment_findings(studio), [])
        squad = 'Issue github:TomasBack2Future/voice-agent-squad#149，用户 2026-10-29 授权。' + self.CLOSEOUT
        self.assertIn('squad-repair-without-designation', [r for r, _ in contract.assignment_findings(squad)])
        self.assertIn('squad-repair-without-designation', [r for r, _ in contract.assignment_findings(squad + ' no maintenance agent designated.')])
        designated = squad + ' 用户本轮指定维护 agent 修复。'
        self.assertEqual(contract.assignment_findings(designated), [])

    def test_implementation_assignment_must_cite_the_verification_block(self):
        base = 'Issue github:o/voice-agent-studio#1801。自主结项，无例行预关单审批；不同 Issue 使用独立任务身份。'
        rule = lambda text: [r for r, _ in contract.assignment_findings(text)]
        self.assertIn('missing-verification-block', rule(base))
        for cited in [" Done means every row of the Issue's Verification block Design / admission revision v2 passes at its stated layer.",
                      ' Closure standard: Verification (v2) in the Issue.',
                      ' Verification block rev 3 applies.',
                      ' 验证标准 v1。']:
            self.assertEqual(rule(base + cited), [], cited)
        for absent in [' There is no Verification block yet; revision 3 of the design applies.',
                       ' Verification block: none, see design revision 2.',
                       ' Verification is by the usual checks.']:
            self.assertIn('missing-verification-block', rule(base + absent), absent)
        # Other roles carry their own completion contract.
        self.assertEqual(rule('Investigate why the staging runner lost its pull secret and report evidence. 无例行预关单审批。'), [])

    def test_new_acceptance_tooling_is_caught(self):
        rule = lambda text: [r for r, _ in contract.findings(text)]
        for text in ['Add an acceptance workflow that runs the check as a k8s Job.',
                     'Create a verification executor for this Issue and submit it as a PR.',
                     'Write a verify workflow for the staging runner.',
                     'Add an acceptance tool via PR.',
                     'Add an accept-session workflow to the repository for this check.',
                     'Create a new workflow for acceptance.',
                     'Test it in a Kubernetes Job.',
                     '新增一个验收工具并提交 PR',
                     '新增一个 Job 来验收']:
            self.assertIn('acceptance-tool-pr', rule(text), text)
        for ok in ['Do not add an acceptance workflow or cluster Job for this Issue.',
                   'Never create a verification executor to prove one Issue.',
                   'It is forbidden to add an acceptance workflow.',
                   'Add a regression test for the boundary value.',
                   'Submit the PR, then run acceptance; the staging-deploy workflow deploys it.',
                   'Create the fixture, run verification, and watch the deploy workflow finish.',
                   'This Issue is to add retry to the existing acceptance executor.',
                   'Do not test it in a Kubernetes Job.',
                   '不要新增验收工具',
                   '本 Issue 不需要新增验收工具']:
            self.assertEqual(rule(ok), [], ok)

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

    def test_continuity_handoff_and_state_rules_are_stated(self):
        cont = (WORKSPACE / 'coordination-skills/squad-dispatcher/references/session-continuity.md').read_text()
        flat = ' '.join(cont.split())
        for needle in ('## Continuous ownership', '## Shared causes: one repair owner', '## Handoff means ownership transfer',
                       '## State vocabulary', '## Takeover summary', '`receiver-confirmed`', 'twice for the same Worker generation',
                       'rejects a native already bound'):
            self.assertIn(needle, flat)
        worker = (WORKSPACE / 'agent-loop/roles/worker/SKILL.md').read_text()
        self.assertIn('## Phase reports are not handoffs', worker)
        callback = (WORKSPACE / 'coordination-skills/studio-issue-worker/references/dispatcher-callback.md').read_text()
        self.assertRegex(callback.replace('\n', ' '), r'Never emit\s+`handoff-complete` for a phase report')


class GeneratedPromptTests(unittest.TestCase):
    @unittest.skipUnless(os.environ.get('ASSIGNMENT_PROMPT_FILES'), 'set to generated prompt paths (os.pathsep separated)')
    def test_real_generated_assignments(self):
        for name in os.environ['ASSIGNMENT_PROMPT_FILES'].split(os.pathsep):
            self.assertEqual(contract.assignment_findings(Path(name).read_text()), [], name)


if __name__ == '__main__':
    unittest.main()
