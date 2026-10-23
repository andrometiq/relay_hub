# WhatsApp functional implementation — local validation checkpoint

> Historical checkpoint. Claims and test results below describe that stage of development. Use the [current support matrix](provider-support.md), [API reference](api-reference.md) and [testing guide](testing.md) for current behavior.

> Superseded contracts: manual participant addition and invite revocation were removed. Use invitation-based membership and invite get/reset. Legacy service tokens no longer authenticate the running Hub.

All changes remain local on `develop`. Core is not installed on a Frappe site and
no live Meta account is configured. This is not a production-readiness claim.

## Implemented

Hub account-scoped MCP tools:

- `whatsapp.execute`: capabilities, account profile, event cursors/latest events,
  contact-specific incoming history, text/reply/reaction/media messages,
  template list/create/update/delete/send, group list/create/info/update/delete,
  invite creation/revocation, participant addition/removal, join-request
  listing/approval/rejection, call-permission query/request and call signaling.
- `whatsapp.enqueue`: atomic batches of 1–50 message intents. Each caller supplies
  a stable request key. An identical retry returns the existing delivery; reusing
  a key with different contents fails. Queues cap at 10,000 active intents/account.
- `whatsapp.deliveries`: account-scoped, cursor-paged results and cancellation of
  queued intents. Core exposes this account-wide view only to managers.

The durable outbox uses numeric identity keys and PostgreSQL `SKIP LOCKED` claims.
An adaptive worker scheduler runs between one worker and `RELAY_OUTBOX_WORKERS`
(default 4, maximum 8), with a bounded connection pool. The idle minimum performs
recovery checks. This is worker concurrency scaling, not Kubernetes/VM autoscaling.
A shared PostgreSQL pacing gate caps sends per account across replicas; configure
`messages_per_second` in the private account registry (default 50, maximum 1000).
Actual throughput depends on provider latency and account eligibility; the setting
is an upper bound, not a throughput guarantee.

Accepted, rejected, and uncertain provider outcomes are distinct. Lost responses
are not blindly retried. Signed status callbacks carrying the outbox correlation
value can reconcile uncertain delivery results without resending. Interrupted
workers become uncertain after two minutes. Explicit provider rejections remain
rejected; operator reconciliation is required before resubmission.

Webhooks verify the raw-body signature and WABA/phone scope, then commit raw
payloads, status projections and incoming-message history together before 200.
Four webhook bodies can be processed concurrently; overload returns 503 for retry.
Read/delivered states do not regress on older callbacks. Message history has
account/contact filters, numeric cursor ordering, forward/reverse pages and a
response-byte budget. Historical raw events from before this projection are not
backfilled automatically.

Optional Hub→Core callbacks have separate realtime and status lanes. Replies and
calls cannot be held behind a status lane that is retrying. Status events batch up
to 50 with a byte budget. Both lanes retain their cursor until Core acknowledges
its database transaction. Failures back off, without deleting stored events.
Core validates the timestamp, HMAC, account, authenticated service role and event
shape. `Relay Hub Event` uses sequential names and a unique external event key.
Repeated callbacks are idempotent. These events are durably stored; full native
`Relay Message`/business-record materialization is a separate remaining task.

The Core WhatsApp view provides Messages, Templates, Groups, Calls and Deliveries:

- Select one of several Contacts linked to a business record; recipient numbers
  are resolved and permission-checked server-side. A contact may belong to multiple
  business records. Select several contacts for a template batch.
- Queue text replies and image/video/audio/document/sticker messages by HTTPS URL.
  Queued requests retain retry keys across a failed submission/page reload.
- Show incoming messages, choose a message to reply to, and load earlier pages.
  The rendered conversation window is bounded to 200 incoming messages.
- Create/edit body templates, fill numbered variables and send approved templates.
- Create groups, edit subject/description, manage invites, add a linked contact,
  remove participants, approve/reject join requests, and send group text.
- Check/request call permission; connect, answer, decline and end browser calls.
  A connection is called connected only after the WebRTC connection event.
- Record the browser's local/remote audio mix and download it. Recording stops at
  approximately 32 MiB; it is not a server recording/archive or transcription service.
- View queued/submitted/rejected/uncertain/cancelled delivery intents and provider
  delivery/read states. Cancel only while an intent is still queued.

## Configuration

Copy `docs/config-examples/whatsapp.accounts.example.json` to the ignored
`whatsapp.accounts.json`, replace placeholders and chmod 600. Set
`RELAY_WHATSAPP_CONFIG=whatsapp.accounts.json` in `.env`. Keep account tokens distinct
from the service health token. Set each account's Graph version explicitly.
Group/calling flags remain off until provider account eligibility is established.

For callbacks, provide all three private registry fields together:
`core_api_key`, `core_api_secret`, `callback_secret`. The API user must have
`Relay Hub Service` (or System Manager). Configure the same `relay_callback_secret`
and `relay_hub_account` in the Core site's configuration. Core also needs
`relay_hub_url` and the account-scoped `relay_hub_token`. No callback credentials
have been generated or applied to a real Frappe site during this work.

Run `./relay migrate`, then `./relay restart` after schema/source changes.
Docker PostgreSQL remains loopback-only; `.env` remains private and ignored.

## Validation

- Go tests with race detection and real, isolated PostgreSQL test schemas:
  migration replay, account isolation, duplicate batches/events, concurrent claims,
  cancellation, interrupted-worker recovery and receipt reconciliation; HMAC
  callbacks, retry acknowledgement, independent lanes and status batches; numeric
  history ordering and forward/reverse cursors; a 100-message fixture burst that
  scales worker concurrency up and back down.
- Python boundary tests: permission checks, multiple contacts, trusted number
  resolution, batch rejection, account binding, callback tampering/replay, plus
  a real local MCP handshake/health check. Frappe methods themselves use mocked
  framework boundaries in these tests.
- Browser fixtures: second-contact selection, media submission, two-recipient
  template batch, nested group-list shape, settings/invite actions and five tabs
  without horizontal overflow at 375/768/1440 pixels. Synthetic WebRTC/audio
  exercised permission→connect→record→download→end; generated a nonempty WebM.
  This does not establish Meta audio interoperability or audio quality.

## Remaining release gates / scope gaps

No real messages, provider mutations or calls were made. Live Graph contracts,
provider policies/limits, public HTTPS webhook delivery and actual Frappe installation
remain unverified. Group ownership checks inspect at most 1,000 active groups and
fail closed beyond that bound.

Full template header/button/media authoring, direct media upload/download,
interactive/catalog/location/contact payload UIs, group photo uploads, outgoing
conversation-history projection, persistent per-agent unread/assignment state,
concurrent-agent call ownership, TURN/SIP interoperability, server recording,
automated hourly account/template reconciliation and full Hub account onboarding
UI remain unfinished. Telegram/email adapters, AI/flow services and other preview
screens are outside this WhatsApp checkpoint and are not being claimed complete.

## Provider references

Meta's documentation website returned 429 for groups/calling. Its official
[OpenAPI specification](https://github.com/facebook/openapi/blob/main/business-messaging-api_v23.0.yaml)
was accessible and used to verify group response shape, management paths and
calling permissions/signaling. Its filename is v23.0; this does not certify every
configured Graph version or live account capability.

[Meta message collection](https://www.postman.com/meta/whatsapp-business-platform/folder/o48mro7/messages)
and [template collection](https://www.postman.com/meta/whatsapp-business-platform/folder/lczy75a/templates)
were also consulted.
