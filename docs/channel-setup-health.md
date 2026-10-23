# Telegram and email setup and health

Accounts now edits WhatsApp, Telegram and email together. Disabled channels keep
saved credentials. Blank password/token inputs preserve stored values; account
responses return only secret-presence names. Replacement inputs are cleared as
soon as a save succeeds, including when runtime activation subsequently fails.
Use Retry activation after fixing an activation failure.

Telegram accepts the BotFather token. Email exposes SMTP host/port/username,
password and From address; optional IMAP host/port/username/password/folder;
trusted authserv-id and trusted DSN domain. SMTP uses implicit TLS on 465 and
required STARTTLS elsewhere. IMAP uses STARTTLS on 143 and implicit TLS elsewhere.
Blank IMAP credentials reuse SMTP credentials; the default folder is INBOX.
Provider hosts accept raw IPv6 literals (for example `2001:db8::25`), without
brackets or a port suffix.
Trusted authentication settings must match the mail provider's verified evidence.
Empty trust settings do not establish sender authentication or DSN authority.

Both channels expose the three Core delivery credentials. The existing dispatcher
shares these at account scope: configure them on one channel or use identical
values on both. Mixed saves reject conflicting complete sets. Before disabling
the only channel holding these credentials, copy all three to a channel that
will remain enabled; the save rejects losing configured Core delivery. This does not change
the dispatcher or its delivery guarantees.

Connection actions use saved, activated runtime settings, not unsaved form edits:

- `GET /api/accounts/{account}/telegram/connection` checks webhook registration,
  whether it matches this Hub, pending updates and the last webhook-error time.
- `POST` to the same path with `{"action":"repair"}` registers the configured
  public HTTPS origin/prefix with the derived account secret. It preserves pending
  updates and subscribes to the update kinds supported by the parser. Registration
  follows [Telegram's webhook API](https://core.telegram.org/bots/api#setwebhook).
- `POST /api/accounts/{account}/email/connection` with `{"action":"test"}` checks
  SMTP TLS/authentication and optional IMAP TLS/login/read-only folder access.
  It sends no mail, fetches no messages and changes no mailbox cursor. SMTP and
  IMAP share a 20-second deadline, leaving 10 seconds before the HTTP server write
  timeout to return diagnostics even if a provider stalls.
- Email ingress setup retrieves the existing admin-only
  `GET /api/accounts/{account}/email/webhook` endpoint on explicit request. Its
  account-derived ingress secret is masked until revealed and is discarded when
  switching accounts/channels or closing the panel. Provider passwords and bot
  tokens are never returned.
- `GET /api/accounts/health?account=...&channel=Telegram|Email` returns account- and
  channel-scoped inbound callback counts, latest Hub reception time, confirmed
  callback time, outbound pending/uncertain/rejected/retry counts and retry time.
  Email additionally includes mailbox failures/backoff, last sync, UIDVALIDITY,
  last UID and reset-snapshot UIDNEXT. Mailbox rows include retained prior cursors.
  Health reads have a shared two-second deadline. Indexed samples cover the latest
  1,000 inbound events and latest 1,000 unresolved outbox rows (queued,
  dispatching, uncertain or rejected); completed outbox history is excluded.
  `sample_limit`, `inbound_truncated` and `outbox_truncated` describe the cap, and
  the UI labels truncated evidence. Counts, confirmation time and retry time
  describe these samples, not all historical rows. Mailbox rows are capped at 100.
  New events record `received_at` independently from provider `occurred_at`, so
  Telegram edits advance ingress evidence while preserving the original message
  date. Duplicate updates retain their original reception time. Historical rows
  have unknown reception time; migration does not invent recent ingress.

All endpoints require an admin session. Mutating/test actions also require the
existing same-origin CSRF protection. Provider error descriptions and provider
URLs are excluded from responses. The ingress-secret endpoint is the intentional,
explicit credential-distribution exception. A matching Telegram URL does not
prove its secret is current; repair reapplies it, and incoming-update evidence
confirms delivery. Provider acceptance remains distinct from delivery evidence.

## Local validation

Go endpoint tests exercise allow/deny cases, response shapes and redaction;
PostgreSQL tests exercise populated channel queues, mailbox backoff/cursors,
account isolation and encrypted mixed-channel save/reload/blank-secret retention.
Protocol tests use real local TLS SMTP/IMAP fixtures, including rejected login,
invalid TLS, interrupted reads and mailbox recovery. Real HTTP server checks
exercise stalled SMTP, stalled IMAP and sequential slow SMTP/IMAP against the
production write deadline. Query-plan tests over 30,000 events and 45,000 outbox
rows enforce bounded index reads (including callback joins), and a database lock
verifies deadline cancellation. Telegram edit/replay tests use authenticated
webhook ingestion and PostgreSQL. Telegram uses a restricted
local HTTP transport fixture. No external provider messages are sent.

Validated with `go vet ./...`, `go test -p 1 ./... -count=1` using isolated schemas
in `relay_hub_v1_test` at `127.0.0.1:18433`,
`node --test frontend/tests/*.test.mjs`, and `npm run build` in `frontend`.
Browser checks against the production UI build use local fixture API responses at
390 px and 1280 px, in light and dark modes: forms, masked secrets, repair/test
results, inline validation, mixed saves, Connections and horizontal overflow.
These browser checks do not establish connected Core or live-provider acceptance.
