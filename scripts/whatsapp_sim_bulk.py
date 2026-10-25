"""Bench-side fixtures, truth checks and cleanup for the connected bulk campaign step."""
import time
from zoneinfo import ZoneInfo
from simulation_fixtures import run_number

RECIPIENT = 'Relay Campaign Recipient'
FAILURE_CODES = (131026, 131049)


class Bulk:
    def __init__(self, frappe, fixture, insert):
        self.f, self.fixture, self.insert = frappe, fixture, insert
        self.created = {}

    def seed(self, run, count, repliers, failure_every, reply_pool):
        """Unused sim numbers, a paused replier contact and the failure plan."""
        f = self.f
        base = 6200000000 + (run_number(run) % 700) * 10000
        numbers = ['91' + str(base + i) for i in range(count)]
        assert not f.get_all('Relay Contact Endpoint', filters={'endpoint': ['in', numbers]}, limit_page_length=1), 'Bulk numbers already belong to a contact'
        assert not f.get_all(RECIPIENT, filters={'destination': ['in', numbers]}, limit_page_length=1), 'Bulk numbers are in another campaign'
        replying = numbers[:reply_pool][::max(1, reply_pool // repliers)][:repliers]
        failing = {number: FAILURE_CODES[i % 2] for i, number in enumerate(
            n for n in numbers[1::failure_every] if n not in set(replying))}
        label = f'Sim Bulk Customer {run}'
        group = f.db.get_value('Customer Group', {'is_group': 0}, 'name')
        territory = f.db.get_value('Territory', {'is_group': 0}, 'name')
        customer = self.insert('Customer', customer_name=label, customer_type='Company', customer_group=group, territory=territory)
        person = self.insert('Contact', first_name=label, links=[dict(link_doctype='Customer', link_name=customer.name)])
        contact = self.insert('Relay Contact', contact=person.name, display_name=label, automation_paused=1)
        for number in replying:
            self.insert('Relay Contact Endpoint', channel=self.fixture['channel'], endpoint=number, relay_contact=contact.name, verified=1)
        f.db.commit()
        self.created = dict(customer=customer.name, person=person.name, contact=contact.name, run=run)
        return dict(numbers=numbers, repliers=replying, failures=failing, **self.created)

    def now(self):
        return self.f.utils.now()

    def tick(self):
        """The per-minute campaign recovery that the site scheduler runs; it is disabled on yrp.site."""
        self.f.get_attr('relay_core.campaigns.recover')()
        self.f.db.commit()
        return True

    def wait(self, campaign, status, total=None, timeout=60):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.f.db.rollback()
            doc = self.f.db.get_value('Relay Campaign', campaign, ['status', 'total_count', 'last_error'], as_dict=True)
            if doc.status == status and (total is None or doc.total_count == total):
                return dict(doc, observed_unix_ns=time.time_ns())
            time.sleep(.2)
        raise TimeoutError(f'Campaign stayed {doc.status} with {doc.total_count} rows: {doc.last_error}')

    def truth(self, campaign):
        """Stored counters next to a fresh count of the recipient rows."""
        f = self.f
        f.db.rollback()
        doc = f.get_doc('Relay Campaign', campaign)
        counters = {key: doc.get(key + '_count') or 0 for key in ('total', 'skipped', 'queued', 'sent', 'delivered', 'read', 'failed', 'replied')}
        rows = f.db.sql(f'''SELECT status, COUNT(*), CAST(SUM(replied_at IS NOT NULL) AS SIGNED),
            COUNT(DISTINCT provider_id) FROM `tab{RECIPIENT}` WHERE campaign=%s GROUP BY status''', campaign)
        codes = f.db.sql(f'''SELECT error_code, COUNT(*) FROM `tab{RECIPIENT}` WHERE campaign=%s AND status='Failed'
            GROUP BY error_code''', campaign)
        return dict(status=doc.status, counters=counters, started_at=doc.started_at, finished_at=doc.finished_at,
                    finished_unix_ns=self.epoch_ns(doc.finished_at) if doc.finished_at else None,
                    statuses={row[0]: int(row[1]) for row in rows}, replied=sum(int(row[2] or 0) for row in rows),
                    error_codes={str(row[0]): int(row[1]) for row in codes}, last_error=doc.last_error)

    def wait_truth(self, campaign, expected, timeout=60):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            state = self.truth(campaign)
            if state['counters'] == expected['counters'] and state['status'] == expected['status']:
                return dict(state, observed_unix_ns=time.time_ns())
            time.sleep(.5)
        return dict(state, observed_unix_ns=time.time_ns(), timed_out=True)

    def epoch_ns(self, value):
        zone = ZoneInfo(self.f.utils.get_system_timezone())
        return int(value.replace(tzinfo=zone).timestamp() * 1e9)

    def replies(self, providers):
        rows = self.f.get_all('Relay Message', filters={'provider_id': ['in', providers]},
                              fields=['provider_id', 'creation', 'relay_contact', 'business_name', 'direction'], limit_page_length=0)
        return [dict(row, created_unix_ns=self.epoch_ns(row.creation)) for row in rows]

    def reply_events(self, hub_event_ids):
        rows = self.f.get_all('Relay Hub Event', filters={'account': 'sim-v1', 'hub_event_id': ['in', hub_event_ids]},
                              fields=['hub_event_id', 'projection_status', 'routing_decisions', 'modified'], limit_page_length=0)
        return [dict(row, projected_unix_ns=self.epoch_ns(row.modified)) for row in rows]

    def health(self, since):
        f = self.f
        f.db.rollback()
        logs = f.get_all('Error Log', filters={'creation': ['>=', since]}, fields=['method', 'error'], limit_page_length=0)
        deadlocks = [row for row in logs if 'Deadlock' in (row.error or '') or '1213' in (row.error or '')]
        events = f.db.sql('''SELECT projection_status, COUNT(*) FROM `tabRelay Hub Event` WHERE account='sim-v1'
            AND creation>=%s GROUP BY projection_status''', since)
        return dict(error_logs=len(logs), deadlocks=len(deadlocks), failed_jobs=self.failed_jobs(since),
                    error_methods=sorted({(row.method or '')[:120] for row in logs}),
                    relay_errors=[(row.error or '').strip().splitlines()[-1][:200] for row in logs if 'relay' in (row.error or '').lower()][:10],
                    events={row[0]: int(row[1]) for row in events})

    def failed_jobs(self, since):
        from frappe.utils.background_jobs import get_queue
        from rq.job import Job
        from rq.exceptions import NoSuchJobError
        found = []
        for lane in ('short', 'long', 'default'):
            queue = get_queue(lane)
            for identifier in queue.failed_job_registry.get_job_ids(cleanup=False)[-200:]:
                try:
                    job = Job.fetch(identifier, connection=queue.connection)
                except NoSuchJobError:
                    continue
                method = str(job.kwargs.get('method') or '')
                ended = job.ended_at.timestamp() if job.ended_at else 0
                if job.kwargs.get('site') == self.f.local.site and 'relay_core' in method and ended >= self.epoch_ns(self.f.utils.get_datetime(since)) / 1e9:
                    found.append(dict(method=method, error=(job.exc_info or '').strip().splitlines()[-1:]))
        return found

    def rows(self, since, campaigns, files):
        f = self.f
        count = lambda sql, *values: int(f.db.sql(sql, values)[0][0])
        own = dict(recipients=count(f'SELECT COUNT(*) FROM `tab{RECIPIENT}` WHERE campaign IN %s', tuple(campaigns or [''])),
                   campaigns=len(campaigns),
                   hub_events=count("SELECT COUNT(*) FROM `tabRelay Hub Event` WHERE account='sim-v1' AND creation>=%s", since),
                   bulk_update_events=count("SELECT COUNT(*) FROM `tabRelay Hub Event` WHERE account='sim-v1' AND creation>=%s AND event_key LIKE 'bulk-updates:%%'", since),
                   reply_messages=count('SELECT COUNT(*) FROM `tabRelay Message` WHERE relay_contact=%s', self.created.get('contact')),
                   files=count('SELECT COUNT(*) FROM tabFile WHERE file_url IN %s', tuple(files or [''])))
        site_wide = {doctype: count(f'SELECT COUNT(*) FROM `tab{doctype}` WHERE creation>=%s', since)
                     for doctype in ('Relay Message', 'Relay Ticket', 'Version', 'Comment', 'Error Log', 'File', 'Relay Message Action')}
        return dict(own=own, site_wide_since_start=site_wide)

    def remove(self, doctype, name):
        """Delete without a Deleted Document copy or a deletion feed comment."""
        previous, self.f.flags.in_import = self.f.flags.in_import, True
        try:
            self.f.delete_doc(doctype, name, ignore_permissions=True, force=True, delete_permanently=True)
        finally:
            self.f.flags.in_import = previous

    def cleanup(self, campaigns, since, created=None):
        """Delete only what this step created. The driver deletes uploaded Files over HTTP,
        because this helper may not write outside Hub and /tmp."""
        f = self.f
        f.db.rollback()
        created = created or self.created
        for campaign in campaigns:
            while names := f.get_all(RECIPIENT, filters={'campaign': campaign}, pluck='name', limit_page_length=1000):
                f.db.delete(RECIPIENT, {'name': ['in', names]})
                f.db.commit()
            f.db.delete('Version', {'ref_doctype': 'Relay Campaign', 'docname': campaign})
            self.remove('Relay Campaign', campaign)
            f.db.commit()
        events = f.get_all('Relay Hub Event', filters={'account': 'sim-v1', 'creation': ['>=', since]}, pluck='name', limit_page_length=0)
        tickets = f.get_all('Relay Ticket', filters={'source_hub_event': ['in', events or ['']]}, pluck='name', limit_page_length=0)
        if tickets:
            f.db.delete('ToDo', {'reference_type': 'Relay Ticket', 'reference_name': ['in', tickets]})
            f.db.delete('Relay Ticket', {'name': ['in', tickets]})
        if created:
            contact = created['contact']
            for message in f.get_all('Relay Message', filters={'relay_contact': contact}, pluck='name'):
                self.remove('Relay Message', message)
            f.db.delete('Relay Business Routing', {'relay_contact': contact})
            f.db.delete('Relay Contact Endpoint', {'relay_contact': contact})
            self.remove('Relay Contact', contact)
            self.remove('Contact', created['person'])
            self.remove('Customer', created['customer'])
        if events:
            f.db.delete('Relay Hub Event', {'name': ['in', events]})
        f.cache.delete_value('relay-bulk-reply-until')
        f.db.commit()
        left = int(f.db.sql(f'SELECT COUNT(*) FROM `tab{RECIPIENT}` WHERE campaign IN %s', (tuple(campaigns or ['']),))[0][0])
        return dict(campaigns=len(campaigns), recipients_left=left, tickets=len(tickets), events=len(events), contact=created.get('contact'))
