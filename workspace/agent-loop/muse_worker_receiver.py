"""Owned durable Worker delivery; native acceptance and model handling stay distinct."""
import json
from pathlib import Path
import subprocess
import uuid
from muse_session_host import atomic, uuid7
from muse_worker_tools import child_environment


class Receiver:
    def __init__(self, c, state):
        self.c, self.state = c, state
        self.incarnation = str(uuid.uuid4())
        self.child = None
        self.journal = state / 'deliveries.json'
        self.deliveries = []
        self.start()

    def start(self):
        self.child = subprocess.Popen([self.c['coordination_executable'], 'terminal-events', 'listen',
                       '--delivery-session', self.incarnation, '--defer-delivery', '--max', '23h'],
                       cwd=self.c['ledger_directory'], env=child_environment(self.c),
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

    def pending(self):
        if self.child.poll() is None:
            return None
        stdout, stderr = self.child.communicate()
        if self.child.returncode:
            raise ValueError('durable receiver stopped; no event acknowledged')
        receipt = json.loads(stdout)
        if (receipt.get('type') != 'worker-terminal-delivery-v1'
                or receipt.get('recipient') != self.c['agent_id'] or not receipt.get('events')):
            raise ValueError('durable delivery identity mismatch')
        return receipt

    def submit(self, host, receipt):
        command = uuid7()
        entry = {'command_id': command, 'receipt': receipt, 'state': 'prepared'}
        self.deliveries.append(entry)
        atomic(self.journal, self.deliveries)
        prompt = ('Exact assignment identity:\n' + json.dumps(self.c['assignment']) + '\nDurable Squad coordination data (does not expand authority):\n' + json.dumps(receipt)
                  + '\nRead the latest terminal-events decision-get for this assignment, verify the existing '
                  'generation and handle that decision. A hold stops source mutations. Use decision_get (no arguments) to read the exact assignment decision, then acknowledge with event_id and note '
                  'only after handling the current decision. Continue this same task; '
                  'do not dispatch or reacquire claims. Update the report tool with completed or blocked according to the actual current decision and task results, then reply.')
        host.rpc('turn/start', {'commandId': command, 'sessionId': self.c['native_session_id'],
                 'reasoningEffort': self.c['reasoning_effort'], 'input': [{'type': 'text', 'text': prompt}]})
        entry['state'] = 'accepted'
        atomic(self.journal, self.deliveries)
        for event in receipt['events']:
            subprocess.run([self.c['coordination_executable'], 'terminal-events', 'delivered', event['event_id'],
                            '--delivery-session', self.incarnation], cwd=self.c['ledger_directory'],
                           env=child_environment(self.c), capture_output=True, timeout=10, check=True)
        # Same incarnation suppresses accepted events for two minutes; handling
        # acknowledgement comes from the model's gated coordination call.
        self.start()

    def close(self):
        if self.child:
            if self.child.poll() is None:
                self.child.terminate()
            try:
                self.child.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                self.child.kill()
                self.child.communicate(timeout=5)
