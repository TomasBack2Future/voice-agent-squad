"""Offline contract check for generated Worker assignments and Dispatcher rules.

The scanner is negation-aware: a prompt that says "do not request ready-to-close
approval" passes, one that requires it fails. It reads text only.
"""
import re

NEGATION = re.compile(r"(?i)(?:\bno\b|\bnot\b|\bnever\b|\bwithout\b|\bdon't\b|\bnone\b|不要|无|不得|禁止|不再|不加|不设|取消|没有)[^。.]{0,70}$")

FORBIDDEN = [
    ('pre-close-approval', re.compile(r"(?i)ready[- ]to[- ]close|预关单|pre[- ]close\s+(?:approval|gate|review)|先不要关单|等待\s*Dispatcher[^。\n]{0,20}批准|(?:wait|await)(?:ing)?\s+(?:for\s+)?(?:the\s+)?(?:Dispatcher|dispatcher)[^.\n]{0,30}(?:approv|release|go-ahead|permission)|(?:Dispatcher|dispatcher)[^.\n]{0,30}approv[^.\n]{0,30}(?:before|prior to)[^.\n]{0,20}clos")),
    ('completed-session-reuse', re.compile(r"(?i)(?:reuse|re-?use|resume|wake|re-?activate|re-?assign|复用|重用|唤醒|续用)[^。.\n]{0,40}(?:completed|finished|retired|已完成|已结束|结束)[^。.\n]{0,20}(?:worker|session|native|会话)|(?:completed|finished|已完成)[^。.\n]{0,20}(?:worker|session|native|会话)[^。.\n]{0,40}(?:for|承接|接手)[^。.\n]{0,30}(?:another|next|different|其他|另一|下一)[^。.\n]{0,10}Issue")),
    ('handoff-to-completed', re.compile(r"(?i)hand\s*off[^。.\n]{0,60}(?:completed|finished|已完成)[^。.\n]{0,20}(?:worker|session|native|会话)|(?:交接|移交)[^。.\n]{0,30}(?:已完成|已结束)[^。.\n]{0,10}(?:worker|会话)")),
    ('squad-repair-to-business-worker', re.compile(r"(?i)(?:(?:dispatch|assign|派给|交给|分配|launch)[^。.\n]{0,60}(?:squad|coordination|协作)[^。.\n]{0,40}(?:repair|fix|修复)[^。.\n]{0,60}worker|(?:squad|coordination)[^。.\n]{0,40}(?:repair|fix|修复)[^。.\n]{0,60}(?:dispatch|assign|派给|交给|分配|launch)[^。.\n]{0,60}worker|(?:dispatch|assign|派给|交给|分配|launch)[^。.\n]{0,30}worker[^。.\n]{0,60}(?:squad|coordination|协作)[^。.\n]{0,40}(?:repair|fix|修复)|(?:dispatch|assign|派给|交给|分配|launch)[^。.\n]{0,30}worker[^。.\n]{0,20}(?:repair|fix|修复)[^。.\n]{0,40}(?:squad|coordination|协作))")),
]


# A one-Issue acceptance must not grow a new tool or workflow, or run as a
# cluster Job. Matching is clause-local (no crossing of , ; : or a sentence end)
# so "submit the PR, then run acceptance; the deploy workflow ..." is not a hit;
# work on an existing maintained tool and negated wording are exempt.
_TOOL = r"(?:tool|workflow|k8s\s+job|kubernetes\s+job|cluster\s+job|job|executor|runner|script|github\s+action)s?"
_CHECK = r"(?:acceptance|verification|verify|verifier|accept-[\w*-]+|inspect-[\w*-]+|probe-[\w*-]+)"
_MAKE = r"(?:add|create|write|build|commit|submit|open|introduce)"
TOOLING = [
    re.compile(r"(?i)\b" + _MAKE + r"\b[^.,;:\n]{0,40}?\b" + _CHECK + r"\b[^.,;:\n]{0,12}?\b" + _TOOL + r"\b"),
    re.compile(r"(?i)\b" + _MAKE + r"\b[^.,;:\n]{0,30}?\b" + _TOOL + r"\b[^.,;:\n]{0,20}?\b(?:for|to)\s+(?:run\s+|do\s+)?(?:the\s+)?(?:acceptance|verification|verify)\b"),
    re.compile(r"(?i)\b(?:test|verify|accept|check|run)\b[^.,;:\n]{0,40}?\b(?:in|as|through|via)\s+an?\s+(?:new\s+)?(?:k8s|kubernetes|cluster)\s+job\b"),
    re.compile(r"(?:新增|新建|提交|编写|增加|创建)[^。，；：\n]{0,20}(?:验收|验证)[^。，；：\n]{0,10}(?:工具|workflow|工作流|Job|执行器|脚本)"),
    re.compile(r"(?:新增|新建|提交|编写|增加|创建)[^。，；：\n]{0,12}(?:Job|工作流|workflow)[^。，；：\n]{0,8}(?:来|用于|做)?(?:验收|验证)"),
]
TOOLING_NEGATION = re.compile(r"(?i)(?:\bno\b|\bnot\b|\bnever\b|\bwithout\b|\bdon't\b|\bforbidden\b|\bprohibited\b|不要|不得|禁止|不再|无需|无须|不必|不用|不需要)[^.。;；]{0,60}$")
TOOLING_EXEMPT = re.compile(r"(?i)\bexisting\b|\bmaintained\b|现有|既有|已有")


def tooling_findings(flat):
    out = []
    for pattern in TOOLING:
        for m in pattern.finditer(flat):
            before = flat[max(0, m.start() - 70):m.start()]
            if TOOLING_NEGATION.search(before) or TOOLING_EXEMPT.search(m.group(0)) or IN_MATCH_NEGATION.search(m.group(0)):
                continue
            out.append(('acceptance-tool-pr', flat[max(0, m.start() - 20):m.end() + 40].strip()))
    return out[:1] if out else out


# Ordinary follow-on work gated behind another Dispatcher round-trip. Real
# authority boundaries (user, production, budget, merge release) are exempt.
GATED = [
    ('gated-follow-on', re.compile(r"(?i)\b(?:after|once|when)\s+(?:the\s+)?(?:assessment|evaluation|qualification|investigation|preflight|dry[- ]run|fixture check|analysis)\b[^.\n]{0,60}?\b(?:request|submit|wait(?:ing)?\s+for|await|ask\b(?:\s+[\w-]+){0,3}?\s+for|seek|hold\s+until)\b[^.\n]{0,30}?(?:approval|decision|go-ahead|permission|authori[sz]ation|confirm\w*|proceed)")),
    ('gated-follow-on', re.compile(r"(?i)\b(?:assess|evaluate|qualify|investigate|dry-run)\b[^.\n]{0,40}?\bthen\s+(?:hold|wait|pause)\b[^.\n]{0,40}?(?:Dispatcher|approval|decision|confirm\w*)")),
    ('gated-follow-on', re.compile(r"(?:评估|評估|核定|资格核定|预检|dry-run|调查)(?:完成|结束|後|后|之后)?[^。\n]{0,30}?(?:等待|申请|请求|提交|再要|再申请)[^。\n]{0,20}?(?:批准|决定|決定|授权|许可|decision-request)")),
    ('per-step-approval', re.compile(r"(?i)\b(?:request|submit|wait\s+for|await|ask\s+for|seek|obtain|needs?|requires?)\b[^.\n]{0,30}?(?:approval|decision|go-ahead|permission)[^.\n]{0,40}?\b(?:each|every)\s+(?:[\w-]+\s+){0,2}?(?:step|assertion|readback|check)s?\b")),
    ('per-step-approval', re.compile(r"(?i)\b(?:each|every)\s+(?:[\w-]+\s+){0,2}?(?:step|assertion|readback|check)s?\b[^.\n]{0,20}?\b(?:needs?|requires?|waits?\s+for|must\s+(?:get|obtain|request))\b[^.\n]{0,30}?(?:approval|decision|go-ahead|permission)")),
    ('per-step-approval', re.compile(r"(?:每一步|每步|逐步|逐条断言|每条断言)[^。\n]{0,20}(?:申请|等待|请求|提交)[^。\n]{0,20}(?:审批|批准|决定)")),
]
GATED_EXEMPT = re.compile(r"(?i)\buser\b|production|budget|CNY|merge[- ]release|用户|生产|预算|合入|超出")
CLAUSE_NEGATION = re.compile(r"(?i)(?:\bno\b|\bnot\b|\bnever\b|\bwithout\b|\bdon't\b|无需|无须|不必|不用|不要|不得|禁止|不再)[^,，;；:：]{0,40}$")
IN_MATCH_NEGATION = re.compile(r"(?i)\b(?:never|not|no|without)\b|无需|无须|不必|不用|不要|不得|禁止|不再")


def gated_findings(flat):
    out = []
    for rule, pattern in GATED:
        for m in pattern.finditer(flat):
            clause = re.split(r"[,，;；:：]", flat[max(0, m.start() - 60):m.start()])[-1]
            # Exempt only when the authority term belongs to the approval object:
            # inside the tail of the match ("user's authorization", "merge-release
            # decision") or after a condition ("only if ... exceeds the budget").
            tail = m.group(0)[-30:]
            rest = re.split(r"[.。;；\n]", flat[m.end():])[0][:80]
            conditional = re.search(r"(?i)\b(?:only\s+(?:if|when)|unless|if)\b|如需|如果|若|超出", rest)
            from_user = re.match(r"(?i)\s*(?:from|by)\s+(?:the\s+)?user|\s*(?:向|由)用户", rest)
            exempt = GATED_EXEMPT.search(tail) or from_user or (conditional and GATED_EXEMPT.search(rest[conditional.start():]))
            if exempt or IN_MATCH_NEGATION.search(m.group(0)) or CLAUSE_NEGATION.search(clause):
                continue
            out.append((rule, flat[max(0, m.start() - 20):m.end() + 40].strip()))
    return out

def findings(text):
    """Return [(rule, excerpt)] for forbidden wording that is not negated."""
    out = []
    flat = re.sub(r"\s+", " ", text)
    for rule, pattern in FORBIDDEN:
        for m in pattern.finditer(flat):
            before = re.split(r"[;；]", flat[max(0, m.start() - 70):m.start()])[-1]
            after = flat[m.end():m.end() + 24]
            if NEGATION.search(before) or (rule != 'pre-close-approval' and re.search(r"(?i)\b(?:never|not)\b|不得|不要|禁止|无需|无须|不必|不用", m.group(0))) or re.match(r"[^。.;；]{0,12}(?:已取消|不再|无需|取消|is cancelled|no longer)", after):
                continue
            out.append((rule, flat[max(0, m.start() - 20):m.end() + 40].strip()))
    return out + gated_findings(flat) + tooling_findings(flat)


REQUIRED_CLAUSES = {
    'worker-closes-itself': re.compile(r"(?i)(?:close the\s+Issue\s+yourself|owning Worker closes|Worker closes)[^\n]{0,200}|成功.{0,10}关闭.{0,20}无需.{0,10}(?:Dispatcher|审批)|no routine Dispatcher approval|不要提交例行"),
    'independent-identity': re.compile(r"(?i)(?:different Issue|不同 ?Issue)[^\n]{0,160}(?:own|new|独立|separate)"),
    'maintenance-owner': re.compile(r"(?i)maintenance agent[^\n]{0,200}"),
}


def missing(text):
    return [name for name, pattern in REQUIRED_CLAUSES.items() if not pattern.search(text)]


SQUAD_TARGET = re.compile(r"voice-agent-squad|github:[^\s]*squad#\d+|Squad (?:tool|工具|Issue)", re.I)
DESIGNATION = re.compile(r"(?i)(?:user[- ]designat\w*|designated by the user|用户[^。\n]{0,12}指定)[^。\n]{0,40}(?:maintenance[- ]agent|维护 ?agent|agent)|(?:maintenance[- ]agent|维护 ?agent)[^。\n]{0,30}(?:designated|指定)")


# An implementation assignment names its closure standard: the Issue's
# Verification block and its revision. Other roles (Investigator, environment
# recovery, Deployer) carry their own completion contract and are not checked.
IMPLEMENTATION_ROLE = re.compile(r"studio-issue-worker|agent-loop-worker|voice-agent-studio#\d+|convoai-studio-importer#\d+")
VERIFICATION = re.compile(r"(?i)verification(?:\s+block)?[^\n.。]{0,160}?(?:\brevision\s+v?\d+|\brev\.?\s*v?\d+|\(v\d+\)|\bv\d+\b)|验证(?:标准|块)[^\n。]{0,60}?v\d+")
VERIFICATION_ABSENT = re.compile(r"(?i)(?:\bno\b|\bnot\b|\bnone\b|\bwithout\b|\bmissing\b|没有|缺少|尚无|无验证)")


def cites_verification(flat):
    for m in VERIFICATION.finditer(flat):
        around = re.split(r"[.。;；]", flat[max(0, m.start() - 30):m.start()])[-1] + m.group(0)
        if not VERIFICATION_ABSENT.search(around):
            return True
    return False


def assignment_findings(text):
    """Findings for one generated Worker assignment, including repair ownership."""
    out = findings(text)
    if IMPLEMENTATION_ROLE.search(text) and not cites_verification(re.sub(r"\s+", " ", text)):
        out.append(('missing-verification-block', "implementation assignment does not cite the Issue's Verification block and revision"))
    designated = any(not NEGATION.search(re.sub(r"\s+", " ", text)[max(0, m.start() - 40):m.start()]) for m in DESIGNATION.finditer(text))
    if SQUAD_TARGET.search(text) and not designated:
        out.append(('squad-repair-without-designation', 'Squad-tool assignment names no user-designated maintenance agent'))
    return out


if __name__ == '__main__':
    import sys
    bad = 0
    for name in sys.argv[1:]:
        for rule, excerpt in assignment_findings(open(name).read()):
            print(f'{name}: {rule}: {excerpt}')
            bad = 1
    sys.exit(bad)
