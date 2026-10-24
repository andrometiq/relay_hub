# v1.5 Hub ingress and connected-driver validation

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
