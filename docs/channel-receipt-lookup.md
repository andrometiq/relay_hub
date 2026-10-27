# Telegram/email receipt lookup

`channels.deliveries` now accepts an optional `request_key` for a single account-scoped receipt. The authenticated credential fixes the account; messages:read is required. Key lookup cannot be combined with pagination or cancellation. Missing keys return an empty deliveries array. A found receipt includes id, key, operation, state and stored result, without the original payload or credentials.

The query uses the existing UNIQUE(account,request_key) index and does not scan journal pages. It performs no provider call, dispatch, retry or journal mutation. Existing list and cancellation behavior remains available when request_key is omitted.

This is a prerequisite for Telegram/email campaign recovery after lost responses. Core campaigns remain WhatsApp-only until provider-specific preparation, dispatch, cancellation, history projection and UI are implemented and verified. Do not advertise completed multi-channel campaigns on the strength of this change.

Verification: the guarded PostgreSQL TestDurableChannelOutbox passed with Go race detection, including submitted lookup, uncertain lookup without replay, missing/other-account isolation and invalid key rejection. Hub MCP package tests compile and pass; a new authenticated HTTP lookup integration test remains to be added. This source change has not yet replaced the running Hub binary.
