"""Local fixture safety shared by connected channel simulations."""
import ipaddress
import os
from pathlib import Path

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


def fixture_user(frappe, insert, label, roles, run):
    key, secret = frappe.generate_hash(length=20), frappe.generate_hash(length=32)
    previous = frappe.flags.in_import
    frappe.flags.in_import = True
    try:
        doc = insert('User', email=f'relay-sim-{label}-{run}@example.invalid',
            first_name=f'Local simulation {label}', enabled=1, user_type='System User',
            send_welcome_email=0, roles=[{'role': role} for role in roles], api_key=key, api_secret=secret)
    finally:
        frappe.flags.in_import = previous
    return {'name': doc.name, 'api_key': key, 'api_secret': secret}
