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


def findings(text):
    """Return [(rule, excerpt)] for forbidden wording that is not negated."""
    out = []
    flat = re.sub(r"\s+", " ", text)
    for rule, pattern in FORBIDDEN:
        for m in pattern.finditer(flat):
            before = re.split(r"[;；]", flat[max(0, m.start() - 70):m.start()])[-1]
            after = flat[m.end():m.end() + 24]
            if NEGATION.search(before) or (rule != 'pre-close-approval' and re.search(r"(?i)\b(?:never|not)\b|不得|不要|禁止", m.group(0))) or re.match(r"[^。.;；]{0,12}(?:已取消|不再|无需|取消|is cancelled|no longer)", after):
                continue
            out.append((rule, flat[max(0, m.start() - 20):m.end() + 40].strip()))
    return out


REQUIRED_CLAUSES = {
    'worker-closes-itself': re.compile(r"(?i)(?:close the\s+Issue\s+yourself|owning Worker closes|Worker closes)[^\n]{0,200}|成功.{0,10}关闭.{0,20}无需.{0,10}(?:Dispatcher|审批)|no routine Dispatcher approval|不要提交例行"),
    'independent-identity': re.compile(r"(?i)(?:different Issue|不同 ?Issue)[^\n]{0,160}(?:own|new|独立|separate)"),
    'maintenance-owner': re.compile(r"(?i)maintenance agent[^\n]{0,200}"),
}


def missing(text):
    return [name for name, pattern in REQUIRED_CLAUSES.items() if not pattern.search(text)]


SQUAD_TARGET = re.compile(r"voice-agent-squad|github:[^\s]*squad#\d+|Squad (?:tool|工具|Issue)", re.I)
DESIGNATION = re.compile(r"(?i)(?:user[- ]designat\w*|designated by the user|用户[^。\n]{0,12}指定)[^。\n]{0,40}(?:maintenance[- ]agent|维护 ?agent|agent)|(?:maintenance[- ]agent|维护 ?agent)[^。\n]{0,30}(?:designated|指定)")


def assignment_findings(text):
    """Findings for one generated Worker assignment, including repair ownership."""
    out = findings(text)
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
