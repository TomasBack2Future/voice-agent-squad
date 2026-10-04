"""Bounded, passive native progress display; never controls a Worker or its tools."""
import re
import sys


class LiveView:
    def __init__(self, native, stream=None, enabled=True):
        self.native = native
        self.stream = sys.stderr if stream is None else stream
        self.enabled = enabled
        self.items = {}

    def status(self, text):
        if not self.enabled:
            return
        text = re.sub(r'\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))', '', str(text))
        text = ''.join(c for c in text if c in '\n\t' or c.isprintable())
        try:
            print(text, file=self.stream, flush=True)
        except (OSError, ValueError):
            # A closed display is not permission to abandon or kill owned work.
            self.enabled = False

    def observe(self, message):
        p = message.get('params', {})
        if p.get('sessionId') != self.native:
            return
        method = message.get('method')
        if method in ('item/started', 'item/completed'):
            item = p.get('item', {})
            kind, key = item.get('kind'), item.get('itemId')
            if kind not in ('agentMessage', 'toolCall') or not key:
                return
            if key not in self.items:
                self.items[key] = {'kind': kind, 'chars': 0}
                self.status('[Muse] ' + ('agent' if kind == 'agentMessage' else 'tool ' + str(item.get('tool', 'unknown'))))
            if method == 'item/completed':
                text = item.get('text', '') if kind == 'agentMessage' else item.get('visibleOutput', '')
                seen = self.items[key]['chars']
                self.append(key, str(text)[seen:])
                self.status('[Muse] ' + str(item.get('status', 'completed')))
                del self.items[key]
        elif method == 'item/delta':
            key = p.get('itemId')
            if key in self.items:
                field = p.get('field', 'text')
                allowed = ('text',) if self.items[key]['kind'] == 'agentMessage' else ('visibleOutput', 'output')
                if field in allowed:
                    self.append(key, p.get('delta', ''))
        elif method == 'turn/completed':
            self.status('[Muse] turn ' + str(p.get('terminal', 'unknown')))

    def append(self, key, text):
        text = str(text)
        entry = self.items[key]
        remaining = max(0, 16384 - entry['chars'])
        if text and remaining:
            self.status(text[:remaining])
            if len(text) > remaining:
                self.status('[Muse] output truncated; complete evidence remains in the run directory')
        entry['chars'] += len(text)
