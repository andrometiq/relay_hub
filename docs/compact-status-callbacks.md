# Compact delivery-status callbacks

WhatsApp delivery receipts use a separate callback lane from incoming messages. The status lane wakes once per second; realtime messages retain the 250 ms lane. Both use the bounded account scheduler and per-account/lane advisory lock. Receipts that only concern `whatsapp.bulk` messages are journaled in the `bulk` lane instead and reach Core as coalesced bulk updates; see [bulk template sends](api-reference.md#whatsapp-bulk-template-sends).

The Hub posts to `/api/method/relay_core.inbound.receive_statuses` with the existing site API credential and timestamped HMAC. The JSON envelope contains `account` and `statuses`. Each status contains `event_id`, zero-based raw `position`, `message_id`, `recipient`, `status` (`sent`, `delivered`, `read`, `failed`) and integer Unix `timestamp`. Batches contain at most 500 receipts and a 512 KiB status-array budget. The source query is also byte-bounded; large source envelopes may yield smaller batches.

Migration 0026 adds `status_offset` to the callback cursor. Offset zero means `last_id` is fully consumed; a nonzero offset means it is the number of raw status positions consumed inside that immutable event. A failed HTTP request, wrong acknowledgement count, or malformed response leaves both cursor components unchanged. On acknowledged completion the offset resets to zero. A restarted process reads the persisted offset. Unknown provider status kinds consume a raw position but are not emitted as invented receipts; recognized malformed receipts stop that lane for investigation.

Core checks role, signature, account, count, field types and strictly ordered event/position identities. It stores the batch in one durable Relay Hub Event using a canonical digest, then the existing background projection verifies contact/channel binding and updates delivery state without changing message modified timestamps. Replays are idempotent; a regrouped retry is also safe because receipt projection is idempotent. Realtime invalidation is coalesced per batch and affected contact. Acknowledgement means durable Core acceptance, not that the browser has already rendered the update.

Validation: PostgreSQL/race tests cover split 1251-receipt events, failed partial-batch acknowledgement, restart recovery, independent realtime delivery, account isolation and HMAC. The authenticated HTTP Hub simulator passes. Core isolated-site tests cover a 500-receipt signed batch, duplicate acceptance, projection and unchanged modified timestamps. No million-request throughput claim follows from these tests.

The local Hub was migrated and restarted on 24 September 2026; `/readyz` returned HTTP 200. This is not external-provider acceptance. Telegram/email continue through their existing channel-event path; this protocol does not invent delivery/read receipts unsupported by a provider.

## Telegram/email status callbacks — deployed 24 September 2026

Migration 0028 atomically journals terminal channel-outbox transitions. Compact
receipts exclude message bodies, attachments and recipients. Batches contain at
most 500 rows and 512 KiB. Account-scoped per-row acknowledgements preserve late
commits, unlike a high-water cursor. Eight dedicated callback workers and a
persisted retry schedule keep status failures separate from incoming messages.

Core authenticates and durably accepts each signed batch, then its background
worker projects receipts under the original sender's permissions. Submitted
actions cannot regress. A pushed receipt arriving before an HTTP timeout wins
over that timeout, preventing a successfully sent message becoming uncertain.
Projection coalesces after-commit realtime notifications and does not resend.
Telegram acceptance does not imply customer delivery or reading.

The full local regression passed all 41 checks, including database race tests,
500/500/1 batching, failed-ack recovery, late commits, Telegram/email projection,
and actual authenticated HTTP requests against the isolated Frappe site.
Evidence: `.runtime/channel-status-regression.json`.

Installed acceptance: a signed synthetic receipt was posted through the real
`yrp.site` HTTP endpoint with existing service authentication. It returned 200,
and the real background worker marked `RELAY-EVT-0000000006` Projected. No
provider send or customer message was created. This validates installed
HTTP acceptance and worker execution, not a live provider message round trip.
Migration 0028 is installed and the Hub is running the matching dispatcher.
Historical outgoing deliveries are not backfilled by this migration.
