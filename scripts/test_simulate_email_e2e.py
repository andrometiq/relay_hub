"""MIME and connected-run safety contracts for the email driver."""
from email import policy
from email.parser import Parser
import unittest
from unittest.mock import Mock

from channel_simulation import arguments
from simulate_email_e2e import EmailRunner, mail, parsed_sent


class EmailDriverTests(unittest.TestCase):
    def test_reply_preserves_ancestry_and_cc(self):
        raw=mail('sender@example.invalid','<reply@example.invalid>','Local simulation total 250.00',
            parent='<parent@example.invalid>',references=['<root@example.invalid>','<parent@example.invalid>'],cc='cc@example.invalid')
        parsed=Parser(policy=policy.default).parsestr(raw)
        self.assertEqual(str(parsed['In-Reply-To']),'<parent@example.invalid>')
        self.assertEqual(str(parsed['References']),'<root@example.invalid> <parent@example.invalid>')
        self.assertEqual(str(parsed['Cc']),'cc@example.invalid')
        self.assertEqual(str(parsed['To']),'inbox@sim.invalid')
        self.assertIn('\r\n',raw)

    def test_independent_threads_do_not_invent_ancestry(self):
        raw=mail('sender@example.invalid','<new@example.invalid>','Independent')
        parsed=Parser(policy=policy.default).parsestr(raw)
        self.assertIsNone(parsed['In-Reply-To']);self.assertIsNone(parsed['References'])

    def test_capture_matches_decoded_mime_body(self):
        body='Local simulation café invoice ₹250'
        row={'raw':mail('sender@example.invalid','<id@example.invalid>',body),'recipients':['one@example.invalid','cc@example.invalid']}
        matched=parsed_sent({'sent':[row]},body)
        self.assertEqual(len(matched),1)
        self.assertEqual(matched[0][0]['recipients'],row['recipients'])
        self.assertEqual(parsed_sent({'sent':[row]},'other'),[])
        self.assertEqual(parsed_sent({'sent':None},body),[])

    def test_injected_header_is_refused(self):
        with self.assertRaises(ValueError):mail('sender@example.invalid\r\nBcc: victim@example.invalid','<id@example.invalid>','body')

    def test_utf8_8bit_provider_body_matches_core_without_replacement(self):
        body = 'Supplier ledger — 2 returned rows.\nதமிழ் · debit ₹100.10; credit 40.05'
        raw = ('To: ledger@example.invalid\nMessage-ID: <reply@example.invalid>\n'
               'In-Reply-To: <ledger@example.invalid>\nAuto-Submitted: auto-replied\n'
               'Content-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: 8bit\n\n' + body)
        state = {'sent': [{'raw': raw}]}
        self.assertEqual(len(parsed_sent(state, body)), 1)
        runner = object.__new__(EmailRunner)
        runner.medium = 'Email'
        runner.providers = Mock(return_value=state)
        self.assertEqual(runner.accepted_replies('ledger@example.invalid'), [dict(
            body=body, provider='<reply@example.invalid>', parent='<ledger@example.invalid>',
            automatic='auto-replied')])

    def test_webhook_ingress_failure_is_not_treated_as_acceptance(self):
        runner=object.__new__(EmailRunner);runner.run_id='R0425-0051';runner.control=Mock(return_value={'status':400})
        with self.assertRaises(AssertionError):runner.emit('raw',webhook=True)
        runner.emit('raw',webhook=True,invalid_signature=True)

    def test_imap_injection_does_not_require_fake_webhook_ack(self):
        runner=object.__new__(EmailRunner);runner.run_id='R0425-0051';runner.control=Mock(return_value={'uid':42})
        self.assertEqual(runner.emit('raw'),{'uid':42})
        self.assertFalse(runner.control.call_args.args[1]['webhook'])

    def test_reject_unapproved_site_or_report_destination(self):
        for args in (['--site','production'],['--sites','/tmp/sites'],['--report-dir','/tmp/reports']):
            with self.subTest(args=args),self.assertRaises(SystemExit):arguments('Email',args)


if __name__=='__main__':unittest.main()
