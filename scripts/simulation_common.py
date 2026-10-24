"""Shared local-only HTTP, fixture process and Socket.IO simulation transports."""
from simulation_fixtures import run_number
import fcntl
import ipaddress
import json
import math
import os
from pathlib import Path
import queue
import socket
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
BENCH = Path('/home/sakthi/development/pilot/benches/version_16')


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


def request(url, data=None, headers=None, raw=False):
    loopback_origin(urllib.parse.urlunsplit((*urllib.parse.urlsplit(url)[:2], '', '', '')))
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    body = json.dumps(data, separators=(',', ':')).encode() if data is not None else None
    req = urllib.request.Request(url, data=body, headers={'Content-Type': 'application/json', **(headers or {})})
    try:
        with opener.open(req, timeout=20) as reply:
            return reply.read() if raw else json.load(reply)
    except urllib.error.HTTPError as exc:
        # Retain only Frappe's exception class, never its credential-bearing traceback/body.
        try:
            kind = json.load(exc).get('exc_type', '')
        except (ValueError, AttributeError):
            kind = ''
        suffix = f' ({kind})' if isinstance(kind, str) and len(kind) <= 100 and kind.isidentifier() else ''
        raise RuntimeError(f'HTTP {exc.code} at {urllib.parse.urlsplit(url).path}'+suffix) from None


def percentile(values, percent):
    return sorted(values)[max(0, math.ceil(len(values) * percent / 100) - 1)] if values else None


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


class Site:
    def __init__(self, args, log, helper="whatsapp_sim_site.py"):
        self.process = subprocess.Popen([str(args.frappe_python), '-B', str(ROOT / 'scripts' / helper),
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


class SimulationRunner:
    def assert_search_announcement(self, state):
        run = state['runs'][0]
        plan = json.loads(run['search_plan'])
        decision = json.loads(run['decision'])
        match = json.loads(run['match_result'])
        assert match['method'] == 'classifier' and match['selected'], 'Event was not classified'
        assert run['stage'] == 'Search' and run['status'] == 'Completed'
        assert decision['reads'] == [] and not state['flow_actions'], 'Business action ran before confirmation'
        assert plan['confirmed_by'] is None and plan['consumed_by'] is None
        assert state['actions'][0]['status'] == 'Submitted' and state['outgoing'][0]['provider_id']
        text = state['outgoing'][0]['content']
        assert text.startswith('I will search supplier_ledger to '), 'Missing search announcement'
        assert all(value in text for value in ('2026-10-01', '2026-10-31', 'Fields:', 'Search:', 'confirm'))
        calls = [r for r in self.control('state')['records'] if r['kind'] == 'ai']
        assert len(calls) == 2, 'Expected classifier and search declaration before confirmation'
        assert not any(m['role'] == 'tool' for r in calls for m in r['body']['messages']), 'Read before announcement'
        assert not any(marker in json.dumps(calls) + text for marker in ('SIM-OWN-DEBIT', 'SIM-OWN-CREDIT'))

    def assert_confirmed_search(self, offered, state):
        origin, run = offered['runs'][0], state['runs'][0]
        plan = json.loads(run['search_plan'])
        assert run['stage'] == 'Event' and run['ticket'] == origin['ticket']
        assert plan['origin'] == origin['name'] and plan['confirmed_by'] == state['incoming'][0]['name']
        assert plan['consumed_by'] == run['name']
        assert plan['plan'] == json.loads(origin['search_plan'])['plan']
        assert len(json.loads(run['decision'])['reads']) == 1, 'Expected scoped tool evidence'
        assert len(state['flow_actions']) == 1, 'Expected one ordered reply action'
        action = state['flow_actions'][0]
        assert action['action'] == 'reply' and action['status'] == 'Completed' and action['step'] == 0
        assert json.loads(action['result'])['reply'] == state['outgoing'][0]['content']
        # The first provider send was checked as accepted before confirmation
        # was emitted; compare the persisted timeline as well.
        assert offered['outgoing'][0]['creation'] < state['outgoing'][0]['creation'], 'Ledger preceded announcement'
        records = self.control('state')['records']
        announcements = [r for r in records if r['kind'] in ('graph', 'telegram', 'smtp')
            and 'I will search supplier_ledger to ' in json.dumps(r['body'])]
        reads = [r for r in records if r['kind'] == 'ai' and any(
            m['role'] == 'tool' and 'SIM-OWN-DEBIT' in m['content'] for m in r['body']['messages'])]
        assert len(announcements) == len(reads) == 1, 'Missing unique provider announcement or ledger result'
        assert announcements[0]['status'] in (200, 250), 'Search announcement was not accepted'
        assert announcements[0]['finished_ns'] < reads[0]['started_ns'], 'Ledger data reached AI before announcement'

    def assert_identity_mismatch(self, fixture, message):
        before = len([r for r in self.control('state')['records'] if r['kind'] == 'ai'])
        try:
            self.site.call('acceptance_set_ai_identity', matched=False)
            result = self.site.call('acceptance_check_identity', message=message)
            assert result['handoff'] and not result['reply'] and not result.get('reads'), (
                'A sender with a mismatched Supplier identity received ledger data')
        finally:
            self.site.call('acceptance_set_ai_identity', matched=True)
        calls = [r for r in self.control('state')['records'] if r['kind'] == 'ai'][before:]
        assert len(calls) == 2 and all(r['status'] == 200 for r in calls), 'Expected a ledger tool request and denial'
        tool = calls[0]['response']['choices'][0]['message']['tool_calls'][0]['function']
        assert tool['name'] == 'SEARCH' and json.loads(tool['arguments'])['reference'] == 'supplier_ledger', (
            'Identity check did not attempt the declared ledger read')
        results = [m for m in calls[1]['body']['messages'] if m['role'] == 'tool']
        assert len(results) == 1 and set(json.loads(results[0]['content'])) == {'error'}, (
            'Mismatched identity exposed a ledger tool result')
        assert all(marker not in json.dumps(calls) for marker in (
            'SIM-OWN-DEBIT', 'SIM-OWN-CREDIT', 'SIM-OTHER-SUPPLIER-SECRET', 'SIM-OUTSIDE-RANGE', 'SIM-CANCELLED'))
        return True

    def select_run(self):
        used = [path.name.removeprefix('provider-') for path in self.args.report_dir.glob('provider-*')]
        self.run_id = self.site.call('run_label', used=used)
        self.report['run'] = self.run_id

    def lock_site(self):
        self.run_lock = open(ROOT / '.runtime/channel-simulation.lock', 'a')
        try:
            fcntl.flock(self.run_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError('Another connected channel driver owns the local site') from None

    def prepare_database(self):
        import psycopg2
        from psycopg2 import sql
        self.database = psycopg2.connect(test_database_url(), connect_timeout=5)
        self.database.autocommit = True
        schema = 'relay_sim_test_'+self.run_id.lower().replace('-', '_')
        with self.database.cursor() as cur:
            cur.execute(sql.SQL('CREATE SCHEMA {}').format(sql.Identifier(schema)))
        self.schema = schema
        self.db_env = dict(DATABASE_URL=test_database_url()+'?search_path='+self.schema,
            RELAY_CREDENTIAL_KEY=read_env()['RELAY_CREDENTIAL_KEY'])

    def build_launcher(self):
        self.binary = ROOT / '.runtime/relay-sim-connected.test'
        self.state_dir = self.args.report_dir / ('provider-'+self.run_id)
        self.state_dir.mkdir(mode=0o700)
        # A fresh fake mailbox must have a new UIDVALIDITY: Core retains its old provider IDs.
        with (self.state_dir / 'email.json').open('x') as state:
            json.dump(dict(uid_validity=run_number(self.run_id) + 1, uid_next=1,
                           sent=[], inbox=[], faults={}), state)
        (self.state_dir / 'email.json').chmod(0o600)
        subprocess.run(['go','test','-c','-o',str(self.binary),'./cmd/relay-sim'], cwd=ROOT,
            env={**os.environ,'GOCACHE':'/tmp/relay-sim-gocache'}, check=True,
            stdout=self.open_log('build.log'),stderr=subprocess.STDOUT)

    def unique_sequences(self):
        # Core retains event identities between runs; isolated Hub schemas must not reuse them.
        with self.database.cursor() as cur:
            cur.execute('SELECT sequencename FROM pg_sequences WHERE schemaname=%s', (self.schema,))
            for (sequence,) in cur.fetchall():
                cur.execute('SELECT setval(%s::regclass,%s,false)',
                    (self.schema+'.'+sequence, self.run_epoch_ns//1000))

    def close_database(self):
        if getattr(self, 'database', None):
            from psycopg2 import sql
            if hasattr(self, 'schema'):
                with self.database.cursor() as cur:
                    cur.execute(sql.SQL('DROP SCHEMA {} CASCADE').format(sql.Identifier(self.schema)))
            self.database.close()
        if getattr(self, 'run_lock', None):
            self.run_lock.close()

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

    def configure(self):
        result = self.control('provision', {'site': self.canonical, 'api_key': self.fixture['service']['api_key'],
            'api_secret': self.fixture['service']['api_secret'], 'callback_secret': self.callback_secret})
        self.site.call('settings', hub_url=self.hub + '/mcp', token=result['token'], secret=self.callback_secret)
        assert self.core('relay_core.settings.verify_connection', {})['verified']


    def launch(self, command, extra_env=None):
        self.process = subprocess.Popen(command, cwd=ROOT,
            env={**os.environ, **self.db_env, 'RELAY_SIM_CONTROL_TOKEN': self.control_token,
                 'RELAY_SIM_RUN_LABEL': self.run_id, **(extra_env or {})},
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


def test_database_url():
    values = read_env()
    dsn = os.environ.get('RELAY_TEST_DATABASE_URL')
    if not dsn:
        source = urllib.parse.urlsplit(values['DATABASE_URL'])
        assert source.hostname == '127.0.0.1' and source.port == 18433, 'Unexpected database source'
        dsn = urllib.parse.urlunsplit(source._replace(path='/relay_hub_v1_test', query='', fragment=''))
    validate_test_database(dsn)
    return dsn


def validate_test_database(dsn):
    parsed = urllib.parse.urlsplit(dsn)
    if (parsed.scheme not in ('postgres','postgresql') or parsed.hostname != '127.0.0.1'
            or parsed.port != 18433 or parsed.path != '/relay_hub_v1_test' or parsed.query or parsed.fragment):
        raise ValueError('Only relay_hub_v1_test at 127.0.0.1:18433 without query overrides is allowed')
