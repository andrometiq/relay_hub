# Telegram and email recovery

Hub journals inbound events and outbound intents in PostgreSQL. Core owns
business behavior. Provider acceptance is not a delivered/read receipt.

## Inbound callbacks

Migration 0030 creates a transactional per-event delivery queue, including
existing events. Legacy cursors cannot prove acknowledgement when transactions
commit out of sequence, so previously stored events are offered once again.
Core must continue deduplicating the immutable Hub event identity. Existing
unresolved quarantines remain quarantined.

Successful callbacks acknowledge exactly the posted event identities. Batches
remain bounded to 50 events and just under 2 MiB. Rejected batches retry as
individual events with exponential backoff. Eight failures quarantine an event;
oversized events quarantine immediately. Later events continue flowing.

Authenticated admins inspect failures through `GET /api/callback-failures`
with `account` and `channel=Telegram` or `channel=Email`. The response includes
failure count, reason, next attempt and quarantine state. Recovery uses the
existing CSRF-protected `POST /api/callback-failures/retry` endpoint:

```json
{"account":"configured-account","channel":"Telegram","id":"123","action":"replay","note":"Core validation corrected and event inspected"}
```

`action` is `quarantine` or `replay`. A 10–2000 character evidence note is
required. The authenticated actor, action and note commit with the state change.
Replay schedules only that event and retains its identity; it does not rewind
other events. The API returns after scheduling, not after Core acknowledges it.

## Outbound scheduling and uncertainty

All sends, including synchronous admission, use the same fair account scheduler.
An earlier queued, dispatching or uncertain delivery blocks later sends to its
destination. Telegram serializes each chat, including its topics. Email orders
overlapping To/CC/BCC envelope recipients after mailbox parsing and case folding.
Legacy email rows conservatively block their account until resolved because SQL
cannot safely reconstruct arbitrary RFC mailbox syntax.

Workers persist a claim generation and mark submission immediately before a
Telegram mutation or SMTP DATA command. Stale workers cannot cross that boundary
after recovery has reclaimed their work. A claim interrupted before the marker
returns to queued; one interrupted after it becomes uncertain. Migration 0031
marks legacy in-flight claims as potentially submitted.

Telegram's explicit 429 response schedules at most eight attempts, with
exponential backoff respecting `retry_after`. Disabled/missing configuration is
rejected before submission. Transport failures, malformed acceptance and
ambiguous server errors remain uncertain. Migration hints are recorded without
changing the verified destination.

SMTP records configuration, compose, connect, TLS, greeting, auth, MAIL, RCPT,
DATA-command, DATA-body and final-DATA-reply stages. Only proven non-acceptance
before the body is transmitted can retry automatically, up to eight attempts.
Permanent rejection remains rejected. Lost/ambiguous DATA outcomes remain
uncertain; explicit DATA rejection is recorded without automatic retry. A failed
QUIT after successful DATA cannot reverse acceptance. SMTP error responses expose
stage/code/classification, never the provider's potentially sensitive text.

A durably queued send returns a successful MCP receipt with `pending: true`,
`state`, `delivery_id`, `request_key` and `next_attempt`. This includes scheduled
provider retries and sends waiting for their turn. If the caller omits its key,
Hub returns the generated key for lookup through `channels.deliveries` or safe
resubmission of the same intent. A duplicate while dispatching returns the same
pending identity without a retry timestamp. Pending does not mean provider
acceptance.

## Manual resolution

`POST /api/delivery-queue` retains its admin, origin and CSRF requirements.
Use `channel="Telegram and email"`, `action="resolve"`, the account and delivery
ID, `outcome="submitted"` or `"rejected"`, and a 10–2000 character evidence note.
Submitted Telegram resolutions additionally require numeric `chat_id` matching
the original destination and a positive `provider_id` string containing the
message ID. Email requires `provider_id` containing the verified RFC Message-ID,
including angle brackets. Reaction evidence must identify its original message.

Resolution updates only an uncertain row, records the evidence and timestamp,
and emits the existing transactional outcome callback with provider identifiers.
It never invokes a provider. Rejected resolution requires no receipt identifier.

## Local validation boundary

Tests use isolated schemas in the approved loopback test database, local HTTP
Bot API/Core fixtures and real TLS SMTP fixtures. They cover reversed commits,
restart, duplicate ACK, poison isolation, replay scope/audit, fair competing
workers, ordered load, crash fencing, safe retries, SMTP stages and resolution
callbacks. They do not establish connected Core/browser acceptance or live
Telegram/email provider behavior. No deployment or production migration is
part of this change.
