#!/usr/bin/env python3
"""Connected WhatsApp acceptance. No provider traffic and no HTTP message polling."""
import argparse
from contextlib import contextmanager
import hashlib
import json
import math
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

from simulation_fixtures import run_label
from whatsapp_bulk_campaign import BulkCampaign
from simulation_common import (ROOT, BENCH, loopback_origin, address, NoRedirect, request,
                               percentile, read_env, Site, Realtime, SimulationRunner)
STEPS = {'7': 'Signed ingress', '8': 'Journal, Core projection and deduplication',
         '9': 'Core send idempotency', '10': '1,251 receipt batch check',
         '10b': 'inbound latency under a sustained receipt storm',
         '11': '503, wrong acknowledgement and restart recovery', '12': 'Template lifecycle',
         '13': 'Supplier AI, scoped ledger and human handoff',
         '14': 'Business event delivery and Hub outage isolation',
         'realtime': 'Authenticated Socket.IO message event without HTTP polling',
         'bulk': 'Bulk campaign through Core: pause, resume, cancel and exact counters'}


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
    assert len(state['tickets']) == 1, 'Expected one ticket and held episode'
    ticket = state['tickets'][0]
    assert ticket['ai_disabled'] and ticket['active_scope_key'], 'Expected an active episode hold'
    assert not state['paused'], 'Handoff must not pause every conversation for this contact'
    assert ticket['assigned_to'] == fixture['handoff_owner']
    assert ticket['business_doctype'] == 'Supplier' and ticket['business_name'] == fixture['supplier']
    assert len(state['runs']) == 1 and ticket['source_run'] == fixture['source_run']
    assert state['runs'][0]['ticket'] == ticket['name']
    assert state['runs'][0]['status'] == 'Handoff'
    assert not state['actions'] and not state['outgoing'], 'Handoff must not send an automatic reply'


def receipt_storm(ids, count=None, endpoint='919999999999'):
    stamp = int(time.time())
    statuses = [
        {'id': provider, 'recipient_id': endpoint, 'status': status, 'timestamp': str(stamp + offset)}
        for provider in ids for offset, status in enumerate(('sent', 'delivered', 'read'))]
    return envelope({'metadata': {'phone_number_id': '110'}, 'statuses': statuses[-count:] if count else statuses})


def sustained_storm(ids, count, endpoint='919999999999'):
    statuses = receipt_storm(ids, count, endpoint)['entry'][0]['changes'][0]['value']['statuses']
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
        s['started_ns'] < t['finished_ns'] and s['observed_ns'] >= t['started_ns'] for t in spans)]
    duration = (max(t['finished_ns'] for t in spans) - min(t['started_ns'] for t in spans)) / 1e9 if spans else None
    return {'samples': len(overlapping), 'p50': percentile(overlapping, 50), 'p95': percentile(overlapping, 95),
            'target_ms': 500, 'passed': len(overlapping) >= 5 and percentile(overlapping, 95) < 500,
            'receipt_count': receipt_count, 'status_projection_seconds': duration,
            'receipts_per_second': receipt_count / duration if duration else None}


class Runner(SimulationRunner):
    def __init__(self, args):
        self.args = args
        self.hub = 'http://' + args.hub_addr
        self.meta = 'http://' + args.meta_addr
        self.control_token = secrets.token_urlsafe(32)
        self.run_id = run_label()
        self.run_epoch_ns = time.time_ns()
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


    def start(self, resume=False):
        self.launch([str(self.binary), '-test.run=^TestLauncherProcess$', '--', '--addr', self.args.hub_addr,
            '--meta-addr', self.args.meta_addr, '--core-origin', self.args.core_origin,
            '--state-dir', str(self.state_dir), '--resume' if resume else '--migrate'], {'RELAY_SIM_CHILD': '1'})



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
        result = self.emit(payload or inbound(provider, self.fixture['endpoint']))
        observed = self.site.call('wait', kind='message', id=provider, timeout=5, group='storm' if phase == 'processing' else 'historical')
        # Both clocks are CLOCK_MONOTONIC on this host; control overhead is separately visible.
        self.record_inbound(provider, result, observed, phase)
        return observed

    def sample_receipt_projection(self, event_ids):
        deadline = time.monotonic() + max(60, self.args.storm_receipts / 100)
        count = 0
        next_sample = time.monotonic()
        while time.monotonic() < deadline:
            time.sleep(max(0, next_sample - time.monotonic()))
            observed = self.observe_inbound(f'wamid.sim.processing.{self.run_id}.{count}', phase='processing')
            count += 1
            if observed['receipts_complete']:
                return count
            window = self.site.call('receipt_window', event_ids=event_ids)
            now = time.monotonic()
            if window:
                # Status callbacks run once per second. Aim the *observation*
                # at the next projection's midpoint using measured worker times
                # and our last inbound latency. Free-running samples can alias
                # the ~100 ms projection windows and leave only four overlaps.
                latency = self.report['inbound_samples'][-1]['latency_ms'] / 1000
                midpoint = (window['started_ns'] + window['finished_ns']) / 2e9
                next_sample = midpoint + 1 - latency
                if next_sample < now:
                    next_sample += math.floor(now - next_sample) + 1
            else:
                next_sample = now  # Bootstrap until the first real timing exists.
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
        self.assert_search_announcement(offered)
        fixture['source_run'] = offered['runs'][0]['name']
        confirmed = f'wamid.sim.ai.confirm.{self.run_id}'
        payload = inbound(confirmed, fixture['endpoint'], 'yes')
        self.emit(payload)
        result = self.check_ai_reply(confirmed, fixture, 2)
        assert_ledger_reply(result['outgoing'][0]['content'])
        self.assert_confirmed_search(offered, result)
        identity_denied = self.assert_identity_mismatch(fixture, result['incoming'][0]['name'])
        self.emit(payload)
        self.wait_control(lambda state: all(row['acknowledged'] for row in state['callbacks'] if row['lane'] == 'realtime'))
        assert len(provider_posts(self.control('state'), fixture['endpoint'])) == 2, 'Replay resent AI reply'
        person = f'wamid.sim.ai.person.{self.run_id}'
        payload = inbound(person, fixture['endpoint'], 'talk to a human')
        self.emit(payload)
        result = self.site.call('acceptance_wait_ai', provider=person, status='Handoff')
        self.emit(payload)
        paused = f'wamid.sim.ai.paused.{self.run_id}'
        self.emit(inbound(paused, fixture['endpoint'], 'talk to a human'))
        self.wait_control(lambda state: all(row['acknowledged'] for row in state['callbacks'] if row['lane'] == 'realtime'))
        self.site.call('acceptance_wait_paused', provider=paused)
        result = self.site.call('acceptance_ai_state', provider=person)
        assert_handoff(result, fixture)
        state = self.control('state')
        assert len(provider_posts(state, fixture['endpoint'])) == 2, 'Handoff or paused conversation sent an AI reply'
        ai_calls = [row for row in state['records'] if row['kind'] == 'ai']
        assert len(ai_calls) == 6 and all(row['status'] == 200 for row in ai_calls), 'Expected classifier, declaration, read/result and identity-denied read/result; human request bypasses AI'
        assert all(marker not in json.dumps(ai_calls) for marker in ('SIM-OTHER-SUPPLIER-SECRET', 'SIM-OUTSIDE-RANGE', 'SIM-CANCELLED'))
        return dict(supplier=fixture['supplier'], accepted_provider_posts=2, fake_ai_calls=len(ai_calls),
                    ticket=result['tickets'][0]['name'], statuses=['Sent', 'Delivered', 'Read'], episode_held=True,
                    mismatched_identity_denied=identity_denied, search_announced_before_data=True)

    def business_event_acceptance(self):
        template = self.report['steps']['12'].get('template_id')
        assert template, 'Step 12 must provide an approved template'
        self.site.call('acceptance_seed_event', run=self.run_id, template_id=template)
        before = len(provider_posts(self.control('state'), self.fixture['endpoint']))
        submitted = self.site.call('acceptance_submit_event')
        state = self.site.call('acceptance_wait_event', source=submitted['source'])
        assert len(state['deliveries']) == len(state['actions']) == len(state['messages']) == 1
        assert state['actions'][0]['status'] == 'Submitted'
        assert state['deliveries'][0]['business_name'] == self.fixture['customer'] and state['docstatus'] == 1
        message = state['messages'][0]
        hub = self.control('state')
        posts = provider_posts(hub, self.fixture['endpoint'])
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
                'socketio': self.args.socketio_origin, 'database': 'isolated schema in relay_hub_v1_test@127.0.0.1:18433',
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
        self.lock_site()
        self.select_run()
        self.prepare_database()
        self.build_launcher()
        self.start()
        self.unique_sequences()
        self.fixture = self.site.call('provision', run=self.run_id)
        self.callback_secret = secrets.token_urlsafe(32)
        self.configure()
        self.report['fixture'] = {key: self.fixture[key] for key in ('contact', 'channel', 'customer', 'person', 'endpoint')}
        with self.step('realtime', continue_on_error=True):
            self.realtime = Realtime(self.args.socketio_origin, self.canonical, self.args.site, self.fixture['operator'], self.fixture['contact'])
            self.denied = Realtime(self.args.socketio_origin, self.canonical, self.args.site, self.fixture['unauthorized'], self.fixture['contact'])
        first = f'wamid.sim.in.{self.run_id}'
        first_payload = inbound(first, self.fixture['endpoint'])
        row = emitted = None
        started = time.monotonic_ns()
        if self.args.bulk_recipients:
            for key, step in self.report['steps'].items():
                if key not in ('realtime', 'bulk'):
                    step['status'] = 'skipped'
            with self.step('bulk', continue_on_error=True) as evidence:
                bulk = BulkCampaign(self, self.args.bulk_recipients)
                try:
                    bulk.run()
                finally:
                    evidence.update(bulk.evidence)
                assert evidence['campaign']['passed'] and evidence['cancel']['passed'], 'Bulk campaign truth did not match'
            return
        self.report['steps']['bulk']['status'] = 'skipped'
        with self.step('7', continue_on_error=True):
            self.emit(inbound(first, self.fixture['endpoint']), invalid_signature=True)
            wrong = inbound(first, self.fixture['endpoint'])
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
            assert post['body']['to'] == self.fixture['endpoint'] and post['body']['messaging_product'] == 'whatsapp'
            assert post['body']['context']['message_id'] == first and post['body']['text']['body'] == 'Simulation reply'
            assert post['body']['biz_opaque_callback_data'] == 'relay-outbox:' + str(deliveries[0]['id'])
            outgoing = self.site.call('wait', kind='message', id=deliveries[0]['provider_message_id'])
            assert outgoing['rows'][0]['direction'] == 'Outgoing'
            assert len([a for a in self.site.call('snapshot')['actions'] if a['request_key'] == key]) == 1
            evidence.update(action=first_send['action'], outbox_id=deliveries[0]['id'], accepted_provider_posts=1)
        # Hold only receipts so a fresh inbound exercises the independent realtime lane.
        storm = receipt_storm(self.fixture['historical'], endpoint=self.fixture['endpoint'])
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
            late = receipt_storm(self.fixture['historical'][:1], endpoint=self.fixture['endpoint'])
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
            for payload in sustained_storm(providers, count, self.fixture['endpoint']):
                self.emit(payload)
            committed = self.control('state')
            storm_ids = [e['id'] for e in committed['events'] if e['lane'] == 'status' and e['id'] not in previous_ids]
            processing_samples = self.sample_receipt_projection(storm_ids)
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
            template = {'name': 'sim_update_' + self.run_id.lower().replace('-', '_'), 'language': 'en_US', 'category': 'UTILITY',
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
                self.site.call('restore')
            except Exception as exc:
                self.report['cleanup_error'] = str(exc)
                self.report['error'] = 'Simulation automation could not be disabled'
            try:
                self.report['core_diagnostics'] = self.site.call('diagnostics')
            except Exception:
                pass
            self.site.close()
        self.close_database()
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
        self.report['passed'] = all(s in ('pass', 'skipped') for s in statuses) and 'error' not in self.report
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
    parser.add_argument('--site', choices=['yrp.site'], default='yrp.site')
    parser.add_argument('--sites', type=Path, default=BENCH / 'sites')
    parser.add_argument('--frappe-python', type=Path, default=BENCH / 'env/bin/python')
    parser.add_argument('--core-origin', type=loopback_origin, default='http://127.0.0.1:8001')
    parser.add_argument('--socketio-origin', type=loopback_origin, default='http://127.0.0.1:9001')
    parser.add_argument('--hub-addr', type=address, default='127.0.0.1:18091')
    parser.add_argument('--meta-addr', type=address, default='127.0.0.1:18094')
    parser.add_argument('--report-dir', type=Path, default=ROOT / '.runtime/sim-report')
    parser.add_argument('--storm-receipts', type=int, default=12000, help='Receipt count for step 10b (default: 12000)')
    parser.add_argument('--bulk-recipients', type=int, default=0,
                        help='Run only the bulk campaign step with this many recipients (default: off)')
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args(argv)
    if args.sites.resolve() != (BENCH / 'sites').resolve():
        parser.error('Sites directory must be the local development bench')
    if not 0 <= args.bulk_recipients <= 20000:
        parser.error('--bulk-recipients must be 0-20000')
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
