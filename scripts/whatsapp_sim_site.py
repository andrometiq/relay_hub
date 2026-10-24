"""Bench-side fixture and read assertions, invoked only by the connected runner."""
import json
import ipaddress
import os
from pathlib import Path
import sys
import time

ROOT = Path(__file__).resolve().parents[1]


def confine_writes(event, args):
    if event == 'socket.connect' and isinstance(args[1], tuple):
        try:
            local = ipaddress.ip_address(args[1][0]).is_loopback
        except ValueError:
            local = False
        if not local:
            raise PermissionError('Simulation refuses non-loopback connections')
    paths = []
    if event == 'open':
        path, mode, flags = args
        if isinstance(path, (str, bytes)) and (flags & (os.O_WRONLY | os.O_RDWR | os.O_CREAT | os.O_TRUNC)):
            paths = [path]
    elif event in {'os.mkdir', 'os.remove', 'os.rmdir', 'os.chmod', 'os.rename'}:
        paths = args[:2] if event == 'os.rename' else args[:1]
    for path in paths:
        resolved = Path(os.fsdecode(path)).resolve()
        if not (resolved.is_relative_to(ROOT) or resolved.is_relative_to('/tmp')):
            raise PermissionError('Simulation refuses filesystem writes outside Hub and /tmp')


def expected_rows(kind, identity, data, historical):
    if kind == 'message':
        found = [m for m in data['messages'] if m['provider_id'] == identity]
        return len(found) == 1, found
    if kind == 'read':
        providers = set(historical)
        found = [m for m in data['messages'] if m['provider_id'] in providers]
        return (bool(providers) and len(providers) == len(found)
                and {m['provider_id'] for m in found} == providers
                and all(m['status'] == 'Read' for m in found)), found
    if kind == 'template':
        found = [t for t in data['templates'] if t['template_id'] == identity and t['status'] == 'APPROVED']
        return len(found) == 1, found
    if kind == 'event':
        found = [e for e in data['events'] if e['hub_event_id'] == str(identity)]
        return bool(found) and all(e['projection_status'] == 'Projected' for e in found), found
    raise ValueError('Unknown observation')


def finished_projection_timings(queue, site, event_ids, lane):
    from rq.exceptions import NoSuchJobError
    from rq.job import Job
    timings = []
    for identifier in queue.connection.zrange(queue.finished_job_registry.key, -2000, -1):
        try:
            job = Job.fetch(identifier.decode() if isinstance(identifier, bytes) else identifier, connection=queue.connection)
        except NoSuchJobError:
            continue
        name = (job.kwargs.get('kwargs') or {}).get('name')
        if job.kwargs.get('site') == site and name in event_ids and job.meta.get('relay_timing'):
            timings.append(dict(job.meta['relay_timing'], event=name, hub_event_id=event_ids[name], queue=lane))
    return timings


def require_worker_isolation(workers):
    lanes = [{name.rsplit(':', 1)[-1] for name in worker.queue_names()} for worker in workers]
    assert all(not ('default' in row and row & {'short', 'long'}) for row in lanes), (
        'Automation default queue must have a separate worker from short/long projection')
    assert {'default', 'short', 'long'} <= set().union(*lanes), 'Automation and both projection queues need workers'


def fixture_contact(frappe, insert, channel, run):
    endpoint = frappe.db.get_value('Relay Contact Endpoint',
        {'channel': channel.name, 'endpoint': '919999999999'}, 'name')
    binding = frappe.get_doc('Relay Contact Endpoint', endpoint) if endpoint else None
    if binding:
        assert (frappe.db.get_value('Relay Contact', binding.relay_contact, 'display_name') or '').startswith('Local simulation '), 'Endpoint belongs to non-simulation data'
    routed = frappe.db.get_value('Relay Business Routing',
        {'channel': channel.name, 'endpoint': '919999999999'}, 'relay_contact')
    existing = routed or (binding.relay_contact if binding else None)
    if existing:
        contact = frappe.get_doc('Relay Contact', existing)
        assert contact.display_name.startswith('Local simulation ') and contact.automation_paused, 'Contact is not a paused simulation fixture'
        person = frappe.get_doc('Contact', contact.contact)
        links = [(link.link_doctype, link.link_name) for link in person.links]
        assert len(links) == 1 and links[0][0] == 'Customer', 'Simulation contact must link to one Customer'
        customer = frappe.get_doc(*links[0])
        assert customer.customer_name.startswith('Local Relay simulation '), 'Customer belongs to non-simulation data'
    else:
        group = frappe.db.get_value('Customer Group', {'is_group': 0}, 'name')
        territory = frappe.db.get_value('Territory', {'is_group': 0}, 'name')
        assert group and territory, 'Customer Group and Territory fixtures are required'
        customer = insert('Customer', customer_name=f'Local Relay simulation {run}', customer_type='Company',
                          customer_group=group, territory=territory)
        person = insert('Contact', first_name=f'Local Relay simulation {run}',
            phone_nos=[dict(phone='919999999999', is_primary_mobile_no=1)],
            links=[dict(link_doctype='Customer', link_name=customer.name)])
        contact = insert('Relay Contact', contact=person.name, display_name=f'Local simulation {run}', automation_paused=1)
    if binding:
        if binding.relay_contact != contact.name or not binding.verified:
            binding.relay_contact, binding.verified = contact.name, 1
            binding.save(ignore_permissions=True)
    else:
        insert('Relay Contact Endpoint', channel=channel.name, endpoint='919999999999', relay_contact=contact.name, verified=1)
    return contact, person, customer


def main():
    sys.addaudithook(confine_writes)
    os.environ['FRAPPE_STREAM_LOGGING'] = '1'
    import frappe
    site, sites = sys.argv[1:3]
    os.chdir(sites)
    frappe.init(site=site, sites_path=sites)
    frappe.connect()
    frappe.set_user('Administrator')
    frappe.flags.mute_emails = True
    frappe.flags.in_test = True
    fixture = {}
    observed_providers = set()

    def insert(doctype, **values):
        return frappe.get_doc(dict(doctype=doctype, **values)).insert(ignore_permissions=True)

    def user(label, roles, run):
        key, secret = frappe.generate_hash(length=20), frappe.generate_hash(length=32)
        # Repeated local runs exceed Frappe's hourly user-creation throttle.
        frappe.flags.in_import = True
        try:
            doc = insert('User', email=f'relay-sim-{label}-{run}@example.invalid', first_name=f'Local simulation {label}',
                         enabled=1, user_type='System User', send_welcome_email=0,
                         roles=[{'role': role} for role in roles], api_key=key, api_secret=secret)
        finally:
            frappe.flags.in_import = False
        return {'name': doc.name, 'api_key': key, 'api_secret': secret}

    def seed_messages(group, run, count):
        from relay_core.message_updates import provider_key
        from relay_core.automation.service import internal_save
        historical = []
        for i in range(count):
            provider = f'wamid.sim.{group}.{run}.{i}'
            doc = frappe.get_doc(dict(doctype='Relay Message', relay_contact=fixture['contact'], channel=fixture['channel'],
                direction='Outgoing', status='Sent', provider_id=provider, sender_endpoint='919999999999',
                provider_message_key=provider_key(fixture['channel'], '919999999999', provider),
                source_key=f'sim-{group}-{run}-{i}', message_type='text', content=f'Local simulation {group} {i+1}',
                business_doctype='Customer', business_name=fixture['customer'], sent_at=frappe.utils.now_datetime(),
                provider_context=json.dumps({'provider': 'WhatsApp', 'message_id': provider})))
            internal_save(doc, new=True)
            historical.append(provider)
        fixture[group] = historical
        frappe.db.commit()
        return historical

    def snapshot():
        frappe.db.rollback()
        messages = frappe.get_all('Relay Message', filters={'relay_contact': fixture['contact'],
            'provider_id': ['in', fixture['historical'] + fixture.get('storm', []) + sorted(observed_providers)]},
            fields=['name', 'provider_id', 'status', 'direction', 'source_key', 'content'], limit_page_length=0)
        templates = frappe.get_all('Relay WhatsApp Template', filters={'account': 'sim-v1'},
            fields=['template_id', 'status', 'hub_event_id', 'template_name'])
        events = frappe.get_all('Relay Hub Event', filters={'account': 'sim-v1'},
            fields=['name', 'hub_event_id', 'projection_status'], limit_page_length=5000)
        actions = frappe.get_all('Relay Message Action', filters={'owner': fixture['operator']['name']},
            fields=['name', 'request_key', 'status', 'outbound_message'])
        return dict(messages=messages, templates=templates, events=events, actions=actions)

    from whatsapp_sim_acceptance import Acceptance
    acceptance = Acceptance(frappe, fixture, insert)

    def dispatch(req):
        action = req['action']
        if action.startswith('acceptance_'):
            method = action.removeprefix('acceptance_')
            assert method in {'seed_ai', 'wait_ai', 'wait_paused', 'ai_state', 'seed_event', 'submit_event', 'event_state', 'wait_event', 'disable'}
            return getattr(acceptance, method)(**{key: value for key, value in req.items() if key != 'action'})
        if action == 'preflight':
            assert frappe.conf.developer_mode, 'Target must be a development site'
            acceptance.preflight()
            from frappe.utils.background_jobs import get_workers
            require_worker_isolation(get_workers())
            assert 'relay_core' in frappe.get_installed_apps(), 'Relay Core is not installed'
            for name in ['Relay Settings', 'Relay Hub Event', 'Relay WhatsApp Template', 'Relay Business Routing', 'Customer']:
                assert frappe.db.exists('DocType', name), f'Missing installed DocType: {name}'
            # A paused synthetic contact blocks AI/flows. No external channels are seeded.
            assert not frappe.get_all('Notification', filters={'enabled': 1, 'document_type': ['in',
                ['Contact', 'Customer', 'Supplier', 'GL Entry', 'Sales Order', 'Item', 'Price List', 'User', 'Relay Message', 'Relay Contact']]}, limit_page_length=1), 'Disable external fixture notifications first'
            for doctype, field in [('Webhook', 'webhook_doctype'), ('YRP Notification Template', 'document_type')]:
                if frappe.db.exists('DocType', doctype):
                    assert not frappe.get_all(doctype, filters={'enabled': 1, field: ['in', ['Sales Order', 'Supplier', 'GL Entry', 'Item', 'Price List']]}, limit_page_length=1), 'Disable external fixture webhooks/notifications first'
            return {'canonical_origin': frappe.utils.get_url().rstrip('/'), 'version': frappe.__version__,
                    'site': site, 'developer_mode': bool(frappe.conf.developer_mode)}
        if action == 'provision':
            run = req['run']
            fixture['service'] = user('service', ['Relay Hub Service'], run)
            fixture['operator'] = user('operator', ['System Manager', 'Relay Communication Manager', 'Sales Manager', 'Accounts Manager', 'Purchase Manager'], run)
            fixture['unauthorized'] = user('unauthorized', ['Relay Communication User'], run)
            contact_type = frappe.db.get_value('Relay Contact Type', {'business_doctype': 'Customer'}, 'name')
            if contact_type:
                assert frappe.db.get_value('Relay Contact Type', contact_type, 'enabled'), 'Customer contact type is disabled'
            else:
                insert('Relay Contact Type', business_doctype='Customer', enabled=1, label='Local simulation customers')
            channels = frappe.get_all('Relay Channel', filters={'account': 'sim-v1', 'channel': 'WhatsApp'}, pluck='name')
            assert len(channels) <= 1, 'Simulation account has multiple channels'
            channel = frappe.get_doc('Relay Channel', channels[0]) if channels else insert('Relay Channel', channel='WhatsApp', account='sim-v1', enabled=1)
            assert channel.enabled, 'Simulation channel is disabled'
            contact, person, customer = fixture_contact(frappe, insert, channel, run)
            fixture.update(contact=contact.name, channel=channel.name, customer=customer.name, person=person.name)
            seed_messages('historical', run, 417)
            return fixture
        if action == 'seed_storm':
            return seed_messages('storm', req['run'], req['count'])
        if action == 'settings':
            settings = frappe.get_single('Relay Settings')
            settings.update({'hub_url': req['hub_url'], 'hub_account': 'sim-v1',
                             'hub_token': req['token'], 'callback_secret': req['secret']})
            settings.save(ignore_permissions=True)
            frappe.db.commit()
            return {'saved': True}
        if action == 'diagnostics':
            frappe.db.rollback()
            from frappe.utils.background_jobs import get_workers, get_queue
            from rq.job import Job
            events = frappe.get_all('Relay Hub Event', filters={'account': 'sim-v1'},
                        fields=['name', 'hub_event_id', 'projection_status'], limit_page_length=5000)
            failed, workers, timings = [], {}, []
            event_ids = {event.name: event.hub_event_id for event in events}
            for lane in ('short', 'long'):
                queue = get_queue(lane)
                workers[lane] = len(get_workers(queue))
                # Read the registry directly; do not run RQ's expired-job cleanup.
                for identifier in queue.connection.zrange(queue.failed_job_registry.key, -30, -1):
                    job = Job.fetch(identifier.decode() if isinstance(identifier, bytes) else identifier, connection=queue.connection)
                    name = (job.kwargs.get('kwargs') or {}).get('name')
                    if job.kwargs.get('site') == site and name in {event.name for event in events}:
                        failed.append({'event': name, 'method': job.kwargs.get('method'),
                                       'error': (job.exc_info or '').replace(str(frappe.conf.db_name) + '.', '').splitlines()[-1:]})
                timings.extend(finished_projection_timings(queue, site, event_ids, lane))
            return {'events': events, 'failed_jobs': failed,
                    'projection_timings': timings,
                    'routing_decisions_column': frappe.db.has_column('Relay Hub Event', 'routing_decisions'),
                    'workers': workers}
        if action == 'snapshot':
            return snapshot()
        if action == 'wait':
            if req['kind'] == 'message':
                observed_providers.add(req['id'])
            providers = fixture[req.get('group', 'historical')]
            deadline = time.monotonic() + req.get('timeout', 30)
            while time.monotonic() < deadline:
                data = snapshot()
                ready, found = expected_rows(req['kind'], req['id'], data, providers)
                if ready:
                    return {'observed_ns': time.monotonic_ns(), 'rows': found,
                            'receipts_complete': expected_rows('read', '', data, providers)[0]}
                time.sleep(.02)
            raise TimeoutError('Core projection did not reach the expected state; see core_diagnostics in report.json')
        raise ValueError('Unknown fixture action')

    try:
        for line in sys.stdin:
            try:
                req = json.loads(line)
                result = dispatch(req)
                print(json.dumps({'result': result}, default=str), flush=True)
            except Exception as exc:
                frappe.db.rollback()
                # Never serialize Frappe request/configuration or credential-bearing tracebacks.
                detail = str(exc) if isinstance(exc, (AssertionError, TimeoutError)) else type(exc).__name__
                print(json.dumps({'error': detail}), flush=True)
    finally:
        frappe.destroy()


if __name__ == '__main__':
    main()
