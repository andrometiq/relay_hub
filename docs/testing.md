# Test Relay Hub

Use synthetic accounts and local provider simulators. The standard tests do not require a live Meta account, bot or mailbox. Database integration tests require an explicitly selected development database and create disposable schemas; never point them at production.

## Focused checks

From the Hub root:

```sh
GOMAXPROCS=2 go test -p=2 ./...
GOMAXPROCS=2 go vet ./...
node --test frontend/tests/*.test.mjs
git diff --check
```

Go database tests skip when `RELAY_TEST_DATABASE_URL` is unset. A green run with those skips does not establish database integration. Go race checks additionally need the platform's supported C toolchain.

For this checkout's native private development database, enable database/race tests explicitly:

```sh
RELAY_TEST_DATABASE_URL="host=$PWD/.runtime/pgsocket port=18432 user=$(id -un) dbname=relay_hub sslmode=disable" \
  GOMAXPROCS=2 go test -race -p=2 -count=1 ./...
```

For Docker or another isolated server, provide its development DSN securely instead. Keep passwords out of command history. Successful Go output has `ok` package results and no failures; review skips separately. This command validates fixtures and persistence, not live provider delivery.

Build the UI after frontend changes:

```sh
./relay build-ui
```

A frontend production build or component test does not establish installed Core permissions, callback persistence or real browser/provider interoperability.

## Combined Hub and Core simulation

[scripts/simulate.py](../scripts/simulate.py) runs Hub race/database tests, vet, Core unit tests, real isolated Frappe scenarios, frontend data tests and whitespace checks. It writes a machine-readable report under `.runtime/` and fails if Hub integration tests skip.

This is an existing local test-harness command, not a portable blank-machine installer. It requires:

- The sibling `frappe_relay_core` checkout, or explicit `--core`.
- A Frappe Python environment supplied with `--frappe-python`.
- The prepared `relay-ai-test.local` site under Core's `.runtime/automation-bench/sites`, or explicit `--sites`.
- The dedicated `relay_ai_test_20260922` PostgreSQL database on `127.0.0.1:18433` and Redis configuration on `127.0.0.1:18089`. The runner validates those boundaries and refuses other database targets.

For the existing workspace layout:

```sh
python3 scripts/simulate.py \
  --frappe-python ../pilot/benches/version16-dev/env/bin/python
```

The runner prints `RUN` and `PASS`/`FAIL` for each check, then the report path. Success requires exit code 0 and `"passed": true` in `.runtime/simulation-report.json`. It strips provider-key environment variables, uses bounded concurrency, and can start/stop its own isolated Redis process. It does not load the Hub's production `.env` or send provider messages.

For measured totals, skips and scenario descriptions, use [simulation-validation.md](simulation-validation.md). Do not copy those counts into new guides; a changed test suite can make an old total misleading.

## What to validate

| Area | Evidence |
| --- | --- |
| Authentication and tenants | PostgreSQL-backed user, session, grant, token, scope, revocation and CSRF tests |
| Persistence and recovery | Migration rollback/checksums, idempotent queue admission, worker recovery, uncertain outcomes, webhook deduplication and callback acknowledgement |
| Group routing | [Group simulations](group-simulation-coverage.md): account, group, participant, topic and original message identity remain distinct |
| Rich messages | Adapter exact-payload tests, [WhatsApp rich-message cases](whatsapp-rich-message-simulation.md), typed Telegram messages, signed inbound events and permission-checked sticker retrieval |
| Resources | Size/deadline bounds and [fixture scaling measurements](scaling-validation.md), with workload limitations stated |

Keep test failures attached to their feature until corrected. Separate fixture success from the later operator-run acceptance using configured live accounts; live tests can send messages and are not part of the simulation command.
