"""Local-only fixtures for connected AI and business-event acceptance.

GL rows are identifiable read-contract fixtures, not posted accounting vouchers.
The actual worker reads them through permission-checked GL Entry get_list/get_doc.
Sales Orders use the normal insert/submit lifecycle.
"""
import hashlib
import json
import time


def fixture_origin(value):
    from simulate_whatsapp_e2e import loopback_origin
    return loopback_origin(value)


class Acceptance:
    def __init__(self, frappe, fixture, insert):
        self.f, self.fixture, self.insert = frappe, fixture, insert
        self.ai = {}
        self.event = {}

    def preflight(self):
        assert self.f.conf.get('relay_automation_enabled'), (
            'Step 13 requires relay_automation_enabled=1 on yrp.site; '
            'site configuration activation needs separate approval. '
            'See docs/simulation-validation.md.')

    def seed_ai(self, run, origin):
        self.preflight()
        f, fixture = self.f, self.fixture
        origin = fixture_origin(origin)
        assert f.conf.developer_mode and f.local.site == 'yrp.site'
        assert f.db.has_column('Relay AI Skill', 'sample_key'), 'Reload Relay AI Skill before the connected run'
        from relay_core.ai.samples import install_samples, LEDGER_TOOL
        install_samples()
        skill = f.get_doc('Relay AI Skill', {'sample_key': 'supplier_ledger'})
        assert json.loads(skill.allowed_tools) == [LEDGER_TOOL], 'Sample read tools were customized; review before simulation'
        assert not skill.channel or skill.channel == fixture['channel'], 'Sample belongs to another channel'
        assert not f.get_all('Relay AI Skill', filters={'channel': fixture['channel'], 'business_doctype': 'Supplier',
            'enabled': 1, 'name': ['!=', skill.name]}, limit_page_length=1), 'Another supplier skill owns this channel'
        group = f.db.get_value('Supplier Group', {'is_group': 0}, 'name')
        assert group, 'Supplier Group fixture is required'
        suppliers = [self.insert('Supplier', supplier_name=f'Local Relay simulation {label} {run}',
            supplier_type='Company', supplier_group=group) for label in ('supplier', 'excluded supplier')]
        endpoint = '9198' + str(int(hashlib.sha256(run.encode()).hexdigest()[:12], 16) % 100000000).zfill(8)
        assert not f.db.exists('Relay Contact Endpoint', {'channel': fixture['channel'], 'endpoint': endpoint}), 'Fixture endpoint collision'
        kind = f.db.get_value('Relay Contact Type', {'business_doctype': 'Supplier'}, 'name')
        if not kind:
            self.insert('Relay Contact Type', business_doctype='Supplier', enabled=1, label='Local simulation suppliers')
        else:
            assert f.db.get_value('Relay Contact Type', kind, 'enabled'), 'Supplier contact type is disabled'
        person = self.insert('Contact', first_name=f'Local Relay simulation supplier {run}',
            phone_nos=[dict(phone=endpoint, is_primary_mobile_no=1)],
            links=[dict(link_doctype='Supplier', link_name=suppliers[0].name)])
        contact = self.insert('Relay Contact', contact=person.name, display_name=f'Local simulation supplier {run}', automation_paused=0)
        self.insert('Relay Contact Endpoint', channel=fixture['channel'], endpoint=endpoint, relay_contact=contact.name, verified=1)
        company = f.db.get_value('Company', {}, 'name')
        account = f.db.get_value('Account', {'company': company, 'is_group': 0, 'account_type': 'Payable'}, 'name')
        assert company and account, 'A company and payable account are required for GL fixtures'
        entries = []
        for index, (party, debit, credit, date, marker, cancelled) in enumerate([
            (suppliers[0].name, 1250, 0, '2026-10-02', 'SIM-CANCELLED', 1),
            (suppliers[0].name, 100.10, 0, '2026-10-03', 'SIM-OWN-DEBIT', 0),
            (suppliers[0].name, 0, 40.05, '2026-10-04', 'SIM-OWN-CREDIT', 0),
            (suppliers[1].name, 88000, 0, '2026-10-03', 'SIM-OTHER-SUPPLIER-SECRET', 0),
            (suppliers[0].name, 77000, 0, '2026-09-03', 'SIM-OUTSIDE-RANGE', 0)]):
            doc = f.get_doc(dict(doctype='GL Entry', name=f'RELAY-SIM-GL-{run}-{index}',
                company=company, account=account, posting_date=date, party_type='Supplier', party=party,
                voucher_type='Journal Entry', voucher_no=f'RELAY-SIM-VOUCHER-{run}-{index}',
                debit=debit, credit=credit, remarks=marker, docstatus=1, is_cancelled=cancelled))
            doc.db_insert()
            entries.append(doc.name)
        provider = self.insert('AI Provider Account', account_name=f'Local Relay simulation AI {run}',
            provider='OpenRouter', auth_type='API Key', enabled=1, api_key='relay-sim-ai-only', base_url=origin + '/ai')
        model = self.insert('AI Model', model_label=f'Local Relay simulation ledger {run}', enabled=1,
            account=provider.name, model_id='relay-sim-ledger', temperature=0, max_tokens=1200)
        skill.update(dict(enabled=1, mode='Automatic', channel=fixture['channel'], ai_model=model.name,
            run_as_user=fixture['operator']['name'], handoff_owner=fixture['operator']['name']))
        skill.save(ignore_permissions=True)
        f.db.commit()
        self.ai = dict(supplier=suppliers[0].name, excluded_supplier=suppliers[1].name, contact=contact.name,
            endpoint=endpoint, skill=skill.name, handoff_owner=skill.handoff_owner, entries=entries)
        return self.ai

    def ai_state(self, provider):
        f = self.f
        f.db.rollback()
        incoming = f.get_all('Relay Message', filters={'relay_contact': self.ai['contact'], 'provider_id': provider},
            fields=['name', 'business_doctype', 'business_name'])
        runs = f.get_all('Relay Automation Run', filters={'message': ['in', [row.name for row in incoming]]},
            fields=['name', 'status', 'decision', 'ticket']) if incoming else []
        actions = f.get_all('Relay Message Action', filters={'source_message': ['in', [row.name for row in incoming]]},
            fields=['name', 'status', 'request_key', 'outbound_message']) if incoming else []
        outgoing = [f.get_doc('Relay Message', row.outbound_message).as_dict() for row in actions if row.outbound_message]
        tickets = f.get_all('Relay Ticket', filters={'business_doctype': 'Supplier', 'business_name': self.ai['supplier']},
            fields=['name', 'assigned_to', 'business_doctype', 'business_name', 'source_run'])
        return dict(incoming=incoming, runs=runs, actions=actions, outgoing=outgoing, tickets=tickets,
            paused=f.db.get_value('Relay Contact', self.ai['contact'], 'automation_paused'))

    def wait_ai(self, provider, status, receipt=None):
        deadline = time.monotonic() + 35
        while time.monotonic() < deadline:
            state = self.ai_state(provider)
            if len(state['runs']) == 1 and state['runs'][0]['status'] == status:
                if status == 'Handoff' or (len(state['outgoing']) == 1 and state['outgoing'][0]['provider_id']
                        and (not receipt or state['outgoing'][0]['status'] == receipt)):
                    return state
            time.sleep(.05)
        raise AssertionError('AI worker did not reach expected run/history state')

    def wait_paused(self, provider):
        deadline = time.monotonic() + 35
        while time.monotonic() < deadline:
            state = self.ai_state(provider)
            if state['incoming']:
                # Projection inserts the message and creates any automation run
                # in the same transaction. A committed row is the barrier, not
                # the Hub callback acknowledgement or provider request count.
                assert len(state['incoming']) == 1, 'Duplicate paused message'
                assert state['paused'], 'Conversation is no longer paused'
                assert not state['runs'], 'Paused message created an automation run'
                assert not state['actions'] and not state['outgoing'], 'Paused message created an action'
                return state
            time.sleep(.05)
        raise AssertionError('Paused message was not projected')

    def seed_event(self, run, template_id):
        f, fixture = self.f, self.fixture
        assert f.conf.developer_mode and f.local.site == 'yrp.site'
        template = f.db.get_value('Relay WhatsApp Template', {'account': 'sim-v1', 'template_id': template_id, 'status': 'APPROVED'}, 'name')
        assert template, 'Step 12 approved template is required'
        assert not f.get_all('Relay Business Event Rule', filters={'source_doctype': 'Sales Order', 'enabled': 1}, limit_page_length=1), 'Disable other Sales Order rules before simulation'
        item_group = f.db.get_value('Item Group', {'is_group': 0}, 'name')
        uom = f.db.get_value('UOM', {'enabled': 1}, 'name')
        company = f.db.get_value('Company', {}, 'name')
        assert item_group and uom and company, 'Company, Item Group and UOM fixtures are required'
        item = self.insert('Item', item_code=f'RELAY-SIM-SERVICE-{run}', item_name='Local Relay simulation service',
            item_group=item_group, stock_uom=uom, is_stock_item=0, is_sales_item=1)
        rule = self.insert('Relay Business Event Rule', title=f'Local Relay simulation order {run}', enabled=1,
            source_doctype='Sales Order', event='on_submit', recipient_field='customer', business_doctype='Customer',
            channel=fixture['channel'], run_as_user=fixture['operator']['name'], template=template,
            parameter_mapping=json.dumps(['name']))
        self.event = dict(rule=rule.name, item=item.name, company=company, uom=uom)
        f.db.commit()
        return self.event

    def submit_event(self):
        f, fixture = self.f, self.fixture
        doc = f.get_doc(dict(doctype='Sales Order', customer=fixture['customer'], company=self.event['company'],
            transaction_date=f.utils.today(), delivery_date=f.utils.add_days(f.utils.today(), 7),
            items=[dict(item_code=self.event['item'], qty=2, rate=125, uom=self.event['uom'])]))
        doc.insert(ignore_permissions=True)
        doc.submit()
        # Replay the lifecycle observer to prove its durable identity is idempotent.
        from relay_core.business_events import on_event
        on_event(doc, 'on_submit')
        f.db.commit()
        assert f.db.get_value('Sales Order', doc.name, 'docstatus') == 1
        return dict(source=doc.name, docstatus=1, total_qty=doc.total_qty)

    def event_state(self, source):
        f = self.f
        f.db.rollback()
        rows = f.get_all('Relay Business Event Delivery', filters={'source_doctype': 'Sales Order',
            'source_name': source, 'rule': self.event['rule']}, fields=['name', 'status', 'action', 'business_name', 'payload'])
        assert len(rows) <= 1, 'Duplicate business event delivery'
        actions = [f.get_doc('Relay Message Action', row.action).as_dict() for row in rows if row.action]
        messages = [f.get_doc('Relay Message', row['outbound_message']).as_dict() for row in actions if row['outbound_message']]
        return dict(deliveries=rows, actions=actions, messages=messages, docstatus=f.db.get_value('Sales Order', source, 'docstatus'))

    def wait_event(self, source):
        deadline = time.monotonic() + 35
        while time.monotonic() < deadline:
            state = self.event_state(source)
            if len(state['messages']) == 1 and state['messages'][0]['provider_id']:
                return state
            time.sleep(.05)
        raise AssertionError('Business event worker did not create outgoing history')

    def disable(self):
        # Retain every fixture for inspection; stop leftover simulation automation.
        for dt, name in [('Relay AI Skill', self.ai.get('skill')), ('Relay Business Event Rule', self.event.get('rule'))]:
            if name:
                doc = self.f.get_doc(dt, name)
                doc.enabled = 0
                doc.save(ignore_permissions=True)
        self.f.db.commit()
        return {'disabled': True}
