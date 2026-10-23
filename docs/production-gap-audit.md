# Relay production gap audit — 2026-09-22

> Historical checkpoint. Claims and test results below describe that stage of development. Use the [current support matrix](provider-support.md), [API reference](api-reference.md) and [testing guide](testing.md) for current behavior.

The Hub now has PostgreSQL-backed authentication, account/channel credential storage, encrypted secrets, admin-only account APIs, a normalized operational audit trail, indexed session/queue reads, WhatsApp provider primitives, durable outbox/events, and a communication-focused Overview. This does **not** make the complete multi-channel product production-ready.

## Blocking gaps

- Saved credentials are loaded into the live provider registry after UI save through the authenticated reload endpoint; atomic account-scoped replacement is covered locally. Rollback and provider-health telemetry remain deployment work.
- Telegram adapter now has account-scoped Bot API send, verified webhook parsing, durable inbound events, Core callbacks, and automation dispatch. Long-poll fallback, rate-limit telemetry, and real-provider verification remain.
- Email adapter now has account-scoped SMTP send, CC/BCC validation, durable event/callback plumbing, and automation dispatch. IMAP receive, threading, attachments, and real-provider verification remain.
- Credential rotation and recovery still need the key rotation command/procedure and tests for preserving unreadable legacy entries. The schema now carries credential versions, verification timestamps and last errors.
- Channels UI currently displays connection metadata; provider health and last sync are not yet wired to live account records.
- WhatsApp template sync, hourly profile sync, calls/recording and provider callbacks need real Meta credentials and end-to-end tests.
- Production deployment still needs TLS/reverse proxy, service manager restart/recovery, backup/restore, alerting, rate-limit/load tests and migration rehearsal.

## Current verification

`go test ./...`, the production frontend build, the frontend validation tests, all fourteen PostgreSQL 18 migrations, Hub readiness, and the isolated real Frappe/PostgreSQL automation suite pass locally. The admin audit endpoint is wired with bounded cursor pagination, and the Events view reads it without exposing secrets. No real provider credentials or customer data were used.

Do not use the current branch as a complete production release until every blocking gap has an owner, implementation, and passing integration test. The database connection is an internal Hub dependency; it must not be presented as a communication channel or configuration feature in the UI.
