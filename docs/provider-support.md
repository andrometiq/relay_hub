# Provider support matrix

This matrix describes the local `develop` implementation reviewed on **2026-09-24**. A supported adapter path means it exists and has local simulation coverage; it does not mean the configured provider account has accepted a live request. The working tree can contain changes not yet pushed to GitHub.

| Capability | WhatsApp | Telegram | Email |
| --- | --- | --- | --- |
| Send text | Direct and group text | Private, group, supergroup and permitted channel destination | Plain-text SMTP with To/CC/BCC |
| Quoted reply | Original provider message ID | Original message ID, with group/topic routing | In-Reply-To/References headers are supported |
| Send sticker | Direct/group media operation | File ID; static WebP via public HTTPS URL | Not applicable |
| Receive/view sticker | Preserved metadata; authenticated WebP proxy | Preserved metadata; authenticated WebP/WebM/TGS proxy | Not implemented |
| Send location/contact card | Direct messages | Typed location/contact operations | Not applicable |
| Set/clear reaction | Direct messages | Emoji/custom emoji; explicit clear | Not applicable |
| Other outbound media | Image, video, audio, document by provider ID or URL | Photo/video/audio/voice/document by file ID or bounded upload | Multipart text/HTML and bounded MIME attachments |
| Inbound events | Signed messages, statuses, group/template/call event payloads | Verified messages, edits, reactions, member/join/service events | Authenticated normalized ingress and configured IMAP mailbox reader |
| Durable outgoing intents | PostgreSQL queue, stable-key deduplication, bounded bulk admission | PostgreSQL durable channel outbox with stable request keys | Direct SMTP submission; no equivalent durable outbox yet |
| Callbacks to Core | Durable realtime/status lanes with batched statuses | Durable channel journal/cursor callback path | Durable channel journal/cursor callback path |
| Templates | List/create/update/delete/send; provider approval applies | No WhatsApp-style template equivalent | Compose body in Core; no Hub template engine |
| Group administration | Lifecycle, invitations, join requests, removal, settings, photo | Existing-group settings, members, invitations, pins and forum topics | No group administration adapter |
| Calls | Capability-gated permission and signaling operations | Not implemented | Not applicable |
| Server call recording | Not implemented | Not implemented | Not applicable |

Message payloads and scopes are in the [API reference](api-reference.md). The [group operation list](group-simulation-coverage.md#supported-group-operations) names each exposed group command; changing a group photo is different from sending image media.

## WhatsApp group boundaries

Group functionality is enabled per account and remains subject to Meta eligibility and permissions. Supported group sends are text, media, and supported templates. Relay rejects `groups.react`, `groups.location` and `groups.contacts` because those are not established in the documented supported group message list. No fallback converts them into direct messages.

Participants join by invitation, with approval when configured. There is no manual `groups.participants.add` operation. Invitations use get/reset; `groups.invite.create` remains a compatibility alias for reset, and invented invite-revoke behavior is rejected. Group deletion, photo and other lifecycle operations may be asynchronous; request acceptance is not proof of provider completion.

Group ownership is checked with bounded pagination and fails closed when ownership cannot be established. The aggregate message delivery state is the highest observed participant state: `Read` does not establish that every member read the message. Original participant receipt events remain stored.

Reference contracts and simulated cases are recorded in [WhatsApp group coverage](whatsapp-group-simulation.md) and [rich-message coverage](whatsapp-rich-message-simulation.md). Official Meta references were checked during that implementation pass on 2026-09-23: [Groups reference](https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/reference), [group messaging](https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/groups-messaging), [group webhooks](https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/webhooks). Provider account eligibility requires separate verification.

## Telegram boundaries

Bots operate in existing chats with the required membership/administrator permissions. The adapter does not create or delete groups. Forum topics are separate resources and have supported create/edit/close/reopen/delete operations. A participant ID does not identify a group conversation.

The typed message tool covers text, sticker, reaction, location and contact only. It does not expose every [Telegram Bot API](https://core.telegram.org/bots/api) method, arbitrary method names, paid reactions, general media upload, message editing/deletion, polls, inline keyboards or arbitrary callback-query responses. Unsupported inbound content can remain in the raw journal without gaining a matching outbound operation. The Bot API reference was checked on 2026-09-23.

Telegram mutations submit once. Rate-limit and migration metadata are surfaced to callers; automatic retry or destination migration is not implied. Bots must be configured to receive the relevant webhook updates. The Hub does not register bots, infer unrelated chat identities, or bypass provider messaging limits.

## Email boundaries

The SMTP adapter requires TLS submission and supports To/CC/BCC, text/HTML alternatives, reply-thread headers, Auto-Submitted headers and bounded MIME attachments. The inbound MIME parser retains thread metadata and bounded attachments. The Hub includes configured IMAP polling with UID/UIDVALIDITY cursor handling. Core enforces conversation permissions; the normalized ingress is not a universal email-provider verifier. Local transport tests do not establish inbox delivery.

SMTP acceptance cannot guarantee inbox delivery or read status. Reference protocols are [SMTP](https://www.rfc-editor.org/rfc/rfc5321.html) and [TLS for email submission](https://www.rfc-editor.org/rfc/rfc8314.html); the earlier [provider research](provider-adapter-research.md) records additional planned email work.

## Deployment limits

Local simulation success does not verify live credentials, provider policy approval, real recipient rendering, audio interoperability, public webhook subscriptions or production capacity. WhatsApp adaptive workers scale within a configured process ceiling; there is no automatic machine/Kubernetes scaling supplied here. Opt-in hourly WhatsApp profile/template reconciliation is implemented. Core stores private attachments behind conversation access checks. Live provider acceptance and operational restore/load verification remain separate requirements.

The [configuration reference](configuration.md#current-administration-limits) records outstanding credential-field and account-grant UI gaps. See [testing](testing.md) for reproducible checks and [combined results](simulation-validation.md) for the measured checkpoint. Those gaps and the missing capabilities above must not be hidden behind a claim of full provider API coverage.
