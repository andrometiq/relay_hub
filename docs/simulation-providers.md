# Local Telegram and email providers

`cmd/relay-sim` runs the actual channel registry, durable outbox, inbound/status
callback workers and mailbox scheduler alongside the existing WhatsApp launcher.
The control API requires `Authorization: Bearer $RELAY_SIM_CONTROL_TOKEN`.
Provisioning `/_sim/provision` enables all three channels on `sim-v1` and returns
its scoped Hub token. Business logic and AI remain in Core.

Provider state lives in `--state-dir` (default `.runtime/relay-sim-state`). Reuse the
same directory with `--resume`. Acceptance, message/file identities, pending
Telegram updates, email UID state and unconsumed faults are written atomically
and synced before replies. Captures are per-process; provider snapshots survive
restart. Keep the directory private and unversioned: it contains local fixture
message content. Use one launcher per directory.

The new controls use the existing fake Meta/control HTTP listener. GET
`/_sim/providers` returns Telegram/email snapshots and redacted capture records.
GET `/_sim/state` also includes channel deliveries, events and callback ACKs.

## Telegram

The transport maps only `https://api.telegram.org` to the local provider. The
synthetic bot token is `123456:simulation_bot_token_local_only`. Captures redact
bot-token URL segments and ingress-secret headers. Upload captures contain
filename/size metadata rather than binary bodies. The provider implements
`getMe`, `sendMessage`, photo/video/animation/audio/voice/document/sticker sends,
location/contact sends, reactions, chat actions, `getFile`, file downloads and
`getUpdates`. It is a bounded Bot API subset, not a complete Telegram server.

- POST `/_sim/telegram/emit` with `{"payload": <update>}` persists an update and
  delivers it to `/webhooks/telegram/sim-v1` using the Hub's derived secret.
  `invalid_signature: true` exercises rejection. Repeating an identical update
  exercises Hub deduplication; a conflicting update ID is rejected.
- POST `/_sim/telegram/queue` with the same shape queues without delivering.
  `getUpdates` supports offset acknowledgement and a limit of up to 100.
- POST `/_sim/telegram/faults` with `{"faults": ["429", "503"]}` replaces the
  pending fault sequence. Other values are `blocked`, `migrate`,
  `timeout_before`, `timeout_after`, `disconnect_before`, `disconnect_after`.
  A 429 includes `retry_after: 1`. The migration hint is `-1009001`.
  “After” faults persist acceptance before losing the response. Timeouts wait
  for client cancellation, capped at 15 seconds. No fault implicitly resends.

Webhook delivery is explicit and replayable; this subset does not implement
Telegram webhook registration or group administration.

## Email

`--smtp-addr` defaults to `127.0.0.1:18095`; `--imap-addr` defaults to
`127.0.0.1:18096`. SMTP requires STARTTLS and AUTH PLAIN. IMAP uses implicit TLS
and implements the read-only commands issued by Hub's mailbox reader. Both use
an ephemeral local CA with certificate and hostname verification enabled.
Nothing changes the system trust store. No dependencies were added.

The narrowly injected email transport maps only `127.0.0.1:587` and
`127.0.0.1:993` to these listeners, rejecting every other address. The fixed
fixture login is `sim-mail` / `sim-mail-local-only`. The provider never relays
mail or dials recipients. SMTP captures preserve the envelope (including BCC)
and accepted MIME, but never AUTH credentials. Sent messages are separate from
the inbox; inject a reply explicitly.

- POST `/_sim/email/emit` with `{"raw": "<RFC 5322 message>",
  "authentication": "pass"}` appends to INBOX. `uid` optionally introduces a
  forward UID gap. `authentication` accepts `pass`, `fail`, or `none`; omit it
  to preserve raw malformed/untrusted fixtures.
- The optional `authserv_id` defaults to `mx.sim.invalid`, which is the trusted
  authserv-id configured on the simulated Hub client. A different value tests
  untrusted evidence. Generated `Authentication-Results` sits above a new
  `Received` boundary, so supplied lower headers cannot assert local trust.
- Add `"webhook": true, "id": "unique-local-event"` to deliver through the
  authenticated Hub email webhook instead of IMAP. `invalid_signature: true`
  tests the account-scoped ingress boundary.
- POST `/_sim/email/bounce` with `{"message_id": "<original@domain>",
  "recipient": "to@example.invalid"}` generates an authenticated structured
  DSN (`failed`, `5.1.1`). The same webhook/id options apply. Hub processes this
  through its normal recipient-specific delivery evidence path.
- POST `/_sim/email/faults` with `{"stage": "rcpt", "faults": ["451"]}`.
  Stages: `connect`, `auth`, `rcpt`, `data_command`, `data_reply`.
  Faults: `451`, `550`, `disconnect`, `timeout`. `connect` additionally supports
  `refuse` at the injected dial boundary. `data_reply` additionally supports
  `disconnect_after`, which stores the accepted message before dropping the
  final reply. Other DATA faults do not store acceptance. Transport loss after
  DATA remains ambiguous to Hub regardless of the fake's internal evidence.

## Validation boundary

The launcher test creates an isolated schema in the existing local
`relay_hub_v1_test` database using `RELAY_TEST_DATABASE_URL`. Its test-only entry
point refuses other database names, addresses and schema prefixes. It exercises
real MCP authentication, account provisioning, Telegram safe retry and replay,
SMTP submission, TLS IMAP ingestion, authenticated webhooks, callback ACKs,
restart deduplication and DSN outcome callbacks. It does not use `channel_hub`
or `relay_hub_sim`.

Provider tests additionally cover concurrent Telegram sends, before/after
acceptance faults, media round trips, pending-fault persistence, TLS trust and
hostname rejection, SMTP stage classifications, MIME/BCC fidelity, mailbox
paging/UID gaps, malformed/oversized messages and denied external dialing.

These are local provider/Hub integration tests. The connected Telegram/email
Core and Socket.IO drivers are documented in [Connected channel simulations](connected-channel-simulations.md),
including their current Core defects. Browser rendering and the 12,000-event
latency acceptance remain separate release proofs.
