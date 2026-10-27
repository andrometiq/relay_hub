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
| `RELAY_CREDENTIAL_KEY` | Required stable private material used to encrypt channel, container and site secrets. Back it up separately from the database. Use the offline `relay-keyrotate` command described in operations.md; do not replace the key before re-encrypting readable fields. |
| `RELAY_OUTBOX_WORKERS` | WhatsApp adaptive worker ceiling: default 4, maximum 8. This does not scale machines or remove provider limits. |
| `RELAY_BULK_UPDATE_WINDOW` | How long bulk send results and receipts wait to be coalesced before Core receives them, for example `30s`: default 30s, range 1s to 5m. A batch is sent earlier when 500 changes are pending. Replies never wait. |
| `GOMAXPROCS`, `GOFLAGS` | Launcher defaults are `2` and `-p=2` to bound local build/runtime concurrency. |
| `RELAY_WHATSAPP_CONFIG` | Optional private JSON account-registry path; empty disables this file source. Database-backed accounts are loaded separately. |
| `RELAY_TELEGRAM_WEBHOOK_SECRET` | Private master, at least 32 characters, for deriving a different ingress secret per Telegram bot. Never register the master with Telegram. |
| `RELAY_EMAIL_WEBHOOK_SECRET` | Server-only master of at least 32 bytes, used to derive a different ingress secret per mailbox. Never send the master. Get `secret_token` from `GET /api/accounts/{mailbox key}/email/webhook` and send it as `X-Relay-Email-Secret`. This is not SMTP authentication or a universal provider webhook verifier. |
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

## Containers, sites and channels

The Hub keeps three kinds of records:

| Record | What it is |
| --- | --- |
| Container | A group of provider apps and their channels, for example one Meta app. A container is not tied to a site. Its WhatsApp app holds `app_id`, `graph_version`, `app_secret`, `verify_token` and the system-user access token. |
| Channel | One WhatsApp number, Telegram bot or mailbox in a container. Its key (for example `sales`) is unique per provider. |
| Site | One Core (Frappe) site: an ID, a name, an origin and the three Core delivery credentials `core_api_key`, `core_api_secret` and `callback_secret`. |

A site uses one or more channels. A channel belongs to one site at a time, or to no site. Many sites can share one container, so one Meta app can serve numbers of different sites. Site origins may use HTTP or HTTPS, including local names and ports such as `http://yrp.site:8002`. Paths, embedded credentials, query strings and fragments are rejected. Browser Hub login still requires HTTPS.

Administrators edit these records on the **Containers** and **Sites** pages, or with the admin API in the [API reference](api-reference.md#administration-routes). Each change reloads the runtime at once. Browser mutations need the session cookie, the exact Origin and `X-Relay-CSRF`. The runtime checks the new configuration before it uses it; a request that is already running can finish with the old credentials.

| Channel | Main fields |
| --- | --- |
| WhatsApp | `phone_id`, `waba_id`, `display_number`; capability flags `templates`, `groups`, `calls`; optional `messages_per_second`. A number uses the container app and token by default. A number can override them with its own token and app fields. |
| Telegram | Bot token in the channel secret. Chat and topic IDs come from provider updates and Core bindings. Bot group permissions still apply. |
| Email | SMTP password in the channel secret; `host`, `port`, `username`, `from`. Port 465 selects implicit TLS; other ports require STARTTLS (normally submission port 587). Optional IMAP fields read the mailbox. |

A channel without a site still receives events. The Hub stores them and answers the provider with success, but sends them to no site. An administrator can replay them to the site that the channel has now with `POST /api/unassigned-events/replay`. A channel cannot move to another site while it has sends that are not finished. A send keeps the site it was queued for.

On Core, set the matching site ID, the callback secret and a Frappe service user with the required role. Keep Hub API tokens apart from provider credentials and Frappe API credentials. The private [WhatsApp JSON example](config-examples/whatsapp.accounts.example.json) documents the old file source. Prefer the [per-user token flow](authentication.md).

Older API clients can still use `/api/accounts`. It shows the simple setup where a site, its container and its channel keys all have the same ID, as one account with that ID.

### Credential storage and grants

The channel token or password, the container app secrets and the site Core credentials are encrypted with AES-GCM. Reads return only public fields and `configured_secrets` names. A blank secret in an edit keeps the stored value. Unknown configuration keys are rejected. Keep the same `RELAY_CREDENTIAL_KEY` and a secure backup of the database and the key. Key rotation is a separate operation.

Grants and API tokens belong to a site. Grant choices read the current sites on each request, so a new site shows without a restart.

## Webhook registration

Each public webhook address has a random path ID. The ID does not contain a name or a key, so the address does not show which business it serves. Copy the address from the admin UI.

| Channel | Hub route | Verification |
| --- | --- | --- |
| WhatsApp | `GET` and `POST /webhooks/whatsapp/{container path ID}` | GET verification token; POST raw-body `X-Hub-Signature-256` with the container app secret. The Hub routes each change to the number by its phone number ID. |
| Telegram | `POST /webhooks/telegram/{bot path ID}` | `X-Telegram-Bot-Api-Secret-Token`, checked against the secret derived for that bot. |
| Email | `POST /webhooks/email/{mailbox path ID}` | `X-Relay-Email-Secret` for Relay's normalized JSON ingress. Provider adapters must normalize and authenticate mail events first. |

An unknown path gets 404. One WhatsApp address serves all numbers of a container, in all WABAs of its Meta app.

Containers, bots and mailboxes that existed before random paths also answer on their old readable key path, so live provider settings keep working. After you change the provider to the new address, click **Turn off old address** on the container or channel, or call `DELETE /api/containers/{container}/old-address` or `DELETE /api/containers/{container}/channels/{channel}/{key}/old-address`. Then only the random path works. New containers and channels never get an old address.

The Hub registers each bot's webhook with Telegram when you save or check the bot. To do it by hand, get `{path, secret_token}` from `GET /api/accounts/{bot key}/telegram/webhook` and use [Telegram `setWebhook`](https://core.telegram.org/bots/api#setwebhook). Rotating `RELAY_TELEGRAM_WEBHOOK_SECRET` changes all derived tokens and requires new registration. The response is `no-store` and does not show the master or the bot token. The Hub stores and deduplicates each update before it answers success.

See the [provider matrix](provider-support.md) for ingress limits and [operations](operations.md) for troubleshooting.

The Hub admin UI receives change notices over authenticated Socket.IO at `/socket.io/` (or `/hub/socket.io/` behind the prefix proxy), using WebSocket only. Proxies must forward WebSocket upgrades and keep Host and Origin. The Go listener uses PostgreSQL LISTEN/NOTIFY; the UI reads the endpoints again on a notice and on reconnect, without polling. Apply new numbered Hub migrations (`./relay migrate`) before you start a new build, then rebuild the frontend and restart the Hub.

## Webhook forwarding

The Hub can send a copy of each provider webhook to other URLs, for example an older system that must keep working while the Hub is the main receiver. Set the targets on the container page: the **Forward copies to** card on the WhatsApp tab serves the container's Meta webhook, and the dialog of a Telegram bot or mailbox serves that channel. A scope has at most 5 targets.

| Rule | Behavior |
| --- | --- |
| What is forwarded | Only a POST that passed the signature or secret check and was stored in the event journal. WhatsApp changes of unknown numbers are forwarded too. GET verification and rejected requests are not forwarded. |
| What is sent | A POST to the URL exactly as configured, with the same body bytes and only these headers: `Content-Type`, `User-Agent`, `X-Hub-Signature-256`, `X-Hub-Signature`, `X-Telegram-Bot-Api-Secret-Token`, `X-Relay-Email-Secret`. A target that knows the Meta app secret can check the signature. |
| Timing | The Hub answers the provider first. A background worker sends the copy with a 10 second timeout. Each target gets its copies one at a time, in the order received. |
| Retries | Up to 3 tries: at once, after 30 seconds and after 2 more minutes. A 2xx answer is delivered. Redirects are not followed and count as a failure. |
| Results | Each copy records its result, HTTP status, time, tries and a short error. Records are deleted after 7 days. Removing a target deletes its records. |
| URLs | `https` for any host. `http` only for `localhost`, loopback, private and Tailscale (`100.64.0.0/10`) IP addresses. No user name or password in the URL. |

Without targets the Hub behaves as before. The stored copy holds the provider headers, including the Telegram and email secret tokens, until it is pruned; the API and logs never show bodies or header values.
