"""Connected channel runner using the WhatsApp transport and fixture protocol."""
import argparse
from contextlib import contextmanager
import json
import fcntl
import os
from pathlib import Path
import secrets
import subprocess
import time
import traceback
import urllib.parse
from simulation_common import (ROOT, BENCH, Site, Realtime, SimulationRunner, address,
                               loopback_origin, percentile, read_env)


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


class ChannelRunner(SimulationRunner):
    def __init__(self, args, medium):
        self.args, self.medium = args, medium
        self.run_id = str(time.time_ns())
        self.hub, self.meta = 'http://'+args.hub_addr, 'http://'+args.meta_addr
        self.control_token, self.callback_secret = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
        self.process = self.site = self.realtime = self.denied = self.database = self.run_lock = None
        self.logs, self.captures = [], []
        self.fixture = {}
        self.report = dict(run=self.run_id, channel=medium, site=args.site, steps={}, latencies_ms=[],
            limitations=['AI/automation and handoff skipped: next Core wave.',
                         'Real Socket.IO and permission-checked HTTP APIs; browser rendering is not exercised.',
                         '12,000-event latency acceptance is a separate plan item.'])
        args.report_dir.mkdir(parents=True, exist_ok=True)

    @contextmanager
    def step(self, name, continue_on_error=False):
        print(name, flush=True)
        row = self.report['steps'][name] = {'status':'running'}
        start = time.monotonic()
        try:
            yield row
        except Exception as exc:
            detail = str(exc) if isinstance(exc, (AssertionError, RuntimeError, TimeoutError)) else type(exc).__name__
            row.update(status='fail', error=detail or type(exc).__name__,
                location=':'.join(map(str,(traceback.extract_tb(exc.__traceback__)[-1].filename,
                                          traceback.extract_tb(exc.__traceback__)[-1].lineno))))
            if not continue_on_error:
                raise
        else:
            row['status'] = 'pass'
        finally:
            row['seconds'] = round(time.monotonic()-start, 3)

    def start(self, resume=False):
        # The existing test entry runs the actual launcher with its stricter test DB guard.
        command = [str(self.binary), '-test.run=^TestLauncherProcess$', '--', '--addr', self.args.hub_addr,
            '--meta-addr', self.args.meta_addr, '--core-origin', self.args.core_origin,
            '--smtp-addr', self.args.smtp_addr, '--imap-addr', self.args.imap_addr,
            '--state-dir', str(self.state_dir), '--resume' if resume else '--migrate']
        self.launch(command, {'RELAY_SIM_CHILD': '1'})

    def setup(self):
        import psycopg2
        from psycopg2 import sql
        self.run_lock = open(ROOT / '.runtime/channel-simulation.lock', 'a')
        try:
            fcntl.flock(self.run_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError('Another connected channel driver owns the local site') from None
        self.site = Site(self.args, self.open_log('site.log'), 'channel_sim_site.py')
        self.report['preflight'] = self.site.call('preflight')
        self.canonical = self.report['preflight']['canonical_origin']
        self.database = psycopg2.connect(test_database_url(), connect_timeout=5)
        self.database.autocommit = True
        schema = 'relay_sim_test_'+self.run_id
        with self.database.cursor() as cur:
            cur.execute(sql.SQL('CREATE SCHEMA {}').format(sql.Identifier(schema)))
        self.schema = schema
        self.db_env = dict(DATABASE_URL=test_database_url()+'?search_path='+self.schema,
            RELAY_CREDENTIAL_KEY=read_env()['RELAY_CREDENTIAL_KEY'])
        self.binary = ROOT / '.runtime/relay-sim-connected.test'
        self.state_dir = self.args.report_dir / ('provider-'+self.run_id)
        self.state_dir.mkdir(mode=0o700)
        # A fresh fake mailbox must have a new UIDVALIDITY: Core retains its old provider IDs.
        with (self.state_dir / 'email.json').open('x') as state:
            json.dump(dict(uid_validity=int(self.run_id)//1000000000, uid_next=1,
                           sent=[], inbox=[], faults={}), state)
        (self.state_dir / 'email.json').chmod(0o600)
        subprocess.run(['go','test','-c','-o',str(self.binary),'./cmd/relay-sim'], cwd=ROOT,
            env={**os.environ,'GOCACHE':'/tmp/relay-sim-gocache'}, check=True,
            stdout=self.open_log('build.log'),stderr=subprocess.STDOUT)
        self.start()
        # Core retains event identities between runs; isolated Hub schemas must not reuse them.
        with self.database.cursor() as cur:
            cur.execute('SELECT sequencename FROM pg_sequences WHERE schemaname=%s', (self.schema,))
            for (sequence,) in cur.fetchall():
                cur.execute('SELECT setval(%s::regclass,%s,false)',
                    (self.schema+'.'+sequence, int(self.run_id)//1000))
        self.fixture = self.site.call('provision', run=self.run_id, medium=self.medium)
        self.report['fixture'] = {k:v for k,v in self.fixture.items() if k not in ('service','operator','unauthorized')}
        self.configure()
        self.realtime = Realtime(self.args.socketio_origin,self.canonical,self.args.site,self.fixture['operator'],self.fixture['contact'])
        self.realtime.ws.send('42'+self.realtime.ns+','+json.dumps(['doc_subscribe','Relay Contact',self.fixture['shared_contact']]))
        self.denied = Realtime(self.args.socketio_origin,self.canonical,self.args.site,self.fixture['unauthorized'],self.fixture['contact'])
        self.denied.ws.send('42'+self.denied.ns+','+json.dumps(['doc_subscribe','Relay Contact',self.fixture['shared_contact']]))

    def stop_hub(self):
        if self.process and self.process.poll() is None:
            try:
                self.captures.extend(self.control('state')['records'])
            except (OSError, RuntimeError):
                self.report['capture_error'] = 'Could not capture running Hub before shutdown'
        super().stop_hub()

    def snapshot(self):
        return self.site.call('snapshot')

    def observe(self, emit, predicate, kind='messages', timeout=35):
        """Read only after real Socket.IO invalidations, never poll Core."""
        since = started = time.monotonic_ns()
        emit()
        deadline = time.monotonic()+timeout
        while time.monotonic()<deadline:
            event = self.realtime.wait(kind, since, max(.01,deadline-time.monotonic()))
            since = event['monotonic_ns']+1
            state = self.snapshot()
            result = predicate(state)
            if result:
                elapsed = (time.monotonic_ns()-started)/1e6
                self.report['latencies_ms'].append(elapsed)
                self.report.setdefault('latency_samples', []).append(dict(kind=kind,
                    ingress=getattr(self, 'last_ingress', 'unknown'), milliseconds=elapsed))
                return result
        raise TimeoutError('Socket.IO invalidation did not expose the expected Core state')

    def message(self, body, emit):
        def match(state):
            rows = [m for m in state['messages'] if m['content'].strip()==body and m['direction']=='Incoming']
            assert len(rows)<=1, 'Duplicate Core message'
            return rows[0] if rows else None
        row = self.observe(emit, match)
        visible = self.core('relay_core.api.messages',dict(contact=row['relay_contact'],channel=self.fixture['channel']))['items']
        assert any(m['name']==row['name'] for m in visible), 'Projected message is absent from authorized API'
        return row

    def send_observed(self, method, values, body):
        result = {}
        def submit():
            result.update(self.core(method, values))
        self.last_ingress='core_send'
        self.observe(submit, lambda state: next((m for m in state['messages']
            if m['direction']=='Outgoing' and m['content'].strip()==body), None))
        return result

    def providers(self):
        return self.control('providers')[self.medium.lower()]

    def wait_provider(self, predicate, timeout=35):
        deadline=time.monotonic()+timeout
        while time.monotonic()<deadline:
            state=self.providers()
            if predicate(state):return state
            time.sleep(.05)
        raise TimeoutError('Fake provider did not reach expected state')

    def permission_denied(self, method, data):
        try:
            self.core(method, data, self.fixture['unauthorized'])
        except RuntimeError as exc:
            assert str(exc).startswith('HTTP 403 '), str(exc)
        else:
            raise AssertionError('Unauthorized operation succeeded')

    def finish(self):
        try:
            if self.process and self.process.poll() is None:
                self.report['hub_state']=self.control('state')
                self.report['providers']=self.control('providers')
            if self.fixture:self.report['core_state']=self.snapshot()
        except Exception as exc:
            self.report['capture_error']=type(exc).__name__
        for connection in (self.realtime,self.denied):
            if connection:connection.close()
        self.stop_hub()
        if self.site:
            try:self.site.call('restore')
            except Exception as exc:self.report['cleanup_error']=type(exc).__name__
            self.site.close()
        if self.database:
            from psycopg2 import sql
            if hasattr(self,'schema'):
                with self.database.cursor() as cur:
                    cur.execute(sql.SQL('DROP SCHEMA {} CASCADE').format(sql.Identifier(self.schema)))
            self.database.close()
        if self.run_lock:self.run_lock.close()
        for log in self.logs:log.close()
        self.report['captures']=self.captures
        self.report['latency_basis']='Control emit start to event-gated Core observation (no HTTP polling)'
        self.report['latency_ms']={key:percentile(self.report['latencies_ms'],n) for key,n in [('p50',50),('p95',95),('p99',99)]}
        samples=self.report.get('latency_samples', [])
        self.report['latency_by_ingress']={ingress:dict(samples=len(values),p50=percentile(values,50),
            p95=percentile(values,95),p99=percentile(values,99)) for ingress in {s['ingress'] for s in samples}
            for values in [[s['milliseconds'] for s in samples if s['ingress']==ingress]]}
        self.report['passed']=bool(self.report['steps']) and all(s['status']=='pass' for s in self.report['steps'].values()) and not any(k in self.report for k in ('error','cleanup_error','capture_error'))
        (self.args.report_dir/'report.json').write_text(json.dumps(self.report,indent=2,default=str)+'\n')
        lines=[f'# Connected {self.medium} simulation', '', f"Passed: {self.report['passed']}", '',
               '| Case | Result | Seconds |', '| --- | --- | --- |']
        for name, row in self.report['steps'].items():
            lines.append(f"| {name} | {row['status']} | {row['seconds']} |")
        lines += ['', f"Socket.IO latency (ms): {self.report['latency_ms']}", '', *self.report['limitations']]
        for name, row in self.report['steps'].items():
            if row['status']=='fail':lines += ['', name+': '+row['error']]
        (self.args.report_dir/'report.md').write_text('\n'.join(lines)+'\n')


def arguments(medium, argv=None):
    p=argparse.ArgumentParser(description='Connected '+medium+' simulation; yrp.site fixtures only')
    p.add_argument('--site',choices=['yrp.site'],default='yrp.site')
    p.add_argument('--sites',type=Path,default=BENCH/'sites')
    p.add_argument('--frappe-python',type=Path,default=BENCH/'env/bin/python')
    p.add_argument('--core-origin',type=loopback_origin,default='http://127.0.0.1:8001')
    p.add_argument('--socketio-origin',type=loopback_origin,default='http://127.0.0.1:9001')
    for name,port in [('hub',18091),('meta',18094),('smtp',18095),('imap',18096)]:
        p.add_argument('--'+name+'-addr',type=address,default='127.0.0.1:'+str(port))
    p.add_argument('--report-dir',type=Path,default=ROOT/'.runtime/sim-report'/medium.lower())
    args=p.parse_args(argv)
    args.report_dir=args.report_dir.resolve()
    if not args.report_dir.is_relative_to(ROOT/'.runtime'):
        p.error('Reports and provider state must remain in Hub .runtime')
    if args.sites.resolve()!=BENCH/'sites':p.error('Only the approved bench sites directory is allowed')
    return args


def run(driver, medium, argv=None):
    runner=driver(arguments(medium,argv),medium)
    try:
        with runner.step('preflight_and_fixture'):
            runner.setup()
        runner.execute()
    except Exception as exc:
        # Database exceptions can contain credentials; expose only explicit assertions and safe HTTP failures.
        runner.report['error']=str(exc) if isinstance(exc,(AssertionError,RuntimeError,TimeoutError)) else type(exc).__name__
    finally:
        runner.finish()
    print(('PASS' if runner.report['passed'] else 'NOT VALIDATED')+': '+str(runner.args.report_dir/'report.json'),flush=True)
    return 0 if runner.report['passed'] else 1
