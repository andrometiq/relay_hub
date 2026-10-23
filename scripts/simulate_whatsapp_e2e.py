#!/usr/bin/env python3
"""Connected WhatsApp acceptance. No provider traffic and no HTTP message polling."""
import argparse
from contextlib import contextmanager
import hashlib
import ipaddress
import json
import math
import os
from pathlib import Path
import queue
import secrets
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
BENCH = Path('/home/sakthi/development/pilot/benches/version_16')
STEPS = {'7': 'Signed ingress', '8': 'Journal, Core projection and deduplication',
         '9': 'Core send idempotency', '10': '1,251 receipt batch check',
         '10b': 'inbound latency under a sustained receipt storm',
         '11': '503, wrong acknowledgement and restart recovery', '12': 'Template lifecycle',
         '13': 'Supplier AI, scoped ledger and human handoff',
         '14': 'Business event delivery and Hub outage isolation',
         'realtime': 'Authenticated Socket.IO message event without HTTP polling'}


def loopback_origin(value):
    parsed = urllib.parse.urlsplit(value)
    try:
        valid = ipaddress.ip_address(parsed.hostname).is_loopback
        port = parsed.port
    except (ValueError, TypeError):
        valid, port = False, None
    if not valid or parsed.scheme != 'http' or not port or parsed.username or parsed.password or parsed.path not in ('', '/') or parsed.query or parsed.fragment:
        raise ValueError('Expected HTTP origin with a literal loopback address and port')
    return value.rstrip('/')


def address(value):
    if loopback_origin('http://' + value) != 'http://' + value:
        raise ValueError('Expected host:port without a path')
    return value


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError('Simulation refuses HTTP redirects')


def request(url, data=None, headers=None):
    loopback_origin(urllib.parse.urlunsplit((*urllib.parse.urlsplit(url)[:2], '', '', '')))
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    body = json.dumps(data, separators=(',', ':')).encode() if data is not None else None
    req = urllib.request.Request(url, data=body, headers={'Content-Type': 'application/json', **(headers or {})})
    try:
        with opener.open(req, timeout=20) as reply:
            return json.load(reply)
    except urllib.error.HTTPError as exc:
        raise RuntimeError(f'HTTP {exc.code} at {urllib.parse.urlsplit(url).path}') from None


def percentile(values, percent):
    return sorted(values)[max(0, math.ceil(len(values) * percent / 100) - 1)] if values else None


def envelope(value, field='messages'):
    return {'object': 'whatsapp_business_account', 'entry': [{'id': '220', 'changes': [{'field': field, 'value': value}]}]}


def inbound(provider, sender='919999999999', text='Simulation inbound'):
    return envelope({'metadata': {'phone_number_id': '110'}, 'messages': [{'id': provider,
        'from': sender, 'timestamp': str(int(time.time())), 'type': 'text', 'text': {'body': text}}]})


def provider_posts(state, endpoint):
    return [row for row in state['records'] if row['kind'] == 'graph' and row['method'] == 'POST'
            and row['path'].endswith('/messages') and row['body'].get('to') == endpoint]


def assert_ledger_reply(text):
    for expected in ('SIM-OWN-DEBIT', 'SIM-OWN-CREDIT', 'Debit: 100.1', 'Credit: 40.05', 'Net debit: 60.05'):
        assert expected in text, 'Missing code-computed ledger rows/totals'
    for excluded in ('SIM-OTHER-SUPPLIER-SECRET', 'SIM-OUTSIDE-RANGE', 'SIM-CANCELLED', '999999', '88000', '77000'):
        assert excluded not in text, 'Unscoped or model-invented ledger data leaked'


def assert_handoff(state, fixture):
    assert state['paused'] and len(state['tickets']) == 1, 'Expected one ticket and paused automation'
    ticket = state['tickets'][0]
    assert ticket['assigned_to'] == fixture['handoff_owner']
    assert ticket['business_doctype'] == 'Supplier' and ticket['business_name'] == fixture['supplier']
    assert len(state['runs']) == 1 and ticket['source_run'] == state['runs'][0]['name']
    assert state['runs'][0]['status'] == 'Handoff'
    assert not state['actions'] and not state['outgoing'], 'Handoff must not send an automatic reply'


def receipt_storm(ids, count=None):
    stamp = int(time.time())
    statuses = [
        {'id': provider, 'recipient_id': '919999999999', 'status': status, 'timestamp': str(stamp + offset)}
        for provider in ids for offset, status in enumerate(('sent', 'delivered', 'read'))]
    return envelope({'metadata': {'phone_number_id': '110'}, 'statuses': statuses[-count:] if count else statuses})


def sustained_storm(ids, count):
    statuses = receipt_storm(ids, count)['entry'][0]['changes'][0]['value']['statuses']
    # Keep signed ingress envelopes below Hub's 1 MiB limit.
    for offset in range(0, len(statuses), 3000):
        yield envelope({'metadata': {'phone_number_id': '110'}, 'statuses': statuses[offset:offset + 3000]})


def successful_status_batches(records, event_id):
    return [r for r in records if r['kind'] == 'status' and r['status'] == 200
            and not r.get('fault') and r['body']['statuses'][0]['event_id'] == event_id]


def inbound_breakdown(records, sample):
    provider = json.dumps(sample['provider'])
    webhook = next(r for r in records if r['kind'] == 'webhook' and r['status'] == 200
                   and provider in json.dumps(r['body']))
    callback = next(r for r in records if r['kind'] == 'callback' and r['status'] == 200
                    and provider in json.dumps(r['body']))
    dispatch = float(callback.get('headers', {}).get('X-Relay-Dispatch-Ms', ['0'])[0])
    return dict(sample,
        ingress_ms=(webhook['finished_ns'] - webhook['started_ns']) / 1e6,
        hub_schedule_ms=max(0, (callback['started_ns'] - webhook['finished_ns']) / 1e6 - dispatch),
        hub_dispatch_ms=dispatch,
        callback_http_ms=(callback['finished_ns'] - callback['started_ns']) / 1e6,
        post_callback_ms=max(0, sample['latency_ms'] - (callback['finished_ns'] - webhook['started_ns']) / 1e6),
        **callback['response']['message'].get('timing', {}))


def storm_latency(samples, timings, event_ids, receipt_count):
    spans = [t for t in timings if t['hub_event_id'] in {str(e) for e in event_ids} and t['queue'] == 'long']
    overlapping = [s['latency_ms'] for s in samples if s['phase'] == 'processing' and any(
        t['started_ns'] <= s['observed_ns'] < t['finished_ns'] for t in spans)]
    duration = (max(t['finished_ns'] for t in spans) - min(t['started_ns'] for t in spans)) / 1e9 if spans else None
    return {'samples': len(overlapping), 'p50': percentile(overlapping, 50), 'p95': percentile(overlapping, 95),
            'target_ms': 500, 'passed': len(overlapping) >= 5 and percentile(overlapping, 95) < 500,
            'receipt_count': receipt_count, 'status_projection_seconds': duration,
            'receipts_per_second': receipt_count / duration if duration else None}


def read_env():
    values = {}
    for line in (ROOT / '.env').read_text().splitlines():
        if not line.strip() or line.lstrip().startswith('#'):
            continue
        key, sep, value = line.removeprefix('export ').partition('=')
        if sep:
            value = value.strip()
            if value[:1] in ('"', "'") and value[-1:] == value[:1]:
                value = value[1:-1]
            values[key.strip()] = value
    return values


def database_env():
    import psycopg2
    from psycopg2 import sql
    values = read_env()
    original = urllib.parse.urlsplit(values['DATABASE_URL'])
    assert original.hostname == '127.0.0.1' and original.port == 18433, 'Unexpected source database destination'
    owner = urllib.parse.unquote(original.username)
    try:
        conn = psycopg2.connect(host='127.0.0.1', port=18433, dbname='postgres', user='postgres',
                              password=values['POSTGRES_PASSWORD'], connect_timeout=5)
        try:
            conn.autocommit = True
            with conn.cursor() as cur:
                cur.execute('SELECT 1 FROM pg_database WHERE datname=%s', ('relay_hub_sim',))
                if not cur.fetchone():
                    cur.execute(sql.SQL('CREATE DATABASE relay_hub_sim OWNER {}').format(sql.Identifier(owner)))
        finally:
            conn.close()
    except psycopg2.Error as exc:
        raise RuntimeError('Cannot provision relay_hub_sim on local PostgreSQL (SQLSTATE ' + str(exc.pgcode) + ')') from None
    dsn = urllib.parse.urlunsplit(original._replace(path='/relay_hub_sim', query='', fragment=''))
    return {'DATABASE_URL': dsn, 'RELAY_CREDENTIAL_KEY': values['RELAY_CREDENTIAL_KEY']}


class Site:
    def __init__(self, args, log):
        self.process = subprocess.Popen([str(args.frappe_python), '-B', str(ROOT / 'scripts/whatsapp_sim_site.py'),
            args.site, str(args.sites)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=log, text=True,
            env={**os.environ, 'PYTHONDONTWRITEBYTECODE': '1', 'FRAPPE_STREAM_LOGGING': '1'})
        self.responses = queue.Queue()
        def read():
            for line in self.process.stdout:
                try:
                    value = json.loads(line)
                    if 'result' in value or 'error' in value:
                        self.responses.put(value)
                except ValueError:
                    pass
            self.responses.put({'error': 'Bench helper exited; see site.log'})
        threading.Thread(target=read, daemon=True).start()

    def call(self, action, **values):
        self.process.stdin.write(json.dumps(dict(action=action, **values)) + '\n')
        self.process.stdin.flush()
        result = self.responses.get(timeout=90)
        if 'error' in result:
            raise RuntimeError(result['error'])
        return result['result']

    def close(self):
        if self.process.poll() is None:
            self.process.stdin.close()
            try:
                self.process.wait(5)
            except subprocess.TimeoutExpired:
                self.process.terminate()
                self.process.wait(5)


class Realtime:
    """Engine.IO 4 / Socket.IO 4 over WebSocket exclusively (no polling fallback)."""
    def __init__(self, origin, canonical, site, operator, contact):
        from websockets.sync.client import connect
        local = urllib.parse.urlsplit(loopback_origin(origin))
        host = urllib.parse.urlsplit(canonical).hostname
        sock = socket.create_connection((local.hostname, local.port), timeout=10)
        self.ws = None
        try:
            # websockets leaves a supplied socket's TCP timeout unchanged.
            sock.settimeout(None)
            self.ws = connect(f'ws://{host}:{local.port}/socket.io/?EIO=4&transport=websocket', sock=sock,
                origin=canonical, proxy=None, additional_headers={'Authorization': f"token {operator['api_key']}:{operator['api_secret']}",
                                                                'X-Frappe-Site-Name': site}, open_timeout=10, ping_interval=None)
            self.ns = '/' + site
            assert self.ws.recv(timeout=10).startswith('0'), 'Missing Engine.IO handshake'
            self.ws.send('40' + self.ns + ',')
            deadline = time.monotonic() + 15
            while True:
                ack = self.ws.recv(timeout=max(0, deadline - time.monotonic()))
                if ack == '2':
                    self.ws.send('3')
                    continue
                assert ack.startswith('40' + self.ns + ','), 'Socket.IO authentication failed'
                break
        except Exception:
            if self.ws:
                self.ws.close()
            sock.close()
            raise
        self.events = []
        self.condition = threading.Condition()
        self.error = None
        self.closed = False
        self.ws.send('42' + self.ns + ',' + json.dumps(['doc_subscribe', 'Relay Contact', contact]))
        self.ws.send('42' + self.ns + ',' + json.dumps(['doctype_subscribe', 'Relay WhatsApp Template']))
        self.thread = threading.Thread(target=self.listen, daemon=True)
        self.thread.start()
        # Frappe checks room permissions asynchronously and has no subscription acknowledgement.
        time.sleep(.5)

    def listen(self):
        try:
            while not self.closed:
                packet = self.ws.recv()
                if packet == '2':
                    self.ws.send('3')
                elif packet == '1' or packet.startswith('41' + self.ns):
                    raise RuntimeError('Socket.IO namespace disconnected')
                elif packet.startswith('42' + self.ns + ','):
                    event = json.loads(packet.split(',', 1)[1])
                    with self.condition:
                        self.events.append({'event': event, 'monotonic_ns': time.monotonic_ns()})
                        self.condition.notify_all()
        except Exception as exc:
            if not self.closed:
                with self.condition:
                    self.error = str(exc) or type(exc).__name__
                    self.condition.notify_all()

    def wait(self, kind, since, timeout=30):
        deadline = time.monotonic() + timeout
        with self.condition:
            while True:
                for row in self.events:
                    if row['monotonic_ns'] >= since and row['event'] == ['relay_changed', {'kind': kind}]:
                        return row
                if self.error:
                    raise RuntimeError('Socket.IO disconnected: ' + self.error)
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise TimeoutError('No matching Socket.IO invalidation')
                self.condition.wait(remaining)

    def close(self):
        self.closed = True
        self.ws.close()
        self.thread.join(3)


class Runner:
    def __init__(self, args):
        self.args = args
        self.hub = 'http://' + args.hub_addr
        self.meta = 'http://' + args.meta_addr
        self.control_token = secrets.token_urlsafe(32)
        self.run_id = str(time.time_ns())
        self.report = {'run': self.run_id, 'mode': 'dry-run' if args.dry_run else 'connected', 'site': args.site,
            'steps': {k: {'name': v, 'status': 'not_run'} for k, v in STEPS.items()}, 'latencies_ms': [],
            'batch_sizes': [], 'inbound_samples': [],
            'limitations': ['Socket.IO transport acceptance; installed browser rendering is not exercised.']}
        self.process = self.site = self.realtime = self.denied = None
        self.captures = []
        self.logs = []
        self.args.report_dir.mkdir(parents=True, exist_ok=True)

    @contextmanager
    def step(self, key, continue_on_error=False):
        print(f'Step {key}: {STEPS[key]}', flush=True)
        try:
            yield self.report['steps'][key]
        except Exception as exc:
            step = self.report['steps'][key]
            step.setdefault('errors', []).append(str(exc) or type(exc).__name__)
            step.update(status='fail', error='; '.join(step['errors']))
            if not continue_on_error:
                raise
        else:
            if self.report['steps'][key]['status'] != 'fail':
                self.report['steps'][key]['status'] = 'pass'

    def control(self, path, data=None):
        return request(self.meta + '/_sim/' + path, data, {'Authorization': 'Bearer ' + self.control_token})

    def core(self, method, data=None, user=None):
        actor = user or self.fixture['operator']
        return request(self.args.core_origin + '/api/method/' + method, data,
            {'Host': urllib.parse.urlsplit(self.canonical).netloc,
             'Authorization': f"token {actor['api_key']}:{actor['api_secret']}"})['message']

    def open_log(self, name):
        log = open(self.args.report_dir / name, 'a')
        self.logs.append(log)
        return log

    def start(self, resume=False):
        self.process = subprocess.Popen([str(self.binary), '--addr', self.args.hub_addr,
            '--meta-addr', self.args.meta_addr, '--core-origin', self.args.core_origin,
            '--resume' if resume else '--migrate'],
            cwd=ROOT, env={**os.environ, **self.db_env, 'RELAY_SIM_CONTROL_TOKEN': self.control_token},
            stdout=self.open_log('hub.log'), stderr=subprocess.STDOUT)
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise RuntimeError('relay-sim exited; see hub.log')
            try:
                request(self.hub + '/readyz')
                return
            except (OSError, RuntimeError):
                time.sleep(.1)
        raise TimeoutError('relay-sim readiness timeout')

    def configure(self):
        result = self.control('provision', {'site': self.canonical, 'api_key': self.fixture['service']['api_key'],
            'api_secret': self.fixture['service']['api_secret'], 'callback_secret': self.callback_secret})
        self.site.call('settings', hub_url=self.hub + '/mcp', token=result['token'], secret=self.callback_secret)
        assert self.core('relay_core.settings.verify_connection', {})['verified']

    def stop_hub(self):
        if self.process and self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(10)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(5)

    def wait_control(self, predicate, timeout=35):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            state = self.control('state')
            if predicate(state):
                return state
            time.sleep(.05)
        raise TimeoutError('Hub did not reach expected durable state')

    def emit(self, payload, **values):
        result = self.control('emit', dict(payload=payload, **values))
        assert result['status'] == (403 if values.get('invalid_signature') else 200), 'Unexpected webhook response'
        return result

    def record_inbound(self, provider, result, observed, phase):
        latency = (observed['observed_ns'] - result['monotonic_ns']) / 1e6
        self.report['latencies_ms'].append(latency)
        self.report['inbound_samples'].append(dict(provider=provider, phase=phase, latency_ms=latency,
            started_ns=result['monotonic_ns'], observed_ns=observed['observed_ns']))

    def observe_inbound(self, provider, payload=None, phase='held'):
        result = self.emit(payload or inbound(provider))
        observed = self.site.call('wait', kind='message', id=provider, timeout=5, group='storm' if phase == 'processing' else 'historical')
        # Both clocks are CLOCK_MONOTONIC on this host; control overhead is separately visible.
        self.record_inbound(provider, result, observed, phase)
        return observed

    def sample_receipt_projection(self):
        deadline = time.monotonic() + max(60, self.args.storm_receipts / 100)
        count = 0
        while time.monotonic() < deadline:
            started = time.monotonic()
            observed = self.observe_inbound(f'wamid.sim.processing.{self.run_id}.{count}', phase='processing')
            count += 1
            if observed['receipts_complete']:
                return count
            # Pace send starts without adding the observation latency to the spacing.
            time.sleep(max(0, .25 - (time.monotonic() - started)))
        raise TimeoutError('Receipt projection did not finish during inbound sampling')

    def wait_projection_timings(self, event_ids, batches):
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            timings = self.site.call('diagnostics')['projection_timings']
            completed = {t['event'] for t in timings if t['hub_event_id'] in {str(e) for e in event_ids} and t['queue'] == 'long'}
            if len(completed) == batches:
                return timings
            time.sleep(.05)
        raise TimeoutError(f'Missing timings for {batches} committed receipt projection jobs')

    def check_ai_reply(self, provider, fixture, expected_posts):
        state = self.site.call('acceptance_wait_ai', provider=provider, status='Completed')
        assert len(state['incoming']) == len(state['runs']) == len(state['actions']) == len(state['outgoing']) == 1
        assert state['incoming'][0]['business_name'] == fixture['supplier']
        assert state['actions'][0]['status'] == 'Submitted'
        message = state['outgoing'][0]
        assert message['business_doctype'] == 'Supplier' and message['business_name'] == fixture['supplier']
        hub = self.control('state')
        posts = provider_posts(hub, fixture['endpoint'])
        assert len(posts) == expected_posts and all(post['status'] == 200 for post in posts), 'Duplicate or failed fake Meta POST'
        assert posts[-1]['body']['text']['body'] == message['content']
        assert posts[-1]['response']['messages'][0]['id'] == message['provider_id']
        deliveries = [row for row in hub['deliveries'] if row['request_key'] == state['actions'][0]['request_key']]
        assert len(deliveries) == 1 and deliveries[0]['state'] == 'submitted', 'Missing durable Hub intent'
        for status in ('sent', 'delivered', 'read'):
            self.emit(envelope({'metadata': {'phone_number_id': '110'}, 'statuses': [{'id': message['provider_id'],
                'recipient_id': fixture['endpoint'], 'status': status, 'timestamp': str(int(time.time()))}]}))
            self.site.call('acceptance_wait_ai', provider=provider, status='Completed', receipt=status.title())
        return state

    def ai_acceptance(self):
        fixture = self.site.call('acceptance_seed_ai', run=self.run_id, origin=self.meta)
        first = f'wamid.sim.ai.offer.{self.run_id}'
        self.emit(inbound(first, fixture['endpoint'], 'I have a doubt in my ledger'))
        offered = self.check_ai_reply(first, fixture, 1)
        offer = offered['outgoing'][0]['content']
        assert '2026-10-01' in offer and '2026-10-31' in offer and 'confirm' in offer.lower()
        assert json.loads(offered['runs'][0]['decision'])['reads'] == [], 'Ledger read before confirmation'
        confirmed = f'wamid.sim.ai.confirm.{self.run_id}'
        payload = inbound(confirmed, fixture['endpoint'], 'yes')
        self.emit(payload)
        result = self.check_ai_reply(confirmed, fixture, 2)
        assert_ledger_reply(result['outgoing'][0]['content'])
        assert len(json.loads(result['runs'][0]['decision'])['reads']) == 1, 'Expected scoped tool evidence'
        self.emit(payload)
        self.wait_control(lambda state: all(row['acknowledged'] for row in state['callbacks'] if row['lane'] == 'realtime'))
        assert len(provider_posts(self.control('state'), fixture['endpoint'])) == 2, 'Replay resent AI reply'
        person = f'wamid.sim.ai.person.{self.run_id}'
        payload = inbound(person, fixture['endpoint'], 'please connect me to a person')
        self.emit(payload)
        result = self.site.call('acceptance_wait_ai', provider=person, status='Handoff')
        self.emit(payload)
        paused = f'wamid.sim.ai.paused.{self.run_id}'
        self.emit(inbound(paused, fixture['endpoint'], 'please connect me to a person'))
        self.wait_control(lambda state: all(row['acknowledged'] for row in state['callbacks'] if row['lane'] == 'realtime'))
        self.site.call('acceptance_wait_paused', provider=paused)
        result = self.site.call('acceptance_ai_state', provider=person)
        assert_handoff(result, fixture)
        state = self.control('state')
        assert len(provider_posts(state, fixture['endpoint'])) == 2, 'Handoff or paused conversation sent an AI reply'
        ai_calls = [row for row in state['records'] if row['kind'] == 'ai']
        assert len(ai_calls) == 4 and all(row['status'] == 200 for row in ai_calls), 'Expected offer, tool request, tool result and handoff through fake AI'
        assert all(marker not in json.dumps(ai_calls) for marker in ('SIM-OTHER-SUPPLIER-SECRET', 'SIM-OUTSIDE-RANGE', 'SIM-CANCELLED'))
        return dict(supplier=fixture['supplier'], accepted_provider_posts=2, fake_ai_calls=len(ai_calls),
                    ticket=result['tickets'][0]['name'], statuses=['Sent', 'Delivered', 'Read'], automation_paused=True)

    def business_event_acceptance(self):
        template = self.report['steps']['12'].get('template_id')
        assert template, 'Step 12 must provide an approved template'
        self.site.call('acceptance_seed_event', run=self.run_id, template_id=template)
        before = len(provider_posts(self.control('state'), '919999999999'))
        submitted = self.site.call('acceptance_submit_event')
        state = self.site.call('acceptance_wait_event', source=submitted['source'])
        assert len(state['deliveries']) == len(state['actions']) == len(state['messages']) == 1
        assert state['actions'][0]['status'] == 'Submitted'
        assert state['deliveries'][0]['business_name'] == self.fixture['customer'] and state['docstatus'] == 1
        message = state['messages'][0]
        hub = self.control('state')
        posts = provider_posts(hub, '919999999999')
        assert len(posts) == before + 1 and posts[-1]['status'] == 200
        assert posts[-1]['body']['type'] == 'template'
        assert posts[-1]['body']['template']['name'] == json.loads(state['deliveries'][0]['payload'])['name']
        assert posts[-1]['body']['template']['components'][0]['parameters'] == [{'type': 'text', 'text': submitted['source']}]
        assert posts[-1]['response']['messages'][0]['id'] == message['provider_id']
        timeline = self.core('relay_core.api.messages?' + urllib.parse.urlencode({'contact': self.fixture['contact'], 'channel': self.fixture['channel']}))
        assert sum(row['name'] == message['name'] for row in timeline['items']) == 1, 'Missing customer timeline entry'
        # Retain captures before stopping only our own launcher. Core is untouched.
        self.captures.extend(hub.pop('records'))
        self.report['hub_state'] = hub
        self.stop_hub()
        offline = self.site.call('acceptance_submit_event')
        offline_state = self.site.call('acceptance_event_state', source=offline['source'])
        assert offline_state['docstatus'] == 1 and len(offline_state['deliveries']) == 1, 'Hub outage interrupted submit or lost the intent'
        return dict(source=submitted['source'], accepted_provider_posts=1, timeline_message=message['name'],
                    offline_source=offline['source'], offline_docstatus=offline_state['docstatus'])

    def execute(self):
        if self.args.dry_run:
            self.report['plan'] = {'hub': self.hub, 'fake_meta': self.meta, 'core': self.args.core_origin,
                'socketio': self.args.socketio_origin, 'database': 'relay_hub_sim@127.0.0.1:18433',
                'bench_python': str(self.args.frappe_python), 'sites': str(self.args.sites),
                'writes': 'Only an actual run provisions the Hub database and Core fixture/settings.',
                'receipt_count': 1251, 'expected_batches': [500, 500, 251],
                'storm_receipts': self.args.storm_receipts}
            return
        for origin in (self.hub, self.meta):
            parsed = urllib.parse.urlsplit(origin)
            with socket.socket(socket.AF_INET6 if ':' in parsed.hostname else socket.AF_INET) as probe:
                probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                probe.bind((parsed.hostname, parsed.port))
        assert request(self.args.core_origin + '/api/method/ping', headers={'Host': self.args.site})['message'] == 'pong', 'Core unavailable'
        self.site = Site(self.args, self.open_log('site.log'))
        self.report['preflight'] = self.site.call('preflight')
        self.canonical = self.report['preflight']['canonical_origin']
        parsed = urllib.parse.urlsplit(self.canonical)
        assert parsed.scheme == 'http' and parsed.hostname == self.args.site and parsed.port == urllib.parse.urlsplit(self.args.core_origin).port, 'Core canonical origin must match site and local HTTP port'
        self.db_env = database_env()
        self.binary = ROOT / '.runtime/relay-sim'
        subprocess.run(['go', 'build', '-o', str(self.binary), './cmd/relay-sim'], cwd=ROOT,
            env={**os.environ, 'GOCACHE': '/tmp/relay-sim-gocache'}, check=True, stdout=self.open_log('build.log'), stderr=subprocess.STDOUT)
        self.start()
        self.fixture = self.site.call('provision', run=self.run_id)
        self.callback_secret = secrets.token_urlsafe(32)
        self.configure()
        self.report['fixture'] = {key: self.fixture[key] for key in ('contact', 'channel', 'customer', 'person')}
        with self.step('realtime', continue_on_error=True):
            self.realtime = Realtime(self.args.socketio_origin, self.canonical, self.args.site, self.fixture['operator'], self.fixture['contact'])
            self.denied = Realtime(self.args.socketio_origin, self.canonical, self.args.site, self.fixture['unauthorized'], self.fixture['contact'])
        first = f'wamid.sim.in.{self.run_id}'
        first_payload = inbound(first)
        row = emitted = None
        started = time.monotonic_ns()
        with self.step('7', continue_on_error=True):
            self.emit(inbound(first), invalid_signature=True)
            wrong = inbound(first)
            wrong['entry'][0]['changes'][0]['value']['metadata']['phone_number_id'] = '999'
            rejected = self.control('emit', {'payload': wrong})
            assert rejected['status'] == 403, 'Wrong-account webhook accepted'
            started = time.monotonic_ns()
            emitted = self.emit(first_payload)
        with self.step('8', continue_on_error=True) as evidence:
            assert emitted, 'Projection requires successful signed ingress'
            observed = self.site.call('wait', kind='message', id=first)
            row = observed['rows'][0]
            self.record_inbound(first, emitted, observed, 'initial')
        with self.step('realtime', continue_on_error=True) as evidence:
            assert self.realtime and self.denied, 'Authenticated Socket.IO connection unavailable'
            event = self.realtime.wait('messages', started, timeout=2)
            evidence.update(event=event['event'], latency_ms=(event['monotonic_ns'] - started) / 1e6,
                            transport='websocket', http_message_polls=0)
            time.sleep(.2)
            assert not self.denied.error, 'Unauthorized Socket.IO observer disconnected'
            assert not any(e['event'] == ['relay_changed', {'kind': 'messages'}] for e in self.denied.events), 'Unauthorized message event'
        with self.step('8', continue_on_error=True) as evidence:
            self.emit(first_payload)
            state = self.wait_control(lambda s: all(c['acknowledged'] for c in s['callbacks'] if c['lane'] == 'realtime'))
            snap = self.site.call('snapshot')
            assert len([m for m in snap['messages'] if m['provider_id'] == first]) == 1, 'Expected exactly one projected Core Relay Message'
            assert len([m for m in state['incoming'] if m['provider_id'] == first]) == 1
            events = [e for e in state['events'] if first in json.dumps(e['payload'])]
            assert len(events) == 1, 'Inbound journal deduplication failed'
            assert any(e['hub_event_id'] == str(events[0]['id']) and e['projection_status'] == 'Projected' for e in snap['events'])
            evidence.update(message=row['name'], event_id=events[0]['id'])
        with self.step('9', continue_on_error=True) as evidence:
            assert row, 'Send requires a projected inbound Core message'
            data = {'message': row['name'], 'kind': 'text', 'data': {'text': 'Simulation reply'},
                    'request_key': 'sim-v1-reply-' + self.run_id, 'quote': 1}
            first_send = self.core('relay_core.messaging.send', data)
            second_send = self.core('relay_core.messaging.send', data)
            assert first_send['action'] == second_send['action'], 'Duplicate Core action'
            state = self.wait_control(lambda s: any(d['state'] == 'submitted' for d in s['deliveries'] if d['request_key'] == hashlib.sha256((self.fixture['operator']['name'] + ':' + data['request_key']).encode()).hexdigest()))
            key = hashlib.sha256((self.fixture['operator']['name'] + ':' + data['request_key']).encode()).hexdigest()
            deliveries = [d for d in state['deliveries'] if d['request_key'] == key]
            assert len(deliveries) == 1
            posts = [r for r in state['records'] if r['kind'] == 'graph' and r['method'] == 'POST' and r['path'].endswith('/messages')]
            assert [p['status'] for p in posts] == [200], 'Expected exactly one fake Meta POST'
            post = posts[-1]
            assert post['body']['to'] == '919999999999' and post['body']['messaging_product'] == 'whatsapp'
            assert post['body']['context']['message_id'] == first and post['body']['text']['body'] == 'Simulation reply'
            assert post['body']['biz_opaque_callback_data'] == 'relay-outbox:' + str(deliveries[0]['id'])
            outgoing = self.site.call('wait', kind='message', id=deliveries[0]['provider_message_id'])
            assert outgoing['rows'][0]['direction'] == 'Outgoing'
            assert len([a for a in self.site.call('snapshot')['actions'] if a['request_key'] == key]) == 1
            evidence.update(action=first_send['action'], outbox_id=deliveries[0]['id'], accepted_provider_posts=1)
        # Hold only receipts so a fresh inbound exercises the independent realtime lane.
        storm = receipt_storm(self.fixture['historical'])
        failed = None
        fast_errors = []
        with self.step('10', continue_on_error=True):
            self.control('faults', {'target': 'status', 'faults': ['503'], 'hold': True})
            self.emit(storm)
            failed = self.wait_control(lambda s: any(r['kind'] == 'status' and r['status'] == 503 for r in s['records']) and any(c['lane'] == 'status' and c['failures'] > 0 for c in s['callbacks']))
            event_id = max(e['id'] for e in failed['events'] if e['lane'] == 'status')
        recovered = None
        with self.step('11', continue_on_error=True) as evidence:
            assert failed, 'Recovery requires a journaled receipt storm'
            def pending(state):
                return next(c for c in state['callbacks'] if c['event_id'] == event_id and c['lane'] == 'status')
            assert pending(failed)['status_offset'] == 0 and not pending(failed)['acknowledged']
            for i in range(5):
                try:
                    self.observe_inbound(f'wamid.sim.fast.{self.run_id}.{i}')
                except (AssertionError, RuntimeError, TimeoutError) as exc:
                    fast_errors.append(str(exc))
                    break
            self.report['concurrent_inbound_errors'] = fast_errors
            self.control('faults', {'target': 'status', 'faults': ['wrong_ack'], 'hold': True})
            bad_ack = self.wait_control(lambda s: any(r.get('fault') == 'wrong_ack' for r in s['records']) and pending(s)['failures'] >= 2)
            assert pending(bad_ack)['status_offset'] == 0 and not pending(bad_ack)['acknowledged']
            self.captures.extend(bad_ack['records'])
            self.stop_hub()
            self.start(resume=True)
            recovered = self.wait_control(lambda s: pending(s)['acknowledged'], timeout=60)
            evidence.update(event_id=event_id, failed_offset=0, wrong_ack_offset=0, recovered_offset=pending(recovered)['status_offset'], restarted=True)
        with self.step('10', continue_on_error=True) as evidence:
            assert recovered, 'Receipt batch assertion requires successful Hub recovery'
            statuses = successful_status_batches(recovered['records'], event_id)
            sizes = [len(r['body']['statuses']) for r in statuses]
            self.report['batch_sizes'] = sizes
            assert sizes == [500, 500, 251], f'Unexpected successful receipt batches: {sizes}'
            assert all(r['response']['message']['accepted'] == len(r['body']['statuses']) for r in statuses)
            assert all(len(json.dumps(r['body']['statuses'], separators=(',', ':')).encode()) <= 512 * 1024 for r in statuses)
            self.site.call('wait', kind='read', id='')
            assert not fast_errors, 'Concurrent inbound projection failed'
            self.emit(storm)
            # Late delivered must not regress Read.
            late = receipt_storm(self.fixture['historical'][:1])
            late['entry'][0]['changes'][0]['value']['statuses'] = late['entry'][0]['changes'][0]['value']['statuses'][1:2]
            self.emit(late)
            late_state = self.wait_control(lambda s: all(c['acknowledged'] for c in s['callbacks'] if c['lane'] == 'status'))
            late_id = max(e['id'] for e in late_state['events'] if e['lane'] == 'status')
            self.site.call('wait', kind='event', id=late_id)
            self.site.call('wait', kind='read', id='')
            assert not [r for r in self.control('state')['records'] if r['kind'] == 'graph' and r['path'].endswith('/messages')], 'Restart resent a message'
            evidence.update(receipts=1251, messages=417, final_status='Read', held_inbound_samples=5)
        with self.step('10b', continue_on_error=True) as evidence:
            count = self.args.storm_receipts
            providers = self.site.call('seed_storm', run=self.run_id, count=math.ceil(count / 3))
            previous_ids = {e['id'] for e in self.control('state')['events']}
            for payload in sustained_storm(providers, count):
                self.emit(payload)
            processing_samples = self.sample_receipt_projection()
            state = self.wait_control(lambda s: all(c['acknowledged'] for c in s['callbacks'] if c['lane'] == 'status'),
                                      timeout=max(60, count / 100))
            storm_ids = [e['id'] for e in state['events'] if e['lane'] == 'status' and e['id'] not in previous_ids]
            batches = [r for event_id in storm_ids for r in successful_status_batches(state['records'], event_id)]
            assert sum(len(r['body']['statuses']) for r in batches) == count, 'Incomplete storm delivery'
            timings = self.wait_projection_timings(storm_ids, len(batches))
            self.report['storm_latency_ms'] = storm_latency(self.report['inbound_samples'], timings, storm_ids, count)
            evidence.update(self.report['storm_latency_ms'], messages=len(providers),
                            processing_inbound_samples=processing_samples, event_ids=storm_ids)
            assert self.report['storm_latency_ms']['passed'], 'Storm p95 must be < 500 ms with at least five samples overlapping actual receipt projection'
        with self.step('12', continue_on_error=True) as evidence:
            template = {'name': 'sim_v1_update_' + self.run_id, 'language': 'en_US', 'category': 'UTILITY',
                        'components': [{'type': 'BODY', 'text': 'Local simulation order {{1}}'}]}
            result = self.core('relay_core.whatsapp.execute', {'operation': 'templates.create', 'arguments': template})
            assert result['status'] == 'PENDING'
            other = dict(template, name=template['name'] + '_page')
            self.core('relay_core.whatsapp.execute', {'operation': 'templates.create', 'arguments': other})
            page = self.core('relay_core.whatsapp.execute', {'operation': 'templates.list'})
            assert page['data'][0]['id'] == result['id'] and page['data'][0]['status'] == 'PENDING'
            second = self.core('relay_core.whatsapp.execute', {'operation': 'templates.list', 'arguments': {'after': page['paging']['cursors']['after']}})
            assert second['data'][0]['name'] == other['name']
            before = time.monotonic_ns()
            assert self.control('template', {'id': result['id'], 'status': 'APPROVED'})['status'] == 200
            self.site.call('wait', kind='template', id=result['id'])
            assert self.realtime, 'Authenticated Socket.IO connection unavailable'
            self.realtime.wait('templates', before)
            evidence.update(template_id=result['id'], status='APPROVED', pages=2, realtime=True)
        with self.step('13', continue_on_error=True) as evidence:
            evidence.update(self.ai_acceptance())
        with self.step('14', continue_on_error=True) as evidence:
            evidence.update(self.business_event_acceptance())
        # A transport-only idle observation makes no periodic HTTP message/status reads.
        if self.realtime and self.report['steps']['realtime']['status'] == 'pass':
            with self.step('realtime', continue_on_error=True):
                idle_start = time.monotonic()
                time.sleep(31)
                assert not self.realtime.error, 'Socket.IO failed during idle observation: ' + str(self.realtime.error)
                assert not self.denied.error, 'Unauthorized Socket.IO observer disconnected: ' + str(self.denied.error)
                assert not any(e['event'] == ['relay_changed', {'kind': 'messages'}] for e in self.denied.events), 'Unauthorized message event'
                self.report['steps']['realtime']['idle_seconds'] = time.monotonic() - idle_start

    def finish(self):
        if self.process and self.process.poll() is None:
            try:
                state = self.control('state')
                self.captures.extend(state.pop('records'))
                self.report['hub_state'] = state
            except Exception:
                pass
        if self.realtime:
            self.report['realtime_events'] = self.realtime.events
            self.realtime.close()
        if self.denied:
            self.denied.close()
        self.stop_hub()
        if self.site:
            try:
                self.site.call('acceptance_disable')
            except Exception as exc:
                self.report['cleanup_error'] = str(exc)
                self.report['error'] = 'Simulation automation could not be disabled'
            try:
                self.report['core_diagnostics'] = self.site.call('diagnostics')
            except Exception:
                pass
            self.site.close()
        for log in self.logs:
            log.close()
        self.report['captures'] = self.captures
        self.report['inbound_breakdown'] = []
        for sample in self.report['inbound_samples']:
            try:
                self.report['inbound_breakdown'].append(inbound_breakdown(self.captures, sample))
            except (StopIteration, KeyError):
                self.report.setdefault('timing_errors', []).append(sample['provider'])
        self.report['latency_ms'] = {'p50': percentile(self.report['latencies_ms'], 50),
                                     'p95': percentile(self.report['latencies_ms'], 95),
                                     'measurement': 'signed webhook send to observed Core row (CLOCK_MONOTONIC); 20 ms DB observation interval, no HTTP polling'}
        statuses = [s['status'] for s in self.report['steps'].values()]
        self.report['passed'] = all(s == 'pass' for s in statuses) and 'error' not in self.report
        (self.args.report_dir / 'report.json').write_text(json.dumps(self.report, indent=2, default=str) + '\n')
        lines = ['# Connected WhatsApp simulation', '', f"Mode: {self.report['mode']}. Passed: {self.report['passed']}.", '',
                 '| Step | Status | Evidence / error |', '| --- | --- | --- |']
        for key, step in self.report['steps'].items():
            lines.append(f"| {key}: {step['name']} | {step['status']} | {step.get('error', '')} |")
        lines += ['', f"Latency (ms): {self.report['latency_ms']}", f"Storm latency (ms): {self.report.get('storm_latency_ms')}",
                  f"Receipt batches: {self.report['batch_sizes']}", '',
                  *self.report['limitations']]
        if 'error' in self.report:
            lines += ['', self.report['error']]
        (self.args.report_dir / 'report.md').write_text('\n'.join(lines) + '\n')


def arguments(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--site', default='yrp.site')
    parser.add_argument('--sites', type=Path, default=BENCH / 'sites')
    parser.add_argument('--frappe-python', type=Path, default=BENCH / 'env/bin/python')
    parser.add_argument('--core-origin', type=loopback_origin, default='http://127.0.0.1:8001')
    parser.add_argument('--socketio-origin', type=loopback_origin, default='http://127.0.0.1:9001')
    parser.add_argument('--hub-addr', type=address, default='127.0.0.1:18091')
    parser.add_argument('--meta-addr', type=address, default='127.0.0.1:18094')
    parser.add_argument('--report-dir', type=Path, default=ROOT / '.runtime/sim-report')
    parser.add_argument('--storm-receipts', type=int, default=12000, help='Receipt count for step 10b (default: 12000)')
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args(argv)
    if args.storm_receipts <= 0:
        parser.error('--storm-receipts must be positive')
    args.report_dir = args.report_dir.resolve()
    if not (args.report_dir.is_relative_to(ROOT) or args.report_dir.is_relative_to('/tmp')):
        parser.error('Report directory must be inside Hub or /tmp')
    return args


def main(argv=None):
    args = arguments(argv)
    runner = Runner(args)
    try:
        runner.execute()
    except Exception as exc:
        runner.report['error'] = str(exc)
    finally:
        runner.finish()
    print(f"{runner.report['mode']}: {'PASS' if runner.report['passed'] else 'NOT VALIDATED'}; {args.report_dir / 'report.md'}")
    return 0 if runner.report['passed'] or (args.dry_run and 'error' not in runner.report) else 1


if __name__ == '__main__':
    sys.exit(main())
