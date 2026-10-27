# Relay Hub database and authentication model

The Hub owns its PostgreSQL 18 schema. `./relay migrate` applies numbered,
checksum-protected migrations under one transaction. Startup refuses a mismatched
migration ledger or an unavailable database; it does not detect every possible
catalog change. Channel tokens/passwords and private configuration fields are
encrypted with `RELAY_CREDENTIAL_KEY`; public `config` JSON contains only allowed
non-secret provider settings. See [credential storage](configuration.md#credential-storage-and-grants).

## Operational tables

| Table | Purpose and important fields |
| --- | --- |
| `relay_hub_metadata` | Installation metadata: `key text` primary key, `value text`, `updated_at timestamptz`. |
| `relay_accounts` | One linked Frappe site identity. `id bigserial`, `account_id text` (stable external name), `site text` (HTTP or HTTPS origin), `enabled boolean`, `status text` (`active`, `disabled`, `error`, `pending`), `config_version bigint`, runtime error/sync fields, and created/updated timestamps. |
| `relay_channel_credentials` | One row per account/channel. `channel` is `WhatsApp`, `Telegram`, or `Email`; `config jsonb` contains public provider settings; `config_ciphertext bytea` and `config_nonce bytea` contain encrypted private settings; `secret_ciphertext bytea` and `secret_nonce bytea` contain AES-GCM material; `credential_version bigint`, verification/error fields, and `updated_at`. A unique `(account_id, channel)` prevents duplicates. |
| `relay_whatsapp_events` | Durable provider event journal and callback replay source: identity `id bigint`, account, provider digest, payload `jsonb`, lane, and created time. `(account,digest)` is idempotent. |
| `relay_whatsapp_incoming` | Durable inbound message journal: identity, account, provider message id, sender, payload, and created time. `(account,provider_id)` is idempotent. |
| `relay_whatsapp_status` | Latest monotonic status per provider message: account, provider id, status, rank, provider timestamp, and update time. |
| `relay_outbox` | Delivery state machine: identity, account/request key, payload, state (`queued`, `submitting`, `submitted`, `uncertain`, `cancelled`, `rejected`), result/error and timestamps. `(account,request_key)` prevents duplicate sends. |
| `relay_callback_cursor` | Per-account/lane callback checkpoint with `last_id`, failure count, and `next_attempt`. |
| `relay_channel_callback_cursor` | Telegram/email callback progress keyed by `(account, channel)`, with `last_id bigint`, `failures integer` and `next_attempt timestamptz`. |
| `relay_account_send_rate` | Per-account pacing cursor (`next_at timestamptz`). |

All high-volume journals and worker queues use bigint identities, JSONB payloads,
idempotency keys, and account/time indexes. No random string is used as the
primary key for a message or queue row.

## Authentication and sessions

| Table | Purpose and important fields |
| --- | --- |
| `relay_users` | Hub browser identities: bigint identity, unique username, PBKDF2 salt/hash, role (`admin`/`member`), enabled flag, auth-version and creation time. Passwords and raw tokens are never stored. |
| `relay_user_accounts` | Account grants keyed by `(user_id, account_id)`. |
| `relay_api_tokens` | Hashed `relay_` token, owning user/account, scopes array, expiry, revocation and creation times. Tokens are account-scoped. |
| `relay_auth_sessions` | Hashed browser session token, user/auth version, creation/last-seen/expiry timestamps and bounded user agent. Sessions expire after the configured absolute/idle windows and are invalidated by auth-version changes. |
| `relay_auth_audit` | Compatibility security audit with actor, action, target and timestamp; migration 0011 adds optional account and JSON metadata. |
| `relay_operational_audit` | Normalized append-only operational trail. It records actor type/id, account/channel, action, outcome, request/trace ids, source address, user agent, safe metadata and error codes/messages. Outcomes are `accepted`, `completed`, `rejected`, `failed`, or `uncertain`. |
| `relay_channel_events` | Deduplicated Telegram/email event journal. It stores account, channel, direction, provider id, sender/recipient, subject/body, occurrence time, and provider payload with account/channel/time indexes. |

Login success, bootstrap, user/grant/password/token changes are written to both
the compatibility auth audit and the normalized operational audit in the same
transaction. Rejected login attempts are recorded as anonymous operational
events without retaining the supplied username or password. The admin-only
`GET /api/audit` endpoint provides bounded, cursor-based operational history;
it never returns credential ciphertext or source secrets.

## Deployment checks

1. Set `DATABASE_URL` to the dedicated PostgreSQL 18 database and keep it out
   of source control.
2. Set a stable, high-entropy `RELAY_CREDENTIAL_KEY` (mode `0600` in `.env`).
   No automatic key-rotation command is implemented. Rotation needs a separately
   tested decrypt/re-encrypt procedure and preservation of unreadable legacy rows;
   merely replacing the key makes old ciphertext unreadable.
3. Run `./relay migrate`, then `./relay doctor`, before `./relay restart`.
4. Back up PostgreSQL before any manual retention operation. The Hub does not
   delete journals automatically; retention is an explicit operator action.
