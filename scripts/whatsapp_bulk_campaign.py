"""Connected bulk campaign step: real Core campaign APIs, simulation Hub and fake Meta receipts."""
import json
import os
from pathlib import Path
import secrets
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

from simulation_common import NoRedirect, percentile, request

FAILURE_EVERY = 66  # about 1.5% of recipients
SENT = ('Sent', 'Delivered', 'Read')


class ResourceSampler(threading.Thread):
    """Host CPU and memory plus resident memory of the services under test, once a second."""
    GROUPS = {'mariadb': ('mariadbd', 'mysqld'), 'postgres': ('postgres',), 'redis': ('redis-server',),
              'frappe_web': ('frappe serve',), 'frappe_workers': ('frappe worker',), 'sim_hub': ('relay-sim-connected',)}

    def __init__(self, database):
        super().__init__(daemon=True)
        self.database = database
        self.stop_event = threading.Event()
        self.peak = {'cpu_percent': 0.0, 'memory_used_mb': 0.0, 'postgres_connections': 0, 'sim_db_connections': 0,
                     **{key + '_rss_mb': 0.0 for key in self.GROUPS}}
        self.samples = 0

    @staticmethod
    def cpu():
        values = [int(v) for v in Path('/proc/stat').read_text().split('\n', 1)[0].split()[1:]]
        return sum(values), values[3] + values[4]

    def group_rss(self):
        totals = dict.fromkeys(self.GROUPS, 0.0)
        for pid in filter(str.isdigit, os.listdir('/proc')):
            try:
                command = Path(f'/proc/{pid}/cmdline').read_bytes().replace(b'\0', b' ').decode(errors='ignore')
                pages = int(Path(f'/proc/{pid}/statm').read_text().split()[1])
            except (OSError, ValueError, IndexError):
                continue
            for group, markers in self.GROUPS.items():
                if any(marker in command for marker in markers):
                    totals[group] += pages * os.sysconf('SC_PAGE_SIZE') / 2**20
                    break
        return totals

    def run(self):
        total, idle = self.cpu()
        while not self.stop_event.wait(1):
            now_total, now_idle = self.cpu()
            busy = 100 * (1 - (now_idle - idle) / max(1, now_total - total))
            total, idle = now_total, now_idle
            memory = {line.split(':')[0]: int(line.split()[1]) for line in Path('/proc/meminfo').read_text().splitlines()}
            used = (memory['MemTotal'] - memory['MemAvailable']) / 1024
            with self.database.cursor() as cur:
                cur.execute("SELECT count(*), count(*) FILTER (WHERE datname=current_database()) FROM pg_stat_activity WHERE backend_type='client backend'")
                connections, sim = cur.fetchone()
            sample = {'cpu_percent': busy, 'memory_used_mb': used, 'postgres_connections': connections, 'sim_db_connections': sim,
                      **{k + '_rss_mb': v for k, v in self.group_rss().items()}}
            self.peak = {key: round(max(self.peak[key], value), 1) for key, value in sample.items()}
            self.samples += 1

    def stop(self):
        self.stop_event.set()
        self.join(3)
        return dict(self.peak, samples=self.samples, cpu_count=os.cpu_count())


class BulkCampaign:
    def __init__(self, runner, recipients, cancel_recipients=500, repliers=200):
        self.r, self.count, self.cancel_count, self.reply_count = runner, recipients, cancel_recipients, repliers
        self.campaigns, self.files = [], []
        self.ticked, self.ticks = time.monotonic(), 0
        self.evidence = {}

    def run(self):
        r = self.r
        self.since = r.site.call('bulk_now')
        plan = r.site.call('bulk_seed', run=r.run_id, count=self.count + self.cancel_count,
                           repliers=self.reply_count, failure_every=FAILURE_EVERY, reply_pool=self.count)
        self.plan = plan
        main, cancel = plan['numbers'][:self.count], plan['numbers'][self.count:]
        r.control('bulk', {'numbers': plan['numbers'], 'failures': plan['failures'], 'repliers': plan['repliers']})
        self.template = self.approved_template()
        try:
            self.evidence['campaign'] = self.full_campaign(main)
            self.evidence['cancel'] = self.cancelled_campaign(cancel)
        finally:
            self.evidence['health'] = r.site.call('bulk_health', since=self.since)
            if self.campaigns:
                self.evidence['rows'] = dict(r.site.call('bulk_rows', since=self.since, campaigns=self.campaigns, files=[file['file_url'] for file in self.files]), hub=self.hub_rows())
            for file in self.files:
                self.delete_file(file['name'])
            self.evidence['cleanup'] = r.site.call('bulk_cleanup', campaigns=self.campaigns, since=self.since)
        return self.evidence

    def approved_template(self):
        r = self.r
        template = {'name': 'sim_bulk_offer_' + r.run_id.lower().replace('-', '_'), 'language': 'en_US',
                    'category': 'MARKETING', 'parameter_format': 'NAMED', 'components': [
                        {'type': 'HEADER', 'format': 'IMAGE', 'example': {'header_handle': ['https://example.invalid/offer.jpg']}},
                        {'type': 'BODY', 'text': 'Hello {{customer_name}}, your offer code is {{offer_code}}.',
                         'example': {'body_text_named_params': [{'param_name': 'customer_name', 'example': 'Asha'},
                                                                {'param_name': 'offer_code', 'example': 'SPRING'}]}}]}
        created = r.core('relay_core.whatsapp.execute', {'operation': 'templates.create', 'arguments': template})
        assert r.control('template', {'id': created['id'], 'status': 'APPROVED'})['status'] == 200
        r.site.call('wait', kind='template', id=created['id'])
        return template

    def sheet(self, numbers, label):
        from openpyxl import Workbook
        path = self.r.args.report_dir / f'{label}.xlsx'
        book = Workbook(write_only=True)
        sheet = book.create_sheet('Recipients')
        sheet.append(['Phone', 'Customer Name', 'Offer Code', 'Image URL'])
        for index, number in enumerate(numbers):
            phone = '+' + number if index % 2 else f'+{number[:2]} {number[2:7]} {number[7:]}'
            sheet.append([phone, f'Sim Shopper {index + 1}', f'OFFER-{label[-4:].upper()}', 'https://example.invalid/offer.jpg'])
        book.save(path)
        return path

    def upload(self, path):
        r = self.r
        boundary = secrets.token_hex(16)
        fields = {'is_private': '1'}  # As the Campaigns page uploads it: private and unattached.
        body = b''.join(f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode() for k, v in fields.items())
        body += (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{path.name}"\r\n'
                 'Content-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet\r\n\r\n').encode()
        body += path.read_bytes() + f'\r\n--{boundary}--\r\n'.encode()
        actor = r.fixture['operator']
        req = urllib.request.Request(r.args.core_origin + '/api/method/upload_file', data=body, headers={
            'Host': urllib.parse.urlsplit(r.canonical).netloc, 'Content-Type': f'multipart/form-data; boundary={boundary}',
            'Authorization': f"token {actor['api_key']}:{actor['api_secret']}"})
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        try:
            with opener.open(req, timeout=30) as reply:
                return json.load(reply)['message']
        except urllib.error.HTTPError as exc:
            raise RuntimeError(f'Upload failed with HTTP {exc.code}: {json.load(exc).get("exc_type", "")}') from None

    def delete_file(self, name):
        """The web process removes the private file; the bench helper may not write there."""
        r, actor = self.r, self.r.fixture['operator']
        request(r.args.core_origin + '/api/method/frappe.client.delete', {'doctype': 'File', 'name': name},
                {'Host': urllib.parse.urlsplit(r.canonical).netloc, 'Authorization': f"token {actor['api_key']}:{actor['api_secret']}"})

    def prepare(self, numbers, label):
        """Create, upload, inspect and import one campaign through Core's APIs."""
        r = self.r
        title = f"Sim Bulk {label.title()} {r.run_id}"
        doc = r.core('relay_core.bulk_campaigns.create', {'title': title, 'template_name': self.template['name'],
                     'language': 'en_US', 'channel': r.fixture['channel'], 'reply_window_hours': 24})
        name = doc['name']
        self.campaigns.append(name)
        path = self.sheet(numbers, label)
        file = self.upload(path)
        self.files.append(file)
        file_url = file['file_url']
        path.unlink()
        inspected = r.core('relay_core.bulk_campaigns.inspect', {'name': name, 'file_url': file_url})
        assert inspected['mapping']['phone'] == 'Phone', 'Phone column was not suggested'
        assert {f['key'] for f in inspected['fields']} == {'header_media', 'body:customer_name', 'body:offer_code'}, inspected['fields']
        mapping = {'phone': 'Phone', 'fields': {'header_media': 'Image URL', 'body:customer_name': 'Customer Name',
                   'body:offer_code': 'Offer Code'}, 'fixed': {}}
        started = time.monotonic()
        r.core('relay_core.bulk_campaigns.import_rows', {'name': name, 'mapping': json.dumps(mapping)})
        imported = self.wait('bulk_wait', campaign=name, status='Draft', total=len(numbers))
        if r.realtime:
            r.realtime.ws.send('42' + r.realtime.ns + ',' + json.dumps(['doc_subscribe', 'Relay Campaign', name]))
        return name, round(time.monotonic() - started, 2), imported

    def control(self, name, action):
        try:
            return self.r.core('relay_core.bulk_campaigns.control', {'name': name, 'action': action})
        except RuntimeError as exc:
            state = self.r.site.call('bulk_truth', campaign=name)
            raise RuntimeError(f"{action} refused while {state['status']} ({state['last_error'] or 'no error'}): {exc}") from None

    def wait(self, action, attempts=10, **values):
        for _ in range(attempts):
            self.tick()
            try:
                return self.r.site.call(action, **values)
            except RuntimeError as exc:
                if 'Campaign stayed' not in str(exc):
                    raise
        raise TimeoutError(f'{action} did not finish')

    def meta(self):
        self.tick()
        return self.r.control('bulk')

    def tick(self):
        """Run campaign recovery once a minute, as the site scheduler would."""
        if time.monotonic() - self.ticked >= 60:
            self.r.site.call('bulk_tick')
            self.ticked, self.ticks = time.monotonic(), self.ticks + 1

    def wait_sends(self, done, timeout=600, campaign=None):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            state = self.meta()
            if done(state):
                return state
            time.sleep(.25)
        state = self.r.site.call('bulk_truth', campaign=campaign or self.campaigns[-1])
        raise TimeoutError(f"Fake Meta stopped at {self.meta()['stats']['sends']} sends; campaign {state['status']}, "
                           f"rows {state['statuses']}, last error: {state['last_error'] or 'none'}")

    def settle(self, quiet=4):
        """Wait until Hub has drained what Core already handed it."""
        last, since = self.meta()['stats']['sends'], time.monotonic()
        while time.monotonic() - since < quiet:
            time.sleep(.5)
            now = self.meta()['stats']['sends']
            if now != last:
                last, since = now, time.monotonic()
        return last

    def full_campaign(self, numbers):
        r = self.r
        name, import_seconds, _ = self.prepare(numbers, 'campaign')
        failures = {n: c for n, c in self.plan['failures'].items() if n in set(numbers)}
        before = self.meta()['stats']['sends']
        sampler = ResourceSampler(r.database)
        sampler.start()
        started_ns = time.time_ns()
        self.control(name, 'start')
        self.wait_sends(lambda s: s['stats']['sends'] - before >= self.count * .3)
        paused_ns = time.time_ns()
        self.control(name, 'pause')
        at_pause = self.meta()['stats']['sends'] - before
        drained = self.settle() - before
        paused = r.site.call('bulk_truth', campaign=name)
        time.sleep(3)
        assert self.meta()['stats']['sends'] - before == drained, 'Sends continued while paused'
        resumed_ns = time.time_ns()
        self.control(name, 'resume')
        meta = self.wait_sends(lambda s: s['stats']['sends'] - before >= self.count and s['pending'] == 0)
        last_send_ns = meta['stats']['last_unix_ns']
        expected = {'status': 'Completed', 'counters': {'total': self.count, 'skipped': 0, 'queued': 0,
                    'sent': self.count - len(failures), 'delivered': self.count - len(failures),
                    'read': self.count - len(failures), 'failed': len(failures), 'replied': self.reply_count}}
        final = previous = None
        for _ in range(10):
            self.tick()
            final = r.site.call('bulk_wait_truth', campaign=name, expected=expected)
            # Stop when the counters match, or when they stayed the same for a whole minute.
            if not final.get('timed_out') or (previous and previous['counters'] == final['counters']):
                break
            previous = final
        resources = sampler.stop()
        self.settle(1)
        meta = self.meta()
        state = r.control('state')
        replies = self.reply_latency(meta['stats']['replies'])
        bulk_calls = [c for c in state['records'] if c['kind'] == 'callback' and c['path'].endswith('.receive_bulk_updates')]
        reply_calls = [c for c in state['records'] if c['kind'] == 'callback' and c['path'].endswith('.receive')
                       and 'wamid.sim.bulkreply' in json.dumps(c['body'])]
        sent_numbers = {n: meta['sent'].get(n, 0) for n in numbers}
        progress = [e for e in (r.realtime.events if r.realtime else []) if e['event'][0] == 'relay_campaign_progress'
                    and e['event'][1].get('name') == name]
        result = dict(
            campaign=name, recipients=self.count, import_seconds=import_seconds,
            end_to_end_seconds=round((final['observed_unix_ns'] - started_ns) / 1e9, 1),
            completed_seconds=round((final['finished_unix_ns'] - started_ns) / 1e9, 1) if final['finished_unix_ns'] else None,
            send_seconds=round((last_send_ns - meta['stats']['first_unix_ns']) / 1e9, 1),
            paused_seconds=round((resumed_ns - paused_ns) / 1e9, 1),
            sends_per_second=round(self.count / ((last_send_ns - started_ns - (resumed_ns - paused_ns)) / 1e9), 1),
            pause=dict(sends_at_pause=at_pause, sends_after_drain=drained, drained_after_pause=drained - at_pause,
                       status=paused['status'], pending=paused['statuses'].get('Pending', 0)),
            meta_sends=meta['stats']['sends'] - before, duplicate_sends=sum(v > 1 for v in sent_numbers.values()),
            unsent=sum(v == 0 for v in sent_numbers.values()), webhook_failures=meta['stats']['webhook_failures'],
            expected=expected, final=final, counters_match=final['counters'] == expected['counters'] and final['status'] == 'Completed',
            bulk_update_batches=len(bulk_calls), bulk_update_rows=sum(len(c['body']['updates']) for c in bulk_calls),
            bulk_update_failures=sum(c['status'] != 200 for c in bulk_calls),
            core_bulk_update_ms=latency([(c['finished_ns'] - c['started_ns']) / 1e6 for c in bulk_calls if c['status'] == 200]),
            core_inbound_reply_ms=latency([(c['finished_ns'] - c['started_ns']) / 1e6 for c in reply_calls if c['status'] == 200]),
            reply_latency_ms=replies, progress_events=len(progress), recovery_ticks=self.ticks, resources=resources,
            failure_codes_expected={str(code): sum(c == code for c in failures.values()) for code in set(failures.values())})
        result['passed'] = (result['counters_match'] and not result['duplicate_sends'] and not result['unsent']
                            and replies['created'] == self.reply_count and final['error_codes'] == result['failure_codes_expected'])
        return result

    def reply_latency(self, replies):
        """Webhook to Relay Message created, and webhook to Core projection finished."""
        emitted = {x['id']: x['emitted_unix_ns'] for x in replies}
        rows = {row['provider_id']: row for row in self.r.site.call('bulk_replies', providers=list(emitted))}
        created = [(row['created_unix_ns'] - emitted[key]) / 1e6 for key, row in rows.items()]
        with self.r.database.cursor() as cur:
            cur.execute(f'''SELECT id::text, payload->'entry'->0->'changes'->0->'value'->'messages'->0->>'id'
                FROM "{self.r.schema}".relay_whatsapp_events WHERE payload::text LIKE '%%bulkreply%%' ''')
            events = dict(cur.fetchall())
        projected = self.r.site.call('bulk_reply_events', hub_event_ids=list(events))
        done = [(row['projected_unix_ns'] - emitted[events[row['hub_event_id']]]) / 1e6 for row in projected
                if row['projection_status'] != 'Pending' and events.get(row['hub_event_id']) in emitted]
        decisions = {}
        for row in projected:
            for rule in json.loads(row['routing_decisions'] or '{}').values():
                decisions[rule] = decisions.get(rule, 0) + 1
        return dict(message_created=latency(created), projected=latency(done), created=len(rows), emitted=len(replies),
                    projection_statuses={s: sum(r['projection_status'] == s for r in projected) for s in {r['projection_status'] for r in projected}},
                    routing=decisions, linked_to_contact=sum(row['relay_contact'] == self.plan['contact'] for row in rows.values()))

    def cancelled_campaign(self, numbers):
        r = self.r
        name, import_seconds, _ = self.prepare(numbers, 'cancel')
        before = self.meta()['stats']['sends']
        self.control(name, 'start')
        self.wait_sends(lambda s: s['stats']['sends'] - before >= 100)
        at_cancel, cancel_ns = self.meta()['stats']['sends'] - before, time.time_ns()
        self.control(name, 'cancel')
        cancelled = self.wait('bulk_wait', campaign=name, status='Cancelled')
        cancelled_after = round((cancelled['observed_unix_ns'] - cancel_ns) / 1e9, 1)
        sends_when_cancelled = self.meta()['stats']['sends'] - before
        self.settle()
        sent = {n for n in numbers if self.meta()['sent'].get(n)}
        failed = sum(n in self.plan['failures'] for n in sent)
        expected = {'Read': len(sent) - failed, 'Failed': failed, 'Cancelled': len(numbers) - len(sent)}
        expected = {k: v for k, v in expected.items() if v}
        deadline, truth = time.monotonic() + 120, None
        while time.monotonic() < deadline:
            self.tick()
            truth = r.site.call('bulk_truth', campaign=name)
            if truth['statuses'] == expected:
                break
            time.sleep(2)
        return dict(campaign=name, recipients=len(numbers), import_seconds=import_seconds, sends_at_cancel=at_cancel,
                    sends_when_cancelled=sends_when_cancelled, cancelled_after_seconds=cancelled_after, sends_total=len(sent),
                    status=cancelled['status'], expected_statuses=expected, statuses=truth['statuses'],
                    counters=truth['counters'], passed=truth['statuses'] == expected)

    def hub_rows(self):
        with self.r.database.cursor() as cur:
            counts = {}
            for table in ('relay_outbox', 'relay_whatsapp_events', 'relay_whatsapp_status', 'relay_whatsapp_incoming', 'relay_bulk_pending'):
                cur.execute(f'SELECT count(*) FROM "{self.r.schema}".{table}')
                counts[table] = cur.fetchone()[0]
            return counts


def latency(values):
    return {'samples': len(values), 'p50': round(percentile(values, 50), 1) if values else None,
            'p95': round(percentile(values, 95), 1) if values else None, 'max': round(max(values), 1) if values else None}
