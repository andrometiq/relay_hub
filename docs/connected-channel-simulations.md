# Connected Telegram and email simulations

The 2026-10-24 checkpoint passes all 11 Telegram cases and all 13 email cases. The drivers connect the real Hub launcher, PostgreSQL journals, outbox workers, Core HTTP APIs and Frappe Socket.IO to local fake providers. They do not render a browser.

| Channel | Connected evidence | Single inbound p50/p95 |
| --- | --- | ---: |
| Telegram | Verified receive/replay; send permissions and idempotency; 429 retry; attachment round trip; queued restart recovery; uncertain acceptance without resend; sender choice; scoped AI/handoff; business events and Hub outage | 105/123 ms |
| Email | TLS SMTP/IMAP; separate Customer/Supplier threads; To/CC and reply headers; send replay; sender-authentication rejection; bounce DSN to Core; sender choice; scoped AI/handoff; business events and Hub outage; provider Message-ID | 107/119 ms |

Each latency result uses ten sequential text probes from `single_inbound_latency_ms`. Measurement starts before provider emit and ends at a Core snapshot gated by a real Socket.IO event. The snapshot cost is included. The report's combined `latency_ms` also includes other observations and can differ. Two email IMAP observations report p50/p95 121.6/127.6 ms, including mailbox arrival.

## Run and inspect

From a Hub checkout, with the development Core services already running:

```sh
RELAY_SIM_PY=/home/sakthi/development/pilot/benches/version_16/env/bin/python
"$RELAY_SIM_PY" -B scripts/simulate_telegram_e2e.py --report-dir .runtime/sim-report/telegram
"$RELAY_SIM_PY" -B scripts/simulate_email_e2e.py --report-dir .runtime/sim-report/email
```

Run one driver at a time. Both default to `yrp.site`, Core at `http://127.0.0.1:8001` and Socket.IO at `http://127.0.0.1:9001`. Reports are `report.json` and `report.md` in the selected directory. The retained installed-checkout reports are under `/home/sakthi/development/services/relay_hub/.runtime/sim-report`.

Read [release evidence](simulation-validation.md#rerun) for prerequisites and local writes. Each driver uses a unique temporary schema in `relay_hub_v1_test`, restores prior Hub settings on cleanup and retains Core fixtures. The driver does not migrate Core or edit site configuration. Runtime artifacts can contain private fixture data.

## Evidence limits

| Area | Boundary |
| --- | --- |
| Browser | Real Socket.IO and permission-checked APIs are tested. Composer interaction, browser paint, mobile layout and email rendering are not tested by these drivers. |
| Load | The single inbound gate is p95 below 500 ms. These reports do not establish Telegram/email 12,000-status throughput. |
| Telegram | The driver proves a document round trip and text sends. It does not test all media, group, topic or administration operations. Bot acceptance is not recipient delivery/read evidence. |
| Email | The driver proves To/CC and bounce DSN handling. It does not prove BCC, forwarding, every SMTP failure stage, attachment round trips or all IMAP recovery cases. SMTP acceptance is not inbox delivery. |
| Deployment | Fake providers do not prove live credentials, external webhook access, provider permissions or production capacity. Bulk campaigns are outside this evidence. |

See [provider support](provider-support.md) for source-backed capabilities. The [latency diagnosis](v15-hub-latency-validation.md#2026-10-24-lat2-follow-up-snapshot-scan-and-mime-decoding) explains the corrected snapshot query and MIME decoding. The supplied reports pass sender choice, failed-authentication triage, DSN projection, AI handoff and provider identity checks.
