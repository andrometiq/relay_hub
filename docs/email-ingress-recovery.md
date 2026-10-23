# Email ingress and recovery

Email webhook credentials are now account-scoped HMAC derivations. Set
`RELAY_EMAIL_WEBHOOK_SECRET` to a server-only master of at least 32 bytes, then
retrieve the account credential as a Hub admin using
`GET /api/accounts/{account}/email/webhook`. Send its `secret_token` in
`X-Relay-Email-Secret`. The master and another account's token are rejected.
Existing senders using the shared master must switch to the derived token.

The JSON envelope retains `id` and optional `occurred_at`. Supply `raw` with the
RFC email source to obtain parsed thread, recipient and authentication metadata.
Legacy `sender`, `recipient`, `subject`, `body` and recognized payload metadata
remain supported; their sender authentication is always `unverified`. Supplied
payload authentication and delivery-status claims are discarded. Event identity
is account-scoped and retries with the same `id` do not create new work.

## Trusted receiving edge

Account configuration may set `trusted_authserv_id`. Enable it only for an
administratively trusted receiving server that removes forged local
Authentication-Results fields and prepends its own result above its Received
field. Raw webhook publishers must forward the source from that receiving edge;
a raw source supplied directly by an Internet sender is not a trusted transport.
IMAP TLS authenticates the mailbox server, not the message's From address.

Hub accepts one Authentication-Results field above the first Received boundary,
with an exact configured authserv-id and a matching Received `by` server. It
recognizes a strict `dmarc=RESULT header.from=DOMAIN` clause and requires that
domain to match the single parsed From address. Missing, ambiguous, unsupported
or misplaced evidence stays `unverified`; explicit trusted failures stay `fail`.
It does not infer sender authentication from SPF alone, display names, or
arbitrary payload flags. The edge's stripping contract remains essential; see
[RFC 8601's trust boundary requirements](https://www.rfc-editor.org/rfc/rfc8601.html).

## Recipient delivery evidence

Additionally configure `trusted_dsn_domain` to the exact From domain of an
approved reporting provider. Only a DMARC pass from the trusted edge for that
domain can produce delivery evidence. Generic Internet bounce mail is retained
as incoming mail without delivery authority. Reports from other legitimate
providers need an explicitly supported trust configuration before they can
update delivery status.

Hub parses bounded `multipart/report; report-type=delivery-status` messages,
including each Final-Recipient, Action and enhanced Status code. Recipient
addresses are bounded to 254 bytes, including after normalization; oversized
trusted reports are rejected and checkpointed so following mail can proceed. Correlation
requires the original Message-ID from returned headers or the DSN's
Original-Message-ID field, an account-scoped outbound receipt, and an exact
normalized envelope recipient. Ambiguous message IDs and unrelated recipients
do not publish statuses. Relayed/expanded actions are not delivery proof. See
[RFC 3464](https://www.rfc-editor.org/rfc/rfc3464.html).

Migration 0032 stores evidence independently of the SMTP submission result.
Evidence arriving before that result commits remains pending. The existing
signed `receive_channel_statuses` callback publishes recipient receipts with
`state` equal to `delayed`, `bounced` or `delivered`, and receipt fields
`message_id`, `recipient`, `delivery_status` and `status_code`. Core must handle
these as recipient delivery evidence, separately from submission outcomes.
The outbox's SMTP submission state is unchanged; no report resends mail.

For each account/message/recipient, delivery evidence outranks bounce evidence,
which outranks delay. This deterministic order favors positive delivery proof
when reports conflict. Duplicates and lower-ranked late reports produce no new
callback work. Projection and callback acknowledgement are durable and retain
the existing bounded, per-row retry path. Legacy outbound rows lacking a known
envelope destination remain unmatched rather than guessing recipient ownership.

## Mailbox scheduling

Mailboxes share the bounded callback-worker scheduler: eight workers, at most
one active poll per account in a scheduler, plus a database mailbox lock across
replicas. Production uses a dedicated pool of eight mailbox connections; stalled
IMAP I/O cannot consume the three connections in the shared request pool. Each page reads at most 20 bodies, searching at most 10,000 UIDs to
skip gaps. Backlogs are eligible again on the next one-second scheduler tick;
idle mailboxes wait 30 seconds. Failures persist exponential reconnect backoff
from two seconds up to 256 seconds using the shared retry policy. Successful
polls clear it. Shutdown/timeout closes active connections.

Migration 0033 persists retry schedules and content identities. Before rewinding
local ingestion cursors, it seeds identities from existing mailbox events. Legacy
messages without raw hashes are correlated against their stored headers, body,
and attachment bytes, scoped to account and mailbox. This prevents acknowledged
mail from becoming fresh callbacks if UIDVALIDITY changes before the rescan
finishes. The rescan fills raw hashes under the existing event identities.

Every accepted body or explicit rejection commits with its UID checkpoint. A
truncated network read leaves the UID pending. UIDVALIDITY changes checkpoint
the new epoch and its initial UIDNEXT before fetching bodies, including for empty
mailboxes. Historical matching applies only below that persisted boundary, across
pages and reconnects. Later identical arrivals receive new events. Within the
reset snapshot, exact-content occurrences map back to prior events, preserving
distinct identical copies and distinguishing changed content even when Message-ID
is reused. Attachments keep their original event identity. Raw-hash correlation
assumes the server preserves message bytes when rebuilding a mailbox; legacy
correlation uses the content retained in the event and attachment stores.

## Local validation

Tests exercise account-isolated ingress through PostgreSQL and signed HTTP
callbacks, recipient outcomes, early evidence, replay, reversed transitions,
failed ACK/restart behavior, concurrent projection of 1,003 statuses, competing
mailbox schedulers across 13 accounts, reconnect backoff, backlog draining,
UIDVALIDITY changes and identical-message occurrences. Populated upgrade tests
assert no callback replay before or midway through identity seeding. Pool tests
stall eight mailboxes while all three shared request connections remain usable. Real TLS IMAP fixtures
cover read-only paging, interrupted literals, reconnect, reset checkpoints, sink
failure, oversized authenticated DSNs followed by valid reports, and TLS rejection. All database tests use isolated schemas in the approved local test
database; no live provider, production database or Core site is mutated.

These checks do not establish connected Core/browser acceptance, subsecond
mailbox arrival latency, or compatibility with every provider's authentication
header format. Provider push/IMAP IDLE and Core UI/projection work remain outside
this Hub change.
