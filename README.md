# Relay Hub

Go-based communication transport for WhatsApp, Telegram and email. One Hub serves multiple Relay Core sites; each channel account belongs to exactly one site.

## Status

Repository foundation and proposed architecture. No provider adapters, live API, database schema or production service have been implemented yet.

## Responsibilities

- Receive and deliver channel messages through account-scoped adapters.
- Durably accept bulk submissions and dispatch through bounded workers.
- Forward inbound replies and reactions promptly; batch delivery-status callbacks.
- Synchronize WhatsApp templates and channel metadata.
- Support calls and recordings where provider capabilities permit.

Relay Core is the separate Frappe application. It owns business records, user permissions, inbox UI, flows and AI services. Core-to-Hub commands will use MCP; provider webhooks and authenticated callbacks handle events.

## Planned stack

Go, PostgreSQL durable inbox/outbox, official MCP Go SDK, and private media storage. Dependencies will be selected and pinned as implementation starts.

See [architecture](docs/architecture.md) for service boundaries, delivery semantics and milestones.
