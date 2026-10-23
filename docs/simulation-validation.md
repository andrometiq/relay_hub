# Connected WhatsApp simulation

The Hub-owned runner connects `cmd/relay-sim` to an **already running** local
Relay Core, its real background workers and Frappe Socket.IO. Only Meta and
injected callback failures are simulated. Hub authentication, MCP, PostgreSQL
journals/outbox, callback signatures, Core HTTP authentication and worker
projection remain real. This is local acceptance, not live Meta certification.

## Coalesced automation follow-up (2026-10-24)

Core now persists an indexed `Relay Message.automation_status` marker with each
new incoming message and drains committed markers in batches on `default`.
Step 13's paused-message barrier reads `Complete` from that row; per-message RQ
jobs no longer exist for new traffic. Blank/Pending never satisfies the barrier.
The worker-isolation and no-run/action/reply assertions remain unchanged.

An explicit Core site benchmark used the existing single default worker on
`yrp.site`: 300 paused/no-skill messages finished in one job in **1.641 s**, versus
**138 s** estimated from the measured old 0.46 s/job. A late enabled AI contact
behind another 300 messages produced its intent in **1.543 s** and completed a
real Shadow worker decision through a loopback HTTP provider in **2.111 s**.
A second process proved concurrent-drain exclusion. Killing a process after its
uncommitted AI intent/ack writes left both rolled back; recovery produced one
committed run and one provider call. See Core `docs/automation-queue.md` and
`tests/automation_queue_site.py` for reproduction and the recovery contract.

Only Relay Message was reloaded with `frappe.reload_doc` on yrp.site, adding
`automation_status` and `automation_status_index`. **RESTART NEEDED** for cached
web code before rerunning this full simulation. No restart, migration, fixture
export, site_config change or production write was performed. These focused
site measurements are not a passing full connected simulation report.

The retained `.runtime/sim-report/full/report.json`, run
`1792796027374890250`, separately explains step 10b's timeout: **35 status
callbacks returned HTTP 500 / QueryDeadlockError**. Its recorded long-lane
projection jobs have approximately 5–7 ms queue waits, so default-queue
starvation alone does not explain this failure. A read-only follow-up found all
4,000 storm messages eventually at Read. The receipt timeout was not relaxed;
the database lock cycle still needs reproduction/tracing during a fresh storm.
Shared database contention may contribute, but these observations do not prove
that the automation drain fixes the receipt failure.

## Automation-enabled run fixes (2026-10-24)

The full report captured successful receipt batches of 500, 500 and 251 for
event 273. Step 10 saw only the first two because `/_sim/state` copied captures
before reading callback acknowledgements. It now reads captures last, preserving
the ordering between a transport capture and the subsequent durable ACK. The
batch-size and complete-delivery assertions are unchanged.

Inbound automation previously read policies and created intents during message
insertion. New AI jobs used `short`; recovery and translation used `long`.
The installed worker consumed `default,short,long`, so none of these queues had
independent worker capacity. Core now defers inbound automation until after
message commit and uses `default` for initial and downstream automation jobs.
Preflight rejects workers that consume both automation and projection queues.
Step 13 waits for the paused message's deferred automation check to finish,
then rereads its state before asserting no run/action/reply.

The recorded 668 ms storm p95 also includes Hub/HTTP delays; Core inline
projection in that sample took 43 ms. The queue fixes do not establish a new
latency result. Full connected acceptance remains pending activation below.

Step 14's Item failed India Compliance's mandatory SAC validation. The fixture
now supplies the installed IT consulting SAC 998313, the configured default
Company and an explicit local selling price list in that Company's currency.
The arbitrary Company previously selected had no applicable fiscal year.
A local diagnostic inserted and submitted the corrected Sales Order with
quantity 2 and total 250, then rolled back without sending externally.

Activation requires the operator to replace the shared worker and reload web.
These commands have **not** been executed. No migration, asset build or site
configuration change is needed:

```sh
systemctl --user stop v16-worker.service
systemd-run --user --collect --unit=v16-projection --working-directory=/home/sakthi/development/pilot/benches/version_16/sites /home/sakthi/development/pilot/benches/version_16/env/bin/python -B -m frappe.utils.bench_helper frappe worker --queue short,long
systemd-run --user --collect --unit=v16-automation --working-directory=/home/sakthi/development/pilot/benches/version_16/sites /home/sakthi/development/pilot/benches/version_16/env/bin/python -B -m frappe.utils.bench_helper frappe worker --queue default
systemctl --user restart v16-web.service
```

Then rerun the connected command below with `--report-dir .runtime/sim-report/full`.
The existing full report is retained unchanged as failure evidence.

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
workers must run the installed Core code, with a separate `default` automation
worker. The target must have developer mode,
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
417 identifiable outgoing history rows plus 4,000 storm rows by default. It sets **Relay Settings** to the
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
| 10b | A separate sustained storm defaults to 12,000 receipts over 4,000 outgoing messages. Signed single inbounds start immediately after injection, paced at 250 ms until all storm rows are Read. At least five Core-row observations must overlap measured receipt projection intervals, with p95 < 500 ms. |
| 11 | Controlled 503 and wrong acknowledgement leave the durable receipt offset at zero. Restarting only relay-sim with `--resume` loads its stored account and resumes the original event, retaining the existing Hub token and Core settings. No new provider send is allowed. |
| 12 | Core creates a PENDING template, lists two paginated templates, then projects a signed APPROVED notification into Relay WhatsApp Template and emits its realtime invalidation. |
| Realtime | The authorized operator subscribes through authenticated Socket.IO over WebSocket; a `relay_changed` messages event arrives. An unauthorized operator must not receive that contact's message event. A successful run observes 31 idle seconds without application HTTP polling. |

Use `--storm-receipts N` to configure step 10b independently of step 10's fixed
1,251 receipts. Any positive count is accepted; the runner seeds `ceil(N / 3)`
messages and omits initial statuses from the first message when needed, preserving
its final Read receipt. The storm uses signed envelopes of at most 3,000 receipts
to stay below Hub's 1 MiB ingress limit. Timing combines all storm event IDs. Sampling has a timeout of `max(60, N / 100)` seconds.
The report includes storm receipt count, first-to-last projection seconds (including
batch gaps), receipts/second, qualifying sample count and inbound p50/p95. Only
observations inside actual projection intervals count toward the latency gate;
batch gaps and held-receipt samples never count.

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
substitute for this connected run. The storm gate explicitly requires p95 < 500 ms
and at least five samples whose Core-row observation falls within a receipt
worker's projection interval. Held 503 samples and the initial cold sample do
not qualify. The measured samples and failures are retained.

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

The PostgreSQL launcher test is opt-in. It creates and drops an isolated schema
in the existing `relay_hub_v1_test` database at `127.0.0.1:18433`, selected by
`RELAY_TEST_DATABASE_URL`. It never rebinds the connected simulation account.
It starts actual launcher child processes, real MCP/outbox/mailbox workers and
a signed local Core HTTP fixture. Coverage includes WhatsApp's 503/wrong-ACK
restart recovery into 500/500/251 batches, plus the Telegram and email provider
paths documented in [Local Telegram and email providers](simulation-providers.md).
This verifies the launcher, **not actual Core projection**.

With `RELAY_TEST_DATABASE_URL` supplied securely for that test database:

```sh
go test -race ./cmd/relay-sim -run TestLauncherPostgres -v -count=1
```

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

## Inbound latency work — awaiting connected validation

The saved baseline captures permit this breakdown for the five warm inbounds
sent while receipts were held at 503 (milliseconds):

| Segment | p50 | p95 |
| --- | ---: | ---: |
| Signed webhook HTTP, including journal commit | 10.50 | 10.55 |
| Webhook response to callback start, including Hub scheduling/database work | 152.14 | 203.33 |
| Callback HTTP | 44.22 | 46.16 |
| Callback completion to observed Core row | 542.70 | 554.07 |

The initial cold message spent 1,044.65 ms after callback completion. The old
report cannot separate receive, RQ enqueue, worker pickup/setup and projection;
these numbers must not be described as measured worker execution times. Also,
the held-receipt samples do not demonstrate latency during receipt processing.

The pending change wakes the realtime dispatcher after successful webhook
persistence. Signals coalesce and remain pending while an account or worker
slot is busy. The 250 ms ticker still recovers missed signals, remote commits
and retries; status delivery retains its separate one-second schedule and
worker capacity. Database advisory locks and durable acknowledgements remain
in force. This is an in-process wake, not a cross-replica notification.

Core already used `short` for inbound and `long` for statuses. Now a callback
containing one small WhatsApp text envelope (at most 16 KiB) projects within
the receive transaction. Event/message writes commit together, failures abort
the request for Hub retry, and replay keeps the existing event identity. Bulk
inbound, media, other channels and malformed events retain background
projection; statuses/templates remain on `long`. Automation, provider work and
realtime publication retain their existing after-commit/background paths.

New report evidence:

- `inbound_breakdown`: ingress, estimated scheduling, Hub database/batch
  preparation, callback HTTP, Core receive and inline projection durations.
  Scheduling uses webhook response time as a commit proxy, so it is approximate
  and clamped at zero when an immediate callback overlaps that response.
- `core_diagnostics.projection_timings`: completed RQ job metadata for receive
  through enqueue submission, enqueue preparation, queue wait, worker setup and
  projection through commit. RQ enqueue/start timestamps use UTC wall time;
  projection spans and inbound overlap use this host's monotonic clock.
  `enqueue_ms` ends at RQ's enqueue timestamp, not Redis round-trip completion.
- `storm_latency_ms`: p50/p95 only for inbounds overlapping actual receipt
  projection, sample count, and receipt throughput over the first-to-last
  projection span (including gaps between batches). It excludes initial queue
  wait. Compare that span and successful callback batch timing on subsequent runs
  before claiming status throughput is preserved.

Timing samples are diagnostic, not additive: inline projection is included in
receive and HTTP; queued projection can start before callback HTTP completes.
RQ metadata expires with the normal finished-job retention. Its bounded registry
reads and database observations are test instrumentation, never UI polling.

**RESTART NEEDED.** The sandbox cannot inspect the user systemd bus or verify
worker reload. Core backend changes and the new worker entry point have not been
activated or tested through `yrp.site`. No service restart, migration, fixture
export or connected rerun was performed. The original report is preserved;
sub-500 ms storm latency and status throughput remain unverified.

Orchestrator commands after reviewing the uncommitted changes:

```sh
systemctl --user restart v16-web v16-worker
cd /home/sakthi/development/services/relay_hub
PYTHONDONTWRITEBYTECODE=1 \
/home/sakthi/development/pilot/benches/version_16/env/bin/python -B \
  scripts/simulate_whatsapp_e2e.py \
  --site yrp.site \
  --sites /home/sakthi/development/pilot/benches/version_16/sites \
  --core-origin http://127.0.0.1:8001 \
  --socketio-origin http://127.0.0.1:9001 \
  --report-dir .runtime/sim-report/latency
```

No DocType or hooks changes require migration. The connected runner still
provisions the documented local fixtures and simulation settings.

Local checks for this change: `go test -race ./...`, `go vet ./...`, the Hub
binary build, 20 runner tests, eight new Core projection tests and runner dry-run
passed. The full Core suite ran 180 tests before the final two test additions:
175 passed, three skipped, and two existing audio tests errored because this
sandbox has no `ffprobe`. Opt-in database/provider tests were not enabled.

## Paced inbound sampling — connected validation, 2026-10-24

The sampler now starts immediately after restart releases the held storm, without
waiting for a successful receipt callback. It sends signed single inbounds at
250 ms start spacing until the historical rows are Read, with a 60 s timeout.
The overlap filter requires the Core-row observation itself to fall inside a
receipt job's measured projection interval; observations after commit do not count.

All 22 runner tests passed with the bench environment on `PATH` (the default
Python lacks `rq`). The exact connected command above completed, but acceptance
**failed**: 19 processing samples produced zero qualifying observations. The three
projection jobs lasted 87.43, 89.19 and 76.32 ms across a 2.08 s first-to-last
span. These short intervals cannot reliably yield ten observations at 250 ms
spacing. Gaps between jobs remain excluded, and the gate remains p95 < 500 ms
with at least five overlapping samples. Overall inbound p95 was 107.61 ms.
Steps 7, 8, 9, 11, 12 and realtime passed, including receipt batches of
500/500/251, recovery and final Read states. That run did not validate the latency gate. The report paths below now contain
the subsequent sustained-storm run.


## Sustained receipt storm — connected validation, 2026-10-24

Step 10 retains the 1,251-receipt batch/replay assertions. Step 10b independently
seeds 4,000 outgoing rows and injects 12,000 receipts by default. The first attempt
hit HTTP 413 with a single large envelope; the runner now sends four signed
3,000-receipt envelopes within the existing ingress limit. Paced single inbound
messages continue until the storm rows reach Read. The strict overlap filter and
p95 < 500 ms threshold are unchanged.

The exact connected command above passed on `yrp.site` (Frappe 16.52.0,
developer mode enabled), including recovery, templates and authenticated realtime.
All 25 runner unit tests passed with the bench Python. No app source, site
configuration or Frappe schema was changed.

| Measurement | Result |
| --- | ---: |
| Step 10 receipt batches | 500 / 500 / 251 |
| Step 10b storm receipts / outgoing messages | 12,000 / 4,000 |
| First-to-last projection span, including gaps | 29.096 s |
| Receipt throughput over that span | 412.43/s |
| Paced inbound observations / actual projection overlaps | 117 / 23 |
| Overlapping inbound p50 / p95 | 131.42 / 148.39 ms |
| Overall inbound p50 / p95 | 127.47 / 143.14 ms |
| Connected acceptance | All steps passed |

Evidence: `.runtime/sim-report/latency/report.md` and `report.json`. These are
webhook-to-Core-row measurements, not browser rendering measurements. Changes
remain uncommitted for orchestrator review.

## AI and business-event extension (steps 13–14)

The runner now prepares steps 13 and 14 after template approval. These additions
are **not yet connected-validated**. The target web unit uses `--noreload`; restart
is required before running changed Core code. The agent must not restart the
user's units. The operator command is:

```sh
systemctl --user restart v16-web.service v16-worker.service
```

Socket.IO is unchanged. Reload only `Relay AI Skill` using
`frappe.reload_doc('relay_core', 'doctype', 'relay_ai_skill', force=True)` on
`yrp.site`; do not migrate or export fixtures. This reload was applied during
preparation, and the disabled Shadow sample RELAY-AIS-0000000001 was installed. The disabled Supplier ledger sample is installed idempotently by
Core's install/migrate hooks and by the connected fixture setup. Existing sample
configuration is preserved; the runner refuses customized read contracts or a
sample assigned to another channel.

Step 13 also requires the global `relay_automation_enabled` gate. It is absent
from yrp.site's merged configuration as checked during review, so a restart
alone cannot activate AI. Preflight now refuses a missing/disabled gate before
provisioning fixtures or starting the Hub; AI fixture setup rechecks it too.
The runner never changes site configuration. **Separate user approval is required**
for this activation command, run from `/home/sakthi/development/pilot/benches/version_16`:

```sh
bench --site yrp.site set-config relay_automation_enabled 1 --parse
```

This enables the site's automation gate, not just the simulation skill. After
approval and activation, the operator must perform the web/worker restart above
before connected validation. Neither command was run during this review.

| Step | Assertions added | Current validation |
| --- | --- | --- |
| 13 | Offer an explicit range before a read; confirmation invokes the real scoped GL tool; one durable action/outbox/Meta POST/history row per reply; Sent, Delivered and Read receipts; webhook replay does not resend; one assigned supplier ticket and pause on handoff; second supplier, cancelled and out-of-range GL rows never reach the fake AI or reply. | Unit checks only; connected run pending gate activation and restart. |
| 14 | Normal Sales Order insert/submit captures one intent despite lifecycle replay; one template POST to the customer; actual conversation timeline entry; a second submit succeeds with relay-sim stopped and retains its intent. | Unit checks only; connected run pending gate activation and restart. |

`cmd/relay-sim` exposes `/ai/chat/completions` on its existing loopback fake-Meta
listener. It implements only the `relay-sim-ledger` fixture conversation and
accepts only a synthetic key. It has no outbound transport. A dedicated **AI
Provider Account** uses the existing OpenRouter adapter with that loopback base
URL, and a dedicated **AI Model** selects it. No shared provider code or site
configuration changes are needed for provider selection; the automation gate
activation above is still required. The fake deliberately returns incorrect final
arithmetic; Core must replace it with its deterministic ledger presentation.
AI request captures use the existing credential redaction.

The runner creates two identifiable Suppliers, a linked Contact and verified
WhatsApp endpoint, plus five GL Entry read-contract fixtures: debit 100.10,
credit 40.05, another supplier's debit 88,000, an out-of-range debit 77,000,
and a cancelled debit 1,250. Cancelled rows must never reach the model or reply.
GL rows are inserted directly as test read data, **not posted accounting
vouchers**. Production GL controllers and accounting posting are outside this
acceptance scope. The real AI worker still uses Frappe query/document/field
permissions to read those rows. The event source is a normally submitted Sales
Order with a non-stock service item at rate 125 and quantity two.
Company, payable Account, Supplier/Item Groups and UOM must already exist.

Preflight refuses enabled notifications/webhooks for the new fixture document
types. No real provider credential is seeded. At completion the runner disables
its sample skill and event rule, retaining data for inspection. An outage intent
may remain Pending/Uncertain/Failed; this step establishes submit isolation, not
outage recovery. The runner stops only its own Hub. No connected success should
be inferred from dry-run, UI build, or unit tests.

The paused-message assertion waits for that exact provider ID's committed Core
message before checking zero automation runs, actions and outgoing messages.
The deferred automation check must finish before the runner rereads that message's
state; callback acknowledgements and message insertion alone do not establish
this. Even a queued Pending run fails the check.


### Review fixes: local validation

All four review findings were confirmed and fixed. The ledger query excludes
cancelled rows before pagination; unit coverage combines cancellation with full
and truncated results and rejects attempts to override the filter. The paused
message check rejects even a Pending automation run after its own projection.
Ticket badges and detail names wrap long business names within 375px cards.

- Core: 240 unit tests, 235 passed, three skipped, the two known ffprobe errors.
- Frontend: 64 Node tests passed; `npm run build` and `npm run build:site` passed.
- Browser fixture: assignment, close, empty state, shadow result and live preview
  passed at 375/768/1440px, plus spaced and unbroken long names at 375px.
  This uses intercepted APIs and is not target-site acceptance.
- Hub: `go vet ./...`, `go test ./...` and 30 Python runner tests passed.
- No connected run, restart, site-config change, migration or fixture export was
  performed. Steps 13–14 remain unvalidated on yrp.site until the prerequisites
  above are fulfilled. Existing steps 7–12 were not rerun during this review.
