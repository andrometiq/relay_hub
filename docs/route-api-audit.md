# Route and API audit — local, 2026-09-21

> Historical checkpoint. Claims and test results below describe that stage of development. Use the [current support matrix](provider-support.md), [API reference](api-reference.md) and [testing guide](testing.md) for current behavior.

## Fixed locally

- Main views now have hash URLs in both applications. Direct links, refresh and
  Back navigation work on the existing static servers without rewrite rules.
  Previously the active view existed only in component memory.
- Core channel updates are explicitly POST-only. Read endpoints allow GET/POST.
- Core validates channel/contact/cursor identifiers before lookup, rejects invalid
  account names and enabled flags, and validates/caps page limits.

## Checked

Browser navigation and reload: Core dashboard, inbox, contacts, campaigns,
canned-replies, flows, teams, channels; Hub overview, accounts, channels, delivery,
events. Core Back navigation returns to the preceding view. Unknown Hub hash
routes normalize to overview. These URLs preserve the view, not session drafts.

Live Hub HTTP checks: GET /, /healthz, /readyz → 200; POST /healthz and /readyz →
405; /missing and /.env → 404; unauthenticated GET/POST/DELETE /mcp → 401;
PUT /mcp → 405. Core-to-Hub MCP initialize + health succeeds; wrong token fails.
Go tests cover denied browser origins, internal-error redaction, invalid JSON,
unknown methods/tools and oversized bodies; malformed requests do not invoke
health checks. Database migration tests run against the isolated local Hub DB.

Core: 14 Python tests pass, including 5 new API contract tests and 2 live Hub
checks. The Core Frappe API tests use a mocked Frappe boundary; they do NOT prove
installation, transaction behavior or actual Frappe HTTP dispatch. Four frontend
unit tests pass. Both frontend builds pass.

## Current API inventory

- Hub: GET /healthz; GET /readyz; browser-authenticated admin GET /api/accounts,
  `/api/channels`, `/api/deliveries`, `/api/audit`, and `/api/channel-events`;
  authenticated `/mcp` exposes health, WhatsApp, Telegram and email operations.
- Core (source code, not installed): /api/method/relay_core.api.channel_settings;
  /api/method/relay_core.api.update_channel; /api/method/relay_core.api.messages.
- Core message retrieval uses permission-aware get_list, a maximum of 100 records,
  and sequential-name cursors. Secret fields are omitted from channel responses.

## Not ready / unverified

Message sending, provider callbacks, template sync, bulk jobs, status batching
and autoscaling endpoints do not exist yet. Live provider runtime reload after
account save is still pending. Core APIs have not been installed on a Frappe
test site. This audit does not establish production readiness or message
throughput.

No changes from this audit were committed or pushed. Production data untouched.
