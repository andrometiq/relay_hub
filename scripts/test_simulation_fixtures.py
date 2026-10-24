"""Readable naming must preserve retained-run and endpoint isolation."""
from datetime import datetime, timedelta, timezone
from itertools import product
from types import SimpleNamespace
import unittest
from unittest.mock import Mock
from unittest.mock import patch

from simulation_fixtures import (SUFFIXES, available_run, fixture_channel,
                                 fixture_email, fixture_endpoint, run_label, run_number)


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


if __name__ == '__main__':
    unittest.main()
