#!/usr/bin/env python3
"""Run local provider/database simulations and retain a machine-readable report.

Never loads the Hub .env or connects to a live provider. PostgreSQL credentials
are read only from the explicitly selected isolated Frappe test site's config.
"""
import argparse
import datetime as dt
import json
import os
import re
from pathlib import Path
import socket
import subprocess
import time
from urllib.parse import quote, urlsplit

HUB = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--core', type=Path, default=HUB.parent / 'frappe_relay_core')
    parser.add_argument('--sites', type=Path)
    parser.add_argument('--frappe-python', type=Path, required=True)
    parser.add_argument('--report', type=Path, default=HUB / '.runtime/simulation-report.json')
    args = parser.parse_args()
    core = args.core.resolve()
    sites = (args.sites or core / '.runtime/automation-bench/sites').resolve()
    config = json.loads((sites / 'relay-ai-test.local/site_config.json').read_text())
    if config.get('db_name') != 'relay_ai_test_20260922' or config.get('db_host') != '127.0.0.1':
        parser.error('Only the isolated loopback relay_ai_test_20260922 database is permitted')
    if config.get('db_port') != 18433:
        parser.error('Unexpected isolated PostgreSQL port')
    common = json.loads((sites / 'common_site_config.json').read_text())
    for key in ('redis_cache', 'redis_queue', 'redis_socketio'):
        endpoint = urlsplit(common.get(key, ''))
        if endpoint.hostname != '127.0.0.1' or endpoint.port != 18089:
            parser.error('Expected the isolated Redis service on 127.0.0.1:18089')
    env = os.environ.copy()
    # Test providers use local fixture transports, not environment credentials.
    for key in list(env):
        if key.endswith(('API_KEY', 'BOT_TOKEN', 'ACCESS_TOKEN')):
            env.pop(key, None)
    credential = config['db_password']
    env['RELAY_TEST_DATABASE_URL'] = (
        'postgresql://' + quote(config.get('db_user') or config['db_name'], safe='') + ':'
        + quote(credential, safe='') + '@127.0.0.1:18433/' + config['db_name'] + '?sslmode=disable')
    env.pop('RELAY_LIVE_TEST', None)
    env['GOMAXPROCS'] = '2'
    env['PYTHONPATH'] = str(core) + os.pathsep + env.get('PYTHONPATH', '')
    env['RELAY_AUTOMATION_TEST_SITES'] = str(sites)
    report = {'started_at': dt.datetime.now(dt.timezone.utc).isoformat(),
              'database': config['db_name'], 'provider_mode': 'local simulators', 'checks': []}
    def safe(text):
        return text.replace(credential, '[redacted]').replace(quote(credential, safe=''), '[redacted]')
    def check(label, command, cwd, timeout=180, go_json=False):
        print('RUN', label, flush=True)
        started = time.monotonic()
        try:
            result = subprocess.run(command, cwd=cwd, env=env, text=True, stdout=subprocess.PIPE,
                                    stderr=subprocess.STDOUT, timeout=timeout)
            output = safe(result.stdout)
            code = result.returncode
        except subprocess.TimeoutExpired as exc:
            raw = exc.stdout or b''
            output = safe(raw.decode(errors='replace') if isinstance(raw, bytes) else raw) + '\nTIMED OUT'
            code = 124
        item = {'name': label, 'command': command, 'exit_code': code,
                'seconds': round(time.monotonic()-started, 3), 'output': output}
        if label == 'Core unit contracts':
            found = re.search(r'Ran (\d+) tests?', output)
            skipped = re.search(r'skipped=(\d+)', output)
            if found:
                item['tests_run'] = int(found[1])
                item['tests_skipped'] = int(skipped[1]) if skipped else 0
                if code == 0:
                    item['tests_passed'] = item['tests_run'] - item['tests_skipped']
        if go_json:
            events = []
            for line in output.splitlines():
                try: events.append(json.loads(line))
                except ValueError: pass
            for action in ('pass', 'skip', 'fail'):
                item[action] = [e['Package'] + '/' + e['Test'] for e in events
                                if e.get('Action') == action and 'Test' in e]
            item['fail'].extend(e.get('Package') or e.get('ImportPath') or 'package build' for e in events
                                if e.get('Action') in ('fail','build-fail') and 'Test' not in e)
            if item['skip']:
                item['unexpected_skips'] = True
        report['checks'].append(item)
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, indent=2) + '\n')
        print(('PASS' if code == 0 and not item.get('unexpected_skips') else 'FAIL'), label,
              '(' + str(item['seconds']) + 's)', flush=True)
        if code: print(output[-7000:], flush=True)
    redis = None
    run_finished = False
    def redis_ready():
        try:
            with socket.create_connection(('127.0.0.1', 18089), timeout=.5) as conn:
                conn.sendall(b'*1\r\n$4\r\nPING\r\n')
                return conn.recv(64).startswith(b'+PONG')
        except OSError: return False
    try:
        if not redis_ready():
            # Binding check prevents us from stopping/replacing somebody else's service.
            with socket.socket() as probe:
                probe.bind(('127.0.0.1', 18089))
            redis = subprocess.Popen(['redis-server', '--bind', '127.0.0.1', '--port', '18089',
                                      '--save', '', '--appendonly', 'no'],
                                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            for _ in range(50):
                if redis_ready(): break
                if redis.poll() is not None: raise RuntimeError('Isolated Redis failed to start')
                time.sleep(.1)
            else: raise RuntimeError('Isolated Redis readiness timed out')
        check('Hub provider/database simulations (race detector, no skipped integration tests)',
              ['go', 'test', '-json', '-race', '-p', '1', '-count=1', './...'], HUB, timeout=300, go_json=True)
        check('Hub static analysis', ['go', 'vet', './...'], HUB)
        check('Core unit contracts', [str(args.frappe_python), '-m', 'unittest', 'discover', '-s', 'tests'], core)
        for name in ('integration_automation_site.py', 'integration_channels_site.py', 'integration_groups_site.py',
                     'integration_message_types_site.py', 'integration_prepared_reports_site.py', 'integration_settings_site.py', 'integration_inbox_site.py',
                     'integration_email_site.py', 'integration_channel_status_http.py', 'integration_ticket_notifications_site.py', 'integration_flows_site.py',
                     'integration_multimodal_site.py', 'integration_translations_site.py', 'integration_learning_site.py', 'integration_collaboration_site.py',
                     'integration_legacy_site.py', 'integration_enrollment_site.py', 'integration_summaries_site.py',
                     'integration_campaigns_site.py', 'integration_channel_campaigns_site.py', 'integration_typing_site.py',
                     'integration_telegram_recovery_site.py', 'integration_overview_site.py', 'integration_operations_site.py', 'integration_recovery_runtime_site.py', 'integration_realtime_site.py', 'integration_link_search_site.py', 'integration_calls_site.py', 'integration_meta_flows_site.py', 'integration_topics_site.py', 'integration_metadata_site.py', 'integration_telegram_topics_site.py', 'integration_cases_site.py', 'integration_retailer_site.py'):
            check('Core ' + name, [str(args.frappe_python), str(core / 'tests' / name)], core)
        for root in (HUB, core):
            tests = sorted(str(p.relative_to(root)) for pattern in ('*.test.mjs','*.test.js') for p in (root/'frontend/tests').glob(pattern))
            check(root.name + ' frontend data simulations', ['node', '--test', *tests], root)
            check(root.name + ' whitespace', ['git', 'diff', '--check'], root)
        run_finished = True
    finally:
        if redis is not None:
            redis.terminate()
            try: redis.wait(timeout=5)
            except subprocess.TimeoutExpired: redis.kill(); redis.wait()
        report['finished_at'] = dt.datetime.now(dt.timezone.utc).isoformat()
        report['passed'] = run_finished and bool(report['checks']) and all(
            c['exit_code'] == 0 and not c.get('unexpected_skips') for c in report['checks'])
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, indent=2) + '\n')
    print('Report:', args.report)
    return 0 if report['passed'] else 1

if __name__ == '__main__':
    raise SystemExit(main())
