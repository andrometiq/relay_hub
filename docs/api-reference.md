# MCP and message API reference

This reference describes the registered Hub tools and typed adapter inputs reviewed on **2026-09-23**. Examples use synthetic identifiers. Sending examples make provider mutations when used with configured accounts; they are not a simulation switch.

## Transport and account binding

Connect a server-side MCP client to `/mcp`, or `/hub/mcp` through the review mount. Relay uses stateless Streamable HTTP with JSON responses and protocol version `2025-06-18`. Send:

```text
Authorization: Bearer <account-scoped Hub API token>
Content-Type: application/json
Accept: application/json, text/event-stream
MCP-Protocol-Version: 2025-06-18
```

Initialize the MCP connection before listing or calling tools. The client is bound to the token's account; message inputs cannot override that account or supply provider credentials. Browser `Origin` requests are rejected. Browser cookies, the legacy `.env` service token and provider tokens do not authenticate MCP. See [authentication](authentication.md) for provisioning.

An initialization request is:

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"relay-example","version":"1.0"}}}
```

Follow with the `notifications/initialized` notification, then `tools/list` for the exact generated JSON schemas. A read-only health call is:

```json
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"health.get","arguments":{}}}
```

Its structured result contains `{"status":"ready","service":"relay_hub"}` when database readiness passes. A tool failure can arrive in HTTP 200 with MCP `isError: true`; inspect the tool result, not only the HTTP status.

## Tool inventory

| Tool | Arguments | Required scope |
| --- | --- | --- |
| `health.get` | `{}` | Valid API token |
| `whatsapp.send` | `message: {key, message: {...}}` | `messages:send` |
| `whatsapp.bulk` | `messages: [{key, message: {operation: "templates.send", ...}}]`, 1–50 template sends | `messages:send` |
| `whatsapp.deliveries` | Optional `after`; optional `cancel_id` for cancellation | `deliveries:read` or `deliveries:cancel` |
| `whatsapp.execute` | `operation` and typed operation fields | Operation scope below |
| `whatsapp.media` | `media_id` of a received sticker | `messages:read` |
| `telegram.message` | `operation`, `chat_id` and typed message fields | `messages:send` |
| `telegram.send` | Compatibility text tool: `chat_id`, `text`, optional `reply_to`, `message_thread_id` | `messages:send` |
| `telegram.groups` | `operation`, `chat_id` and typed group fields | `groups:read` for `info`, `member`, `count`, `administrators`; otherwise `groups:write` |
| `telegram.media` | `file_id` of a received sticker | `messages:read` |
| `email.send` | `to[]`, `subject`, `body`; optional `cc[]`, `bcc[]` | `messages:send` |

WhatsApp operation scopes:

| Operations | Scope |
| --- | --- |
| `capabilities`, `account.profile` | `account:read` |
| `messages.history`, `events.list`, `events.latest` | `messages:read` |
| `messages.send`, `messages.media`, `messages.react`, `messages.location`, `messages.contacts`, `templates.send`, `groups.send`, `groups.media`, `groups.template`, `groups.pin` | `messages:send` |
| `templates.list`; template mutations | `templates:read`; `templates:write` |
| `groups.list`, `groups.info`, `groups.requests.list`, `groups.invite.get`; group administration | `groups:read`; `groups:write` |
| `calls.permission.get`; call mutations | `calls:read`; `calls:write` |

Scopes authorize Hub account operations; they do not grant access to Frappe business records. Core checks those permissions separately. Channel enablement, provider capability and provider permissions must also pass.

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

```json
{"operation":"messages.react","to":"15555550123","reply_to":"wamid.SYNTHETIC_ORIGINAL","emoji":"👍"}
```

Clear a reaction by sending the same target with **`"emoji":""`**. A reaction is an update to a message, not a new conversational text reply.

```json
{"operation":"messages.location","to":"15555550123","location":{"latitude":0,"longitude":0,"name":"Collection point","address":"Example Road"}}
```

```json
{"operation":"messages.contacts","to":"15555550123","contacts":[{"name":{"formatted_name":"Demo Contact","first_name":"Demo","last_name":"Contact"},"phones":[{"phone":"+15555550124","type":"WORK"}],"emails":[{"email":"demo@example.com","type":"WORK"}]}]}
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

## WhatsApp bulk template sends

`whatsapp.bulk` queues 1–50 `templates.send` messages in one call. It is for campaigns: other operations are refused. Each item uses the same stable `key` rules as `whatsapp.send`. A recipient that fails validation, for example a number that is not 7–15 digits, comes back with `state: "rejected"`, an empty `id` and an `error`; the other items are queued. A full account queue (10,000 queued or submitting intents) refuses the whole call, and so do 5,000 queued or submitting bulk intents: half of the account queue stays free for conversational sends.

```json
{"deliveries":[{"id":"812","key":"winter-launch-1","state":"queued","attempts":0,"next_attempt":"0001-01-01T00:00:00Z"},{"id":"","key":"winter-launch-2","state":"rejected","error":"enabled template, recipient, name and language are required"}],"queued":731,"limit":5000,"messages_per_second":80}
```

Use `queued` (all queued or submitting intents of the account) for backpressure: stop pushing while it is above your own limit and pace by `messages_per_second`. `limit` is the bulk share of the queue. Workers send bulk rows one by one at the account rate, after any non-bulk sends of the same account. Retries follow the outbox rules: only errors that Meta marks as transient or rate limits are retried.

Bulk results do not use the realtime or status lanes. The Hub posts the latest state of each changed bulk row to `/api/method/relay_core.inbound.receive_bulk_updates`, signed like the status callbacks. A batch is sent when 500 rows changed or when the oldest change waited `RELAY_BULK_UPDATE_WINDOW` (default 30 s). Rows are in ascending `id` order:

```json
{"account":"essdee","updates":[{"id":812,"key":"winter-launch-1","state":"submitted","message_id":"wamid.SYNTHETIC","status":"read","updated_at":"2026-10-25T18:00:00Z"},{"id":813,"key":"winter-launch-7","state":"rejected","message_id":"","status":"","error":{"code":131009,"title":"Parameter value is not valid","message":"Header image URL could not be downloaded."},"updated_at":"2026-10-25T18:00:01Z"}]}
```

`state` is the outbox state (`queued`, `submitting`, `submitted`, `rejected`, `uncertain`, `cancelled`). `status` is the latest Meta receipt (`sent`, `delivered`, `read`, `failed`) and never moves back. `error` holds the Meta code and its user title and message, for a rejected send or a failed receipt (for example 131026, 131049 or 130472). Core acknowledges with `{"message":{"accepted":N}}`. A row that changes again is sent again in a later batch. Replies to campaign messages stay on the realtime lane.

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
{"operation":"location","chat_id":123456789,"latitude":13.0827,"longitude":80.2707}
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

Use `whatsapp.media` with `{"media_id":"123456789012345"}` or `telegram.media` with `{"file_id":"CAACAgIAAxkBAAIBDemoSyntheticFileID"}`. The account must have a persisted inbound sticker referencing that exact ID, and the API token needs `messages:read`.

The response uses MCP `structuredContent`:

```json
{"content_base64":"<base64 bytes>","content_type":"image/webp","filename":"sticker.webp"}
```

Maximum decoded content is **2 MiB**. WhatsApp currently returns WebP only. Telegram can return `image/webp`, `video/webm` or `application/x-tgsticker`. Treat TGS as an animation file/download, not a directly displayable image. This is not an arbitrary URL downloader or general attachment endpoint.

The Hub fetches from constrained provider origins with deadlines, size/type validation and no redirects; tokens and provider download URLs are not returned. Core additionally checks the requesting user's access to the source message before serving bytes to a browser. `messages:read` is account-level access at the Hub, not a substitute for that Core permission check.

## Email

```json
{"to":["recipient@example.com"],"cc":["team@example.com"],"bcc":["archive@example.com"],"subject":"Request received","body":"Thank you. Our team will reply soon."}
```

`email.send` sends plain text through configured TLS SMTP. CC/BCC are envelope recipients; BCC is not written into the visible message headers. A successful send means SMTP acceptance, not inbox delivery or reading. MIME attachments, HTML composition and IMAP synchronization are outside this adapter's current contract.

## Inbound events and browser routes

Provider webhooks use their own [verification contracts](configuration.md#webhook-registration). The Hub persists raw provider payloads and normalized journal fields before acknowledging receipt. Signed callbacks preserve account/channel, event identity and original message data for Core deduplication.

Telegram retains chat type, participant or anonymous sender, topic, quote target, edited-message kind, membership/service events and reaction changes. Sticker, location and contact fields remain structured. Unsupported message content is retained as such; it is not promoted into a supported outbound type. Edits, reactions and membership events must not become new text prompts merely because they contain text fields.

The administration routes are browser-session APIs, not a second public messaging API:

| Route | Purpose |
| --- | --- |
| `GET /api/accounts`, `POST /api/accounts` | Read authorized accounts; administrator save |
| `POST /api/accounts/reload` | Administrator runtime activation |
| `GET /api/accounts/{account}/telegram/webhook` | Administrator retrieves derived registration secret |
| `GET /api/channels`, `/api/channel-events`, `/api/deliveries` | Runtime/channel state and bounded account-filtered history |
| `GET /api/audit` | Administrator operational audit |
| `GET /api/ready` | Authenticated readiness view |

Source contracts: [MCP registration](../internal/hubmcp/server.go), [WhatsApp inputs](../internal/whatsapp/service.go), [Telegram messages](../internal/telegram/messages.go), [Telegram groups](../internal/telegram/groups.go), [route handlers](../cmd/relay-hub/main.go). Use the [support matrix](provider-support.md) to distinguish implemented paths from unimplemented provider features.

## Account binding

The MCP tool `account.get` takes an empty argument object and requires
`account:read`. It returns `{account, site, enabled}` for the token's granted
account. Disabled/missing accounts are rejected; no credential values are returned.
Core uses this tool to verify connection settings before saving them.

Telegram/email delivery reads require both `deliveries:read` and `messages:read`, since stored provider results can contain message text and replies. Cancellation additionally requires `deliveries:cancel`. Resolution requires `deliveries:read` and `deliveries:cancel`. Message-only tokens no longer authorize delivery administration. `channels.resolve` requires `provider_id` for accepted messages and `chat_id` for accepted Telegram messages. Rejections require neither provider field.
