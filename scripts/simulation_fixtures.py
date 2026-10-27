"""Local fixture safety shared by connected channel simulations."""
import ipaddress
import os
from datetime import datetime, timezone
from itertools import product
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SUFFIXES = '0123456789abcdefghijklmnopqrstuvwxyz'


def run_label(exists=lambda label: False, now=None):
    """Use the UTC minute, adding two characters only for retained runs."""
    base = (now or datetime.now(timezone.utc)).astimezone(timezone.utc).strftime('R%m%d-%H%M')
    for suffix in ['', *(''.join(pair) for pair in product(SUFFIXES, repeat=2))]:
        label = base + ('-' + suffix if suffix else '')
        if not exists(label):
            return label
    raise RuntimeError('Simulation run labels exhausted for this UTC minute')


def fixture_email(label, run):
    return f'sim-{label}-{run.lower()}@example.invalid'


def run_number(run):
    """An injective, bounded numeric identity for the readable label."""
    stamp = datetime.strptime('2000' + run[1:10], '%Y%m%d-%H%M')
    minute = ((stamp.timetuple().tm_yday - 1) * 24 + stamp.hour) * 60 + stamp.minute
    suffix = 1 + int(run[11:], 36) if len(run) > 10 else 0
    return minute * (36 * 36 + 1) + suffix


def fixture_endpoint(medium, run, role='customer'):
    if medium == 'Email':
        return fixture_email(role, run)
    slot = ('customer', 'shared', 'supplier', 'fault').index(role)
    number = run_number(run) * 4 + slot
    return ('91' + str(6000000000 + number) if medium == 'WhatsApp'
            else str(1000000000 + number))


def available_run(frappe, used=()):
    return run_label(lambda label: label in used or frappe.db.exists('User', fixture_email('service', label)))


def fixture_channel(frappe, insert, medium, run):
    names = frappe.get_all('Relay Channel', filters={'account': 'sim-v1', 'channel': medium}, pluck='name')
    assert len(names) <= 1, 'Ambiguous simulation channel'
    title = f'Sim {medium} {run}'
    if names:
        channel = frappe.get_doc('Relay Channel', names[0])
        assert channel.enabled, 'Simulation channel is disabled'
        channel.title = title
        channel.save(ignore_permissions=True)
        return channel
    return insert('Relay Channel', title=title, channel=medium, account='sim-v1', enabled=1)


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


def fixture_user(frappe, insert, label, roles, run):
    key, secret = frappe.generate_hash(length=20), frappe.generate_hash(length=32)
    previous = frappe.flags.in_import
    frappe.flags.in_import = True
    try:
        doc = insert('User', email=fixture_email(label, run),
            first_name=f'Sim {label.title()} {run}', enabled=1, user_type='System User',
            send_welcome_email=0, roles=[{'role': role} for role in roles], api_key=key, api_secret=secret)
    finally:
        frappe.flags.in_import = previous
    return {'name': doc.name, 'api_key': key, 'api_secret': secret}
