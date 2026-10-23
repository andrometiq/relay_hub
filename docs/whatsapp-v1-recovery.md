# WhatsApp v1 delivery and recovery

The Hub server uses database account configuration exclusively. An empty enabled
set stays empty; `RELAY_WHATSAPP_CONFIG` is not a startup fallback. Existing
configuration files are not imported automatically. API tokens require an enabled
account. Saving an account changes only the channels present in the request;
disable a channel explicitly with `enabled: false`.

WhatsApp v1 exposes individual sends and templates. `whatsapp.bulk` and
`whatsapp.enqueue` are unregistered; `groups.*` and `calls.*` are rejected by the
MCP boundary, including keyed sends. Stored capability settings are preserved,
while the server's runtime disables groups and calls.

## Send outcomes

Outbox state changes to submitted, rejected, uncertain or cancelled atomically
insert an immutable `relay_whatsapp_events` row. The existing signed
`/api/method/relay_core.inbound.receive` callback carries the row in `events`:

```json
{
  "id": 123,
  "payload": {
    "object": "relay_delivery",
    "delivery": {
      "channel": "WhatsApp",
      "id": "42",
      "request_key": "core-action-key",
      "state": "submitted",
      "outcome": "accepted",
      "provider_message_id": "wamid.example",
      "error": "",
      "provider_error": null
    }
  }
}
```

Core must durably ingest each event before returning
`{"message":{"accepted":N}}`. Deduplicate on account and event ID; correlate the
outgoing action by `delivery.request_key`. `outcome` is accepted, rejected or
uncertain (cancelled maps to rejected); accepted means provider submission, not
customer delivery. A later receipt or operator resolution can follow an uncertain
outcome with accepted/rejected. Provider IDs are null when unavailable. Existing
terminal deliveries are not backfilled as new send outcomes by this migration.

Network failures and wrong acknowledgement counts retain event identities for
retry, including after restart. The Hub tests use a signed HTTP Core fixture;
real Core projection of this envelope must be implemented/verified in Core.

HTTP 429, explicit transient errors, and throughput/pair-limit codes 130429 and
131056 retry up to eight attempts. Backoff starts at one second and doubles;
`Retry-After` is honored up to one hour. The queue exposes attempts, next attempt,
and numeric HTTP/code/subcode/transient/retry metadata, excluding provider text.
Unknown submission results are never automatically retried. Missing configuration,
pacing database errors and upload failures defer before message submission;
validation errors reject explicitly. A durable submission checkpoint distinguishes
safe crash recovery from a request that may have reached Meta.

Claims rotate between accounts, exclude paced accounts, and keep each account's
recipient queue in ID order. Queued, in-flight and uncertain predecessors block
that recipient until resolved; other recipients and accounts can proceed.

## Callback lanes and recovery

Each immutable event has independent acknowledgement state for its required
lanes. The legacy cursor is diagnostic only. Upgrading schedules existing events
again with their original identities, recovering potentially skipped commits;
Core must retain its event/receipt deduplication. Mixed envelopes deliver messages
without statuses on the realtime lane and compact statuses separately, bounded
to 500 receipts and 512 KiB per status array. The original envelope remains in
`raw_payload`; account-filtered projections remain in `payload`.

Shared webhooks authenticate each active destination against the original bytes.
Phone changes route only to their authenticated phone account; WABA notifications
fan out to authenticated accounts sharing that WABA. Disabled, unconfigured and
unauthenticated destinations are omitted from projections, even when only one
active destination remains. An envelope with no authenticated active destination
returns 403. The raw journal retains the full envelope for accepted destinations.
Retries after partial persistence deduplicate each projection. Phone-specific
messages/statuses/calls require phone metadata.

Malformed status events record the raw receipt position, failure count and next
retry time. Their backoff does not block later events or the realtime lane.
Each dispatch continues past malformed events and delivers valid receipts in
the same page, including those collected before a failure.
Administrators can inspect failures with:

```
GET /api/callback-failures?account=ACCOUNT&channel=WhatsApp
```

When `more` is true, pass the last item's ID as `before` for the next page.
Pages contain up to 51 rows so both failed lanes of an event stay together.

Use the existing browser session, Origin and X-Relay-CSRF protections to submit:

```json
{
  "account": "ACCOUNT",
  "channel": "WhatsApp",
  "id": "123",
  "lane": "status",
  "action": "quarantine",
  "note": "Verified malformed receipt; incident reference"
}
```

The endpoint is `POST /api/callback-failures/retry`; `action: "replay"` reschedules
that event from its acknowledged offset. Both actions require a 10–2000 character
note and journal the authenticated actor, event, lane and time. Quarantine retains
the original data. Replay does not repair malformed data or force acceptance;
it will fail again until the decoder or receiving service can handle the event.
Event inspection shows Pending, Retrying, Quarantined or Forwarded from per-event
state. No periodic browser polling or new realtime transport is introduced.

Stop older Hub workers before applying migrations 0028 and 0029 and starting
this version; mixed-version outbox workers are not supported. The migrations were
exercised only in disposable schemas in the dedicated local test database, not
applied to the running Hub or any Frappe site.
