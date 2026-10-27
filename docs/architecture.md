# Relay Hub architecture

Relay Hub is the Go transport boundary. Relay Core is a Frappe app installed alongside business apps and uses Frappe authentication. Hub's separate administrator login manages transport accounts, users and tokens; it is not a replacement Core login.

```mermaid
flowchart LR
    UI[Hub administration UI] -->|Secure session and CSRF| Hub[Go Relay Hub]
    Core[Relay Core on each Frappe site] -->|Account-scoped MCP token| Hub
    Hub --> Auth[(PostgreSQL accounts, users, sessions, audit)]
    Hub -->|WhatsApp delivery intents| Queue[(Durable WhatsApp outbox)]
    Queue --> Workers[Bounded workers and account pacing]
    Workers --> WA[Meta WhatsApp]
    Hub -->|Direct submission| TG[Telegram Bot API]
    Hub -->|TLS SMTP| Mail[Email server]
    WA -->|Verified webhook| Events[(Durable event journals)]
    TG -->|Per-account verified webhook| Events
    Ingress[Normalized email ingress] -->|Verified JSON| Events
    Events --> Callbacks[Durable callback cursors]
    Callbacks -->|Signed events and status batches| Core
```

## Ownership and identity

A Hub account links one Core site and can enable several channels. Provider credentials stay in Hub; tokens authorize an account and explicit tool scopes. Core resolves Contacts, business records, teams, user permissions, conversation bindings and AI actions. An account-level Hub permission does not replace a business-record check in Core.

PostgreSQL stores sequential numeric identities for high-volume journals and queue rows. Provider message IDs, Telegram chat/topic IDs and caller request keys are separate fields. Group conversations remain separate from participant DMs; quoted replies retain the original provider message ID. See the [database model](database-authentication-model.md) and [API reference](api-reference.md).

## Send and receive guarantees

WhatsApp's durable outbox admits bounded batches of up to 50 intents with stable-key deduplication. Workers claim work through PostgreSQL and obey account pacing. A queued result means accepted locally, while submitted means the provider returned an accepted result; neither proves delivery or reading.

A lost provider response can leave an intent uncertain. The Hub does not blindly replay that mutation. Stored status events can reconcile receipts without resending. Direct WhatsApp execution, Telegram message operations and SMTP sending do not inherit the WhatsApp queue's admission/deduplication guarantees.

Verified inbound payloads are journaled before acknowledgement. Callback cursors advance after Core acknowledges durable processing. WhatsApp separates realtime and batched-status lanes; Telegram/email use the channel-event callback path. Raw payloads remain available for provenance and typed projection. Reactions, edited messages and membership events are not equivalent to new conversational text.

## Bounded resources

The launcher defaults to limited Go/build concurrency. WhatsApp adapts worker count between one and the configured ceiling, up to eight; this is process worker scaling, not automatic deployment scaling. PostgreSQL coordinates account send pacing across workers.

Queries, batches, provider bodies, photo dimensions and sticker downloads have explicit bounds. Frontend lists use [bounded rendering](frappe-virtualization.md) and server pagination. Capacity depends on provider latency, database limits and workload; simulations do not promise a production throughput figure.

## Credentials and media

Runtime registry replacement publishes validated snapshots so later requests see rotated or disabled channel credentials. An already in-flight request can complete against the older snapshot. Browser sessions and Hub API tokens are PostgreSQL-backed and revocable.

The main channel secret is encrypted. Some generic configuration fields that contain secrets are not; the [configuration reference](configuration.md#current-administration-limits) records that production gap and the current grant-list limitation.

Sticker retrieval requires an account-owned inbound record and uses bounded, constrained provider downloads. Hub returns bytes and MIME metadata to Core, which checks message visibility before serving the browser. This is not a general private media store, attachment archive or call-recording service.

## Current boundaries

See the [provider support matrix](provider-support.md) for the exact implemented surface. General Telegram media/edit/delete APIs, full email mailbox/MIME handling, hourly provider reconciliation, server call recording and replica autoscaling are not supplied by this implementation. Live account eligibility and provider delivery remain a separate acceptance step after [local simulation](testing.md).

### Bounded channel callback replay

Telegram/email production callbacks and explicit replay now share one encoder
and acknowledgement path. The source query selects at most 50 rows and applies
an aggregate source-byte bound; final JSON encoding still enforces the callback
body limit (2 MiB minus 4 KiB headroom). The first source row is always returned
so an oversized event produces an explicit failure rather than disappearing
behind a size filter. Failed callbacks retain their cursor. This does not yet
provide poison-event quarantine or separate compact status lanes for these
channels.

Validation: isolated PostgreSQL with Go race checks for `internal/channels` and
`internal/hubmcp` passed. Coverage includes recovery/account isolation, two
800 kB events followed by a one-event batch, and an oversized first event that
is neither transmitted nor skipped. Provider calls use local simulators. This
change is local and has not yet replaced the running Hub binary.

### Channel callback quarantine (local, not deployed)

Migration 0027 records an oversized Telegram/email callback by original event
ID, account, channel and reason. Production dispatch commits that record and
cursor movement in one transaction. It does not alter or delete the journaled
payload. A malformed, unauthenticated or unavailable Core response still
retries without advancing; only a locally proven oversized event qualifies.
Explicit replay returns the size failure and does not itself skip events.

The admin-only `/api/callback-failures` endpoint lists 50 failures per page with
account/channel filters and keyset pagination. Hub Channels exposes this list
for Telegram and email. Original event inspection also includes the quarantine
reason. The isolated PostgreSQL/race test proves original-data retention and
later-event delivery after quarantine. Operator recovery/replay controls and
installed browser/API authorization acceptance remain unfinished; this change
must remain local until those checks are complete.

Callback recovery now implemented locally: an admin-only CSRF/origin-protected
POST retries exactly the selected quarantined event under the channel advisory
lock. Runtime account credentials are resolved server-side. Successful Core
acknowledgement sets `resolved_at`; failure keeps quarantine active. Production
cursors do not rewind. Failure history and original payload remain retained.
The Channels failure list exposes Retry callback with an explicit underlying
issue warning; retries do not magically reduce an oversized payload.

Isolated database/race checks pass for successful single-event retry, rejected
oversized retry, cross-channel rejection, failed acknowledgement retaining
quarantine, and already-resolved rejection. HTTP unit tests cover non-admin,
origin, CSRF, unexpected fields, channel validation and failure responses.
Installed deployment/browser acceptance remains outstanding.

Callback recovery deployed locally: migration 0027 applied successfully;
`relay-hub-callback-recovery` PID 2931375 passed readiness. The deployed UI files
were exercised through a temporary loopback static server with explicit API
fixtures (server stopped afterwards). Browser validation caught and fixed a
failed-retry row being hidden by its error; the rerun proves the row remains,
the retry uses the selected account/channel/event, and successful recovery
refreshes the list. No real callback was replayed. The live unauthenticated
failure-list request redirects to login (303). An existing 18126 browser tab
was a separate test preview, not an authenticated session on this runtime;
live authenticated admin API/browser acceptance remains unproven.

## Message event inspection (24 September 2026)

The Events screen defaults to provider messages; system authentication activity
is a separate tab. The admin-only `/api/event-inspector` endpoint requires an
account and channel, lists 50 summaries per keyset page, and loads payloads only
on selection. SQL limits summaries to 240 characters and payload previews to
65,536 characters. The complete source remains in the database. Forwarded
means Core acknowledged receipt, not that a customer read a message.

The local event-inspector release is deployed and `/readyz` passed. Isolated
PostgreSQL tests cover account/channel isolation, bounded reads, cursor paging,
quarantine visibility and partially forwarded WhatsApp batches. Browser fixture
checks cover payload escaping, pagination, scope reset and desktop/mobile
layouts in light/dark themes. Live authenticated Hub browser acceptance remains
separate from these fixture checks.
