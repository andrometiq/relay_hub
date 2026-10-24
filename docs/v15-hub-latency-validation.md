# v1.5 Hub ingress and connected-driver validation

Latest results: [lat2 snapshot and MIME follow-up](#2026-10-24-lat2-follow-up-snapshot-scan-and-mime-decoding).

Work is uncommitted on the authorized `tmp/v15-l1` worktree. Core source is
read-only. All connected runs use yrp.site, loopback fake providers, and isolated
schemas in `relay_hub_v1_test` at 127.0.0.1:18433. Neither `channel_hub` nor
`relay_hub_sim` was opened. No site configuration, migration, fixture export,
external provider send, commit or push was performed.

## Changes

- Telegram/email journal commits now coalesce an immediate callback wake, using
  the same scheduler as WhatsApp. The 250 ms ticker still covers retries and
  other replicas. Immutable per-event ACKs, bounded batches, failure isolation,
  and the independent one-second batched status/DSN pool remain intact.
- IMAP IDLE watchers wake the bounded fetch scheduler. They hold neither a fetch
  slot nor a database connection while idle. EXAMINE closes the connection gap;
  a 30-second reopen bounds missed notifications and supports non-IDLE servers.
  Disconnects back off exponentially. Credential replacement cancels old watches.
  Notifications arriving during a fetch survive its final scheduling update.
  Existing read-only fetch, UIDVALIDITY snapshots, occurrence matching and
  20-message fairness remain authoritative.
- The fake TLS email provider advertises IDLE and broadcasts EXISTS, including
  arrivals between EXAMINE and IDLE.
- WhatsApp sampling uses recent actual receipt-worker intervals and inbound
  latency to schedule observations near the next projection midpoint. The
  original five-overlap and strict 500 ms p95 requirements are unchanged.
  No receipt delay or synthetic sample is introduced. The fixed-cadence
  intermediate run reproduced the defect with only three overlaps; measured
  window scheduling produced 18 and 21 overlaps in consecutive runs.
- All three drivers share isolated-schema setup, unique event sequences, a site
  lock and restoration of prior Hub settings. Telegram's uncertain-send fixture
  has its own endpoint so its intentional destination block cannot prevent a
  later business-event send.
- Telegram/email now exercise the real scoped supplier ledger, replay, explicit
  human handoff, paused-message suppression, customer Sales Order on_submit
  delivery and Hub-outage intent retention. Explicit contact and channel consent
  are seeded for business events. Ten sequential text messages enforce the
  no-load p95 target. Failures remain failures while independent cases continue.

## Measurements

| Path / p95 (ms) | Before | After run 1 | After run 2 |
| --- | ---: | ---: | ---: |
| Telegram webhook | 1,037.2 | 1,132.2 | 1,266.8 |
| Email webhook | 1,070.7 | 1,029.3 | 1,060.8 |
| Email IMAP arrival | 30,739.5 | 1,325.0 | 1,159.3 |
| WhatsApp under 12,000 receipts | 274.8 | 298.2 | 293.2 |

Before webhook figures use each original driver's five mixed webhook
observations. After webhook figures use ten dedicated sequential text probes
per run; these sample sets are not identical. The first immediate-wake Telegram
run using the original five observations measured 925.4 ms p95. None of the
Telegram/email runs establishes the <500 ms target. IMAP uses two arrivals per
run; p95 is therefore the maximum. WhatsApp's final two runs had **18 and 21**
real projection-overlapping samples, processing **513 and 520 receipts/s**.
The last WhatsApp run passed every case including AI and business events; the
preceding run passed latency but timed out waiting for AI. Telegram's first
changed run passed ledger and exactly-one-ticket handoff; its latest repeat and
both final email repeats were blocked by Pending Core automation hooks. Both
channels passed Sales Order delivery and Hub-outage intent retention after
correcting recipient fixtures.

Evidence: baseline reports `l1-before-{whatsapp,telegram,email}`; final
WhatsApp `l1-final-whatsapp-{2,3}`, Telegram `l1-final-telegram-{1,2}`, email
`l1-final-email-{1,2}`. The intermediate WhatsApp `l1-final-whatsapp-1` retains
the reproduced three-overlap failure. Earlier fixture failures remain in the
reports and were not reclassified as passes.

Reports retain raw samples, real Socket.IO
invalidations, callback timing, provider captures, and automation diagnostics in
`.runtime/sim-report/l1-*`. Runtime reports contain private fixture credentials
and are not versioned. Measurements are local acceptance, not live-provider
certification. WhatsApp uses its existing Core-row observation; Telegram/email
use event-gated snapshots after real Socket.IO invalidation. IMAP includes
mailbox arrival; webhook and Core-send measurements are separate.

## Tests

- All three drivers were run end to end repeatedly, including two measured
  post-change runs per channel. The last WhatsApp run passed every case.
  Telegram/email remain nonzero because their strict latency and Core-dependent
  assertions fail; independent business-event cases pass.
- `python -B -m unittest discover -s scripts -p 'test_simulate_*e2e.py'`:
  **52 passed**.
- `go vet ./...`: **passed**.
- `go test -p 1 ./... -count=1`: **passed**, with the approved local test DSN,
  serially after the drivers. This includes durable ACK/retry recovery, mailbox
  UIDVALIDITY/no-duplicate and populated-upgrade checks, slow-mailbox fairness,
  stalled-connection capacity, and the new wake-during-fetch regression.
- Focused race checks for TLS IDLE arrival/disconnect/cancellation and bounded
  rechecks with and without server IDLE support: **passed**.
- `git diff --check`: **passed**.

Logs: `.runtime/l1-python.log`, `.runtime/l1-go-vet.log`,
`.runtime/l1-go-test.log`, `.runtime/l1-race.log`.

## CORE DEFECTS

1. `relay_core/inbound.py:124–128` explicitly rejects Telegram/Email from
   `single_text()`, used for inline eligibility at line 115. Repro: either
   driver's `single_inbound_latency` case, ten sequential text webhooks.
   Hub dispatch is about 1–2 ms; Core acknowledges in tens of milliseconds with
   `inline_projection_ms=0`, but realtime observation takes roughly a second.
   The strict <500 ms gate fails. Hub wakeups alone cannot remove worker startup.
2. `relay_core/relay_core/doctype/relay_message/relay_message.json:71–74` defines
   `provider_id` as Data without `ignore_xss_filter`. `relay_core/email.py:332`
   inserts the RFC Message-ID, and Frappe's `model/base_document.py:1310–1350`
   treats `<relay-...@sim.invalid>` as HTML and strips it. Repro: email AI reply
   or Sales Order delivery. SMTP and action.result contain the accepted ID,
   provider_context.message_id preserves it, but provider_id is empty. The
   driver uses the intact canonical context to finish independent checks and
   separately fails `email_provider_identity`; it does not patch the row.
3. `relay_core/automation/queue.py:50–60` can be observed with a live NX wake
   reservation but no corresponding job, an empty default queue and Pending
   messages. The exact cause of the missing enqueue is not established.
   Repro observed during consecutive driver runs: Telegram final run 2 retained
   its AI source Pending, default_queue=0, wake_ttl=75, wake_job_exists=false.
   Earlier Telegram ledger/handoff passed; subsequent AI checks sometimes time
   out, including WhatsApp. No reservation was cleared and no hook was invoked
   manually. Inspect `automation_diagnostics` and the real workers to investigate
   enqueue recovery and loaded code. Core's docs/automation-queue.md also records
   a web reload prerequisite; no Core service was restarted here.

## Remaining acceptance

Core must resolve inline eligibility, provider identity preservation and the
observed automation wake failure, then all three drivers must pass together.
Telegram/email 12,000-status connected load acceptance and installed-browser
rendering are not established by this change. No production or live-provider
acceptance is claimed.

## 2026-10-24 lat2 follow-up: Snapshot scan and MIME decoding

This follow-up is uncommitted in `tmp/v15-lat2`. Relay Core is unchanged.
All six final connected runs passed, sequentially, against yrp.site and the
isolated driver schemas. No production access, migration, fixture export,
site configuration change, service restart, commit or push was performed.
The temporary trace code was removed. Existing report timing fields remain.

### Root causes

The suspected fixed callback wait was a driver observation cost. Both channels
already use the WhatsApp commit-triggered callback scheduler and small-text
inline projection. Before the fix, Socket.IO arrived in roughly 100 ms; the
subsequent snapshot inspected every retained `sim-v1` Hub event payload with
leading-wildcard `LIKE` filters. Temporary per-query timers measured 419–482 ms
for that query, versus about 1–2 ms for each other snapshot query. The snapshot
now first restricts events through the existing unique `event_key` index's
`sim-v1:Telegram:` or `sim-v1:Email:` prefix. Sender filters, duplicate checks,
committed message checks, permissions, the Socket.IO gate, the ten samples and
the strict p95 < 500 ms target are unchanged. Snapshot time still counts.
The report label now calls this inbound observation latency, not Socket.IO latency.

The email ledger assertion failed on the second reply because `Parser.parsestr`
misdecoded the captured UTF-8 8bit MIME body: the em dash became literal
`\u2014` and middle dots became replacement characters. `BytesParser` receives
the original UTF-8 bytes in both provider-body readers. Assertions still compare
the complete decoded body, and now explain expected versus actual count/body.
The regression includes Tamil, rupees, an em dash and a middle dot. Telegram
passes the same ledger/handoff acceptance through its JSON provider path; both
channels verify two replies, replay idempotency, permission-scoped ledger reads,
exactly one assigned business-linked ticket and suppression of paused messages.

### Per-hop evidence

Milliseconds for one small inbound per channel. The first three columns use
WhatsApp run 1 and the reproduced Telegram/email baselines. The last columns
use the traced corrected Telegram run 1 and email run 2. These are individual
samples, not percentile summaries. Inline time is contained within callback
HTTP time, not an additional sequential hop.

| Measured interval | WhatsApp | Telegram before | Email before | Telegram after | Email after |
| --- | ---: | ---: | ---: | ---: | ---: |
| Provider webhook send → durable Hub acknowledgement | 11.5 | 3.1 | 9.7 | 2.9 | 5.1 |
| Hub acknowledgement → callback dispatch | 7.3 | 8.9 | 8.1 | 8.6 | 8.3 |
| Callback HTTP through committed acknowledgement | 136.6 | 91.7 | 83.7 | 137.2 | 148.6 |
| Inline projection within that callback | 54.7 | 45.3 | 38.2 | 40.5 | 48.3 |
| Callback dispatch → post-commit Redis publication observed | 114.5 | — | — | 80.5 | 128.9 |
| Redis publication observed → Socket.IO client | ≤ 0.92 | — | — | 0.21 | 0.17 |
| Emit → Socket.IO client | ≤ 134.2 | 116.4 | 94.5 | 101.3 | 143.5 |
| Socket.IO client → snapshot completed | — | 414.4 | 507.8 | 4.4 | 9.6 |

Timing boundaries are explicit: transport ingress includes the local HTTP trip,
and durable acknowledgement bounds the persistence point. Core's SQL commit
was not separately instrumented: Redis observation is after commit; HTTP response
can finish later because other after-commit work still runs. WhatsApp's existing
realtime step starts just before its emit call, so its Socket.IO figures above
are upper bounds. No Core source edit was needed or made just to obtain a SQL
commit timestamp. Redis observations were passive subscriptions, not polling.
The gap is directly measured after Socket.IO, so it is not explained by a Core
queue, projection eligibility or realtime debounce.

Evidence: `.runtime/sim-report/hop-evidence.json`, the trace runs' `hub.log` and
`publish-trace.json`, plus baseline `trace-email/site.log` and
`before-telegram/site.log`. Only sanitized transport timestamps were added to
temporary Hub logs; request credentials were not printed.

### Before/after acceptance

All figures below retain each driver's original timing basis. WhatsApp measures
webhook to Core-row observation (including its test observer interval).
Telegram/email measure emit to a snapshot gated by real Socket.IO. These are
not interchangeable browser rendering measurements. The WhatsApp baseline is
provided by the task; Telegram/email were also reproduced locally.

| Channel / p50, p95 ms | Supplied baseline | Reproduced baseline | Final run 1 | Final run 2 |
| --- | ---: | ---: | ---: | ---: |
| WhatsApp | 266, 293 | — | 271.6, 292.0 | 275.3, 300.1 |
| Telegram | 548, 1026 | 536.6, 576.3 | 107.6, 132.6 | 104.6, 127.6 |
| Email webhook | 571, 734 | 565.4, 602.4 | 97.8, 119.6 | 105.3, 153.1 |

Both WhatsApp storms processed 12,000 receipts with 23 qualifying overlapping
inbounds: 519.3 and 520.1 receipts/s, storm p95 292.0 and 300.1 ms. Receipt
batching, retry/restart, no-polling and permission checks stayed green.

| Channel | Step | Before | Run 1 | Run 2 |
| --- | --- | --- | --- | --- |
| WhatsApp | 7: Signed ingress | pass (supplied) | pass | pass |
| WhatsApp | 8: Journal, Core projection and deduplication | pass (supplied) | pass | pass |
| WhatsApp | 9: Core send idempotency | pass (supplied) | pass | pass |
| WhatsApp | 10: 1,251 receipt batch check | pass (supplied) | pass | pass |
| WhatsApp | 10b: inbound latency under a sustained receipt storm | pass (supplied) | pass | pass |
| WhatsApp | 11: 503, wrong acknowledgement and restart recovery | pass (supplied) | pass | pass |
| WhatsApp | 12: Template lifecycle | pass (supplied) | pass | pass |
| WhatsApp | 13: Supplier AI, scoped ledger and human handoff | pass (supplied) | pass | pass |
| WhatsApp | 14: Business event delivery and Hub outage isolation | pass (supplied) | pass | pass |
| WhatsApp | realtime: Authenticated Socket.IO message event without HTTP polling | pass (supplied) | pass | pass |
| Telegram | preflight_and_fixture | pass | pass | pass |
| Telegram | signed_ingress_and_deduplication | pass | pass | pass |
| Telegram | core_send_and_permissions | pass | pass | pass |
| Telegram | 429_backoff | pass | pass | pass |
| Telegram | attachment_upload_send_ingress_download | pass | pass | pass |
| Telegram | queued_send_restart_recovery | pass | pass | pass |
| Telegram | ambiguous_acceptance_and_restart_no_resend | pass | pass | pass |
| Telegram | shared_customer_supplier_sender_choice | pass | pass | pass |
| Telegram | single_inbound_latency | fail | pass | pass |
| Telegram | supplier_ai_scoped_ledger_and_handoff | pass | pass | pass |
| Telegram | sales_order_event_and_hub_outage | pass | pass | pass |
| Email | preflight_and_fixture | pass | pass | pass |
| Email | smtp_imap_customer_thread_and_recipients | pass | pass | pass |
| Email | webhook_independent_supplier_thread | pass | pass | pass |
| Email | smtp_reply_to_cc_idempotency_and_permissions | pass | pass | pass |
| Email | smtp_to_imap_reply_retains_customer_business | pass | pass | pass |
| Email | spoofed_sender_and_unsigned_webhook_rejected | pass | pass | pass |
| Email | single_business_spoofed_sender_rejected | pass | pass | pass |
| Email | bounce_dsn_reaches_core_status | pass | pass | pass |
| Email | shared_customer_supplier_sender_choice | pass | pass | pass |
| Email | single_inbound_latency | fail | pass | pass |
| Email | supplier_ai_scoped_ledger_and_handoff | fail | pass | pass |
| Email | sales_order_event_and_hub_outage | pass | pass | pass |
| Email | email_provider_identity | pass | pass | pass |

### Validation and remaining scope

- Six connected driver runs: **PASS**, every step (10 WhatsApp, 11 Telegram,
  13 email steps per run). Reports are in `.runtime/sim-report/after-{1,2}-{full,telegram,email}/report.{json,md}`.
- Driver Python suite: **54 tests PASS** using the bench Python with `-B` and
  `PYTHONDONTWRITEBYTECODE=1`; `.runtime/driver-tests.log`.
- `go vet ./...`: **PASS**; `.runtime/go-vet.log`.
- `go test -p 1 ./... -count=1`: **PASS** with `RELAY_TEST_DATABASE_URL`
  privately derived for `relay_hub_v1_test` on 127.0.0.1:18433; `.runtime/go-test.log`.
- Core unit/API/site suites: not applicable; no Core or API changes. The
  connected drivers exercise the installed site over real HTTP and Socket.IO.
- No frontend assets changed, so no build was needed. No restart is needed.
- Not done: a separate Core SQL-commit timestamp and browser rendering
  measurement; the bounds and actual socket/observation timing are stated above.
  Changes remain uncommitted for the orchestrator's review.
