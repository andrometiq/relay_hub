"""Shared local-only HTTP, fixture process and Socket.IO simulation transports."""
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
            env={**os.environ, **self.db_env, 'RELAY_SIM_CONTROL_TOKEN': self.control_token, **(extra_env or {})},
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
