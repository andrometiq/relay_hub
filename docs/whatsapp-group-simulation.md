# WhatsApp group simulation coverage

The test fixtures use synthetic accounts and local HTTP servers. Integration tests use a newly created schema in the isolated PostgreSQL test database and remove only that schema afterward. They do not send messages to Meta.

Provider contracts were checked on 2026-09-23 against Meta's official [group management](https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/reference), [group messaging](https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/groups-messaging), and [group webhooks](https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/webhooks) references. The documents were available as Markdown using a standard HTTP client when the browser fetch returned throttling errors.

| Behavior | Evidence |
| --- | --- |
| Create with approval-required or auto-approve; list, info, update, delete | `TestGroupLifecycleSimulation`, `TestGroupJoinModesAndRequestPagination` |
| Get/reset invite; reject unsupported add-participant or revoke-invite endpoints | Lifecycle, validation and ownership tests |
| List, approve/reject join requests; remove participants | Lifecycle simulation checks request payloads and resulting member/request state |
| Partial join-request and participant results remain explicit, without retry or all-success synthesis | `TestGroupPartialResultsRemainExplicit` |
| Text, image, video, audio, document, sticker, template, pin/unpin | Lifecycle payload comparison; integrated outbox for text, document, template and pin |
| Reply destination and quoted message context | `TestWhatsAppGroupAuthenticatedSimulation` |
| Profile picture upload with exact multipart bytes | `TestGroupPhotoSimulation`; authenticated MCP integration uses a JPEG whose base64 exceeds 1 MiB |
| Photo validation: encoding, malformed/truncated JPEG, size and dimensions | Photo validation subtests |
| Account capability, scope, ownership and foreign/deleted group rejection | Ownership/validation tests and authenticated integration |
| Short ownership pages with a cursor; opaque request pagination; repeated cursor; token-bearing paging URL removal | Pagination tests |
| HTTP 403, 429 and 503 are surfaced; no automatic mutation retry; provider details do not leak | `TestGroupPaginationAndProviderFailureSimulation` |
| Stable queue keys, replay and conflicting payloads; queued group dispatch | Authenticated MCP/PostgreSQL integration |
| Group participant identity, reply context, attachments, group lifecycle events | Signed webhook persistence and exact callback comparison |
| Group messages do not enter participant direct-message history; group history includes different participants | Authenticated PostgreSQL history comparison |
| Group sent/read/delivered receipts retain participant IDs and out-of-order statuses do not regress | Authenticated webhook/receipt simulation |

## Supported contract and limits

- Meta does not provide a manual add-participant operation. Participants use invite links, followed by approval when enabled. `groups.participants.add` is rejected locally.
- Meta provides invite **get/reset** operations. `groups.invite.get` and `groups.invite.reset` are canonical. The existing `groups.invite.create` name remains an alias for reset. `groups.invite.revoke` is rejected locally.
- `groups.photo` accepts `photo_base64`; it never reads a Hub filesystem path or fetches an arbitrary URL. JPEG uploads are limited to 5 MiB and square dimensions from 192 to 2048 pixels. The 2048-pixel ceiling is a local memory limit, not a Meta requirement. These bounds apply per request; this pass does not establish production concurrency capacity.
- Group ownership checks follow opaque cursors, including short pages, within the existing bounded lookup budget. If ownership cannot be established, the operation fails closed.
- `delivery_status` is the highest observed status for a message. For groups, a `read` status means at least one participant reported a read; it does **not** prove that all members read the message. Immutable webhook records retain `recipient_participant_id` and each reported status. A dedicated per-participant receipt UI is outside these simulations.
- Group creation/deletion/settings operations may complete asynchronously. Fixtures validate request acceptance and separate lifecycle webhook preservation; a returned request ID does not prove live completion.
- These tests do not prove Meta account eligibility, webhook subscription setup, real invite usability, provider media fetching, or live delivery. Those remain the user's configured-provider acceptance pass.
