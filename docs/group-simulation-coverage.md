# WhatsApp and Telegram group coverage

This report covers local simulations using synthetic data, real Hub PostgreSQL
and authentication, real Core Frappe permissions/projection/dispatch, and local
HTTP provider fixtures. It does not assert live provider acceptance or complete
coverage of every Telegram Bot API method. The reproducible combined result is
recorded in `.runtime/simulation-report.json`; run instructions are in
[simulation-validation.md](simulation-validation.md).

Latest combined result on 2026-09-23: **425 Hub test/subtest passes, zero failures
or skips**, **65 Core unit passes with 2 opt-in live-Hub smoke tests skipped**,
and **95 real Core database scenarios including 17 group and 24 typed-message
scenarios**. Race
detection, vet, frontend data tests and whitespace checks passed. Core's frontend
production build passed after the existing WhatsApp group controls were aligned
with invite get/reset and invitation-based membership.

## Supported group operations

WhatsApp uses the account-scoped `whatsapp.execute` MCP tool. Group message
operations also use the durable `whatsapp.send` / `whatsapp.bulk` outbox.

| Area | WhatsApp operations |
| --- | --- |
| Lifecycle | `groups.create`, `groups.list`, `groups.info`, `groups.update`, `groups.delete` |
| Invitations | `groups.invite.get`, `groups.invite.reset`; `groups.invite.create` remains a reset alias |
| Membership | `groups.requests.list`, `groups.requests.approve`, `groups.requests.reject`, `groups.participants.remove` |
| Messaging | `groups.send`, `groups.media`, `groups.template`, `groups.pin` (pin/unpin) |
| Picture | `groups.photo` |

Telegram uses the account-scoped `telegram.groups` MCP tool; `telegram.send`
accepts `chat_id`, `text`, `reply_to`, and `message_thread_id`. Core exposes
`relay.telegram.groups` and permission-checked `relay_core.telegram.groups`;
group administration requires Relay Communication Manager access. The Core
`execute` alias uses the same Hub tool.

| Area | Telegram operations |
| --- | --- |
| Inspection | `info`, `member`, `count`, `administrators` |
| Settings | `title`, `description`, `permissions`, `photo.set`, `photo.delete` |
| Invitations | `invite.create`, `invite.edit`, `invite.revoke`, `requests.approve`, `requests.reject` |
| Members | `members.ban`, `members.unban`, `members.restrict`, `members.promote`, `members.title`, `members.ban_sender`, `members.unban_sender` |
| Forum topics | `topics.create`, `topics.edit`, `topics.close`, `topics.reopen`, `topics.delete`, `topics.unpin_all` |
| General topic | `topics.general.edit`, `topics.general.close`, `topics.general.reopen`, `topics.general.hide`, `topics.general.unhide`, `topics.general.unpin_all` |
| Pins and leaving | `pins.pin`, `pins.unpin`, `pins.unpin_all`, `leave` |

Photo uploads accept bounded JPEG bytes, never server paths or arbitrary URLs.
Decoded data is limited to 5 MiB and 2048 pixels per dimension. WhatsApp requires
a square image of at least 192 pixels. The MCP request ceiling is 7 MiB to allow
base64 encoding; other adapter request and response limits remain in force.

## Simulation coverage and assertions

| Path | What is checked |
| --- | --- |
| WhatsApp lifecycle | Stateful create/list/update/invite/join approval/rejection/removal/delete; exact HTTP method, resource, token and payload; stale/deleted groups fail |
| WhatsApp group sends | Text, media, templates, pin/unpin, quoted replies; account ownership checked through paginated results; recipient remains the group |
| WhatsApp durable queue | Actual PostgreSQL admission, bulk replay, conflicting idempotency keys, worker delivery, account isolation, provider receipt correlation |
| WhatsApp webhooks and history | Signature checks, event replay, lifecycle payload retention, participant receipts, monotonic status, signed Core callbacks; group history excludes participant DMs and vice versa |
| Telegram lifecycle | Stateful group settings → invite → join → ban/unban → topic create/send/close/reopen/delete → leave |
| Telegram operation contracts | All operations above validate required fields, exact provider method and payload, and accepted response shape |
| Telegram inbound | Private/group/supergroup/channel identity, participant and anonymous sender, topic and reply references; edits, reactions, joins, membership changes, migrations and service events retained |
| Authentication | Real account-scoped tokens, independent read/write/send scopes, token revocation, unknown accounts, secret-verified webhook deduplication, account-separated callback payloads |
| Failure behavior | Permission rejection, malformed/false success, rate limits, migration hints, provider/server errors, cancellation and redirects; no blind mutation replay and no token-bearing error URLs |
| Core routing | Group destination separated from participant; verified group binding required; topic and original provider message ID frozen into replies and rechecked at dispatch |
| Core isolation | New messages in another group/topic/private chat do not supersede this conversation; changed bindings/provenance/permissions prevent stale sends; service events do not trigger fresh AI replies |

Key evidence files:

- Hub: `internal/whatsapp/group_simulation_test.go`, `group_media_test.go`,
  `group_photo_test.go`, and `internal/hubmcp/whatsapp_group_simulation_test.go`.
- Hub: `internal/telegram/groups_test.go`, `group_workflow_test.go`,
  `updates_test.go`, `photo_test.go`, and
  `internal/hubmcp/telegram_groups_simulation_test.go`.
- Core: `tests/integration_groups_site.py` and the group/provenance unit tests.

## Corrections made during this pass

- Removed invented WhatsApp participant-add and invite-revoke operations.
  Participants join through invitations/approval; invitation invalidation uses
  the documented reset operation.
- Fixed group ownership pagination when a short page still supplies a cursor.
- Separated WhatsApp group history from a participant's private conversation and
  added an index for bounded conversation queries.
- Preserved Telegram topic, participant and original message IDs through Hub and
  Core; Telegram update IDs are no longer mistaken for message reply IDs.
- Scoped Core's latest-message checks to the conversation and topic.
- Kept Telegram rate-limit, migration and uncertain-outcome metadata in safe MCP
  errors. A migration hint does not authorize changing a contact binding.
- Replaced shared Telegram webhook authentication with per-account HMAC-derived
  secrets. A token for one account cannot submit events to another account.
- Fixed runtime reloads so existing MCP handlers see rotated credentials and
  disabled accounts. Immutable registry snapshots are published atomically;
  a request already in flight can finish using its previous snapshot.

For Telegram webhook registration, set a private random
`RELAY_TELEGRAM_WEBHOOK_SECRET` master of at least 32 characters in `.env`.
An authenticated Hub administrator retrieves the account-specific path and
`secret_token` from `GET /api/accounts/{account}/telegram/webhook`, then uses that
derived token in Telegram `setWebhook`. The master is never sent to Telegram.
Existing bots registered with the old shared value must be registered again with
their derived token; legacy shared values are intentionally rejected. Rotating
the master requires registering all affected bot webhooks again.

## Provider and test boundaries

Meta documents invitation-based membership, group metadata webhooks, supported
settings and group messaging in its [Groups reference](https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/reference),
[messaging reference](https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/groups-messaging)
and [group webhooks](https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/webhooks).
Actual group availability still depends on the configured WhatsApp account.

Telegram operations follow the [official Bot API](https://core.telegram.org/bots/api).
Bots operate in existing groups with the appropriate administrator rights; group
creation/deletion is rejected by this adapter. Topic icons/sticker management,
member tags and the full general media/edit/delete API surface remain outside
this pass. Typed sticker/reply/location/contact/reaction paths were added in the
subsequent message work; see the [current API reference](api-reference.md).

WhatsApp's aggregate delivery state is the best reported participant state;
`Read` does not mean every participant read the message. Original participant
receipt events are preserved. No simulated success proves live eligibility,
production throughput or complete installed-UI behavior. These simulations do
not send messages to real people or deploy either application.
