# Connected release evidence

The 2026-10-24 08:03 quiet-machine checkpoint passes all connected cases: WhatsApp 10/10, Telegram 11/11 and email 13/13. The drivers use local fake providers with real Hub journals, outboxes, workers and signed callbacks. Core HTTP authentication, business permissions, projection and Socket.IO remain real. External AI responses use a local fixture.

## Measurements

| Channel | Passed steps | Single inbound p50 | Single inbound p95 |
| --- | ---: | ---: | ---: |
| WhatsApp | 10/10 | About 270 ms | About 295 ms |
| Telegram | 11/11 | 105 ms | 123 ms |
| Email webhook | 13/13 | 107 ms | 119 ms |

The WhatsApp single inbound figures and zero-deadlock result are supplied with the release checkpoint. The retained `full/report.json` reports combined inbound p50/p95 of 266.1/293.2 ms; it has no separate single inbound percentile field or per-run deadlock counter. Its storm result is 12,000 receipts in 23.08 seconds, 519.9 receipts/s, and inbound p95 293.2 ms across 22 observations that overlap receipt processing. The required gate is at least five overlaps and p95 below 500 ms.

Telegram and email values come from `single_inbound_latency_ms`: ten sequential text probes per channel, with exact p50/p95 of 104.5/122.7 ms and 107.2/119.4 ms. The report headings use `latency_ms`, which combines other test observations. Those mixed figures are not the single inbound gate. Email also records two IMAP arrival observations at p50/p95 121.6/127.6 ms; this small sample does not establish general mailbox latency.

WhatsApp measures signed webhook send to an observed Core row, including a 20 ms test database observation interval. Telegram/email measure control emit to a Core snapshot after a real Socket.IO event. Snapshot time counts. IMAP observations include mailbox arrival. None of these figures measures browser rendering or a separate Core SQL commit timestamp.

Read the [latency diagnosis](v15-hub-latency-validation.md#2026-10-24-lat2-follow-up-snapshot-scan-and-mime-decoding) for the snapshot-query and MIME-decoding findings. The current evidence files are read-only reports at `/home/sakthi/development/services/relay_hub/.runtime/sim-report/{full,telegram,email}/report.{json,md}`. The run IDs are `1792808042199589057`, `1792809143944478303` and `1792809165611001235` respectively. Runtime files can contain private fixture data and must not enter Git.

## Proven behavior

| Channel | Connected assertions |
| --- | --- |
| WhatsApp | Signed ingress and replay; one send for one request key; 1,251 receipts in batches of 500, 500 and 251; no status regression; sustained 12,000-receipt storm; 503/wrong-acknowledgement retry and Hub restart; template lifecycle; authorized Socket.IO and denied-user isolation |
| Telegram | Verified ingress and replay; Core send permissions; 429 backoff; attachment upload/send/receive/download; queued restart recovery; ambiguous acceptance without resend; shared Customer/Supplier sender choice |
| Email | SMTP/IMAP round trip; independent Customer/Supplier threads; To/CC and reply headers; send replay and permissions; failed sender authentication and unsigned ingress rejection; bounce DSN to Core Failed status; sender choice; preserved provider Message-ID |
| All three | Supplier AI with scoped ledger reads, replay, one assigned handoff ticket and paused-contact suppression; Sales Order event delivery and retained delivery intent during Hub outage |

This evidence does not cover bulk campaigns, live provider acceptance, installed-browser rendering or production capacity. Telegram/email 12,000-status storms are not covered. The connected email cases do not cover every SMTP failure, BCC, forwarding or attachment path. See [provider support](provider-support.md) for implemented features and limits.

## Rerun

Run the drivers sequentially from a Hub checkout. The examples use the local bench interpreter and the report directories above. They are commands for an operator; reading this evidence does not require a rerun.

```sh
RELAY_SIM_PY=/home/sakthi/development/pilot/benches/version_16/env/bin/python

"$RELAY_SIM_PY" -B scripts/simulate_whatsapp_e2e.py \
  --site yrp.site \
  --sites /home/sakthi/development/pilot/benches/version_16/sites \
  --core-origin http://127.0.0.1:8001 \
  --socketio-origin http://127.0.0.1:9001 \
  --storm-receipts 12000 \
  --report-dir .runtime/sim-report/full

"$RELAY_SIM_PY" -B scripts/simulate_telegram_e2e.py \
  --report-dir .runtime/sim-report/telegram

"$RELAY_SIM_PY" -B scripts/simulate_email_e2e.py \
  --report-dir .runtime/sim-report/email
```

| Requirement or effect | Current driver behavior |
| --- | --- |
| Core services | The development site, web, Socket.IO and workers must already run the current code. Projection uses short/long workers; default automation has a separate worker. |
| Data | Drivers create identifiable local users, Contacts, business records, verified endpoints, consent, messages, AI fixtures and non-zero business documents. Core fixture records remain for inspection. |
| Side effects | Local provider fixtures handle sends. Preflight rejects active notifications/webhooks on fixture DocTypes. Fixture provisioning mutes email. |
| Database | Drivers use a unique schema in `relay_hub_v1_test` at `127.0.0.1:18433`. They apply Hub migrations there and drop that temporary schema on cleanup. They do not open `channel_hub` or `relay_hub_sim`. |
| Settings | Drivers temporarily replace Hub connection settings, restore prior values on cleanup and disable fixture automation. They do not edit `site_config` or migrate Core. |
| Concurrency | A shared local file lock prevents concurrent drivers in the same checkout. Run only one driver against the site at a time. |
| Output | Reports, logs and private provider state stay under `.runtime`. A failed connected run exits nonzero. |

The bench interpreter needs the installed `psycopg2` and `websockets` packages. The drivers build the Hub test launcher with Go. Private database credentials come from the ignored Hub `.env`; do not print them. The WhatsApp driver also supports `--dry-run`, which produces a plan without starting services or writing Core records.

Source: `scripts/simulate_{whatsapp,telegram,email}_e2e.py`, `scripts/channel_simulation.py`, `scripts/simulation_common.py`, `scripts/whatsapp_sim_site.py` and `scripts/channel_sim_site.py`.
