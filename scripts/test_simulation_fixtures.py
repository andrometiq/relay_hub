"""Readable naming must preserve retained-run and endpoint isolation."""
import json
from datetime import datetime, timedelta, timezone
from itertools import product
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, call
from unittest.mock import patch

from simulation_fixtures import (SUFFIXES, available_run, fixture_channel,
                                 fixture_email, fixture_endpoint, run_label, run_number)
from whatsapp_sim_acceptance import configure_ai_channel
from simulation_common import SimulationRunner


class FixtureNamingTests(unittest.TestCase):
    def test_utc_minute_and_collision_suffixes(self):
        now = datetime(2026, 4, 25, 6, 21, tzinfo=timezone(timedelta(hours=5, minutes=30)))
        self.assertEqual(run_label(now=now), 'R0425-0051')
        used = {'R0425-0051', 'R0425-0051-00'}
        self.assertEqual(run_label(used.__contains__, now), 'R0425-0051-01')
        with self.assertRaisesRegex(RuntimeError, 'exhausted'):
            run_label(lambda label: True, now)

    def test_collision_check_uses_retained_core_user(self):
        frappe = Mock()
        frappe.db.exists.side_effect = [True, False]
        label = available_run(frappe)
        self.assertTrue(label.endswith('-00'))
        self.assertEqual(frappe.db.exists.call_args.args,
                         ('User', fixture_email('service', label)))

    def test_failed_run_provider_state_also_reserves_label(self):
        frappe = Mock()
        frappe.db.exists.return_value = False
        now = datetime(2026, 4, 25, 0, 51, tzinfo=timezone.utc)
        with patch('simulation_fixtures.run_label', side_effect=lambda exists: run_label(exists, now)):
            self.assertEqual(available_run(frappe, ['R0425-0051']), 'R0425-0051-00')

    def test_numeric_endpoints_are_short_and_disjoint_across_runs_and_roles(self):
        runs = ['R0425-0051', 'R0425-0052', 'R1231-2359']
        runs += ['R0425-0051-' + ''.join(pair) for pair in product(SUFFIXES, repeat=2)]
        for medium in ('WhatsApp', 'Telegram', 'Email'):
            seen = set()
            for run in runs:
                self.assertLess(run_number(run) + 1, 2**32)  # IMAP UIDVALIDITY
                for role in ('customer', 'shared', 'supplier', 'fault'):
                    endpoint = fixture_endpoint(medium, run, role)
                    self.assertNotIn(endpoint, seen)
                    seen.add(endpoint)
                    if medium == 'WhatsApp':
                        self.assertRegex(endpoint, r'^91[6-9][0-9]{9}$')
                    elif medium == 'Telegram':
                        self.assertRegex(endpoint, r'^[1-9][0-9]{9}$')
                    else:
                        self.assertEqual(endpoint, f'sim-{role}-{run.lower()}@example.invalid')

    def test_channels_get_titles_without_renaming_existing_records(self):
        frappe, insert = Mock(), Mock()
        frappe.get_all.return_value = []
        fixture_channel(frappe, insert, 'WhatsApp', 'R0425-0051')
        self.assertEqual(insert.call_args.kwargs['title'], 'Sim WhatsApp R0425-0051')
        existing = SimpleNamespace(name='legacy-channel', enabled=1, save=Mock())
        frappe.get_all.return_value = [existing.name]
        frappe.get_doc.return_value = existing
        self.assertIs(fixture_channel(frappe, insert, 'WhatsApp', 'R0425-0051-00'), existing)
        self.assertEqual(existing.name, 'legacy-channel')
        self.assertEqual(existing.title, 'Sim WhatsApp R0425-0051-00')


class AIChannelFixtureTests(unittest.TestCase):
    def test_uses_existing_channel_company_or_deterministic_payable_company(self):
        for company in ('Sim Ledger Company', None):
            with self.subTest(company=company):
                frappe = Mock()
                channel = SimpleNamespace(company=company, save=Mock())
                frappe.get_doc.return_value = channel
                frappe.get_all.return_value = [SimpleNamespace(name='Sim Payable', company='Sim Ledger Company')]
                self.assertEqual(configure_ai_channel(frappe, 'sim-channel', 'sim-owner'),
                    ('Sim Ledger Company', 'Sim Payable'))
                query = frappe.get_all.call_args.kwargs
                self.assertEqual(query['filters'].get('company'), company)
                self.assertEqual(query['filters']['account_type'], 'Payable')
                self.assertEqual(query['filters']['is_group'], 0)
                self.assertEqual(query['filters']['disabled'], 0)
                self.assertEqual(query['order_by'], 'company asc, name asc')
                self.assertEqual(channel.company, 'Sim Ledger Company')
                self.assertEqual(channel.triage_owner, 'sim-owner')
                channel.save.assert_called_once_with(ignore_permissions=True)

    def test_does_not_replace_configured_company_without_payable_account(self):
        frappe = Mock()
        channel = SimpleNamespace(company='Sim Empty Company', save=Mock())
        frappe.get_doc.return_value = channel
        frappe.get_all.return_value = []
        with self.assertRaisesRegex(AssertionError, 'payable account'):
            configure_ai_channel(frappe, 'sim-channel', 'sim-owner')
        self.assertEqual(frappe.get_all.call_args.kwargs['filters']['company'], 'Sim Empty Company')
        channel.save.assert_not_called()


class AIIdentityAcceptanceTests(unittest.TestCase):
    def runner(self, tool_result):
        runner = SimulationRunner()
        runner.site = Mock()
        runner.site.call.return_value = {'handoff': True, 'reply': '', 'reads': []}
        runner.control = Mock(side_effect=[{'records': []}, {'records': [
            {'kind': 'ai', 'status': 200, 'response': {'choices': [{'message': {'tool_calls': [
                {'function': {'name': 'SEARCH', 'arguments': json.dumps({'reference': 'supplier_ledger'})}}]}}]}},
            {'kind': 'ai', 'status': 200, 'body': {'messages': [
                {'role': 'tool', 'content': json.dumps(tool_result)}]}}]}])
        return runner

    def test_rejected_read_reaches_model_without_ledger_data(self):
        runner = self.runner({'error': 'Business data is unavailable'})
        self.assertTrue(runner.assert_identity_mismatch({'skill': 'sim-skill'}, 'sim-message'))
        self.assertEqual(runner.site.call.call_args_list, [
            call('acceptance_set_ai_identity', matched=False),
            call('acceptance_check_identity', message='sim-message'),
            call('acceptance_set_ai_identity', matched=True)])

    def test_rejects_tool_data_even_if_api_reports_handoff(self):
        runner = self.runner({'rows': [{'remarks': 'SIM-OWN-DEBIT'}]})
        with self.assertRaisesRegex(AssertionError, 'exposed a ledger tool result'):
            runner.assert_identity_mismatch({'skill': 'sim-skill'}, 'sim-message')

    def test_restores_identity_when_confirmed_search_check_fails(self):
        runner = self.runner({})
        runner.site.call.side_effect = [{}, RuntimeError('Search check unavailable'), {}]
        with self.assertRaisesRegex(RuntimeError, 'Search check unavailable'):
            runner.assert_identity_mismatch({'skill': 'sim-skill'}, 'sim-message')
        runner.site.call.assert_called_with('acceptance_set_ai_identity', matched=True)


if __name__ == '__main__':
    unittest.main()
