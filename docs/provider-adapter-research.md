# Provider adapters — official documentation review, 2026-09-21

> Historical checkpoint. Claims and test results below describe that stage of development. Use the [current support matrix](provider-support.md), [API reference](api-reference.md) and [testing guide](testing.md) for current behavior.

This is implementation guidance, not a claim that adapters exist or were tested
against provider accounts. No provider credentials were used or messages sent.

## WhatsApp

Meta's [official Cloud API collection](https://www.postman.com/meta/whatsapp-business-platform/documentation/wlk6lh4/whatsapp-cloud-api)
identifies WABA and phone-number resources, token authentication, phone metadata,
and POST /{phone-number-id}/messages. Persist the returned provider message ID
separately from Relay's sequential primary key. The official
[Templates collection](https://www.postman.com/meta/whatsapp-business-platform/folder/lczy75a/templates)
covers listing, creation, editing and deletion. Model templates as synchronized
provider resources, not just local text snippets. Pin the Graph version in config.

Meta's [webhook SDK reference](https://whatsapp.github.io/WhatsApp-Nodejs-SDK/api-reference/webhooks/start/)
documents verification and x-hub-signature-256 checks. Implement validation on
raw request bytes before parsing, then persist received events before returning
success. Verification tokens and app secrets have different jobs.

Design: reconcile phone metadata hourly as requested; reconcile templates after
changes and through incoming events, with polling as recovery. Deduplicate provider
message/status events; retain event history instead of overwriting blindly.

Direct current Meta overview/groups pages were login-gated or inaccessible.
Current throughput entitlements, group eligibility, calling/recording capabilities,
and full template/webhook policies remain unverified. Do not promise these features
or hard-code commonly quoted throughput values from third-party articles. Verify
against the account's current Meta documentation/capabilities before enabling them.

## Telegram

The [Bot API](https://core.telegram.org/bots/api#setwebhook) specifies HTTPS
webhooks, a secret-token header, retry delivery after unsuccessful responses, and
1–100 webhook connections (default 40). Long polling and webhooks are alternatives.
Updates supply chat IDs: retain them per bot/account and route replies with the
same channel context. Persist/deduplicate update IDs before acknowledging. Subscribe
explicitly to required reaction updates. Respect retry_after on flood-control errors.

The [official FAQ](https://core.telegram.org/bots/faq#my-bot-is-hitting-limits-how-do-i-avoid-this)
describes roughly 30 free broadcast messages/second, per-chat guidance of one/second,
and group limits of 20/minute. Paid broadcast capacity is a separate feature; never
enable paid sending automatically. More Hub workers cannot bypass provider limits.

Design: separate per-bot and per-chat budgets shared by all workers. Store the chat
identifier as a wide integer/string, not a manually entered phone number. Slash
commands and callbacks feed Core's flow engine; credentials stay in Hub.

## Email

Email has no single provider API. Start with authenticated SMTP submission and
IMAP reception, then add provider-specific APIs when an account requires them.

[SMTP RFC 5321](https://www.rfc-editor.org/rfc/rfc5321.html): distinguish transient
4xx from permanent 5xx failures. SMTP acceptance is not proof of inbox delivery or
reading. Track per-recipient results and ambiguous network failures after DATA;
blind retries can duplicate an already accepted message.

[TLS RFC 8314](https://www.rfc-editor.org/rfc/rfc8314.html): configure protected
submission/access (including implicit TLS submission on 465 and IMAP on 993).
Do not silently downgrade transport protection.

[IMAP RFC 9051](https://www.rfc-editor.org/info/rfc9051/): IDLE enables mailbox
change notifications. Reconnect and resynchronize using mailbox + UIDVALIDITY + UID;
sequence numbers are not durable identifiers. Fetch bounded pages and stream large
bodies/attachments. Capability negotiation is required for deployed servers.

[Message format RFC 5322](https://www.rfc-editor.org/rfc/rfc5322.html): preserve
Message-ID, In-Reply-To and References for conversation threading.
[Automatic replies RFC 3834](https://www.rfc-editor.org/rfc/rfc3834.html): use
Auto-Submitted semantics to prevent reply loops. Signatures belong in composed
plain-text/HTML MIME bodies, not transport headers.

## Adapter contract and validation to implement

Each adapter needs configuration validation, send, receive normalization, retry
classification and reconciliation. Not every channel supports templates, reactions,
read receipts or calling; expose capabilities rather than inventing equivalents.

Test fake-provider 429/5xx/timeouts, malformed signatures, duplicates, out-of-order
statuses, email partial-recipient rejection, IMAP reconnect/UIDVALIDITY changes,
and auto-reply loops before testing real accounts. These tests are not implemented
by the current health-only performance harness.

Keep incoming replies separate from bulk sends and status batches. Autoscaling
must consider runnable backlog, oldest-job age and resource headroom, while respecting
global database and provider budgets. Scale down by draining leased work; do not
terminate active deliveries and assume retries will always be safe.
