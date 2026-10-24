"""Fixture-only writes and read assertions for Telegram/email on yrp.site."""
import json
import os
import sys
from simulation_fixtures import confine_writes, fixture_user


def main():
    site, sites = sys.argv[1:3]
    assert site == 'yrp.site', 'Only yrp.site is approved for connected fixtures'
    sys.addaudithook(confine_writes)
    os.environ['FRAPPE_STREAM_LOGGING'] = '1'
    import frappe
    os.chdir(sites)
    frappe.init(site=site, sites_path=sites)
    frappe.connect()
    frappe.set_user('Administrator')
    frappe.flags.mute_emails = True
    frappe.flags.in_test = True
    fixture, previous_settings = {}, {}

    def insert(doctype, **values):
        return frappe.get_doc(dict(doctype=doctype, **values)).insert(ignore_permissions=True)

    from whatsapp_sim_acceptance import Acceptance
    acceptance = Acceptance(frappe, fixture, insert)

    def dispatch(req):
        action = req['action']
        if action.startswith('acceptance_'):
            method = action.removeprefix('acceptance_')
            assert method in {'seed_ai', 'wait_ai', 'wait_paused', 'ai_state', 'seed_event', 'submit_event', 'event_state', 'wait_event', 'disable', 'diagnostics'}
            return getattr(acceptance, method)(**{key: value for key, value in req.items() if key != 'action'})
        if action == 'preflight':
            acceptance.preflight()
            assert frappe.conf.developer_mode and 'relay_core' in frappe.get_installed_apps()
            from frappe.utils.background_jobs import get_workers
            from whatsapp_sim_site import require_worker_isolation
            require_worker_isolation(get_workers())
            doctypes = ['User', 'Customer', 'Supplier', 'Contact', 'Relay Contact', 'Relay Message', 'GL Entry', 'Sales Order', 'Item', 'Price List']
            for dt, field in [('Notification', 'document_type'), ('Webhook', 'webhook_doctype'),
                              ('YRP Notification Template', 'document_type')]:
                if frappe.db.exists('DocType', dt):
                    assert not frappe.get_all(dt, filters={'enabled': 1, field: ['in', doctypes]}, limit_page_length=1), 'Disable external fixture notifications first'
            return dict(canonical_origin=frappe.utils.get_url().rstrip('/'), version=frappe.__version__,
                        apps=frappe.get_installed_apps(), developer_mode=True)
        if action == 'provision':
            run, medium = req['run'], req['medium']
            assert medium in ('Telegram', 'Email')
            for label, roles in [('service', ['Relay Hub Service']),
                ('operator', ['System Manager', 'Relay Communication Manager', 'Sales Manager', 'Accounts Manager', 'Purchase Manager']),
                ('unauthorized', ['Relay Communication User'])]:
                fixture[label] = fixture_user(frappe, insert, label, roles, run)
            for dt in ('Customer', 'Supplier'):
                existing = frappe.db.get_value('Relay Contact Type', {'business_doctype': dt}, 'name')
                if not existing:
                    insert('Relay Contact Type', business_doctype=dt, enabled=1, label='Local simulation '+dt)
                else:
                    assert frappe.db.get_value('Relay Contact Type', existing, 'enabled')
            customer = insert('Customer', customer_name='Local Relay simulation '+run, customer_type='Company',
                customer_group=frappe.db.get_value('Customer Group', {'is_group': 0}, 'name'),
                territory=frappe.db.get_value('Territory', {'is_group': 0}, 'name'))
            supplier = insert('Supplier', supplier_name='Local Relay simulation '+run, supplier_type='Company',
                supplier_group=frappe.db.get_value('Supplier Group', {'is_group': 0}, 'name'))
            person = insert('Contact', first_name='Local simulation '+run, links=[
                dict(link_doctype='Customer', link_name=customer.name)])
            contact = insert('Relay Contact', contact=person.name, display_name='Local simulation '+run, automation_paused=1)
            channels = {}
            endpoints = {'Telegram': str(int(run[-12:])+100000), 'Email': 'sim-'+run+'@example.invalid', 'WhatsApp': '91'+run[-10:]}
            for kind in endpoints:
                names = frappe.get_all('Relay Channel', filters={'account':'sim-v1','channel':kind}, pluck='name')
                assert len(names) <= 1, 'Ambiguous simulation channel'
                channel = frappe.get_doc('Relay Channel', names[0]) if names else insert('Relay Channel', channel=kind, account='sim-v1', enabled=1)
                assert channel.enabled
                insert('Relay Contact Endpoint', channel=channel.name, endpoint=endpoints[kind], relay_contact=contact.name, verified=1)
                channels[kind] = channel.name
            shared_person = insert('Contact', first_name='Local simulation shared '+run, links=[
                dict(link_doctype='Customer', link_name=customer.name), dict(link_doctype='Supplier', link_name=supplier.name)])
            shared = insert('Relay Contact', contact=shared_person.name, display_name='Local simulation shared '+run, automation_paused=1)
            shared_endpoints = dict(Telegram=str(int(endpoints['Telegram'])+1), Email='shared-'+endpoints['Email'], WhatsApp=str(int(endpoints['WhatsApp'])+1))
            for kind, endpoint in shared_endpoints.items():
                insert('Relay Contact Endpoint', channel=channels[kind], endpoint=endpoint, relay_contact=shared.name, verified=1)
            fault_endpoint = str(int(endpoints['Telegram']) + 300000)
            insert('Relay Contact Endpoint', channel=channels['Telegram'], endpoint=fault_endpoint,
                relay_contact=contact.name, verified=1)
            fixture['fault_endpoint'] = fault_endpoint
            fixture.update(shared_contact=shared.name, shared_person=shared_person.name, shared_endpoints=shared_endpoints)
            cc = 'sim-cc-'+run+'@example.invalid'
            insert('Relay Contact Endpoint', channel=channels['Email'], endpoint=cc, relay_contact=contact.name, verified=1)
            fixture.update(medium=medium, contact=contact.name, person=person.name, customer=customer.name, supplier=supplier.name,
                channels=channels, channel=channels[medium], endpoints=endpoints, endpoint=endpoints[medium], cc=cc)
            frappe.db.commit()
            return fixture
        if action == 'settings':
            doc = frappe.get_single('Relay Settings')
            if not previous_settings:
                for field in ('hub_url','hub_account','hub_token','callback_secret'):
                    previous_settings[field] = doc.get_password(field, raise_exception=False) if field in ('hub_token','callback_secret') else doc.get(field)
            doc.update(dict(hub_url=req['hub_url'],hub_account='sim-v1',hub_token=req['token'],callback_secret=req['secret']))
            doc.save(ignore_permissions=True); frappe.db.commit()
            return True
        if action == 'restore':
            acceptance.disable()
            if previous_settings:
                doc = frappe.get_single('Relay Settings')
                doc.update(previous_settings); doc.save(ignore_permissions=True); frappe.db.commit()
                previous_settings.clear()
            return True
        if action == 'snapshot':
            frappe.db.rollback()
            return dict(events=frappe.get_all('Relay Hub Event', filters={'account':'sim-v1'},
                or_filters=[['payload','like','%'+fixture['endpoint']+'%'],
                            ['payload','like','%'+fixture['shared_endpoints'][fixture['medium']]+'%']],
                fields=['name','hub_event_id','projection_status','routing_decisions'], limit_page_length=100),
                messages=frappe.get_all('Relay Message', filters={'relay_contact':['in',[fixture['contact'],fixture['shared_contact']]]},
                fields=['name','provider_id','provider_context','message_data','content','direction','status','business_doctype','business_name','channel','relay_contact'], limit_page_length=0),
                routing=frappe.get_all('Relay Business Routing', filters={'relay_contact':['in',[fixture['contact'],fixture['shared_contact']]]},
                    fields=['name','choices','rule','prompt_action','prompt_message_id','business_doctype','business_name','conversation_key','relay_contact']),
                actions=frappe.get_all('Relay Message Action', or_filters={'start_contact':['in',[fixture['contact'],fixture['shared_contact']]],
                                'owner':['in',[fixture['operator']['name'],fixture['service']['name']]]},
                    fields=['name','status','operation','outbound_message','owner']))
        raise ValueError('Unknown fixture action')

    try:
        for line in sys.stdin:
            try:
                print(json.dumps({'result':dispatch(json.loads(line))}, default=str), flush=True)
            except Exception as exc:
                frappe.db.rollback()
                detail = str(exc) if isinstance(exc, (AssertionError, TimeoutError)) else type(exc).__name__
                print(json.dumps({'error':detail}), flush=True)
    finally:
        frappe.destroy()


if __name__ == '__main__':
    main()
