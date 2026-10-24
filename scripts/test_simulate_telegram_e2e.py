"""Driver contracts; provider/Core acceptance is a separate executable run."""
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from channel_simulation import ChannelRunner, validate_test_database
from simulate_telegram_e2e import accepted_text, update
from simulation_fixtures import fixture_user


class TelegramDriverTests(unittest.TestCase):
    def test_database_guard_excludes_existing_simulation_and_production(self):
        validate_test_database('postgresql://user:secret@127.0.0.1:18433/relay_hub_v1_test')
        for dsn in ['postgresql://127.0.0.1:18433/'+db for db in ('channel_hub','relay_hub_sim','postgres')]+[
            'postgresql://remote:18433/relay_hub_v1_test',
            'postgresql://127.0.0.1:5432/relay_hub_v1_test',
            'postgresql://127.0.0.1:18433/relay_hub_v1_test?host=remote',
            'postgresql://127.0.0.1:18433/relay_hub_v1_test?search_path=public']:
            with self.subTest(dsn=dsn),self.assertRaises(ValueError):validate_test_database(dsn)

    def test_fixture_user_throttle_bypass_is_restored_even_on_failure(self):
        for previous in (True,False,None):
            f=SimpleNamespace(flags=SimpleNamespace(in_import=previous),generate_hash=lambda **_: 'synthetic')
            def fail(*args,**kwargs):
                self.assertIs(f.flags.in_import,True)
                self.assertEqual(kwargs['send_welcome_email'],0)
                raise RuntimeError('fixture failed')
            with self.assertRaises(RuntimeError):fixture_user(f,fail,'service',[],'test')
            self.assertIs(f.flags.in_import,previous)

    def test_private_identity_and_attachment_updates(self):
        value=update('1234',456,'hello')
        self.assertEqual(value['message']['chat'],{'id':1234,'type':'private'})
        self.assertEqual(value['message']['from']['id'],1234)
        self.assertEqual(value['message']['text'],'hello')
        document=update('1234',457,None,document={'file_id':'fake'},caption='attachment')
        self.assertNotIn('text',document['message'])
        self.assertEqual(document['message']['document']['file_id'],'fake')

    def test_acceptance_count_does_not_count_failed_attempts(self):
        self.assertEqual(accepted_text({'messages':None},'hello'),[])
        self.assertEqual(len(accepted_text({'messages':[{'request':{'text':'hello'}},{'request':{'text':'other'}}]},'hello')),1)

    def test_observe_reads_only_after_realtime_and_matches_expected_state(self):
        runner=object.__new__(ChannelRunner)
        runner.report={'latencies_ms':[]}
        calls=[]
        runner.realtime=Mock()
        runner.realtime.wait.side_effect=lambda *args: (calls.append('event') or {'monotonic_ns':20})
        runner.snapshot=Mock(side_effect=lambda: (calls.append('read') or {'ready':True}))
        with patch('channel_simulation.time.monotonic_ns',side_effect=[10,20]):
            result=runner.observe(lambda:calls.append('emit'),lambda s:s['ready'])
        self.assertTrue(result)
        self.assertEqual(calls,['emit','event','read'])
        self.assertEqual(runner.report['latencies_ms'],[.00001])

    def test_realtime_timeout_never_falls_back_to_snapshot_polling(self):
        runner=object.__new__(ChannelRunner)
        runner.realtime=Mock();runner.realtime.wait.side_effect=TimeoutError('missing event')
        runner.snapshot=Mock()
        with self.assertRaises(TimeoutError):runner.observe(lambda:None,lambda s:True)
        runner.snapshot.assert_not_called()

    def test_failed_optional_case_stays_failed(self):
        runner=object.__new__(ChannelRunner);runner.report={'steps':{}}
        with runner.step('routing',continue_on_error=True):raise AssertionError('Core defect')
        self.assertEqual(runner.report['steps']['routing']['status'],'fail')

    def test_denial_requires_403_not_server_failure(self):
        runner=object.__new__(ChannelRunner);runner.fixture={'unauthorized':{}}
        runner.core=Mock(side_effect=RuntimeError('HTTP 403 at /method'))
        runner.permission_denied('method',{})
        runner.core.side_effect=RuntimeError('HTTP 500 at /method')
        with self.assertRaises(AssertionError):runner.permission_denied('method',{})


class ChannelAcceptanceTests(unittest.TestCase):
    def runner(self, medium):
        from channel_sim_acceptance import ChannelAcceptance
        runner = ChannelAcceptance()
        runner.medium, runner.run_id, runner.report = medium, '1792805488467037716', {}
        runner.emit = Mock()
        return runner

    def test_email_replay_keeps_hub_identity_and_reply_thread(self):
        from email import policy
        from email.parser import Parser
        runner = self.runner('Email')
        provider, emit = runner.ai_payload({'endpoint':'ledger@example.invalid'}, 2, 'yes')
        emit(); emit()
        self.assertEqual(runner.emit.call_args_list[0],runner.emit.call_args_list[1])
        call = runner.emit.call_args
        self.assertEqual(provider,call.kwargs['identifier'])
        message = Parser(policy=policy.default).parsestr(call.args[0])
        self.assertEqual(message['In-Reply-To'], '<ledger-1792805488467037716-1@example.invalid>')
        self.assertEqual(message['References'],message['In-Reply-To'])

    def test_email_blank_provider_field_remains_a_reported_defect(self):
        runner = self.runner('Email')
        runner.check_email_identity(dict(name='outgoing',provider_id='',
            provider_context='{"provider":"Email","message_id":"<accepted@example.invalid>"}'))
        self.assertEqual(runner.report['email_identity_defects'],['outgoing'])

    def test_latency_gate_keeps_failure_and_all_ten_samples(self):
        runner = object.__new__(ChannelRunner)
        runner.run_id, runner.fixture, runner.report = 'run', {}, {'latency_samples':[]}
        runner.ai_payload = Mock(return_value=('id',lambda:None))
        def message(*args): runner.report['latency_samples'].append({'milliseconds':501})
        runner.message = message
        with self.assertRaisesRegex(AssertionError,'500 ms'): runner.latency_acceptance()
        self.assertEqual(runner.report['single_inbound_latency_ms']['samples'],10)
        self.assertFalse(runner.report['single_inbound_latency_ms']['passed'])


if __name__=='__main__':unittest.main()
