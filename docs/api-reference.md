# MCP and message API reference

This reference describes the registered Hub tools and typed adapter inputs reviewed on **2026-09-23**. Examples use synthetic identifiers. Sending examples make provider mutations when used with configured channels; they are not a simulation switch.

## Transport and site binding

Connect a server-side MCP client to `/mcp`, or `/hub/mcp` through the review mount. Relay uses stateless Streamable HTTP with JSON responses and protocol version `2025-06-18`. Send:

```text
Authorization: Bearer <site-scoped Hub API token>
Content-Type: application/json
Accept: application/json, text/event-stream
MCP-Protocol-Version: 2025-06-18
```

Initialize the MCP connection before listing or calling tools. The client is bound to the token's site. It can use only the channels of that site; message inputs cannot supply provider credentials. Browser `Origin` requests are rejected. Browser cookies, the legacy `.env` service token and provider tokens do not authenticate MCP. See [authentication](authentication.md) for provisioning.

An initialization request is:

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"relay-example","version":"1.0"}}}
```

Follow with the `notifications/initialized` notification, then `tools/list` for the exact generated JSON schemas. A read-only health call is:

```json
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"health.get","arguments":{}}}
```

Its structured result contains `{"status":"ready","service":"relay_hub"}` when database readiness passes. A tool failure can arrive in HTTP 200 with MCP `isError: true`; inspect the tool result, not only the HTTP status.

## Channels of a site

A site can use several WhatsApp numbers, Telegram bots and mailboxes. Every send, read and delivery tool takes an optional `channel` argument: the channel key from `channels.list`. Without it, the tool uses the only channel of that type that the site has. When the site has more than one, the tool fails with `channel required: ...`. A key of another site's channel fails with `channel is not available to this site`. To answer an inbound message, pass the `channel_key` of its callback.

`channels.list` returns `{"channels":[...]}`. Each row has `key`, `provider` (`WhatsApp`, `Telegram` or `Email`), `label`, `address`, `display_number`, `waba_id`, `container`, `enabled` and `capabilities` (`templates`, `groups`, `calls`). No credentials are returned:

```json
{"channels":[{"key":"sales","provider":"WhatsApp","label":"Sales desk","address":"100000000000001","display_number":"+91 90000 00101","waba_id":"200000000000001","container":"ct_4f1c2a9b0d3e5f67","enabled":true,"capabilities":{"templates":true,"groups":false,"calls":false}}]}
```

`site.get` returns `{account, site, enabled, channels}`: `account` is the site ID, `site` is its origin and `channels` is the same list. `account.get` is the same tool for older clients. A disabled or missing site is refused.

## Tool inventory

| Tool | Arguments |
| --- | --- |
| `health.get` | `{}` |
| `site.get` (alias `account.get`) | `{}` |
| `channels.list` | `{}` |
| `whatsapp.send` | `message: {key, message: {...}}` |
| `whatsapp.bulk` | `messages: [{key, message: {operation: "templates.send", ...}}]`, 1–50 template, text or media sends |
| `whatsapp.deliveries` | Optional `after`; optional `cancel_id` for cancellation |
| `whatsapp.execute` | `operation` and typed operation fields |
| `whatsapp.media` | `media_id` of a received sticker |
| `telegram.message` | `operation`, `chat_id` and typed message fields |
| `telegram.send` | Compatibility text tool: `chat_id`, `text`, optional `reply_to`, `message_thread_id` |
| `telegram.groups` | `operation`, `chat_id` and typed group fields |
| `telegram.media` | `file_id` of a received sticker |
| `email.send` | `to[]`, `subject`, `body`; optional `cc[]`, `bcc[]` |
| `whatsapp.number.health` | Optional `channel` |
| `whatsapp.number.check` | Optional `channel`, `repair` |
| `whatsapp.number.settings` | Optional `channel`; at least one of `enabled`, `templates`, `sync_metadata` |
| `whatsapp.number.pin_reset` | Optional `channel` |
| `whatsapp.number.remove` | Optional `channel`; `confirm: true` |
| `site.disconnect` | `confirm: true` |

`whatsapp.execute` operations:

| Group | Operations |
| --- | --- |
| Account | `capabilities`, `account.profile` (number quality, limit, status, throughput), `account.business` (WABA review, verification, limit), `account.metadata` (hourly cached copy of both and the templates), `billing.pricing` |
| Read | `messages.history`, `events.list`, `events.latest` |
| Send | `messages.send`, `messages.media`, `messages.media_upload`, `messages.read`, `messages.react`, `messages.location`, `messages.contacts`, `messages.interactive`, `templates.send` |
| Profile | `profile.get`, `profile.update` (`profile` with `about`, `address`, `description`, `email`, `websites`, `vertical`; optional photo as `content_base64` and `mime_type`) |
| Templates and flows | `templates.list` and template mutations; `flows.*`. Mutations are refused when another site has a number in the same WABA. |

`capabilities` returns the number's flags (`templates`, `flows`, `groups`, `calls`, `recording`) and `operations`, the direct message sends the number supports. Core sends flow choices as reply buttons or a list only when `operations` contains `messages.interactive`; otherwise it sends numbered text.

## Site setup tools

These tools let a Core site set up its own WhatsApp numbers. The Hub keeps every token, secret and PIN; no answer contains one. A site can reach only its own numbers.

| Tool | What it does | Result |
| --- | --- | --- |
| `whatsapp.number.health` | Reads the saved state of the number. | `channel`, `label`, `display_number`, `enabled`, `admin_off`, `connection_status` (`connected`, `unchecked`, `failed`, `reconnect_required`, `disabled`), `partner_status`, `verified_at`, `checked_at`, `check_error`, `webhook_subscribed`, `pin_kept`, `settings` (`templates`, `sync_metadata`) and `delivery` (last webhook, last Core confirmation, waiting callbacks). |
| `whatsapp.number.check` | Asks Meta about the number and its webhook subscription. With `repair`, subscribes the app again. | The health fields, `reconnect_required` and a short `profile`. |
| `whatsapp.number.settings` | Turns the number, templates or the hourly Meta sync on or off. Turning a number on checks it at once. A number that a Hub admin turned off stays off and returns `admin_off`. | The health fields. |
| `whatsapp.number.pin_reset` | Sets a new two-step PIN that only the Hub knows. Not for WhatsApp Business app numbers. | The health fields. |
| `whatsapp.number.remove` | Takes the number off the site, wipes its token, deregisters it and unsubscribes the app when no other number uses the WABA. | `removed`, `deregistered`, `unsubscribed`. |
| `site.disconnect` | Turns the site off, revokes its Core service token and writes an audit row. Tokens that an operator issued for the site stay live and work again when the site is turned on. A new setup code or bench enrolment turns it on again; a site that the admin turned off stays off. | `disconnected`. |

A refusal is a tool error with structured content `{"error":{"code":"...","message":"..."}}`. Codes: `not_found`, `no_number`, `channel_required`, `invalid_request`, `confirm_required`, `number_disabled`, `reconnect_required`, `pending_sends`, `not_supported`, `meta_rejected`, `meta_unavailable`, `outcome_unknown`, `admin_off` and `hub_unavailable`. `admin_off` means a Hub admin turned the channel off: only an admin can turn it on again, and a site that turns its own channel off can turn it on again. A channel that was off before the Hub stored `admin_off` is not locked. A missing, revoked or disabled site token is refused before any tool runs.

These tools let a Core site connect and manage its own Telegram bots and mailboxes. A token or password goes in only through `connect` or `settings`. The Hub keeps it encrypted and no answer contains it. The Hub stores bots and mailboxes in the signup container ([user guide 5.13](user-guide.md#513-let-sites-add-their-own-bots-and-mailboxes)); without one, `connect` returns `not_available`.

| Tool | What it does | Result |
| --- | --- | --- |
| `telegram.bot.connect` | Takes `bot_token` and an optional `label`. Checks the token with getMe, stores and links the bot, registers its webhook and tells Core. A refused token stores nothing. | The bot health fields. |
| `telegram.bot.health` | Reads the saved state of the bot. Telegram is not called. | `channel`, `label`, `username`, `bot_id`, `enabled`, `admin_off`, `webhook_status` (`registered`, `not_registered`, `failed`, `removed`), `checked_at`, `check_error`, `token_saved`. |
| `telegram.bot.check` | Runs getMe and reads the webhook. With `repair`, registers the webhook again. | The health fields, `webhook_missing` and `pending_updates`. |
| `telegram.bot.settings` | Changes `label` or `enabled`, or saves a new `bot_token` of the same bot. | The health fields. |
| `telegram.bot.remove` | Takes `confirm`. Wipes the token, unlinks the bot, turns it off and deletes its webhook. | `removed`, `webhook_deleted`. |
| `email.mailbox.connect` | Takes `from`, `host`, `port`, `username`, `password`, the optional `imap_*` fields, `trusted_authserv_id`, `trusted_dsn_domain` and `label`. Signs in to SMTP and IMAP first and stores nothing when that fails. The IMAP reader starts at new mail. | The mailbox health fields. |
| `email.mailbox.health` | Reads the saved state and the sync state. No mail server is called. | `channel`, `label`, `address`, `enabled`, `admin_off`, `connection_status`, `smtp_status`, `imap_status`, `checked_at`, `check_error`, `password_saved`, `imap_password_saved`, `server` (hosts, ports, usernames, folder, trust ids), `sync` and `rejected`. |
| `email.mailbox.check` | Signs in to SMTP and IMAP with the saved settings. No mail is sent or read. | The health fields. |
| `email.mailbox.settings` | Changes `label` or `enabled`, or the server fields. New sign-in settings are tested before they are stored. A blank password keeps the saved one. A new host, user or folder starts the reader again at new mail. The `from` address does not change; remove the mailbox and connect it again. | The health fields. |
| `email.mailbox.remove` | Takes `confirm`. Wipes both passwords, unlinks the mailbox, turns it off and stops the reader. | `removed`. |

Each tool acts only on a bot or mailbox of the caller's site; a key of another site returns `not_found`. A removed bot or mailbox keeps its row and history. When the site that removed it connects it again, it gets the same key. When another site connects it, it gets a new key, so no history or held update moves to that site. A bot id or From address that is live on a site returns `in_use` and does not name that site. A site may try `connect`, or a new token or sign-in, 10 times an hour, and `check` 30 times an hour; the Hub writes each try and outcome to `relay_operational_audit`.

The Hub reaches a site's mail servers only on public internet addresses. It resolves the host once, refuses loopback, private, link-local, shared (100.64.0.0/10), metadata and translated addresses, and connects to the address it checked. SMTP ports are 25, 465, 587 and 2525. IMAP ports are 143 and 993. A mailbox the Hub administrator adds is not limited.

Telegram sends updates only to HTTPS. Without an HTTPS `RELAY_PUBLIC_ORIGIN`, a bot connects and can send, but it gets no messages: `webhook_status` stays `not_registered` and `webhook_missing` says why. The mailbox webhook stays a Hub administrator setting, because its secret is a credential.

More codes for these tools: `no_bot`, `no_mailbox`, `invalid_token`, `bot_changed`, `telegram_unavailable`, `invalid_settings`, `login_failed`, `host_refused`, `host_unknown`, `port_refused`, `in_use`, `not_available`, `rate_limited`, `channel_disabled` and `admin_off`. A bot or mailbox that a Hub admin turned off cannot be turned on, or connected again by any site.

`channels.health` reads the saved health of every number, bot and mailbox of the site in one call, so a page does not call a health tool for each channel. It takes an optional `provider` (`WhatsApp`, `Telegram` or `Email`) and returns one list per provider, for example `{"WhatsApp":[...],"Telegram":[...],"Email":[...]}`. Each item has the fields of `whatsapp.number.health`, `telegram.bot.health` or `email.mailbox.health`. No provider is called and no answer contains a credential. An unknown provider returns `invalid_request`. `admin_off` is `true` when a Hub admin turned the channel off.

A valid API token can call every tool and operation for its own site. It can never use a channel of another site. The Hub does not check business-record permissions: Core checks them with Frappe DocType permissions before it calls the Hub. Channel enablement, provider capability and provider permissions must also pass.

## WhatsApp messages

Use the durable outbox for delivery intents. `key` is a caller-generated stable request key: an identical submission returns its existing intent; the same key with different content is rejected. Acceptance means queued, not delivered or read. `whatsapp.execute` requires `request_key` for sends and routes them through the durable outbox. Read and management operations can execute directly.

The following is the `arguments` object for `whatsapp.send`:

```json
{
  "message": {
    "key": "demo-text-0001",
    "message": {
      "operation": "messages.send",
      "to": "15555550123",
      "body": "Your request is ready.",
      "reply_to": "wamid.SYNTHETIC_ORIGINAL"
    }
  }
}
```

`to` is the direct recipient's international digits. `reply_to` is the original provider message ID, not a Relay row name or webhook event ID. Omit it for an unquoted send. Core must resolve recipients and quote targets from permitted conversation records.

Other direct-message payloads, placed in the inner `message` object or passed to `whatsapp.execute`:

```json
{
  "operation": "messages.media",
  "to": "15555550123",
  "media_type": "sticker",
  "media_id": "123456789012345",
  "reply_to": "wamid.SYNTHETIC_ORIGINAL"
}
```

Use exactly one of `media_id` or `media_url`. Media types are `image`, `video`, `audio`, `document` and `sticker`; a sticker cannot carry a caption or filename. An outbound `media_url` is sent to the provider, not fetched by the Hub's inbound sticker proxy.

`messages.media_upload` uploads one private attachment to the number and returns its media ID. It sends no message. Call it through `whatsapp.execute` without `request_key`. It uses the same validation and 10 MiB limit as inline `content_base64` on `messages.media`:

```json
{"operation":"messages.media_upload","media_type":"document","mime_type":"application/pdf","filename":"invoice.pdf","content_base64":"JVBERi0xLjQK..."}
```

The result is `{"id":"123456789012345"}`. Use the ID as `media_id` in `messages.media`, for example in bulk sends.

```json
{"operation":"messages.react","to":"15555550123","reply_to":"wamid.SYNTHETIC_ORIGINAL","emoji":"👍"}
```

Clear a reaction by sending the same target with **`"emoji":""`**. A reaction is an update to a message, not a new conversational text reply.

`messages.read` marks one received message as read, so the customer sees blue ticks. Set `typing` to `true` to also show a typing indicator to the customer. `message_id` is the inbound provider message ID (`wamid`). Call it through `whatsapp.execute` without `request_key`. The Hub sends it directly: it is not queued, retried or stored, and it does not use send throughput. The result is the provider result, for example `{"success":true}`:

```json
{"operation":"messages.read","message_id":"wamid.SYNTHETIC_INBOUND","typing":true}
```

```json
{"operation":"messages.location","to":"15555550123","location":{"latitude":0,"longitude":0,"name":"Collection point","address":"Example Road"}}
```

```json
{"operation":"messages.contacts","to":"15555550123","contacts":[{"name":{"formatted_name":"Demo Contact","first_name":"Demo","last_name":"Contact"},"phones":[{"phone":"+15555550124","type":"WORK"}],"emails":[{"email":"demo@example.com","type":"WORK"}]}]}
```

```json
{"operation":"messages.interactive","to":"919999999999","interactive":{"type":"button","body":{"text":"Which colour?"},"action":{"buttons":[{"type":"reply","reply":{"id":"poll-1","title":"Red"}},{"type":"reply","reply":{"id":"poll-2","title":"Blue"}}]}}}
```

Both coordinates are required, including explicit zero values. Contact fields are typed and validated; there are bounds on card count, repeated fields and the complete provider payload. See [rich-message validation](whatsapp-rich-message-simulation.md) for the exact limits.

Approved template submission uses `operation: "templates.send"`, `to`, `name`, `language` and optional `components`. Provider approval and conversation-window policies remain authoritative.

### WhatsApp groups

Group message operations use **`to` for the group ID**, not a participant's phone number. Group lifecycle/settings operations instead use `id`:

```json
{"operation":"groups.send","to":"123456789012345","body":"Group update","reply_to":"wamid.SYNTHETIC_GROUP_MESSAGE"}
```

The durable outbox also accepts `groups.media`, `groups.template` and `groups.pin`. Ownership and capability checks apply before provider mutation. Group reactions, locations and contact cards are rejected locally; they never fall back to a participant DM. Group invitations use `groups.invite.get` and `groups.invite.reset`, not manual participant addition. See the [group operation matrix](group-simulation-coverage.md#supported-group-operations).

Group photo administration uses `groups.photo` with bounded JPEG `photo_base64`. Changing the group avatar is separate from sending a sticker or image message.

## WhatsApp pricing

`billing.pricing` reads the Meta pricing analytics of the number's WABA. It needs `account:read`. `start` and `end` are UNIX seconds, and `end` must be after `start`. `granularity` is `DAILY` or `MONTHLY`. `dimensions` is optional and accepts distinct `PRICING_CATEGORY`, `PRICING_TYPE`, `COUNTRY` and `PHONE` values:

```json
{"operation":"billing.pricing","start":1767225600,"end":1769904000,"granularity":"MONTHLY","dimensions":["PRICING_CATEGORY","PRICING_TYPE"]}
```

When another site shares the number's WABA, add `PHONE` to `dimensions`, or the call fails. The Hub then returns only the rows of the site's own numbers and adds their `channel_key`. Rows that Meta does not split by number go to the Hub administrator only.

The result is `{"data_points":[...],"currency":"INR"}`. `currency` is the WABA currency, because the data points do not include one. It is empty when Meta does not return it. Each data point is returned as Meta sends it, for example with `start`, `end`, `volume`, `cost` and the requested dimension values. The list is empty when Meta has no data for the period.

## WhatsApp bulk sends

`whatsapp.bulk` queues 1–50 `templates.send`, `messages.send` or `messages.media` messages in one call. It is for campaigns: other operations are refused. Bulk `messages.media` must use `media_id` or `media_url`; inline `content_base64` is refused, so upload the attachment once with `messages.media_upload`. Each item uses the same stable `key` rules as `whatsapp.send`. A recipient that fails validation, for example a number that is not 7–15 digits, comes back with `state: "rejected"`, an empty `id` and an `error`; the other items are queued. A full number queue (10,000 queued or submitting intents) refuses the whole call, and so do 5,000 queued or submitting bulk intents: half of the number's queue stays free for conversational sends.

```json
{"deliveries":[{"id":"812","key":"winter-launch-1","state":"queued","attempts":0,"next_attempt":"0001-01-01T00:00:00Z"},{"id":"","key":"winter-launch-2","state":"rejected","error":"enabled template, recipient, name and language are required"}],"queued":731,"limit":5000,"messages_per_second":80}
```

Use `queued` (all queued or submitting intents of the number) for backpressure: stop pushing while it is above your own limit and pace by `messages_per_second`. `limit` is the bulk share of the queue. Workers send bulk rows one by one at the number's rate, after any non-bulk sends of the same number. Retries follow the outbox rules: only errors that Meta marks as transient or rate limits are retried.

Bulk results do not use the realtime or status lanes. The Hub posts the latest state of each changed bulk row to `/api/method/relay_core.inbound.receive_bulk_updates`, signed like the status callbacks. A batch is sent when 500 rows changed or when the oldest change waited `RELAY_BULK_UPDATE_WINDOW` (default 3 s). Rows are in ascending `id` order:

```json
{"account":"acme","channel_key":"sales","updates":[{"id":812,"key":"winter-launch-1","state":"submitted","message_id":"wamid.SYNTHETIC","status":"read","updated_at":"2026-10-25T18:00:00Z","channel_key":"sales","to":"+91 90000 00101"},{"id":813,"key":"winter-launch-7","state":"rejected","message_id":"","status":"","error":{"code":131009,"title":"Parameter value is not valid","message":"Header image URL could not be downloaded."},"updated_at":"2026-10-25T18:00:01Z","channel_key":"sales","to":"+91 90000 00101"}]}
```

`state` is the outbox state (`queued`, `submitting`, `submitted`, `rejected`, `uncertain`, `cancelled`). `status` is the latest Meta receipt (`sent`, `delivered`, `read`, `failed`) and never moves back. `error` holds the Meta code and its user title and message, for a rejected send or a failed receipt (for example 131026, 131049 or 130472). Core acknowledges with `{"message":{"accepted":N}}`. A row that changes again is sent again in a later batch. Replies to campaign messages stay on the realtime lane.

## Callbacks to Core

The Hub posts signed batches to the site's Core endpoints: `relay_core.inbound.receive` (events), `relay_core.inbound.receive_statuses` (WhatsApp statuses), `relay_core.inbound.receive_channel_statuses` (Telegram and email statuses) and `relay_core.inbound.receive_bulk_updates`. One batch holds the rows of one channel. The top-level `account` is the site ID and `channel_key` is the channel key. Each row also has `channel_key` and `to`, the number, bot username or mailbox address that the row belongs to:

```json
{"account":"acme","channel_key":"sales","events":[{"id":812,"channel_key":"sales","to":"+91 90000 00101","payload":{}}]}
```

Older Core versions can ignore the new fields. A site also gets a `{"object":"relay_channels","changed":true}` event when its channel list changes; read `channels.list` again then.

## Provider errors

When Meta refuses a request, `whatsapp.execute` returns an MCP tool error whose structured content is `{"provider":"WhatsApp","error":{"http_status":400,"code":132001,"subcode":2494073,"title":"<error_user_title>","message":"<error_user_msg>"}}`. The text has the same code, title and message on one line. Other provider body fields are not passed on. Rejected outbox deliveries keep the same fields in `provider_error`.

## Telegram messages

`telegram.message` accepts one of five operations. It submits once and returns a confirmed provider receipt with `operation`, `chat_id`, `message_id` and `result`; it does not use the WhatsApp durable outbox.

| Operation | Required fields besides `operation` and `chat_id` |
| --- | --- |
| `text` | `text`, 1–4096 characters |
| `sticker` | `sticker`: Telegram file ID or public HTTPS `.webp` URL; animated/video stickers require a file ID |
| `reaction` | Positive `message_id` and explicit zero-or-one-item `reaction` array; optional `is_big` |
| `location` | Numeric `latitude` and `longitude`, including explicit zeros |
| `contact` | `phone_number`, `first_name`; optional `last_name`, `vcard` |

Routing IDs must be valid integers with absolute value below `2^52`. Groups and supergroups use their actual negative `chat_id`. Non-reaction sends can include positive `message_thread_id` and `reply_to`. Preserve both for a reply inside a forum topic:

```json
{"operation":"text","chat_id":-1001234567890,"message_thread_id":42,"reply_to":901,"text":"Received, thank you."}
```

The adapter creates Telegram `reply_parameters.message_id` from `reply_to`. It does not infer a group from a participant ID or use an update ID as the quote target.

```json
{"operation":"sticker","chat_id":-1001234567890,"message_thread_id":42,"reply_to":901,"sticker":"CAACAgIAAxkBAAIBDemoSyntheticFileID"}
```

```json
{"operation":"location","chat_id":123456789,"latitude":12.0,"longitude":77.0}
```

```json
{"operation":"contact","chat_id":123456789,"phone_number":"+15555550124","first_name":"Demo","last_name":"Contact"}
```

Contact names reject control characters. Optional `vcard` is limited to 2048 bytes and one complete version 3.0 or 4.0 card. Unknown operations and fields that conflict with the selected operation are rejected.

Set a reaction:

```json
{"operation":"reaction","chat_id":-1001234567890,"message_id":901,"reaction":[{"type":"emoji","emoji":"👍"}]}
```

Clear it with **an explicit empty array**, not an omitted field:

```json
{"operation":"reaction","chat_id":-1001234567890,"message_id":901,"reaction":[]}
```

Custom reactions use `{"type":"custom_emoji","custom_emoji_id":"123456789"}` and remain subject to provider availability. Paid reactions are unsupported. A reaction cannot include `reply_to` or `message_thread_id`; its target is `chat_id` plus `message_id`.

### Telegram groups and errors

`telegram.groups` covers existing-group settings, members, invitations, pins and forum topics. For example:

```json
{"operation":"topics.create","chat_id":-1001234567890,"name":"Support"}
```

The full [operation list](group-simulation-coverage.md#supported-group-operations) and generated MCP schema define the fields. Bot administrator permissions apply. Group creation/deletion is unsupported; topic creation/deletion is supported.

Provider failures preserve safe structured metadata: `http_status`, `code`, `retry_after`, `migrate_to_chat_id`, `outcome_unknown`. Mutations are not blindly replayed. A migration hint does not authorize sending to a new chat: validate the new Core binding first. Timeout/connection failure can leave the outcome unknown even without a successful receipt.

## Read an incoming sticker

Use `whatsapp.media` with `{"media_id":"123456789012345"}` or `telegram.media` with `{"file_id":"CAACAgIAAxkBAAIBDemoSyntheticFileID"}`. One of the site's channels must have a stored inbound sticker with that exact ID, and the API token needs `messages:read`.

The response uses MCP `structuredContent`:

```json
{"content_base64":"<base64 bytes>","content_type":"image/webp","filename":"sticker.webp"}
```

Maximum decoded content is **2 MiB**. WhatsApp currently returns WebP only. Telegram can return `image/webp`, `video/webm` or `application/x-tgsticker`. Treat TGS as an animation file/download, not a directly displayable image. This is not an arbitrary URL downloader or general attachment endpoint.

The Hub fetches from constrained provider origins with deadlines, size/type validation and no redirects; tokens and provider download URLs are not returned. Core additionally checks the requesting user's access to the source message before serving bytes to a browser. `messages:read` is site-level access at the Hub, not a substitute for that Core permission check.

## Email

```json
{"to":["recipient@example.com"],"cc":["team@example.com"],"bcc":["archive@example.com"],"subject":"Request received","body":"Thank you. Our team will reply soon."}
```

`email.send` sends plain text through configured TLS SMTP. CC/BCC are envelope recipients; BCC is not written into the visible message headers. A successful send means SMTP acceptance, not inbox delivery or reading. MIME attachments, HTML composition and IMAP synchronization are outside this adapter's current contract.

## Inbound events and browser routes

Provider webhooks use their own [verification contracts](configuration.md#webhook-registration). The Hub persists raw provider payloads and normalized journal fields before acknowledging receipt. Signed callbacks keep the site, the channel, the event identity and original message data for Core deduplication.

Telegram retains chat type, participant or anonymous sender, topic, quote target, edited-message kind, membership/service events and reaction changes. Sticker, location and contact fields remain structured. Unsupported message content is retained as such; it is not promoted into a supported outbound type. Edits, reactions and membership events must not become new text prompts merely because they contain text fields.

## Administration routes

The administration routes are browser-session APIs, not a second public messaging API. Writes need an administrator session, the exact Origin and `X-Relay-CSRF`. Each write reloads the runtime.

| Route | Purpose |
| --- | --- |
| `GET`, `POST /api/containers`; `GET`, `PUT /api/containers/{container}` | List, create, read and edit containers and their WhatsApp app. Create takes only a name and returns `{id, webhook_path, csrf}` with status 201. `signup: true` makes the container host self-service numbers; an update without `signup` keeps the saved value. A second signup container gives 409. |
| `GET /api/containers/{container}/whatsapp/webhook` | Callback path and verify token to paste in Meta |
| `POST /api/containers/{container}/channels`; `PUT /api/containers/{container}/channels/{channel}/{key}` | Add and edit a number, bot or mailbox |
| `GET`, `POST /api/channels/{channel}/{key}/connection` | Read or run a connection check (`test`, or `repair` for a Telegram webhook) |
| `GET`, `POST /api/sites`; `GET`, `PUT /api/sites/{site}` | List, create, read and edit sites and their Core credentials |
| `PUT /api/sites/{site}/channels` | Set the full channel list of a site; 409 when a channel is on another site or has unfinished sends |
| `GET`, `POST /api/enrolment-tokens`; `POST /api/enrolment-tokens/{id}/revoke` | List enrolment tokens; create a token `{name, kind, origin_pattern, max_sites, expiry_days}` (201, returns `secret` one time, and `message` for a `setup_code`); revoke a token (204). `kind` is `bench` or `setup_code` |
| `GET /api/sites/{site}/signups` | `{enrolment: {token, enrolled_at}, signups}`: how the site enrolled and its last 20 WhatsApp signup sessions, without secrets |
| `POST /api/enrol` | Site enrolment by Relay Core with `Authorization: Bearer <enrolment token or setup code>`. Not a browser-session route |
| `GET /enrol` | The address of a setup link. It shows only a note that the link is pasted in Relay **Get started**, not opened |
| `GET /api/unassigned-events`; `POST /api/unassigned-events/replay` | List events of channels without a site; replay `{channel, ids}` to the channel's site now |
| `GET /api/channels/{channel}/{key}/held`; `POST /api/channels/{channel}/{key}/held/replay` | Count the held events of one account (`{count}`); give all of them to its linked site (`{replayed}`). The replay returns 409 when the account has no site. Linking an account to a site replays its held events by itself |
| `GET`, `POST /api/forwards/{provider}/{key}`; `DELETE /api/forwards/{provider}/{key}/{id}` | List the forward targets, the newest 50 results (`?before={id}` for older) and counts; add a target `{url}` (201); remove a target and its results (204). `provider` is `whatsapp` with the container ID, or `telegram`/`email` with the channel key. See [webhook forwarding](configuration.md#webhook-forwarding). |
| `GET /api/accounts`, `POST /api/accounts`, `POST /api/accounts/reload` | Older account view and save |
| `GET /api/accounts/{key}/telegram/webhook`, `GET /api/accounts/{key}/email/webhook` | Derived ingress secret and path of a bot or mailbox |
| `GET /api/channels`, `/api/channel-events`, `/api/deliveries` | Runtime and channel state, and bounded history |
| `GET /api/audit` | Administrator operational audit |
| `GET /api/ready` | Authenticated readiness view |

Source contracts: [MCP registration](../internal/hubmcp/server.go), [WhatsApp inputs](../internal/whatsapp/service.go), [Telegram messages](../internal/telegram/messages.go), [Telegram groups](../internal/telegram/groups.go), [route handlers](../cmd/relay-hub/main.go). Use the [support matrix](provider-support.md) to distinguish implemented paths from unimplemented provider features.

## Delivery resolution

`channels.resolve` requires `provider_id` for accepted messages and `chat_id` for accepted Telegram messages. Rejections require neither provider field.
