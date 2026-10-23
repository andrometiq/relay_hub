# Connected WhatsApp simulation

The Hub-owned runner connects `cmd/relay-sim` to an **already running** local
Relay Core, its real background workers and Frappe Socket.IO. Only Meta and
injected callback failures are simulated. Hub authentication, MCP, PostgreSQL
journals/outbox, callback signatures, Core HTTP authentication and worker
projection remain real. This is local acceptance, not live Meta certification.

## Run

From this checkout on `develop`:

```sh
cd /home/sakthi/development/services/relay_hub

# No services, credentials, database connections or Core writes.
python3 -B scripts/simulate_whatsapp_e2e.py --dry-run \
  --report-dir .runtime/sim-report/dry-run

# Uses packages already installed in this bench; installs nothing.
PYTHONDONTWRITEBYTECODE=1 \
/home/sakthi/development/pilot/benches/version_16/env/bin/python -B \
  scripts/simulate_whatsapp_e2e.py \
  --site yrp.site \
  --sites /home/sakthi/development/pilot/benches/version_16/sites \
  --core-origin http://127.0.0.1:8001 \
  --socketio-origin http://127.0.0.1:9001
```

Core HTTP must answer `/api/method/ping` using `Host: yrp.site`. Its canonical
origin must be `http://yrp.site:8001`; the runner reads this from Frappe and does
not change `site_config`. Both `short` (inbound) and `long` (receipts/templates)
workers must run the installed Core code. The target must have developer mode,
ERPNext's Customer/Contact configuration, Relay Core DocTypes and its roles.
Socket.IO must authenticate that site and operator and reach the same Redis
publication stream. The runner neither starts nor rewrites Core infrastructure.

The runner reads PostgreSQL credentials from the ignored Hub `.env`, without
printing them, creates `relay_hub_sim` if absent, and applies **Hub migrations
only**. It never migrates a Frappe site, exports fixtures, builds other apps,
changes site configuration, deletes data or installs dependencies. Hub binaries,
logs and reports stay under `.runtime`; Python bytecode is disabled and bench
helper filesystem writes outside Hub and `/tmp` are rejected.

An actual run writes real local Core records: dedicated service/operator and
unauthorized users, a Customer and linked Contact, a paused Relay Contact,
verified endpoint `919999999999`, one enabled WhatsApp channel for `sim-v1`, and
417 identifiable outgoing history rows. It sets **Relay Settings** to the
simulation account and token. These records and settings remain for inspection;
there is no destructive cleanup or automatic restoration. Previously issued
simulation Hub tokens are revoked when provisioning a new run. Reruns use fresh
message/request identities and reuse the paused simulation contact and linked
Customer, preserving Core's durable conversation binding. An endpoint moved by
an older runner is restored to that binding only after checking both contacts
are simulation fixtures. Assertions select the current run's provider IDs so
retained history cannot hide missing or duplicate rows.
Email is muted during provisioning; active Notification rules on fixture
DocTypes cause preflight failure. The synthetic contact's automation pause
prevents AI/flow execution. No email, SMS, banking or payment channels are seeded.
The runner stops only its own Hub process.

## What is asserted

| Step | Connected assertion |
| --- | --- |
| 7 | Wrong HMAC and wrong phone are rejected; signed text is journaled. |
| 8 | One Hub incoming/journal row and one projected Core Relay Message survive an identical webhook replay. The durable Core Hub Event is checked. |
| 9 | Two real `relay_core.messaging.send` calls with one request key yield one Core action, one Hub intent and exactly one fake Meta POST. Recipient, text, quote context, tracking ID and resulting Core provider ID are checked. |
| 10 | 417 historical messages receive sent/delivered/read receipts: successful signed Core callbacks contain 500, 500 and 251 receipts, each within 512 KiB. Final states are Read; replay and late Delivered cannot regress them. Five fresh inbounds are observed while receipts are held. |
| 11 | Controlled 503 and wrong acknowledgement leave the durable receipt offset at zero. Restarting only relay-sim with `--resume` loads its stored account and resumes the original event, retaining the existing Hub token and Core settings. No new provider send is allowed. |
| 12 | Core creates a PENDING template, lists two paginated templates, then projects a signed APPROVED notification into Relay WhatsApp Template and emits its realtime invalidation. |
| Realtime | The authorized operator subscribes through authenticated Socket.IO over WebSocket; a `relay_changed` messages event arrives. An unauthorized operator must not receive that contact's message event. A successful run observes 31 idle seconds without application HTTP polling. |

Reports are `.runtime/sim-report/report.json` and `report.md`, including per-step
pass/fail/not-run, journal identities, sanitized transport captures, batch sizes,
and p50/p95 latency. Inbound latency uses the host's `CLOCK_MONOTONIC`, from the
signed webhook send until a separate bench process observes the actual Core row
(at a 20 ms database observation interval). It includes Hub delivery and Core
projection; it is **not browser rendering latency**. Socket arrival latency is
reported separately. Database observation and loopback control-state inspection
are test instrumentation; message/status HTTP polling is never used for live
updates. A failed projection is a failed check, never a synthetic replacement.
Independent receipt/template checks continue after an inbound projection failure.
Failures remain failures when later subchecks of the same step succeed; every
subcheck error is retained in the report.

This runner tests the actual Socket.IO protocol using the bench's existing
`websockets` package. It does not drive the installed composer/template DOM,
certify browser rendering, or test browser reconnect/catch-up behavior. The
older `scripts/simulate.py` aggregates separate regression fixtures; it is not a
substitute for this connected run. No p95 acceptance threshold is silently
inferred from a small sample; the measured samples and failures are retained.

## Launcher and control API

`relay-sim` is a test-only Go command, separate from production `relay-hub`.
Build it with `GOCACHE=/tmp/relay-sim-gocache go build -o .runtime/relay-sim
./cmd/relay-sim`. With `DATABASE_URL` pointing to **relay_hub_sim**, a
`RELAY_CREDENTIAL_KEY` and a random `RELAY_SIM_CONTROL_TOKEN` of at least 32
characters in the environment, run:

```sh
.runtime/relay-sim --migrate \
  --addr 127.0.0.1:18091 --meta-addr 127.0.0.1:18094 \
  --core-origin http://127.0.0.1:8001
```

The equivalent address environment variables are `RELAY_SIM_ADDR`,
`RELAY_SIM_META_ADDR` and `RELAY_SIM_CORE_ORIGIN`. `.env` is loaded when present,
without overriding existing environment values. Omitting `--migrate` requires
an already migrated simulation database. A production database name, remote
address, custom search path, remote fallback host or occupied listener is refused.
Origins require explicit valid ports. Use `--resume` for an already provisioned
simulation account: this reads its encrypted database configuration without
rotating tokens or changing Core settings. Ordinary startup leaves accounts
inactive until provisioning, so an earlier run's Core binding is not reused.

Graph requests to exactly `https://graph.facebook.com` are rewritten to fake
Meta. The canonical Core hostname is an account identity; callbacks are explicitly
mapped to the configured literal loopback Core address, retaining the canonical
Host header. The transport disables proxies and redirects and checks every dial
for a literal loopback IP. Other destinations are refused. Fake Meta accepts only
synthetic provider credentials. No real provider credential is needed.

The fake Meta listener also exposes bearer-protected orchestration controls:

| Endpoint | Method / body |
| --- | --- |
| `/_sim/provision` | POST `{site, api_key, api_secret, callback_secret}`: persist `sim-v1`, phone `110`, WABA `220`, and rotate a scoped real Hub token. |
| `/_sim/state` | GET: sanitized request/response captures and fixed, read-only Hub journal/outbox/callback snapshots. |
| `/_sim/emit` | POST `{payload, invalid_signature?: false}`: sign exact JSON bytes and submit any Meta envelope to Hub. Supports text and bulk receipts. |
| `/_sim/faults` | POST `{target: "graph" or "status", faults: ["429", "503"], hold?: false}`. `wrong_ack` is available for status callbacks; `hold` keeps returning 503 for that lane. Realtime callbacks remain independent. |
| `/_sim/template` | POST `{id, status: "APPROVED"}`: update fake template state and emit a signed template lifecycle webhook. PENDING and REJECTED are also supported. |

Fake template pages intentionally contain one row so a two-template test exercises
pagination. Provider IDs are unique per launcher lifetime. The fake provider and
its captures live in memory; the orchestrator collects captures before/after
restart. PostgreSQL event identities, outbox results and acknowledgement offsets
survive restart. Control responses contain newly issued credentials only when
needed for setup; report captures redact authentication, cookies and signatures.

## Tests

```sh
GOCACHE=/tmp/relay-sim-gocache go test -race ./cmd/relay-sim
PYTHONDONTWRITEBYTECODE=1 \
/home/sakthi/development/pilot/benches/version_16/env/bin/python -B \
  -m unittest discover -s scripts -p 'test_simulate_whatsapp_e2e.py' -v
GOCACHE=/tmp/relay-sim-gocache go test ./...
```

The PostgreSQL launcher test is opt-in and uses only `relay_hub_sim`; it keeps
identifiable simulation rows and does not drop schemas or delete data. It starts
actual launcher child processes, real MCP/outbox workers and a signed local Core
HTTP fixture, testing one provider send and 503/wrong-ack restart recovery into
500/500/251 batches. This verifies the launcher, **not actual Core projection**.
After the connected runner has created the simulation database, run it without
printing credentials:

```sh
PYTHONDONTWRITEBYTECODE=1 \
/home/sakthi/development/pilot/benches/version_16/env/bin/python -B - <<'PY'
import os, subprocess, sys, urllib.parse
sys.path.insert(0, 'scripts')
from simulate_whatsapp_e2e import read_env
config = read_env()
url = urllib.parse.urlsplit(config['DATABASE_URL'])._replace(
    path='/relay_hub_sim', query='', fragment='')
env = {**os.environ, 'GOCACHE': '/tmp/relay-sim-gocache',
       'RELAY_SIM_TEST_DATABASE_URL': urllib.parse.urlunsplit(url),
       'RELAY_CREDENTIAL_KEY': config['RELAY_CREDENTIAL_KEY']}
raise SystemExit(subprocess.call(
    ['go', 'test', '-race', './cmd/relay-sim', '-run', 'TestLauncherPostgres', '-v', '-count=1'],
    env=env))
PY
```

The launcher test temporarily binds the simulation account to its own local Core
fixture. Run the connected runner afterward to rebind it to the target Core site.

## Observed validation — 2026-10-24

The repository-wide Go unit suite, launcher race tests, PostgreSQL launcher
restart test, 16 Python runner tests, `go vet` and dry-run passed. The default Go
suite skips database tests unless their opt-in environment variables are set;
the separate launcher PostgreSQL test was explicitly enabled and passed.

The final connected `yrp.site` run passed steps 7–12 and the Socket.IO check:
one inbound projection after replay, one provider POST for repeated Core sends,
**500/500/251** acknowledged receipt batches and final Read states, independent
inbound delivery during held receipts, 503/wrong-ack recovery across a Hub-only
restart, and PENDING → APPROVED template projection with realtime invalidation.
The operator connection remained alive through 31 idle seconds; the unauthorized
observer received no message event. No HTTP message polling was used.

Across six inbound samples, webhook-to-Core-row latency was **744.12 ms p50** and
**1178.95 ms p95**. Initial message Socket.IO arrival was **1169.05 ms**. These
measurements do not meet the design's proposed sub-second p95 target; the runner
reports measurements without treating that unconfirmed target as a pass gate.
Browser rendering and reconnect/catch-up remain outside this transport harness.
No Core source or schema changes were made by this work.

Connected reruns exposed and now cover two harness defects: a supplied WebSocket
retained its 10-second TCP timeout, and replacing a contact invalidated Core's
durable conversation binding. The socket now uses the protocol's own timeouts;
fixture reruns preserve the original conversation contact. Reports retain every
subcheck failure instead of allowing a later success to erase it.
