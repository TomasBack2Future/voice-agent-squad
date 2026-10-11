"""Persistent input exercises the same mediated host, not an unpinned TUI."""
import io
import json
from pathlib import Path
import queue
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parents[1]))
import muse_worker_launcher as launch


class Input:
    def __init__(self):
        self.states = []
        self.idle_reads = 0

    def __enter__(self):
        return self

    def __exit__(self, *args):
        pass

    def status(self, state):
        self.states.append(state)

    def read(self, busy):
        if busy:
            return None
        self.idle_reads += 1
        return ('text', 'Continue the same assignment') if self.idle_reads == 1 else ('exit', '')

    def erase(self):
        pass

    def redraw(self):
        pass

    def restore(self, text):
        self.restored = text


class PersistentTurnsTests(unittest.TestCase):
    def fixture(self, tmp, terminal='completed'):
        class Host:
            def __init__(self):
                self.events = queue.Queue()
                self.calls = []
                self.events.put({'method': 'turn/completed', 'params': {'sessionId': 'native', 'terminal': terminal}})

            def rpc(self, method, params):
                self.calls.append((method, params))
                self.events.put({'method': 'turn/completed', 'params': {'sessionId': 'native', 'terminal': 'completed'}})
                return {}

        class Receiver:
            def pending(self):
                return None

        c = {'native_session_id': 'native', 'reasoning_effort': 'high', 'max_seconds': 5}
        return Host(), c, Path(tmp), Receiver(), launch.LiveView('native', stream=io.StringIO())

    def test_completed_turn_keeps_input_and_accepts_same_native_followup(self):
        with tempfile.TemporaryDirectory() as tmp:
            h, c, state, receiver, view = self.fixture(tmp)
            ui = Input()
            with patch.object(launch, 'read_decision', return_value={'revision': 1, 'action': 'proceed'}), \
                    patch.object(launch, 'coordination'):
                terminal = launch.drive_turns(h, c, state, {}, {}, {}, receiver, view, ui)
            self.assertEqual(terminal, 'completed')
            self.assertEqual(len(h.calls), 1, 'a completed turn must leave editable input for the next turn')
            method, params = h.calls[0]
            self.assertEqual(method, 'turn/start')
            self.assertEqual(params['sessionId'], 'native')
            self.assertEqual(params['reasoningEffort'], 'high')
            self.assertEqual(ui.idle_reads, 2)

    def test_hold_keeps_input_without_starting_prohibited_turn(self):
        with tempfile.TemporaryDirectory() as tmp:
            h, c, state, receiver, view = self.fixture(tmp)
            ui = Input()
            with patch.object(launch, 'read_decision', return_value={'revision': 2, 'action': 'hold'}), \
                    patch.object(launch, 'coordination'):
                launch.drive_turns(h, c, state, {}, {}, {}, receiver, view, ui)
            self.assertEqual(h.calls, [])
            self.assertEqual(ui.idle_reads, 2, 'HOLD must preserve the visible input until explicit exit')
            self.assertIn('HOLD', ' '.join(ui.states))

    def test_escape_suspends_before_interrupt_and_retains_task(self):
        with tempfile.TemporaryDirectory() as tmp:
            h, c, state, receiver, view = self.fixture(tmp)
            ui = Input()
            ui.read = lambda busy: ('interrupt', '')
            order = []
            with patch.object(launch, 'coordination', side_effect=lambda c, action: order.append(action)):
                terminal = launch.drive_turns(h, c, state, {}, {}, {}, receiver, view, ui)
            self.assertEqual(terminal, 'interrupted')
            self.assertTrue((state / 'closed').exists())
            self.assertEqual(order, ['suspend'])
            self.assertEqual(h.calls[0][0], 'turn/interrupt')
            self.assertEqual(h.calls[0][1]['sessionId'], 'native')

    def test_failed_turn_requires_joined_restart_without_new_model_turn(self):
        with tempfile.TemporaryDirectory() as tmp:
            h, c, state, receiver, view = self.fixture(tmp, terminal='failed')
            ui = Input()
            terminal = launch.drive_turns(h, c, state, {}, {}, {}, receiver, view, ui)
            self.assertEqual(terminal, 'failed')
            self.assertEqual(ui.action, 'restart-wait')
            self.assertEqual(h.calls, [])

    def test_reported_turn_is_retained_for_join_and_publish_before_followup(self):
        with tempfile.TemporaryDirectory() as tmp:
            h, c, state, receiver, view = self.fixture(tmp)
            report={'status':'completed','summary':'truthful first result'}
            (state/'report.json').write_text(json.dumps(report))
            ui=Input()
            terminal=launch.drive_turns(h,c,state,{},{},{},receiver,view,ui)
            self.assertEqual(terminal,'completed')
            self.assertEqual(ui.action,'reported')
            self.assertEqual(h.calls,[])
            self.assertEqual(json.loads((state/'report.json').read_text()),report)

    def test_idle_heartbeat_failure_keeps_blocked_input_until_explicit_exit(self):
        ui=Input();actions=iter([None,('exit','')]);ui.read=lambda busy:next(actions)
        with tempfile.TemporaryDirectory() as tmp, \
                patch.object(launch,'run_execution',side_effect=ValueError('model failed')), \
                patch.object(launch,'validate_file',return_value={}), \
                patch.object(launch,'child_environment',return_value={}), \
                patch.object(launch,'heartbeat',side_effect=ValueError('stale heartbeat')), \
                patch.object(launch.time,'monotonic',side_effect=[0,21,22]), \
                patch.object(launch.time,'sleep'):
            result=launch.interactive_loop(Path('/assignment'),Path('/config'),{'native_session_id':'native'},ui,None,Path(tmp))
        self.assertEqual(result['status'],'retained')
        self.assertIn('blocked waiting',' '.join(ui.states))

    def test_frontend_reconnects_for_text_after_report_and_keeps_native_lock(self):
        ui=Input()
        receiver=unittest.mock.Mock();receiver.pending.return_value=None
        c={'native_session_id':'native'};assignment={'reservation':{'key':'reservation','generation':1}}
        result={'status':'completed','native':'native','interactive_action':'reported'}
        with tempfile.TemporaryDirectory() as tmp, \
                patch.object(launch,'run_execution',return_value=result) as execution, \
                patch.object(launch,'validate_file',return_value=assignment), \
                patch.object(launch,'Receiver',return_value=receiver), \
                patch.object(launch,'child_environment',return_value={}), \
                patch.object(launch,'read_decision',return_value={'action':'proceed','revision':1}):
            lock=object()
            actual=launch.interactive_loop(Path('/assignment'),Path('/config'),c,ui,lock,Path(tmp))
        self.assertEqual(actual,result)
        self.assertEqual(execution.call_count,2)
        self.assertIs(execution.call_args.args[4],lock)
        self.assertTrue(execution.call_args.args[2])
        self.assertEqual(execution.call_args.args[5],{'text':'Continue the same assignment'})
        self.assertEqual(receiver.close.call_count,2)

    def test_input_editor_preserves_busy_draft_and_edits_unicode(self):
        from muse_worker_input import TerminalInput
        ui = TerminalInput(output=io.StringIO())
        ui.feed('你号\x7f好', busy=True)
        self.assertEqual(ui.line, '你好')
        ui.feed('\n', busy=True)
        self.assertEqual(ui.line, '你好')
        self.assertEqual(ui.actions, [])
        ui.feed('\x1b[D呀\n', busy=False)
        self.assertEqual(ui.actions, [('text', '你呀好')])
        ui.feed('/exit\n', busy=True)
        self.assertEqual(ui.actions[-1], ('exit', ''))

    def test_interactive_refuses_pipe_instead_of_falling_back_to_headless(self):
        from muse_worker_input import TerminalInput
        with self.assertRaisesRegex(ValueError, 'owned input/output terminal'):
            with TerminalInput(io.StringIO(), io.StringIO()):
                self.fail('not a terminal')

    def test_nonretryable_lease_failure_keeps_ui_without_resampling_or_forcing(self):
        ui = Input()
        ui.read = lambda busy: ('exit', '')
        from muse_session_host import RpcError
        with tempfile.TemporaryDirectory() as tmp, \
                patch.object(launch, 'config_file', return_value={'ui_mode':'interactive','native_session_id':'native','state_directory':tmp}), \
                patch.object(launch, 'TerminalInput', return_value=ui), \
                patch.object(launch, 'run_execution', side_effect=RpcError('session/resume', {'code':-32021})) as execution:
            result = launch.run(Path('/assignment'), Path('/config'), resume=True)
        self.assertEqual(result['status'],'retained')
        self.assertEqual(execution.call_count,1)
        self.assertIn('nonretryable', ' '.join(ui.states))

    def test_hold_restores_editable_draft_without_submitting_it(self):
        from muse_worker_input import TerminalInput
        with tempfile.TemporaryDirectory() as tmp:
            h,c,state,receiver,view=self.fixture(tmp)
            ui=TerminalInput(output=io.StringIO());ui.fd=0
            draft='preserve my instructions';ui.feed(draft+'\n',False)
            read=ui.read;idle=[0]
            def action(busy):
                if not busy:
                    idle[0]+=1
                    if idle[0]>1:return ('exit','')
                return read(busy)
            ui.read=action
            with patch('muse_worker_input.select.select',return_value=([],[],[])), \
                    patch.object(launch,'read_decision',return_value={'revision':2,'action':'hold'}), \
                    patch.object(launch,'coordination'):
                launch.drive_turns(h,c,state,{},{},{},receiver,view,ui)
            self.assertEqual(ui.line,draft)
            self.assertEqual(h.calls,[])

    def test_restored_draft_preserves_new_draft_and_unicode_cursor_width(self):
        from muse_worker_input import TerminalInput
        output=io.StringIO();ui=TerminalInput(output=output)
        ui.feed('new draft',False);ui.restore('你好');ui.cursor=1;ui.redraw()
        self.assertTrue(output.getvalue().endswith('\x1b[2D'))
        ui.feed('\x12',False)
        self.assertEqual(ui.line,'new draft')

    def test_pasted_followup_waits_while_busy_but_escape_remains_available(self):
        from muse_worker_input import TerminalInput
        ui=TerminalInput(output=io.StringIO());ui.fd=0
        ui.actions=[('text','human queued draft'),('interrupt','')]
        with patch('muse_worker_input.select.select',return_value=([],[],[])):
            self.assertEqual(ui.read(True),('interrupt',''))
            self.assertIsNone(ui.read(True))
            self.assertEqual(ui.read(False),('text','human queued draft'))

    def test_foreign_turn_completion_does_not_release_current_input(self):
        with tempfile.TemporaryDirectory() as tmp:
            h, c, state, receiver, view = self.fixture(tmp)
            h.events = queue.Queue()
            h.events.put({'method': 'turn/completed', 'params': {'sessionId': 'foreign', 'terminal': 'completed'}})
            ui = Input()
            ui.read = lambda busy: ('exit', '')
            with patch.object(launch, 'coordination'):
                terminal = launch.drive_turns(h, c, state, {}, {}, {}, receiver, view, ui)
            self.assertEqual(terminal, 'interrupted')

    def test_admission_failure_restores_submitted_text_without_retry(self):
        from muse_worker_input import TerminalInput
        ui = TerminalInput(output=io.StringIO()); ui.fd = 0
        draft = 'preserve this submitted followup'
        ui.feed(draft + '\n', False)
        read = ui.read
        ui.read = lambda busy: read(busy) if ui.actions else ('exit', '')
        listener = unittest.mock.Mock(); listener.pending.return_value = None
        assignment = {'reservation': {'key': 'r', 'generation': 1}}
        result = {'status': 'completed', 'native': 'native', 'interactive_action': 'reported'}
        with tempfile.TemporaryDirectory() as tmp, \
                patch('muse_worker_input.select.select', return_value=([], [], [])), \
                patch.object(launch, 'run_execution', return_value=result) as execution, \
                patch.object(launch, 'validate_file', return_value=assignment), \
                patch.object(launch, 'Receiver', return_value=listener), \
                patch.object(launch, 'child_environment', return_value={}), \
                patch.object(launch, 'read_decision', side_effect=[{'action': 'proceed'}, ValueError('decision unavailable')]), \
                patch.object(launch.time, 'sleep'):
            launch.interactive_loop(Path('/assignment'), Path('/config'), {'native_session_id': 'native'}, ui, object(), Path(tmp))
        self.assertEqual(ui.line, draft)
        self.assertEqual(execution.call_count, 1)
        self.assertEqual(ui.actions, [])

    def test_active_unreported_followup_keeps_text_when_admission_fails(self):
        from muse_worker_input import TerminalInput
        with tempfile.TemporaryDirectory() as tmp:
            h, c, state, receiver, view = self.fixture(tmp)
            ui = TerminalInput(output=io.StringIO()); ui.fd = 0
            draft = 'keep unreported followup'
            ui.feed(draft + '\n', False)
            with patch('muse_worker_input.select.select', return_value=([], [], [])), \
                    patch.object(launch, 'read_decision', side_effect=[{'action': 'proceed'}, ValueError('active decision unavailable')]):
                with self.assertRaisesRegex(ValueError, 'active decision unavailable'):
                    launch.drive_turns(h, c, state, {}, {}, {}, receiver, view, ui)
            self.assertEqual(ui.line, draft)
            self.assertEqual(h.calls, [])

    def test_suspended_unreported_followup_keeps_editable_text(self):
        with tempfile.TemporaryDirectory() as tmp:
            h, c, state, receiver, view = self.fixture(tmp)
            (state / 'closed').touch()
            ui = Input()
            with patch.object(launch, 'read_decision', return_value={'action': 'proceed'}), \
                    patch.object(launch, 'coordination'):
                launch.drive_turns(h, c, state, {}, {}, {}, receiver, view, ui)
            self.assertEqual(ui.restored, 'Continue the same assignment')
            self.assertEqual(h.calls, [])

    def test_recreated_receiver_appends_prior_accepted_delivery(self):
        import muse_worker_receiver as receiver
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            c = {'native_session_id': 'native', 'reasoning_effort': 'high', 'assignment': {},
                 'coordination_executable': 'private-stub', 'ledger_directory': tmp}
            host = unittest.mock.Mock()
            receipt = lambda event: {'events': [{'event_id': event}]}
            with patch.object(receiver.Receiver, 'start'), \
                    patch.object(receiver, 'child_environment', return_value={}), \
                    patch.object(receiver.subprocess, 'run'):
                receiver.Receiver(c, state).submit(host, receipt('event-one'))
                receiver.Receiver(c, state).submit(host, receipt('event-two'))
            entries = json.loads((state / 'deliveries.json').read_text())
            self.assertEqual([entry['receipt']['events'][0]['event_id'] for entry in entries], ['event-one', 'event-two'])
            self.assertEqual([entry['state'] for entry in entries], ['accepted', 'accepted'])

    def test_reconnect_failure_retains_only_unaccepted_input_and_new_draft(self):
        from muse_worker_input import TerminalInput
        import subprocess
        for error in (ValueError('claim rejected'), ValueError('decision unavailable'),
                      OSError('heartbeat unavailable'), subprocess.SubprocessError('native admission failed')):
            for accepted in (False, True):
                with self.subTest(error=str(error), accepted=accepted), tempfile.TemporaryDirectory() as tmp:
                    ui = TerminalInput(output=io.StringIO()); ui.fd = 0
                    ui.feed('submitted text\nnew draft', False)
                    read = ui.read
                    ui.read = lambda busy: read(busy) if ui.actions else ('exit', '')
                    listener = unittest.mock.Mock(); listener.pending.return_value = None
                    assignment = {'reservation': {'key': 'r', 'generation': 1}}
                    reported = {'status': 'completed', 'native': 'native', 'interactive_action': 'reported'}
                    calls = []
                    def execute(*args):
                        calls.append(args)
                        if len(calls) == 1:
                            return reported
                        if accepted:
                            args[5]['accepted'] = True
                        raise error
                    with patch('muse_worker_input.select.select', return_value=([], [], [])), \
                            patch.object(launch, 'run_execution', side_effect=execute), \
                            patch.object(launch, 'validate_file', return_value=assignment), \
                            patch.object(launch, 'Receiver', return_value=listener), \
                            patch.object(launch, 'child_environment', return_value={}), \
                            patch.object(launch, 'read_decision', return_value={'action': 'proceed'}):
                        launch.interactive_loop(Path('/assignment'), Path('/config'), {'native_session_id': 'native'}, ui, object(), Path(tmp))
                    self.assertEqual(len(calls), 2)
                    self.assertEqual(ui.line, 'new draft' if accepted else 'submitted text')
                    if not accepted:
                        ui.feed('\x12', False)
                        self.assertEqual(ui.line, 'new draft')

    def test_inner_admission_errors_retain_submitted_and_new_draft(self):
        from muse_worker_input import TerminalInput
        import subprocess
        for error in (ValueError('claim rejected'), ValueError('decision unavailable'),
                      OSError('heartbeat unavailable'), subprocess.SubprocessError('native admission failed')):
            with self.subTest(error=str(error)), tempfile.TemporaryDirectory() as tmp:
                h, c, state, receiver, view = self.fixture(tmp)
                h.rpc = unittest.mock.Mock(side_effect=error)
                ui = TerminalInput(output=io.StringIO()); ui.fd = 0
                ui.feed('submitted text\nnew draft', False)
                with patch('muse_worker_input.select.select', return_value=([], [], [])), \
                        patch.object(launch, 'read_decision', return_value={'action': 'proceed'}):
                    with self.assertRaises(type(error)):
                        launch.drive_turns(h, c, state, {}, {}, {}, receiver, view, ui)
                self.assertEqual(ui.line, 'submitted text')
                ui.feed('\x12', False)
                self.assertEqual(ui.line, 'new draft')

    def test_native_acceptance_is_the_only_pending_input_consumption(self):
        c = {'native_session_id': 'native', 'reasoning_effort': 'high'}
        h = unittest.mock.Mock()
        pending = {'text': 'submitted text'}
        h.rpc.side_effect = ValueError('native refused')
        with self.assertRaisesRegex(ValueError, 'native refused'):
            launch.submit_text(h, c, 'command', pending['text'], pending)
        self.assertNotIn('accepted', pending)
        h.rpc.side_effect = None
        launch.submit_text(h, c, 'command', pending['text'], pending)
        self.assertTrue(pending['accepted'])
        self.assertEqual(h.rpc.call_args.args[0], 'turn/start')
        self.assertEqual(h.rpc.call_args.args[1]['sessionId'], 'native')


if __name__ == '__main__':
    unittest.main()
