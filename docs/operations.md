# Operate Relay Hub

Run the launcher from this checkout. It uses an operation lock to prevent overlapping lifecycle commands and only stops processes it recognizes as belonging to this installation.

## Commands

For a server, use the systemd service from [deployment](deployment.md). When that unit is installed, `restart`, `status`, `stop` and `logs` act on the service. Without it, they act on a process that this checkout started.

| Command | Result |
| --- | --- |
| `./relay setup` | Configure `.env`, build, migrate and start the Hub as a systemd user service. See [deployment](deployment.md). |
| `./relay update` | `git pull --ff-only`, build, stop, migrate, start and check readiness. Needs the service from setup. |
| `./relay create-admin` | Ask for the first administrator's username and password and create the user. |
| `./relay logs` | Follow the service log, or `.runtime/hub.log` without the service. |
| `./relay install` | Check prerequisites, load/create local configuration, start the selected database, fetch dependencies, apply migrations and build the UI. Existing `.env` values are preserved. |
| `./relay build-ui` | Run locked npm installation and the production frontend build. Restart to activate the new UI snapshot. |
| `./relay migrate` | Start/check the configured database, compile the runner and apply pending numbered SQL migrations. |
| `./relay restart` | Build Go/UI, check database/schema, stop this checkout's Hub, launch its release snapshot, then check readiness. Restore the prior release if the new one fails. |
| `./relay doctor` | Compile the checker and verify PostgreSQL connectivity and supported major version. |
| `./relay status` | Check this checkout's recorded process and readiness. |
| `./relay config` | Display configuration location, address, local database port/mode without secrets. |
| `./relay stop` | Stop the Hub process; keep database data and services. |
| `./relay db-up` | Start the selected local database. |
| `./relay db-status` | Show Docker Compose database status; this command is Docker-specific. |

The Makefile aliases `install`, `build-ui`, `restart`, `migrate`, `doctor`, `status`, `stop` and `test`. There is no `./relay serve` command; `serve` is the compiled Go binary's entry point.

Restart has a brief interruption. It does not perform a zero-downtime deployment, apply pending migrations or scale host replicas. Database migrations must finish before restarting code that requires them.

## Schema changes and backups

Add a new sequential SQL file under [migrations](../internal/database/migrations). Never rewrite an applied migration. The runner uses a transaction, advisory lock and checksum ledger; changed checksums, unknown migrations and unsupported PostgreSQL major versions fail. All pending migrations in one run commit together or roll back.

Use additive migrations and a reviewed data transition for incompatible changes. There is no reset/down command or automatic database major upgrade. Readiness validates the expected migration ledger, not every database catalog object.

Back up the database and its encryption key together under separate access controls, and verify restoration in an isolated environment. The Hub does not automatically delete event journals. The service launcher keeps the three newest releases. Use the offline credential-key rotation workflow below. Replacing the key without re-encrypting readable rows is not a rotation.

## Logs and diagnostics

The private `.runtime/` directory contains release snapshots, process metadata and Hub logs. Use `./relay status` first, then inspect the current launcher log locally. Logs and callback payloads may contain communication data; do not publish runtime directories or credentials in an issue.

`/healthz` checks the process. `/readyz` checks database access and migration readiness. The authenticated `GET /api/audit` provides bounded, cursor-based operational history; delivery and channel-event APIs are account-filtered. A healthy probe does not establish successful provider delivery.

| Symptom | Check |
| --- | --- |
| Service refuses startup | `./relay config`, PostgreSQL 18 availability, explicit Docker DSN, required credential key and HTTPS origin. |
| Pending or changed migration | Run `./relay migrate` for new files. Restore an accidentally edited historical file; never bypass checksum checks. |
| Login loops or invalid host/origin | Browser uses the configured HTTPS origin, proxy preserves Host, mount prefix matches, and secure cookies are enabled. |
| Token call rejected | Token is unexpired, user enabled, account grant present, required scope granted; browser Origin/cookies are not MCP credentials. |
| Saved credentials not active | Save then reload account runtime. See the separate grant-list limitation in [configuration](configuration.md). |
| Provider timeout or `uncertain` intent | Inspect stored receipt/status and reconcile; do not resend with a new key merely because the response was lost. |
| Telegram webhook rejected | Use the account's derived token, not the master or another bot's token; re-register after master rotation. |
| Existing Docker password rejected | Changing `.env` does not change initialized PostgreSQL roles. Rotate the role password explicitly and retain the volume. |

## Stop a local database

For Docker, `docker compose stop postgres` stops the service without deleting its volume. For a native private cluster:

```sh
pg_ctl -D "$PWD/.runtime/postgres" -m fast -w stop
```

Do not remove volumes or `.runtime/postgres` as a troubleshooting step. Use a dedicated database/service manager and documented backup/recovery procedures for deployment; this Linux checkout launcher is primarily a local development workflow.

See [testing](testing.md) before a release and [scaling validation](scaling-validation.md) for the limited scope of measured workloads.

## Offline credential-key rotation

`go run ./cmd/relay-keyrotate` previews rotation using `DATABASE_URL`,
`RELAY_CREDENTIAL_KEY` (current key) and `RELAY_NEW_CREDENTIAL_KEY` from the process
environment. Keys are never accepted as command-line arguments or printed.
The preview reports row/field counts and identifiers of unreadable fields without
changing ciphertext.

Stop the Hub and any credential writers before applying:

```sh
go run ./cmd/relay-keyrotate --apply --service-stopped
```

After a confirmed commit, replace the service's `RELAY_CREDENTIAL_KEY` with the
new value and start the Hub. Retain the old key with the pre-rotation recovery
material until startup and channel decryption are verified. If a commit response
is lost, run the preview again with the same old/new key pair: it recognizes
fields already using the new key. The operation uses one database transaction,
locks credential writes, and processes rows in bounded batches. Unreadable fields
remain byte-for-byte intact and are reported for separate repair; readable fields
in the same row are still rotated. This does not rotate provider tokens or the
Telegram webhook master secret.

Tests cover old/new decryption, replay, dry-run immutability, private-config
rotation, and retention of unreadable fields in an isolated PostgreSQL schema.
No running-site key has been rotated as part of these tests.

## Isolated restore rehearsal

Run `scripts/rehearse_restore.py` with the Frappe environment Python, `--site-config`
pointing to the isolated `relay-ai-test.local/site_config.json`, and `--report`
pointing to a private runtime report. It refuses other database/host/port combinations.
It creates a uniquely named synthetic schema, dumps 1,000 queued/submitted records,
drops only that schema, restores the archive transactionally, and verifies all row
content and identity-sequence continuation. Temporary archives and the schema are
removed afterwards. The measured result is in `.runtime/restore-rehearsal.json`.

This proves the local PostgreSQL dump/restore path for synthetic relational data.
It does not prove full Frappe site restore, private-file recovery, encryption-key
recovery, production RPO/RTO, or backup retention. Those remain separate gates.
