# Configuration reference

The launcher and Go commands read the repository's private `.env`. See [`.env.example`](../.env.example) for the supported shape and the [quickstart](quickstart.md) for a complete Docker example.

Values are literal `KEY=value` lines, with optional surrounding quotes. There is no shell evaluation, interpolation or multiline syntax. Unknown and duplicate keys fail. An environment variable, including an explicitly empty one, overrides the file. Keep `.env` private with mode `0600`; Git ignores it and `.runtime/`.

## Runtime settings

| Key | Meaning |
| --- | --- |
| `DATABASE_URL` | PostgreSQL 18 connection string. Required by standalone Go commands; the launcher supplies a private native-cluster default when blank. Docker mode requires an explicit URL. |
| `RELAY_DATABASE_MODE` | `local` (default) or `docker`. A nonempty external URL in local mode uses the existing database. |
| `RELAY_ADDR` | Literal loopback bind address, default `127.0.0.1:18081`. Public access uses an HTTPS proxy. |
| `RELAY_PUBLIC_ORIGIN` | Exact HTTPS browser origin, without path, query, fragment or embedded credentials. Local development also accepts HTTP on localhost, 127.0.0.1 or ::1 only. Required for browser authentication. |
| `RELAY_PUBLIC_PREFIX` | Empty for a dedicated domain; `/hub` is the supported proxy mount. The proxy strips the prefix and preserves Host. |
| `RELAY_CREDENTIAL_KEY` | Required stable private material used to encrypt channel `Secret` values. Back it up separately from the database. Use the offline `relay-keyrotate` command described in operations.md; do not replace the key before re-encrypting readable fields. |
| `RELAY_OUTBOX_WORKERS` | WhatsApp adaptive worker ceiling: default 4, maximum 8. This does not scale machines or remove provider limits. |
| `GOMAXPROCS`, `GOFLAGS` | Launcher defaults are `2` and `-p=2` to bound local build/runtime concurrency. |
| `RELAY_WHATSAPP_CONFIG` | Optional private JSON account-registry path; empty disables this file source. Database-backed accounts are loaded separately. |
| `RELAY_TELEGRAM_WEBHOOK_SECRET` | Private master, at least 32 characters, for deriving a different ingress secret per Telegram account. Never register the master with Telegram. |
| `RELAY_EMAIL_WEBHOOK_SECRET` | Server-only master of at least 32 bytes, used to derive a different ingress secret per email account. Never send the master. Retrieve `secret_token` from `GET /api/accounts/{account}/email/webhook` and send it as `X-Relay-Email-Secret`. This is not SMTP authentication or a universal provider webhook verifier. |
| `RELAY_MCP_TOKEN` | Legacy fixture configuration only. The running service accepts database-backed user API tokens instead. |

`RELAY_REVIEW_ORIGIN`, `RELAY_REVIEW_USER`, `RELAY_REVIEW_SALT` and `RELAY_REVIEW_PASSWORD_HASH` belong to the separate [review harness](review-access.md), not Hub account authentication. `./relay config` displays nonsensitive effective startup settings.

## Database modes

| Mode | Configuration and behavior |
| --- | --- |
| Docker | Set `RELAY_DATABASE_MODE=docker`, `RELAY_DB_PORT` (default 18433), `POSTGRES_PASSWORD`, `CHANNEL_DB_PASSWORD` and explicit `DATABASE_URL`. The [Compose file](../compose.yaml) uses PostgreSQL 18, a persistent named volume and loopback TCP. |
| Native private cluster | Leave `DATABASE_URL` blank and use local mode. Install PostgreSQL 18 `initdb`, `pg_ctl`, `psql` and `createdb`. Run as a regular user. The launcher uses `.runtime/postgres`, a private Unix socket, peer authentication and no TCP listener. `RELAY_PG_PORT` defaults to 18432. |
| Existing server | Use local mode and a nonempty `DATABASE_URL` for a dedicated PostgreSQL 18 database. Relay checks the server but does not initialize or start it. Provision database ownership/migration rights separately and require TLS for remote access. |

For the native mode, standalone commands such as administrator bootstrap need the launcher-derived DSN explicitly:

```sh
export DATABASE_URL="host=$PWD/.runtime/pgsocket port=18432 user=$(id -un) dbname=relay_hub sslmode=disable"
```

Adjust the port if you changed `RELAY_PG_PORT`. A running native cluster retains its startup port until stopped. Docker initialization scripts run only for a new volume; changing `.env` does not rotate existing database passwords. Preserve the volume when rotating credentials through PostgreSQL.

## Accounts and channel credentials

Each account links one site and has at most one credential record per channel. Account IDs are stable external identifiers; database rows use sequential numeric identities. Site origins may use HTTP or HTTPS, including local names and ports such as `http://yrp.site:8002`. Paths, embedded credentials, query strings and fragments are rejected. Browser Hub login still requires HTTPS.

Administrators save account/channel settings through **Accounts** or `POST /api/accounts`, then explicitly activate them with `POST /api/accounts/reload`. Browser mutations need the session cookie, exact Origin and `X-Relay-CSRF`. Runtime replacement validates configuration before publishing it; an in-flight request may finish with the prior credential snapshot.

| Channel | Main runtime fields |
| --- | --- |
| WhatsApp | Access token in the channel secret; `phone_id`, `waba_id`, `graph_version`, `app_secret`, `verify_token`; capability flags `templates`, `groups`, `calls`; optional `messages_per_second`. |
| Telegram | Bot token in the channel secret; chat/topic IDs come from provider updates and Core bindings. Bot group permissions still apply. |
| Email | SMTP password in the channel secret; `host`, `port`, `username`, `from`. Port 465 selects implicit TLS; other configured ports require STARTTLS (normally submission port 587). |

Core callbacks use `core_api_key`, `core_api_secret` and `callback_secret` together where configured. On Core, configure the matching Hub account and callback secret plus a Frappe service user with the required role. Keep Hub API tokens distinct from provider credentials and Frappe API credentials.

The private [WhatsApp JSON example](config-examples/whatsapp.accounts.example.json) documents the legacy file source. Its `core_token` field is not accepted as a running Hub MCP token. Prefer the [per-user token flow](authentication.md).

### Credential storage and grants

The primary channel token/password and private configuration fields are AES-GCM
encrypted. Private fields include Meta app/verification secrets and Frappe callback
API credentials/signing secrets. Account reads return only public configuration
and `configured_secrets` indicators. Blank secret edits preserve stored values.
Unknown configuration keys are rejected instead of being stored as plain JSON.

Migration 0018 adds encrypted configuration columns. Startup then upgrades legacy
private JSON fields transactionally, including disabled channels. Preserve the
existing `RELAY_CREDENTIAL_KEY`; a failed decrypt aborts the upgrade. Keep a secure
database/key backup before migration. Key rotation is a separate operation.

Administrator grant choices query current accounts on each request. New WhatsApp,
Telegram-only, and email-only accounts appear without restarting the service.

## Webhook registration

| Channel | Hub route | Verification |
| --- | --- | --- |
| WhatsApp | `GET` and `POST /webhooks/whatsapp/{account}` | GET verification token; POST raw-body `X-Hub-Signature-256` and account phone/WABA scope. |
| Telegram | `POST /webhooks/telegram/{account}` | `X-Telegram-Bot-Api-Secret-Token`, checked against the per-account derived secret. |
| Email | `POST /webhooks/email/{account}` | `X-Relay-Email-Secret` for Relay's normalized JSON ingress. Provider-specific adapters must normalize and authenticate upstream mail events. |

For Telegram, an authenticated Hub administrator retrieves `{path, secret_token}` from `GET /api/accounts/{account}/telegram/webhook`. Register the public HTTPS URL and that derived token using [Telegram `setWebhook`](https://core.telegram.org/bots/api#setwebhook). Request the required update types, including reaction updates when needed. Registration is an operator/provider configuration step; the Hub does not create a bot.

Re-register bots that used the old shared secret. Rotating `RELAY_TELEGRAM_WEBHOOK_SECRET` changes all derived tokens and requires re-registration. The response is `no-store` and does not expose the master or bot token. Inbound updates are persisted and deduplicated before success is acknowledged.

See the [provider matrix](provider-support.md) for ingress limits and [operations](operations.md) for troubleshooting.

The Hub admin UI receives account invalidations over authenticated Socket.IO at
`/socket.io/` (or `/hub/socket.io/` behind the prefix proxy), using WebSocket only.
Proxies must forward WebSocket upgrades and preserve Host and Origin. The Go
listener uses PostgreSQL LISTEN/NOTIFY; the UI re-reads authorized endpoints on
invalidation and reconnect, without polling. Apply new numbered Hub migrations
before starting this build, then rebuild the frontend and restart the Hub.
