# Relay Hub

Go-based transport foundation for Relay Core. This repository now provides dependency checks, a local PostgreSQL setup, versioned migrations, a Go HTTP server and a Frappe UI-based status page. Channel delivery, MCP operations and account administration are not implemented yet.

## Install and start

```sh
./relay install
./relay restart
```

Open http://127.0.0.1:18081. Installation checks Go **1.26+**, Node.js **22 or 24**, npm, curl, flock and setsid. The local database option requires PostgreSQL **18.x** server tools (`initdb`, `pg_ctl`, `psql`, `createdb`). Missing tools produce an actionable error; the installer does not run sudo or replace system packages. Use your OS package manager to install the required major versions, then rerun. Keep supported patch releases current.

Installation downloads locked Go/npm dependencies, starts a private PostgreSQL cluster under `.runtime/postgres`, creates the empty Hub database, applies migrations and builds the UI. PostgreSQL uses a private Unix socket and peer authentication, with TCP disabled. It does not touch the system PostgreSQL service or existing Frappe databases. Run as a regular user, not root. This local setup is for development.

## Three daily operations

| Command | Behavior |
|---|---|
| `./relay build-ui` | Install locked frontend dependencies and build the UI. Changes are activated by restart. |
| `./relay restart` | Build UI and Go before stopping the running Hub; check database/schema; gracefully stop only this checkout's process; launch a release with its own UI snapshot; verify readiness. Restore the old release if the new process fails. |
| `./relay migrate` | Compile the migration runner and apply pending SQL changes transactionally. Repeated runs are safe. Does not infer changes from Go structs or modify business records automatically. |

Equivalent commands: `make build-ui`, `make restart`, `make migrate`.

Additional commands: `./relay doctor`, `./relay status`, `./relay stop`. Stop leaves the local database running. To stop this checkout's database explicitly: `pg_ctl -D "$PWD/.runtime/postgres" -m fast -w stop`. No automatic deletion or database major-version upgrades.

## Database migrations

Add a new sequential SQL file in `internal/database/migrations/`. Never edit an already-applied migration. The runner uses a PostgreSQL advisory transaction lock and a checksum ledger. An unknown migration, changed checksum or wrong PostgreSQL major fails. All pending changes in one run commit together; failure rolls back the transaction. No down/reset command is provided. Use additive migrations and a separately reviewed data transition for incompatible production changes.

`restart` does not run migrations implicitly: pending schema changes require `./relay migrate`. The first install applies the initial schema. `/healthz` checks the process; `/readyz` verifies database access and expected migration checksums. This is version/checksum validation, not a full catalog drift detector.

For an existing managed PostgreSQL 18 server, export `DATABASE_URL` before commands. Relay will check it but will not initialize or start an external database. Provision a dedicated Hub database/role first; migrations require DDL rights. Avoid placing credentials in shell history or source control. `.env.example` documents settings; `.env` is not automatically sourced. External deployments should use TLS, dedicated runtime/migration roles and a service manager.

## Testing

```sh
go test ./...
# Against the isolated local Hub database (creates and removes only test schemas):
RELAY_TEST_DATABASE_URL="host=$PWD/.runtime/pgsocket port=18432 user=$(id -un) dbname=relay_hub sslmode=disable" go test ./... -v
go vet ./...
```

Integration tests cover migration reruns/readiness, edited checksums, newer schemas and rollback after failure. The test URL must refer to a development database, never production.

`.runtime/` contains local databases, release binaries/UI snapshots, PID and logs, and is ignored by Git. Release snapshots are retained for rollback; retention cleanup is not yet automatic. Restart has a brief interruption and is not a zero-downtime deployment mechanism. The shell launcher is Linux-specific (`/proc`, `flock`).

The status page is intentionally loopback-only until authentication is implemented. The database is checked at startup; no automatic provider connections or messages are sent.

## Development workflow

Work only on `develop`. The user merges into production `main`. See [architecture](docs/architecture.md).

Dependencies: [PostgreSQL supported versions](https://www.postgresql.org/support/versioning/), [pgx Go driver](https://github.com/jackc/pgx), [Frappe UI](https://github.com/frappe/frappe-ui).
