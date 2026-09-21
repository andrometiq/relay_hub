# Setup validation — 21 September 2026

Test environment: Linux, Go 1.27.0, Node 24.20.0, npm 11.19.0, PostgreSQL 18.6. PostgreSQL cluster belongs only to this checkout and listens on its private Unix socket; shared databases and apps were not modified.

Passed:

- `./relay install`: tool checks, isolated PostgreSQL start, database creation, first migration, locked dependency installation and UI build.
- `./relay migrate` repeated: no duplicate schema/migration records.
- Four real PostgreSQL integration tests: repeat/readiness, checksum drift rejection, newer migration rejection, failed migration transaction rollback.
- Go compilation, `go vet ./...`, shell syntax and Git whitespace checks.
- Restart creates a versioned binary and matching UI snapshot; database remains running.
- Detached Hub remains reachable after launcher completion.
- Deliberately invalid frontend source causes restart to fail before stopping the old release; old PID and readiness remain intact. Source restored immediately afterward.
- Browser opens the served UI and reports Service Ready / PostgreSQL 18 / Channels Not configured.

Limitations: provider/MCP transport is not implemented in this foundation. Runtime crash after startup, automatic reboot recovery, production authentication, high availability and zero-downtime deployment are not claimed. Migration checks validate the applied file ledger, not every database catalog object. Failed-launch rollback is implemented but has not been fault-injected in this validation.
