# WhatsApp rich message simulation

The simulation sends only to local fixtures and uses synthetic records in a disposable PostgreSQL schema. The metadata and download URLs remain the normal Meta-shaped URLs in the client; an injected test transport redirects those requests to local servers and refuses every other destination.

## Supported operations

| Operation | Input | Coverage |
| --- | --- | --- |
| `messages.location` | Typed `location` object with numeric `latitude`, `longitude`, optional `name`/`address`; optional `reply_to` | Zero coordinates, named Unicode locations, missing/nonfinite/out-of-range coordinates, exact provider JSON, quoted context, durable queue and replay |
| `messages.contacts` | Typed `contacts` array: names, phones, emails, addresses, organisation, URLs, birthday; optional `reply_to` | All contact fields, Unicode, invalid dates/phones/emails/URLs/country codes, cardinality and payload bounds, exact JSON and queue replay |
| `messages.react` | `reply_to` target message ID and `emoji`; empty emoji removes the reaction | Set/clear bodies, target validation, size bound, provider rejection without blind retry, durable replay |
| `messages.media` / `groups.media` | `media_type: sticker`, one `media_id` or `media_url`; optional `reply_to` | Direct/group sticker payloads and quoted context; unsupported caption/filename denied |
| `whatsapp.media` | `media_id` received as a sticker on the authenticated account | Account evidence, scoped metadata lookup, bounded authenticated binary download, exact bytes/MIME and integrity checks |

Incoming stickers, locations, contacts, reactions and quote context retain their original structured metadata in the immutable event and message payload. Reaction removal can omit the emoji property; the simulated database round trip preserves that omission. Core separately applies the reaction to its target message instead of using it as a new AI prompt.

Local limits are explicit: contact messages contain at most 257 cards, each repeated contact field has at most 20 entries, and the normal provider request body remains bounded to 64 KiB. Location labels are bounded to 256 characters and addresses to 1024. The proxy currently serves incoming WebP stickers only, at most 2 MiB; this is not a general file download endpoint.

The download must match an indexed, account-owned incoming sticker. Metadata lookup includes `phone_number_id`; the returned media ID, MIME, declared size and supplied SHA-256 are validated. The bearer token is sent only to the exact `lookaside.fbsbx.com` HTTPS host and `/whatsapp_business/attachments/` path. Userinfo, custom ports, alternate paths/hosts and redirects are rejected. The response must also be WebP with a matching size/digest, when provided. Provider URLs, tokens and error bodies do not reach the browser. This restrictive host policy fails closed if Meta changes its download endpoint.

## Provider boundaries

Meta's [Groups API](https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/) currently lists text, media, text-based templates and media-based templates as supported group message types. Group reactions, locations and contact cards are not affirmed in that supported list. Relay rejects `groups.react`, `groups.location` and `groups.contacts` locally; it never silently turns them into direct messages.

Official references inspected on 2026-09-23:

- [Location messages](https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/location-messages/)
- [Contacts messages](https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/contacts-messages/)
- [Reaction messages](https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/reaction-messages/)
- [Reaction webhook including removal](https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/reference/messages/reaction/)
- [Sticker messages](https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/sticker-messages/)
- [Media retrieval](https://developers.facebook.com/documentation/business-messaging/whatsapp/reference/media/media-api/)
- [Media management and download](https://developers.facebook.com/documentation/business-messaging/whatsapp/business-phone-numbers/media/)

Tests: `TestRichMessagePayloadSimulation`, `TestRichMessageValidationSimulation`, `TestRichMessageProviderRejectionDoesNotRetry`, `TestRichMessagesDurableDispatchAndIncomingSimulation`, `TestStickerDownloadSimulation`, and `TestStickerDownloadRequiresAccountEvidence`.

These simulations prove the local adapter, persistence, limits and routing behavior. They do not prove live delivery, recipient-client display, Meta account eligibility or a currently valid provider media URL.
