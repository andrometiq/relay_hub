"""Runner contracts; connected acceptance remains a separate command."""
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, call, patch
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import simulate_whatsapp_e2e as sim


class RunnerTests(unittest.TestCase):
    def test_automation_workers_cannot_share_projection_capacity(self):
        from whatsapp_sim_site import require_worker_isolation
        def worker(*lanes):
            return SimpleNamespace(queue_names=lambda: ['bench:' + lane for lane in lanes])
        require_worker_isolation([worker('default'), worker('short', 'long')])
        for workers in ([], [worker('short', 'long')], [worker('default', 'short', 'long')],
                        [worker('default'), worker('default', 'long'), worker('short')]):
            with self.subTest(workers=workers), self.assertRaises(AssertionError):
                require_worker_isolation(workers)

    def test_automation_barrier_reads_durable_message_completion(self):
        from whatsapp_sim_acceptance import Acceptance
        f = Mock()
        acceptance = Acceptance(f, {}, Mock())
        for status, expected in ((None, False), ('', False), ('Pending', False), ('Complete', True)):
            f.db.get_value.return_value = status
            self.assertEqual(acceptance.automation_checked('message'), expected)
        f.db.get_value.assert_called_with('Relay Message', 'message', 'automation_status')

    def test_paused_assertion_waits_for_automation_and_rereads_committed_state(self):
        from whatsapp_sim_acceptance import Acceptance
        acceptance = Acceptance(Mock(), {}, Mock())
        pending = dict(incoming=[{'name': 'paused'}], paused=1, runs=[], actions=[], outgoing=[])
        acceptance.ai_state = Mock(side_effect=[pending, pending, dict(pending, runs=[{'status': 'Pending'}])])
        acceptance.automation_checked = Mock(side_effect=[False, True])
        with patch('whatsapp_sim_acceptance.time.sleep'), self.assertRaisesRegex(AssertionError, 'created an automation run'):
            acceptance.wait_paused('provider')

    def test_business_event_fixture_supplies_site_tax_currency_and_price_list(self):
        from whatsapp_sim_acceptance import Acceptance
        f = Mock(conf=SimpleNamespace(developer_mode=1), local=SimpleNamespace(site='yrp.site'))
        f.get_all.return_value = []
        f.db.get_single_value.return_value = 'Configured company'
        f.db.get_value.side_effect = lambda dt, *args: {'Relay WhatsApp Template': 'template',
            'Item Group': 'Services', 'UOM': 'Nos', 'Company': 'INR'}[dt]
        f.get_meta.return_value.has_field.return_value = True
        f.db.exists.return_value = True
        insert = Mock(side_effect=lambda dt, **values: SimpleNamespace(name=dt))
        acceptance = Acceptance(f, {'channel': 'channel', 'operator': {'name': 'operator'}, 'customer': 'customer', 'person': 'person'}, insert)
        acceptance.seed_event('run', 'template-id')
        item = insert.call_args_list[0].kwargs
        self.assertEqual((item['gst_hsn_code'], item['is_stock_item']), ('998313', 0))
        price = insert.call_args_list[1].kwargs
        self.assertEqual((price['selling'], price['currency']), (1, 'INR'))
        self.assertEqual(acceptance.event['company'], 'Configured company')
        f.db.get_single_value.assert_called_once_with('Global Defaults', 'default_company')

    def test_ai_preflight_requires_explicit_automation_activation_before_fixtures(self):
        from whatsapp_sim_acceptance import Acceptance
        for conf in ({}, {'relay_automation_enabled': 0}, {'relay_automation_enabled': False}):
            insert = Mock()
            acceptance = Acceptance(SimpleNamespace(conf=conf), {}, insert)
            with self.assertRaisesRegex(AssertionError, 'activation needs separate approval'):
                acceptance.preflight()
            with self.assertRaisesRegex(AssertionError, 'activation needs separate approval'):
                acceptance.seed_ai('run', 'http://127.0.0.1:18094')
            insert.assert_not_called()
        Acceptance(SimpleNamespace(conf={'relay_automation_enabled': 1}), {}, Mock()).preflight()

    def test_paused_message_waits_for_its_committed_projection(self):
        from whatsapp_sim_acceptance import Acceptance
        acceptance = Acceptance(Mock(), {}, Mock())
        acceptance.automation_checked = Mock(return_value=True)
        state = dict(incoming=[{'name': 'PAUSED-MESSAGE'}], paused=1, runs=[], actions=[], outgoing=[])
        acceptance.ai_state = Mock(side_effect=[dict(state, incoming=[]), state, state])
        with patch('whatsapp_sim_acceptance.time.sleep'):
            self.assertEqual(acceptance.wait_paused('wamid.paused'), state)
        self.assertEqual(acceptance.ai_state.call_args_list, [call('wamid.paused'), call('wamid.paused'), call('wamid.paused')])
        for change in (dict(runs=[{'status': 'Pending'}]), dict(actions=[{'status': 'Pending'}]),
                       dict(outgoing=[{}]), dict(paused=0), dict(incoming=[{}, {}])):
            acceptance.ai_state = Mock(return_value=dict(state, **change))
            with self.subTest(change=change), self.assertRaises(AssertionError):
                acceptance.wait_paused('wamid.paused')
        acceptance.ai_state = Mock(return_value=dict(state, incoming=[]))
        with patch('whatsapp_sim_acceptance.time.monotonic', side_effect=[0, 0, 36]), \
                patch('whatsapp_sim_acceptance.time.sleep'):
            with self.assertRaisesRegex(AssertionError, 'not projected'):
                acceptance.wait_paused('wamid.paused')

    def test_ai_step_checks_new_paused_message_even_when_callbacks_are_acknowledged(self):
        from whatsapp_sim_acceptance import Acceptance
        with tempfile.TemporaryDirectory(prefix='relay-sim-') as directory:
            runner = sim.Runner(sim.arguments(['--dry-run', '--report-dir', directory]))
            acceptance = Acceptance(Mock(), {}, Mock())
            acceptance.automation_checked = Mock(return_value=True)
            acceptance.ai_state = Mock(return_value=dict(incoming=[{'name': 'PAUSED-MESSAGE'}],
                paused=1, runs=[{'status': 'Pending'}], actions=[], outgoing=[]))
            def site_call(action, **values):
                if action == 'acceptance_seed_ai': return {'endpoint': '919800000001'}
                if action == 'acceptance_wait_ai': return {}
                if action == 'acceptance_wait_paused': return acceptance.wait_paused(values['provider'])
                self.fail('Inspected old handoff before checking the new paused message')
            runner.site = Mock()
            runner.site.call.side_effect = site_call
            runner.emit = Mock()
            runner.wait_control = Mock(return_value={'callbacks': [{'acknowledged': True}]})
            runner.control = Mock(return_value={})
            runner.check_ai_reply = Mock(side_effect=[
                dict(outgoing=[{'content': 'Confirm 2026-10-01 to 2026-10-31'}], runs=[{'decision': '{"reads":[]}'}]),
                dict(outgoing=[{'content': 'SIM-OWN-DEBIT SIM-OWN-CREDIT Debit: 100.10 Credit: 40.05 Net debit: 60.05'}],
                     runs=[{'decision': '{"reads":[{}]}'}])])
            with patch.object(sim, 'provider_posts', return_value=[{}, {}]), \
                    self.assertRaisesRegex(AssertionError, 'Paused message created an automation run'):
                runner.ai_acceptance()
            self.assertEqual(acceptance.ai_state.call_args_list, [call(f'wamid.sim.ai.paused.{runner.run_id}')] * 2)

    def test_projection_timings_ignore_expired_unrelated_and_other_site_jobs(self):
        from rq.exceptions import NoSuchJobError
        from whatsapp_sim_site import finished_projection_timings
        queue = Mock()
        queue.connection.zrange.return_value = [b'expired', b'other-site', b'other-event', b'ours']
        def job(site, name):
            return SimpleNamespace(kwargs={'site': site, 'kwargs': {'name': name}}, meta={'relay_timing': {'projection_ms': 4}})
        with patch('rq.job.Job.fetch', side_effect=[NoSuchJobError(), job('other', 'event'), job('yrp.site', 'other'), job('yrp.site', 'event')]):
            self.assertEqual(finished_projection_timings(queue, 'yrp.site', {'event': '9'}, 'long'),
                             [{'projection_ms': 4, 'event': 'event', 'hub_event_id': '9', 'queue': 'long'}])

    def test_waits_for_finished_rq_metadata_after_message_commit(self):
        with tempfile.TemporaryDirectory(prefix='relay-sim-') as directory:
            runner = sim.Runner(sim.arguments(['--dry-run', '--report-dir', directory]))
            for batches in (3, 24):
                rows = [dict(event=str(i), hub_event_id='9', queue='long') for i in range(batches)]
                runner.site = Mock()
                runner.site.call.side_effect = [{'projection_timings': rows[:-1]}, {'projection_timings': rows}]
                with patch.object(sim.time, 'sleep'):
                    self.assertEqual(runner.wait_projection_timings([9], batches), rows)
                self.assertEqual(runner.site.call.call_count, 2)

    def test_breakdown_separates_dispatch_http_receive_and_projection(self):
        sample = dict(provider='wamid.sample', phase='held', latency_ms=100)
        records = [dict(kind='webhook', status=200, body={'id': 'wamid.sample'}, started_ns=0, finished_ns=10_000_000),
                   dict(kind='callback', status=200, body={'id': 'wamid.sample'}, started_ns=30_000_000,
                        finished_ns=80_000_000, headers={'X-Relay-Dispatch-Ms': ['5']},
                        response={'message': {'timing': {'receive_ms': 40, 'inline_projection_ms': 30}}})]
        values = sim.inbound_breakdown(records, sample)
        self.assertEqual([values[k] for k in ('ingress_ms', 'hub_schedule_ms', 'hub_dispatch_ms',
                         'callback_http_ms', 'receive_ms', 'inline_projection_ms', 'post_callback_ms')],
                         [10, 15, 5, 50, 40, 30, 20])

    def test_samples_target_measured_projection_midpoints(self):
        with tempfile.TemporaryDirectory(prefix='relay-sim-') as directory:
            runner = sim.Runner(sim.arguments(['--dry-run', '--report-dir', directory]))
            clock, starts = [0.0], []
            def observe(provider, phase):
                starts.append(clock[0])
                clock[0] += .2
                runner.report['inbound_samples'].append({'latency_ms':200})
                return {'receipts_complete':len(starts)==6}
            runner.site = Mock()
            runner.site.call.return_value = dict(started_ns=0,finished_ns=100_000_000)
            with patch.object(runner,'observe_inbound',side_effect=observe), \
                    patch.object(sim.time,'monotonic',side_effect=lambda:clock[0]), \
                    patch.object(sim.time,'sleep',side_effect=lambda duration:clock.__setitem__(0,clock[0]+duration)):
                self.assertEqual(runner.sample_receipt_projection([42]),6)
            for actual, expected in zip(starts,[0,.85,1.85,2.85,3.85,4.85]):
                self.assertAlmostEqual(actual,expected)
            runner.site.call.assert_called_with('receipt_window',event_ids=[42])

    def test_sampling_times_out_if_receipts_never_finish(self):
        with tempfile.TemporaryDirectory(prefix='relay-sim-') as directory:
            runner = sim.Runner(sim.arguments(['--dry-run', '--report-dir', directory]))
            runner.site = Mock()
            runner.site.call.return_value = None
            with patch.object(runner,'observe_inbound',return_value={'receipts_complete':False}) as observe, \
                    patch.object(sim.time,'monotonic',side_effect=[0,0,0,0,.1,120]), \
                    patch.object(sim.time,'sleep'):
                with self.assertRaisesRegex(TimeoutError,'Receipt projection'):
                    runner.sample_receipt_projection([42])
            observe.assert_called_once()

    def test_storm_gate_requires_real_processing_overlap_and_strict_target(self):
        spans = [dict(hub_event_id='9', queue='long', started_ns=1_000_000_000, finished_ns=2_000_000_000)]
        samples = [dict(phase='processing', started_ns=1_100_000_000, observed_ns=1_300_000_000, latency_ms=200)] * 5
        result = sim.storm_latency(samples, spans, [9], 12000)
        self.assertTrue(result['passed'])
        self.assertEqual(result['receipts_per_second'], 12000)
        self.assertEqual(result['receipt_count'], 12000)
        self.assertFalse(sim.storm_latency(samples[:4], spans, [9], 12000)['passed'])
        self.assertFalse(sim.storm_latency(samples, [], [9], 12000)['passed'])
        self.assertFalse(sim.storm_latency([dict(s, phase='held') for s in samples], spans, [9], 12000)['passed'])
        self.assertFalse(sim.storm_latency([dict(s, latency_ms=500) for s in samples], spans, [9], 12000)['passed'])
        self.assertFalse(sim.storm_latency(samples, spans, [10], 12000)['passed'])
        # Samples entirely outside processing are excluded.
        for observed in (900_000_000,):
            self.assertFalse(sim.storm_latency([dict(s, observed_ns=observed) for s in samples], spans, [9], 12000)['passed'])
        self.assertTrue(sim.storm_latency([dict(s, started_ns=900_000_000, observed_ns=1_000_000_000)
                                          for s in samples], spans, [9], 12000)['passed'])

    def test_slow_arrival_remains_in_storm_gate(self):
        spans = [dict(hub_event_id='9', queue='long', started_ns=1_000_000_000, finished_ns=2_000_000_000)]
        samples = [dict(phase='processing', started_ns=1_100_000_000, observed_ns=1_300_000_000, latency_ms=200)] * 5
        samples.append(dict(phase='processing', started_ns=1_900_000_000, observed_ns=2_900_000_000, latency_ms=1000))
        result = sim.storm_latency(samples, spans, [9], 12000)
        self.assertEqual(result['samples'], 6)
        self.assertEqual(result['p95'], 1000)
        self.assertFalse(result['passed'])

    def test_fixture_targets_are_confined(self):
        from whatsapp_sim_site import validate_site
        validate_site('yrp.site', sim.BENCH / 'sites')
        for site, sites in [('sales-prod.site', sim.BENCH / 'sites'), ('yrp.site', '/tmp/sites')]:
            with self.assertRaises(ValueError):
                validate_site(site, sites)
        for args in (['--site', 'sales-prod.site'], ['--sites', '/tmp/sites']):
            with self.assertRaises(SystemExit):
                sim.arguments(args)

    def test_storm_combines_events_but_excludes_projection_gaps(self):
        spans = [dict(hub_event_id=str(event), queue='long', started_ns=start, finished_ns=start + 100_000_000)
                 for event, start in ((9, 1_000_000_000), (10, 2_000_000_000))]
        samples = [dict(phase='processing', started_ns=stamp-10_000_000, observed_ns=stamp, latency_ms=100)
                   for stamp in (1_050_000_000, 2_050_000_000, 1_500_000_000)]
        result = sim.storm_latency(samples, spans, [9, 10], 6000)
        self.assertEqual(result['samples'], 2)
        self.assertEqual(result['status_projection_seconds'], 1.1)
        self.assertAlmostEqual(result['receipts_per_second'], 6000 / 1.1)
        self.assertFalse(result['passed'])

    def test_rejects_remote_or_ambiguous_destinations(self):
        for origin in ['http://localhost:8001', 'https://127.0.0.1:8001', 'http://192.0.2.1:80',
                       'http://0.0.0.0:80', 'http://user:secret@127.0.0.1:80', 'http://127.0.0.1:80/a',
                       'http://127.0.0.1:80?redirect=1', 'http://127.0.0.1']:
            with self.subTest(origin=origin), self.assertRaises(ValueError):
                sim.loopback_origin(origin)
        self.assertEqual(sim.loopback_origin('http://[::1]:8001/'), 'http://[::1]:8001')
        with self.assertRaises(ValueError):
            sim.address('127.0.0.1:18091/')

    def test_storm_contains_exactly_three_receipts_per_message(self):
        value = sim.receipt_storm([f'wamid.{i}' for i in range(417)])['entry'][0]['changes'][0]['value']
        receipts = value['statuses']
        self.assertEqual(len(receipts), 1251)
        self.assertEqual(value['metadata']['phone_number_id'], '110')
        for i in range(417):
            group = receipts[i * 3:i * 3 + 3]
            self.assertEqual([r['status'] for r in group], ['sent', 'delivered', 'read'])
            self.assertEqual({r['id'] for r in group}, {f'wamid.{i}'})
            self.assertEqual({r['recipient_id'] for r in group}, {'919999999999'})

    @patch('simulate_whatsapp_e2e.time.time', return_value=1_700_000_000)
    def test_configurable_storm_preserves_final_read_and_exact_size(self, clock):
        self.assertEqual(sim.arguments([]).storm_receipts, 12000)
        for count in (1, 2, 3, 12000, 12001):
            self.assertEqual(sim.arguments(['--storm-receipts', str(count)]).storm_receipts, count)
            providers = [f'wamid.{i}' for i in range(sim.math.ceil(count / 3))]
            receipts = sim.receipt_storm(providers, count)['entry'][0]['changes'][0]['value']['statuses']
            payloads = list(sim.sustained_storm(providers, count))
            self.assertTrue(all(len(json.dumps(p).encode()) < 1024 * 1024 for p in payloads))
            self.assertEqual([r for p in payloads for r in p['entry'][0]['changes'][0]['value']['statuses']], receipts)
            self.assertEqual(len(receipts), count)
            self.assertEqual({r['id']: r['status'] for r in receipts}, dict.fromkeys(providers, 'read'))
        for count in ('0', '-1'):
            with self.assertRaises(SystemExit):
                sim.arguments(['--storm-receipts', count])

    def test_read_observation_accepts_storm_size_but_not_empty_or_incomplete(self):
        from whatsapp_sim_site import expected_rows
        providers = [str(i) for i in range(4000)]
        data = {'messages': [dict(provider_id=p, status='Read') for p in providers]}
        self.assertTrue(expected_rows('read', '', data, providers)[0])
        self.assertFalse(expected_rows('read', '', data, [])[0])
        data['messages'].pop()
        self.assertFalse(expected_rows('read', '', data, providers)[0])

    def test_successful_batches_handle_omitted_fault_and_filter_other_events(self):
        def row(event, count, **extra):
            return {'kind': 'status', 'status': 200,
                    'body': {'statuses': [{'event_id': event}] * count}, **extra}
        records = [row(9, 500, status=503, fault='503'), row(9, 500, fault='wrong_ack'),
                   row(8, 12), row(9, 500), row(9, 500), row(9, 251), {'kind': 'graph', 'status': 200}]
        self.assertEqual([len(r['body']['statuses']) for r in sim.successful_status_batches(records, 9)],
                         [500, 500, 251])

    def test_independent_checks_continue_and_preserve_failures(self):
        with tempfile.TemporaryDirectory(prefix='relay-sim-') as directory:
            runner = sim.Runner(sim.arguments(['--dry-run', '--report-dir', directory]))
            with runner.step('8', continue_on_error=True):
                raise RuntimeError('Core projection unavailable')
            with runner.step('11', continue_on_error=True):
                pass
            with runner.step('8', continue_on_error=True):
                pass
            with runner.step('8', continue_on_error=True):
                raise RuntimeError('Duplicate inbound')
            self.assertEqual(runner.report['steps']['8']['status'], 'fail')
            self.assertEqual(runner.report['steps']['8']['errors'],
                             ['Core projection unavailable', 'Duplicate inbound'])
            self.assertEqual(runner.report['steps']['11']['status'], 'pass')
            runner.finish()

    def test_core_observation_requires_unique_rows_and_completed_projection(self):
        from whatsapp_sim_site import expected_rows
        data = {'messages': [{'provider_id': 'first', 'status': 'Read'}], 'templates': [],
                'events': [{'hub_event_id': '9', 'projection_status': 'Pending'}]}
        self.assertTrue(expected_rows('message', 'first', data, [])[0])
        data['messages'] *= 2
        self.assertFalse(expected_rows('message', 'first', data, [])[0])
        self.assertFalse(expected_rows('event', 9, data, [])[0])
        data['events'][0]['projection_status'] = 'Projected'
        self.assertTrue(expected_rows('event', 9, data, [])[0])
        providers = [str(i) for i in range(417)]
        data['messages'] = [{'provider_id': p, 'status': 'Read'} for p in providers]
        self.assertTrue(expected_rows('read', '', data, providers)[0])
        data['messages'][-1] = dict(data['messages'][0])
        self.assertFalse(expected_rows('read', '', data, providers)[0])
        data['messages'][-1] = {'provider_id': providers[-1], 'status': 'Read'}
        data['messages'][-1]['status'] = 'Delivered'
        self.assertFalse(expected_rows('read', '', data, providers)[0])
        data['templates'] = [{'template_id': '99', 'status': 'PENDING'}]
        self.assertFalse(expected_rows('template', '99', data, [])[0])
        data['templates'][0]['status'] = 'APPROVED'
        self.assertTrue(expected_rows('template', '99', data, [])[0])

    def test_percentiles_and_empty_samples(self):
        self.assertIsNone(sim.percentile([], 95))
        self.assertEqual(sim.percentile(list(range(1, 101)), 95), 95)
        self.assertEqual(sim.percentile([10], 50), 10)

    def test_fixture_reruns_preserve_conversation_contact(self):
        from whatsapp_sim_site import fixture_contact
        contact = SimpleNamespace(name='contact-1', contact='person-1',
                                  display_name='Sim Customer R0425-0051', automation_paused=1)
        person = SimpleNamespace(name='person-1', links=[SimpleNamespace(link_doctype='Customer', link_name='customer-1')])
        customer = SimpleNamespace(name='customer-1', customer_name='Sim Customer R0425-0051')
        binding = SimpleNamespace(relay_contact='contact-2', verified=1, save=Mock())
        docs = {('Relay Contact Endpoint', 'endpoint'): binding, ('Relay Contact', 'contact-1'): contact,
                ('Contact', 'person-1'): person, ('Customer', 'customer-1'): customer}
        def get_value(doctype, filters, field):
            return {'Relay Contact Endpoint': 'endpoint', 'Relay Contact': 'Sim Customer R0425-0051',
                    'Relay Business Routing': 'contact-1'}[doctype]
        frappe = Mock()
        frappe.db.get_value.side_effect = get_value
        frappe.get_doc.side_effect = lambda *args: docs[args]
        insert = Mock(side_effect=AssertionError('Existing conversation must be reused'))
        for run in ('R0425-0051', 'R0425-0051'):
            self.assertEqual(fixture_contact(frappe, insert, SimpleNamespace(name='channel'), run),
                             (contact, person, customer))
        self.assertEqual(binding.relay_contact, 'contact-1')
        binding.save.assert_called_once_with(ignore_permissions=True)
        contact.display_name = 'Non-simulation contact'
        with self.assertRaisesRegex(AssertionError, 'paused simulation fixture'):
            fixture_contact(frappe, insert, SimpleNamespace(name='channel'), 'R0425-0051')

    def test_ai_assertions_reject_cross_supplier_data_model_arithmetic_and_duplicate_handoffs(self):
        good = 'SIM-OWN-DEBIT SIM-OWN-CREDIT Debit: 100.10 Credit: 40.05 Net debit: 60.05'
        sim.assert_ledger_reply(good)
        for bad in (good + ' SIM-OTHER-SUPPLIER-SECRET', good + ' MODEL INVENTED TOTAL 999999',
                    good.replace('60.05', '60.06'), good + ' SIM-OUTSIDE-RANGE', good + ' SIM-CANCELLED'):
            with self.assertRaises(AssertionError): sim.assert_ledger_reply(bad)
        fixture = dict(supplier='SUP-A', handoff_owner='owner')
        ticket = dict(assigned_to='owner', business_doctype='Supplier', business_name='SUP-A', source_run='RUN-1')
        state = dict(paused=1, tickets=[ticket], runs=[dict(name='RUN-1', status='Handoff')], actions=[], outgoing=[])
        sim.assert_handoff(state, fixture)
        for change in (dict(paused=0), dict(actions=[{}]), dict(runs=[dict(name='RUN-1', status='Completed')]), dict(tickets=[ticket, ticket]),
                       dict(tickets=[dict(ticket, business_name='SUP-B')]),
                       dict(tickets=[dict(ticket, assigned_to='wrong')])):
            with self.assertRaises(AssertionError): sim.assert_handoff(dict(state, **change), fixture)

    def test_ai_inbound_uses_real_sender_and_preserves_confirmation_text(self):
        value = sim.inbound('wamid.ai', '919800000001', 'yes')['entry'][0]['changes'][0]['value']
        self.assertEqual(value['messages'][0]['from'], '919800000001')
        self.assertEqual(value['messages'][0]['text']['body'], 'yes')
        self.assertIn('13', sim.STEPS)
        self.assertIn('14', sim.STEPS)
        from whatsapp_sim_acceptance import fixture_origin
        self.assertEqual(fixture_origin('http://127.0.0.1:18094'), 'http://127.0.0.1:18094')
        for value in ('https://openrouter.ai', 'http://localhost:18094', 'http://127.0.0.1:18094/ai'):
            with self.assertRaises(ValueError): fixture_origin(value)

    def test_dry_run_never_starts_processes_or_network_or_reads_secrets(self):
        with tempfile.TemporaryDirectory(prefix='relay-sim-') as directory:
            with patch.object(sim.subprocess, 'Popen', side_effect=AssertionError('process started')), \
                 patch.object(sim, 'request', side_effect=AssertionError('HTTP called')), \
                 patch.object(sim, 'read_env', side_effect=AssertionError('credentials read')):
                self.assertEqual(sim.main(['--dry-run', '--report-dir', directory]), 0)
            report = json.loads((Path(directory) / 'report.json').read_text())
            self.assertFalse(report['passed'])
            self.assertEqual(report['mode'], 'dry-run')
            self.assertTrue(all(s['status'] == 'not_run' for s in report['steps'].values()))
            self.assertTrue((Path(directory) / 'report.md').exists())
            self.assertEqual(report['plan']['expected_batches'], [500, 500, 251])

    def test_failed_step_is_not_reported_as_pass(self):
        with tempfile.TemporaryDirectory(prefix='relay-sim-') as directory:
            runner = sim.Runner(sim.arguments(['--dry-run', '--report-dir', directory]))
            with self.assertRaisesRegex(AssertionError, 'missing'):
                with runner.step('8'):
                    raise AssertionError('missing Core projection')
            runner.finish()
            report = json.loads((Path(directory) / 'report.json').read_text())
            self.assertFalse(report['passed'])
            self.assertEqual(report['steps']['8']['status'], 'fail')
            self.assertEqual(report['steps']['9']['status'], 'not_run')

    def test_dry_run_failure_returns_nonzero(self):
        with tempfile.TemporaryDirectory(prefix='relay-sim-') as directory:
            with patch.object(sim.Runner, 'execute', side_effect=RuntimeError('failed plan')):
                self.assertEqual(sim.main(['--dry-run', '--report-dir', directory]), 1)

    def test_restart_resumes_without_migrations(self):
        with tempfile.TemporaryDirectory(prefix='relay-sim-') as directory:
            runner = sim.Runner(sim.arguments(['--dry-run', '--report-dir', directory]))
            runner.binary, runner.db_env = Path('/tmp/relay-sim-test'), {}
            runner.state_dir = Path(directory) / 'provider-state'
            with patch.object(sim.subprocess, 'Popen') as popen, patch('simulation_common.request', return_value={'ready': True}):
                popen.return_value.poll.return_value = None
                runner.start(resume=True)
                command = popen.call_args.args[0]
                self.assertIn('--resume', command)
                self.assertNotIn('--migrate', command)
            runner.process = None
            runner.finish()

    def test_socketio_namespace_disconnect_fails_observation(self):
        client = sim.Realtime.__new__(sim.Realtime)
        client.ns, client.closed, client.error = '/yrp.site', False, None
        client.events, client.condition = [], threading.Condition()
        client.ws = Mock()
        client.ws.recv.return_value = '41/yrp.site,'
        client.listen()
        with self.assertRaisesRegex(RuntimeError, 'namespace disconnected'):
            client.wait('messages', 0)

    def test_http_redirects_never_follow(self):
        class Redirect(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(302)
                self.send_header('Location', 'http://192.0.2.1:80/secret')
                self.end_headers()
            def log_message(self, *args):
                pass
        server = ThreadingHTTPServer(('127.0.0.1', 0), Redirect)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with self.assertRaisesRegex(ValueError, 'redirects'):
                sim.request(f'http://127.0.0.1:{server.server_port}/')
        finally:
            server.shutdown()
            server.server_close()
            thread.join()

    def test_bench_helper_confines_filesystem_writes(self):
        # Audit hooks cannot be removed; test in a subprocess.
        command = """
import sys
from whatsapp_sim_site import confine_writes
sys.addaudithook(confine_writes)
try:
    open('/home/relay-sim-forbidden-write', 'w')
except PermissionError:
    print('blocked')
else:
    raise AssertionError('unsafe write allowed')
import socket
try:
    socket.socket().connect(('192.0.2.1', 80))
except PermissionError:
    print('network blocked')
else:
    raise AssertionError('remote connection allowed')
"""
        result = subprocess.run([sys.executable, '-B', '-c', command], cwd=Path(__file__).parent,
                                capture_output=True, text=True, check=True)
        self.assertEqual(result.stdout.strip(), 'blocked\nnetwork blocked')

    @unittest.skipUnless(importlib.util.find_spec('websockets'), 'requires existing bench websockets package')
    def test_socketio_uses_websocket_namespace_auth_and_event_not_polling(self):
        from websockets.sync.server import serve
        packets = []
        def handler(ws):
            self.assertIn('transport=websocket', ws.request.path)
            self.assertEqual(ws.request.headers['Authorization'], 'token fake-key:fake-secret')
            self.assertEqual(ws.request.headers['Origin'], 'http://yrp.site:8001')
            ws.send('0' + json.dumps({'sid': 'test', 'pingInterval': 25000, 'pingTimeout': 20000}))
            self.assertEqual(ws.recv(), '40/yrp.site,')
            ws.send('2')
            self.assertEqual(ws.recv(), '3')
            ws.send('40/yrp.site,{"sid":"test"}')
            packets.extend([ws.recv(), ws.recv()])
            time.sleep(.25)
            ws.send('2')
            self.assertEqual(ws.recv(), '3')
            ws.send('42/yrp.site,["relay_changed",{"kind":"messages"}]')
            try:
                ws.recv()
            except Exception:
                pass
        server = serve(handler, '127.0.0.1', 0)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        client = None
        try:
            before = time.monotonic_ns()
            connect = sim.socket.create_connection
            def short_timeout(*args, **kwargs):
                sock = connect(*args, **kwargs)
                sock.settimeout(.1)
                return sock
            with patch.object(sim.socket, 'create_connection', side_effect=short_timeout):
                client = sim.Realtime(f'http://127.0.0.1:{server.socket.getsockname()[1]}', 'http://yrp.site:8001',
                                      'yrp.site', {'api_key': 'fake-key', 'api_secret': 'fake-secret'}, 'test-contact')
            event = client.wait('messages', before, timeout=2)
            self.assertEqual(event['event'], ['relay_changed', {'kind': 'messages'}])
            self.assertIn('doc_subscribe', packets[0])
            self.assertIn('test-contact', packets[0])
            self.assertIn('Relay WhatsApp Template', packets[1])
        finally:
            if client:
                client.close()
            server.shutdown()
            thread.join(3)

    @unittest.skipUnless(importlib.util.find_spec('websockets'), 'requires existing bench websockets package')
    def test_socketio_failed_authentication_closes_connection(self):
        from websockets.sync.server import serve
        closed = threading.Event()
        def handler(ws):
            ws.send('0{}')
            ws.recv()
            ws.send('44/yrp.site,{"message":"Unauthorized"}')
            try:
                ws.recv(timeout=3)
            except Exception:
                closed.set()
        server = serve(handler, '127.0.0.1', 0)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with self.assertRaisesRegex(AssertionError, 'authentication failed'):
                sim.Realtime(f'http://127.0.0.1:{server.socket.getsockname()[1]}', 'http://yrp.site:8001',
                             'yrp.site', {'api_key': 'key', 'api_secret': 'secret'}, 'test-contact')
            self.assertTrue(closed.wait(2))
        finally:
            server.shutdown()
            thread.join(3)


if __name__ == '__main__':
    unittest.main()

class AcceptanceIsolationTests(unittest.TestCase):
    def test_every_medium_copies_installed_sample(self):
        from whatsapp_sim_acceptance import Acceptance
        from copy import copy
        class StopAfterCopy(Exception):
            pass
        for medium in ['WhatsApp', 'Telegram', 'Email']:
            sample = SimpleNamespace(name='installed', sample_key='supplier_ledger', title='Installed',
                channel='', enabled=0, allowed_tools=json.dumps(['ledger']))
            copies = []
            def copy_doc(doc):
                result = copy(doc)
                def insert(**kwargs):
                    copies.append(result)
                    raise StopAfterCopy()
                result.insert = insert
                return result
            conf = SimpleNamespace(developer_mode=True)
            fake = SimpleNamespace(conf=conf, local=SimpleNamespace(site='yrp.site'),
                db=SimpleNamespace(has_column=lambda *args: True), get_doc=lambda *args: sample, copy_doc=copy_doc)
            installed = SimpleNamespace(install_samples=lambda: None, LEDGER_TOOL='ledger')
            acceptance = Acceptance(fake, {'channel': 'local-fixture'}, None)
            with patch.object(acceptance, 'preflight'), patch.dict(sys.modules, {'relay_core.ai.samples': installed}):
                with self.assertRaises(StopAfterCopy):
                    acceptance.seed_ai('unique-run', 'http://127.0.0.1:18094', medium)
            self.assertEqual(sample.channel, '')
            self.assertEqual(sample.sample_key, 'supplier_ledger')
            self.assertEqual(copies[0].channel, 'local-fixture')
            self.assertIsNone(copies[0].sample_key)
            self.assertIn(medium, copies[0].title)
            self.assertEqual(copies[0].enabled, 0)
