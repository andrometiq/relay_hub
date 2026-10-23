# Synthetic simulation validation — 2026-09-23

This pass uses synthetic records, loopback provider servers, and isolated
PostgreSQL schemas. No live provider account or production deployment is used.
The scope is simulation of the implemented paths, not certification of the full
Relay feature list or a claim that live provider behavior is identical.

## Latest combined result

All eleven runner checks passed on 2026-09-23. The Hub reported **425 successful
test/subtest events, zero failures and zero skips**, including real PostgreSQL
integration and race detection. Core reported **65 unit tests passed and 2
configured-Hub smoke tests skipped**, plus **95 real Frappe/PostgreSQL scenarios**
(10 automation, 44 multi-channel/API/ticket, 17 group and 24 typed-message scenarios).
Hub's 2 frontend data tests and Core's 16 frontend data tests,
static analysis and whitespace checks passed. The browser results below were
run separately against built component fixtures. Core's exact final production
frontend build also passed after the connected message controls were corrected.
See the [full group coverage matrix](group-simulation-coverage.md).

The 2,000-message admission test compared every stored payload with its input.
Forty 50-message MCP chunks were persisted in **2.582 seconds** in this local run;
maximum request size was 57,923 bytes. This measures local queue admission only,
not delivery throughput or production capacity. No external provider calls were
made. Synthetic Core records remain in the isolated fixture for inspection;
temporary Hub schemas and owned test services were cleaned up.

## Reproduce the combined run

From `relay_hub`, with the isolated `relay-ai-test.local` Frappe fixture installed:

```sh
python3 scripts/simulate.py \
  --frappe-python ../pilot/benches/version16-dev/env/bin/python
```

The runner permits only the isolated loopback `relay_ai_test_20260922` database.
It uses bounded Go test concurrency, enables database integration tests and the
race detector, and saves per-check output to `.runtime/simulation-report.json`.
Missing/skipped Go integration tests fail the run. Two older Core running-Hub
smoke tests remain intentionally opt-in; the simulation suite replaces their
need for a configured account with local MCP fixtures, and reports those skips.
The runner starts isolated Redis only when needed and stops only its own process.

## Expected versus observed behavior

| Simulation | Expected result | Evidence |
| --- | --- | --- |
| SMTP STARTTLS and implicit TLS | Authenticated send; verified certificates; To/Cc visible, Bcc envelope-only | `internal/email/tls_simulation_test.go` performs real loopback TLS/SMTP exchanges |
| SMTP refusal/cancellation | Failed recipient prevents DATA; cancellation closes stalled connection; accepted DATA survives failed QUIT | Same suite plus `internal/email/simulation_test.go` |
| Telegram send | Validate provider receipt, reject error/malformed/oversized responses, preserve Tamil/reply IDs, redact token in errors | `internal/telegram/simulation_test.go` |
| Multi-channel callback recovery | Deduplicate events, isolate accounts, sign requests, leave cursor on 503, resume after restart | `internal/channels/callbacks_integration_test.go`: failed 50, retried 50, remaining 3 |
| WhatsApp integrated round trip | Scoped authentication, persistent enqueue, simulated Graph send, signed webhook, monotonic status, signed Core delivery | `internal/hubmcp/roundtrip_simulation_test.go`: actual Hub services; provider and Core endpoints are local HTTP fixtures |
| Core Telegram/email round trip | Signed callback → real DB projection → decision/ticket/outbox → real HubClient HTTP call | `tests/integration_channels_site.py` in Core; deterministic model and local MCP simulator |
| Group workflows | WhatsApp lifecycle/media/templates/receipts and Telegram administration/topics/photos; strict group/DM/account isolation | [Group coverage matrix](group-simulation-coverage.md), including 17 real Core scenarios |
| Typed messages | Exact sticker/reply/location/contact/reaction payloads, reaction removal, vCards, durable WhatsApp replay and private/group routing | Hub Telegram/WhatsApp typed suites and `internal/hubmcp/message_types_simulation_test.go` |
| Account-bound sticker reads | Deny unknown/cross-account IDs before provider calls; bounded WebP/WebM/TGS bytes without token URLs; 2 MiB content fits the Core 3 MiB envelope | Hub media suites and `TestMediaEnvelopeDoesNotDuplicateLargePayload` |
| Core typed projection/actions | Same-second update ordering, no new AI turn from edits/reactions, edited-source cancellation, AI-disabled receiving, revoked business/Contact/channel checks, known sticker reuse, uncertain-send deduplication | Core `tests/integration_message_types_site.py`: 24 scenarios and 30 loopback HTTP submissions |
| Credential reload and webhook registration | Old captured MCP handlers use rotated credentials and reject disabled accounts; per-account ingress secrets reject other accounts; registration metadata is admin-only | Atomic registry race tests, actual MCP reload test and admin HTTP handler tests |
| Bulk admission | 2,000 exact payloads in 40×50 chunks; replay/conflict/oversized batch/account isolation | Same Hub simulation suite; admission only, no provider sends |
| Core authorization changes | Cancel queued delivery after endpoint/channel/business permission/link revocation or reassignment | Same real Frappe suite; configuration changes are actual synthetic DB writes |
| Core history/tickets | Bounded forward/reverse paging, channel filters, multi-channel message links, ticket resolution, owner isolation | Core channel integration suite |
| Core WhatsApp automation | One ticket/send under replay and concurrent workers; takeover/revocation block delivery; uncertain sends not replayed | `tests/integration_automation_site.py` in Core |
| AI providers | Correct auth and Tamil JSON through real HTTP parsing; incomplete/oversized/error/redirect responses rejected | Core `tests/test_provider_simulation.py`, with network destination replaced by loopback |
| Virtual table | Bounded page/cache/DOM usage with reverse loading at mobile/tablet/desktop sizes | Core `frontend/tests/browser/simulation.mjs` against built fixture |

Browser simulation: one million logical records are generated only in requested
pages. At widths 1440, 768, 390 and 320, requests stayed at 50 rows; maximum DOM
rows were 21, 24, 21 and 15 respectively. The viewport scroll extent never exceeded
200 cached rows. All four sizes passed reverse paging and had no horizontal page
overflow or uncaught JavaScript errors. Automatic forward/reverse scrolling was
also exercised at 390px. This is not a million-row database benchmark.

To reproduce the browser fixture, build Core frontend with
`vite build --config tests/browser/vite.config.js`, serve
`frappe_relay_core/.runtime/simulation-ui` on `127.0.0.1:18120`, and invoke the
exported simulation function with a Playwright page. Browser evidence is saved
separately at Core `.runtime/browser-simulation-report.json`.

## Typed message browser checks

The built connected Messages fixture passed seven synthetic submissions covering
quoted text, reaction removal, zero-valued coordinates, contact cards, sticker
reuse, rejected drafts and permission errors. Request keys were preserved on
unconfirmed retries; the browser supplied no provider routing overrides. WhatsApp
group controls excluded unsupported types. Widths 320, 390, 768 and 1440 passed
without horizontal overflow or uncaught errors; requests stayed at 50 rows.

The TGS fixture rendered changing canvas frames and passed explicit loading,
pause/resume, offscreen pause, unmount cleanup, keyboard activation, reduced
motion, 320px layout and missing-decompression fallback checks. No unexpected
external requests occurred. Helper tests cover oversized gzip expansion,
malformed data, external assets, expressions and excessive animation complexity.

Core fixture entry points are `frontend/tests/browser/messagesSimulation.mjs`
and `frontend/tests/browser/tgsSimulation.mjs`. Their Vite configs build isolated
artifacts in `.runtime/messages-simulation-ui` and `.runtime/tgs-simulation-ui`.
Serve only on loopback and run the exported functions with a Playwright page.
The browser endpoints are synthetic; real Frappe permission/database checks are
the separate integration scenarios above.

## Failures found and corrected during simulation

- SMTP ignored cancellation while blocked on network and allowed subject header
  injection. Transport now has a deadline/cancellation path and header validation.
- SMTP enforces TLS; failed QUIT after accepted DATA is not reported as failed send.
- Telegram trusted HTTP status without verifying an accepted message receipt and
  exposed token-bearing URLs in transport errors. Both are now covered.
- Core Telegram/email dispatch failed to recheck business/channel permissions
  after inference. Disabled channels and revoked/reassigned business links now
  cancel queued delivery before HTTP submission.
- AI HTTP failure responses were not closed. Error responses are now closed while
  preserving credential-safe error messages.
- Group simulations corrected WhatsApp ownership pagination, unsupported API
  operations and group/DM history mixing; Telegram replies now retain group/topic
  provenance. Concurrent reloads reach existing MCP handlers and Telegram webhook
  authentication uses per-account secrets. See the group report for details.
- Incoming projection incorrectly stopped when AI was disabled. Basic receiving
  and message updates now run independently; the automation service retains its
  own enablement checks. Revoked business/Contact/channel permissions also block
  typed history, media and sending. Native shared select components corrected
  the connected UI's label association and browser interaction.

## Explicit coverage boundaries

Core inbound simulations invoke its real handler with Werkzeug POST requests,
real Frappe roles and PostgreSQL; they do not run a complete WSGI login server.
Outgoing Core tests exercise real HTTP to a simulated MCP Hub; Hub tests exercise
the real Hub against simulated Core callbacks. These connected segments are not
a single deployed Core+Hub browser session. The virtual table fixture is a real
component test, not full installed Core UI acceptance.

IMAP ingestion/threading/attachment workflows and other unimplemented product
features cannot be certified by passing these tests. Provider template/group/call
contract simulations do not establish live Meta eligibility, recording support,
provider throughput, AI answer accuracy, or production autoscaling. These limits
remain visible instead of treating credentials as a blocker to local simulation.
