"""Connected AI and source-document acceptance shared by Telegram and email."""
from simulation_fixtures import run_number
import json
import time
from email import policy
from email.parser import BytesParser
from simulate_whatsapp_e2e import assert_handoff, assert_ledger_reply
from whatsapp_sim_acceptance import provider_identity


class ChannelAcceptance:
    def check_email_identity(self, message):
        if self.medium == 'Email' and message.get('provider_id') != provider_identity(message):
            self.report.setdefault('email_identity_defects', []).append(message['name'])

    def ai_payload(self, fixture, number, body, *, threaded=True):
        if self.medium == 'Telegram':
            from simulate_telegram_e2e import update
            identifier = run_number(self.run_id) * 2048 + 100 + number
            payload = update(fixture['endpoint'], identifier, body)
            return str(identifier), lambda: self.emit(payload)
        from simulate_email_e2e import mail
        identifier = f'<ledger-{self.run_id}-{number}@example.invalid>'
        root = f'<ledger-{self.run_id}-1@example.invalid>'
        payload = mail(fixture['endpoint'], identifier, body,
            parent=root if threaded and number > 1 else None,
            references=[root] if threaded and number > 1 else [])
        provider = 'ledger_'+self.run_id+'_'+str(number)
        return provider, lambda: self.emit(payload, webhook=True, identifier=provider)

    def accepted_replies(self, endpoint, state=None):
        if state is None:
            state = self.providers()
        if self.medium == 'Telegram':
            return [dict(body=m['request'].get('text'), provider=str(m['result']['message_id']))
                for m in state.get('messages') or [] if str(m['request'].get('chat_id')) == endpoint]
        replies = []
        for row in state.get('sent') or []:
            parsed = BytesParser(policy=policy.default).parsebytes(row['raw'].encode('utf-8'))
            if parsed['To'] == endpoint:
                replies.append(dict(body=parsed.get_body(preferencelist=('plain',)).get_content().strip(),
                    provider=str(parsed['Message-ID']), parent=str(parsed['In-Reply-To']),
                    automatic=str(parsed['Auto-Submitted'])))
        return replies

    def check_channel_ai(self, provider, fixture, expected):
        state = self.site.call('acceptance_wait_ai', provider=provider, status='Completed')
        assert len(state['incoming']) == len(state['runs']) == len(state['actions']) == len(state['outgoing']) == 1
        assert state['incoming'][0]['business_name'] == fixture['supplier']
        action, outgoing = state['actions'][0], state['outgoing'][0]
        assert action['status'] == 'Submitted'
        assert (outgoing['business_doctype'], outgoing['business_name']) == ('Supplier', fixture['supplier'])
        replies = self.accepted_replies(fixture['endpoint'])
        assert len(replies) == expected, f'Expected {expected} provider replies, got {len(replies)}'
        assert replies[-1]['body'] == outgoing['content'].strip(), (
            f"Expected provider body {outgoing['content'].strip()!r}, got {replies[-1]['body']!r}")
        assert replies[-1]['provider'] == provider_identity(outgoing)
        self.check_email_identity(outgoing)
        if self.medium == 'Email':
            assert replies[-1]['parent'] == f'<ledger-{self.run_id}-{provider.rsplit("_",1)[1]}@example.invalid>'
            assert replies[-1]['automatic'] == 'auto-replied'
        deliveries = [d for d in self.control('state')['channel_deliveries'] if d['request_key'] == action['request_key']]
        assert len(deliveries) == 1 and deliveries[0]['state'] == 'submitted'
        visible = self.core('relay_core.api.messages', dict(contact=fixture['contact'], channel=self.fixture['channel']))['items']
        assert sum(m['name'] == outgoing['name'] for m in visible) == 1
        return state

    def channel_ai_acceptance(self):
        fixture = self.site.call('acceptance_seed_ai', run=self.run_id, origin=self.meta, medium=self.medium)
        self.realtime.ws.send('42'+self.realtime.ns+','+json.dumps(['doc_subscribe','Relay Contact',fixture['contact']]))
        started = time.monotonic_ns()
        provider, emit = self.ai_payload(fixture, 1, 'I have a doubt in my ledger')
        emit()
        offered = self.check_channel_ai(provider, fixture, 1)
        self.realtime.wait('messages', started)
        offer = offered['outgoing'][0]['content']
        assert '2026-10-01' in offer and '2026-10-31' in offer and 'confirm' in offer.lower()
        assert json.loads(offered['runs'][0]['decision'])['reads'] == []
        provider, replay = self.ai_payload(fixture, 2, 'yes')
        replay()
        result = self.check_channel_ai(provider, fixture, 2)
        assert_ledger_reply(result['outgoing'][0]['content'])
        assert len(json.loads(result['runs'][0]['decision'])['reads']) == 1
        confirmed_message = result['incoming'][0]['name']
        replay()
        person, replay = self.ai_payload(fixture, 3, 'talk to a human')
        replay()
        self.site.call('acceptance_wait_ai', provider=person, status='Handoff')
        replay()
        paused, emit = self.ai_payload(fixture, 4, 'talk to a human')
        emit()
        self.site.call('acceptance_wait_paused', provider=paused)
        result = self.site.call('acceptance_ai_state', provider=person)
        assert_handoff(result, fixture)
        assert len(self.accepted_replies(fixture['endpoint'])) == 2, 'Replay or handoff sent another reply'
        calls = [r for r in self.control('state')['records'] if r['kind'] == 'ai']
        assert len(calls) == 3 and all(r['status'] == 200 for r in calls), 'Human request must bypass the model'
        assert all(marker not in json.dumps(calls) for marker in ('SIM-OTHER-SUPPLIER-SECRET','SIM-OUTSIDE-RANGE','SIM-CANCELLED'))
        identity_denied = self.assert_identity_mismatch(fixture, confirmed_message)
        return dict(supplier=fixture['supplier'], ticket=result['tickets'][0]['name'], replies=2, ai_calls=len(calls),
            mismatched_identity_denied=identity_denied)

    def channel_business_acceptance(self):
        self.site.call('acceptance_seed_event', run=self.run_id, medium=self.medium)
        endpoint = self.fixture['endpoint']
        before = len(self.accepted_replies(endpoint))
        started = time.monotonic_ns()
        submitted = self.site.call('acceptance_submit_event')
        state = self.site.call('acceptance_wait_event', source=submitted['source'])
        assert state['docstatus'] == 1 and len(state['deliveries']) == len(state['actions']) == len(state['messages']) == 1
        assert state['actions'][0]['status'] == 'Submitted'
        message = state['messages'][0]
        assert message['business_name'] == self.fixture['customer'] and message['relay_contact'] == self.fixture['contact']
        self.check_email_identity(message)
        replies = self.accepted_replies(endpoint)
        assert len(replies) == before + 1 and replies[-1]['provider'] == provider_identity(message)
        assert replies[-1]['body'] == 'Local simulation order ' + submitted['source']
        self.realtime.wait('messages', started)
        visible = self.core('relay_core.api.messages', dict(contact=self.fixture['contact'], channel=self.fixture['channel']))['items']
        assert sum(m['name'] == message['name'] for m in visible) == 1
        self.stop_hub()
        offline = self.site.call('acceptance_submit_event')
        retained = self.site.call('acceptance_event_state', source=offline['source'])
        assert retained['docstatus'] == 1 and len(retained['deliveries']) == 1
        return dict(source=submitted['source'], timeline_message=message['name'], offline_source=offline['source'])
