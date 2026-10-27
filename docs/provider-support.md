# Provider support

Relay Hub is a Go transport service. Core owns business permissions, conversations and AI. This page describes the source checked on 2026-10-24. Implemented support and connected test evidence are separate.

## Channel capabilities

| Capability | WhatsApp | Telegram | Email |
| --- | --- | --- | --- |
| Send | Text, replies, templates and supported media; typed reactions, locations and contacts | Text, replies, topics, stickers, reactions, locations, contacts and bounded photo/video/audio/voice/document uploads | TLS SMTP, To/CC/BCC, text/HTML alternatives and bounded MIME attachments |
| Receive | Signed messages and provider events | Account-secret verified messages, edits, reactions, membership and service updates | Account-secret ingress or TLS IMAP; MIME text/HTML, recipients, thread headers and attachments |
| Status | Provider sent, delivered, read and failure receipts | Bot acceptance, rejection or uncertain outcome; no recipient delivery/read stream | SMTP acceptance; trusted recipient-specific delayed, bounced and delivered DSNs; no read receipts |
| Durable send | PostgreSQL outbox and stable request keys | PostgreSQL channel outbox and stable request keys | PostgreSQL channel outbox and stable request keys |
| Retry | Bounded retry for known retryable rejection; uncertain outcomes require review | Bounded 429 retry honors `retry_after`; uncertain outcomes require review | Retryable failures before DATA can retry; ambiguous DATA outcomes require review |
| Send order | Earlier unresolved sends block the same account/destination | Earlier unresolved sends block the same account/chat | Earlier unresolved sends block overlapping recipient sets within an account |
| Core callbacks | Separate incoming-event and status workers | Signed callbacks with per-event acknowledgements; separate status workers | Signed callbacks with per-event acknowledgements; separate status/DSN workers |
| Setup | Account credentials and provider health | Bot identity, webhook registration, supported update subscriptions and health | SMTP/IMAP configuration, connection checks, mailbox health and rejection records |

The send schedulers select work across accounts. A recovered worker can queue a send again when it stopped before the durable submission boundary. A send that crossed that boundary remains uncertain until evidence resolves it. This is not an exactly-once guarantee from a provider.

Telegram and email incoming callbacks wake after journal commit. Failed events retry separately. Repeated failures and oversized events enter quarantine for inspection and replay. A late transaction cannot be skipped by a high-water cursor. Status callbacks use a separate pool and batches of at most 500 rows and 512 KiB.

Email IMAP IDLE wakes a bounded fetch scheduler. Each fetch reads at most 20 UIDs. EXAMINE and BODY.PEEK keep mailbox messages unread. UID/UIDVALIDITY state supports reconnect and restart. A 30-second recheck also covers servers without IDLE. Slow accounts do not hold all fetch capacity.

## Connected evidence

The 2026-10-24 release checkpoint passes 10/10 WhatsApp, 11/11 Telegram and 13/13 email steps. Single inbound checkpoint p50/p95 values are approximately 270/295 ms, 105/123 ms and 107/119 ms respectively. The WhatsApp storm handles 12,000 receipts at approximately 519 receipts/s with inbound p95 approximately 293 ms. See [release evidence](simulation-validation.md) for report fields, timing boundaries and rerun commands.

| Channel | Proven through the connected local path |
| --- | --- |
| WhatsApp | Signed ingress, replay deduplication, Core send idempotency, receipt batches and order, callback failure/restart recovery, templates, Socket.IO, scoped AI/handoff and business-event outage isolation |
| Telegram | Verified ingress, send/replay, 429 backoff, attachment upload/download, queued restart recovery, uncertain restart without resend, sender choice, scoped AI/handoff and business events |
| Email | TLS SMTP/IMAP round trip, separate Customer/Supplier threads, To/CC, send replay, sender-authentication rejection, bounce DSN to Core, sender choice, scoped AI/handoff and business events |

The reports prove these cases with local fake providers and the real Hub/Core path. Ordering, callback commit order, SMTP failure stages and DSN replay also have focused tests in `internal/channels/*_integration_test.go` and `internal/email/*_test.go`. The connected email driver does not test every SMTP failure stage, BCC or attachment round trip. Telegram/email 12,000-status load acceptance is not established.

## Capability boundaries

| Area | Boundary |
| --- | --- |
| Bulk messaging | Bulk campaigns are outside this release evidence. Bounded admission APIs do not establish campaign delivery support. |
| Browser | Drivers use real Socket.IO and permission-checked APIs. They do not render the installed UI or measure browser paint. |
| Live providers | Local success does not prove credentials, account eligibility, public webhooks, recipient rendering or production capacity. |
| Telegram | Existing chats require bot membership and permissions. No group creation/deletion, calls, polls, inline keyboards, callback-query responses, message editing/deletion or arbitrary Bot API calls are exposed by the typed message tool. Text is plain text, limited to 4,096 characters; captions are limited to 1,024. Migration hints do not change a destination automatically. |
| Email | SMTP acceptance does not prove inbox delivery. Only trusted structured DSNs supply delivery evidence. Normalized JSON without trusted raw-mail evidence remains unverified. A From address alone does not authenticate a sender. The Hub has no email template engine or universal provider-webhook verifier. |
| WhatsApp groups | Account capability and provider permission checks apply. Group sends support text, media and supported templates. Group reactions, locations, contacts and manual participant addition are not supported. Aggregate Read means the highest observed participant state, not that all participants read. |
| Calls | WhatsApp signaling is capability-gated. Server call recording and Telegram calls are not implemented. |

See the [API reference](api-reference.md), [group coverage](group-simulation-coverage.md) and [Telegram/email simulations](connected-channel-simulations.md).

Source: `internal/whatsapp/outbox.go`, `internal/channels/{outbox,callbacks,callback_recovery,status_callbacks,status_journal,registry,mailboxes}.go`, `internal/email/{adapter,mailbox,authentication,status}.go`, `internal/telegram/{messages,uploads,updates,webhook,groups}.go` and `cmd/relay-hub/account_health.go`.
