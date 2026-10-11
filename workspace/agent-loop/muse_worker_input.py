"""Owned terminal line input for the mediated Muse host; no tool execution."""
import codecs
import os
import re
import select
import shutil
import sys
import termios
import time
import unicodedata


class TerminalInput:
    def __init__(self, stream=None, output=None):
        self.stream = sys.stdin if stream is None else stream
        self.output = sys.stderr if output is None else output
        self.line = ''
        self.cursor = 0
        self.escape = ''
        self.escape_at = 0
        self.state = 'starting'
        self.action = None
        self.decoder = codecs.getincrementaldecoder('utf-8')('replace')
        self.actions = []
        self.other_draft = ('', 0)

    def __enter__(self):
        if not self.stream.isatty() or not self.output.isatty():
            raise ValueError('interactive Muse requires an owned input/output terminal; no headless fallback')
        self.fd = self.stream.fileno()
        self.saved = termios.tcgetattr(self.fd)
        changed = termios.tcgetattr(self.fd)
        changed[3] &= ~(termios.ECHO | termios.ICANON | termios.ISIG)
        changed[6][termios.VMIN] = 1
        changed[6][termios.VTIME] = 0
        termios.tcsetattr(self.fd, termios.TCSANOW, changed)
        self.status('starting — Enter submits, Escape interrupts, /resume reconnects after join, /exit exits')
        return self

    def __exit__(self, *args):
        try:
            self.erase()
        finally:
            termios.tcsetattr(self.fd, termios.TCSANOW, self.saved)

    def erase(self):
        self.output.write('\r\x1b[2K')
        self.output.flush()

    def redraw(self):
        # Keep one bounded line visible. Protocol/tool output never becomes input.
        width = max(20, shutil.get_terminal_size((80, 24)).columns - 12)
        def columns(text):
            return sum(0 if unicodedata.combining(char) else
                       2 if unicodedata.east_asian_width(char) in ('W', 'F') else 1 for char in text)
        start = self.cursor
        while start and columns(self.line[start - 1:self.cursor]) <= width:
            start -= 1
        end = self.cursor
        while end < len(self.line) and columns(self.line[start:end + 1]) <= width:
            end += 1
        visible = self.line[start:end]
        self.erase()
        self.output.write('Muse ❯ ' + visible)
        tail = columns(self.line[self.cursor:end])
        if tail > 0:
            self.output.write('\x1b[' + str(tail) + 'D')
        self.output.flush()

    def status(self, state):
        state = re.sub(r'\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))', '', str(state))
        state = ''.join(char for char in state if char in '\n\t' or char.isprintable())
        self.state = state
        self.erase()
        print('[Muse interactive] ' + state, file=self.output, flush=True)
        self.redraw()

    def restore(self, text):
        if self.line:
            self.other_draft = (self.line, self.cursor)
        self.line, self.cursor = text, len(text)
        self.redraw()

    def feed(self, text, busy):
        for char in text:
            if self.escape:
                self.escape += char
                if self.escape == '\x1b[':
                    continue
                if self.escape == '\x1b[D':
                    self.cursor = max(0, self.cursor - 1)
                elif self.escape == '\x1b[C':
                    self.cursor = min(len(self.line), self.cursor + 1)
                elif self.escape == '\x1b[H':
                    self.cursor = 0
                elif self.escape == '\x1b[F':
                    self.cursor = len(self.line)
                self.escape = ''
            elif char == '\x1b':
                self.escape = char
                self.escape_at = time.monotonic()
            elif char == '\x03':
                self.actions.append(('interrupt', ''))
            elif char == '\x04' and not self.line:
                self.actions.append(('exit', ''))
            elif char in ('\r', '\n'):
                text = self.line.strip()
                if text in ('/exit', '/resume'):
                    self.actions.append((text[1:], ''))
                elif text and not busy:
                    self.actions.append(('text', self.line))
                else:
                    # Enter during a running turn preserves the human draft.
                    continue
                self.line = ''
                self.cursor = 0
            elif char in ('\x7f', '\b'):
                if self.cursor:
                    self.line = self.line[:self.cursor - 1] + self.line[self.cursor:]
                    self.cursor -= 1
            elif char == '\x15':
                self.line = self.line[self.cursor:]
                self.cursor = 0
            elif char == '\x01':
                self.cursor = 0
            elif char == '\x05':
                self.cursor = len(self.line)
            elif char == '\x12':
                self.other_draft, (self.line, self.cursor) = (self.line, self.cursor), self.other_draft
            elif char.isprintable() and len(self.line.encode()) < 16380:
                self.line = self.line[:self.cursor] + char + self.line[self.cursor:]
                self.cursor += 1

    def read(self, busy):
        if select.select([self.fd], [], [], 0)[0]:
            data = os.read(self.fd, 4096)
            if not data:
                return ('exit', '')
            self.feed(self.decoder.decode(data), busy)
            self.redraw()
        if self.escape and time.monotonic() - self.escape_at >= 0.1:
            self.escape = ''
            self.actions.append(('interrupt', ''))
        for index, action in enumerate(self.actions):
            if not busy or action[0] != 'text':
                return self.actions.pop(index)
        return None
