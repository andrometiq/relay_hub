# Relay Hub

Relay Hub is the Go transport service for [Relay Core](https://github.com/andrometiq/frappe_relay_core), a Frappe app. One Hub serves multiple accounts; each account links one Core site to its WhatsApp, Telegram and email connections.

The Hub provides a Frappe-style administration UI, PostgreSQL-backed users and sessions, account-scoped MCP tools, provider adapters, event journals and Core callbacks. Core owns contacts, business permissions, conversations and AI automation.

**Start with the [quickstart](docs/quickstart.md).** It covers PostgreSQL 18, private configuration, the first administrator, HTTPS browser access and a readiness check. Installation alone does not provision provider accounts or prove live delivery.

| Task | Documentation |
| --- | --- |
| Understand the system | [Architecture](docs/architecture.md) |
| Install and configure | [Quickstart](docs/quickstart.md), [configuration](docs/configuration.md) |
| Manage users and tokens | [Authentication](docs/authentication.md) |
| Integrate an application | [MCP and message reference](docs/api-reference.md) |
| Check channel support | [Provider support matrix](docs/provider-support.md) |
| Build, restart or migrate | [Operations](docs/operations.md) |
| Run local simulations | [Testing](docs/testing.md) |
| Browse everything | [Documentation index](docs/README.md) |

After setup, the daily commands are:

```sh
./relay build-ui
./relay migrate
./relay restart
```

Local simulations exercise synthetic provider responses, authentication and isolated databases. They do not establish live-provider eligibility, recipient display or production capacity; see [validation results](docs/simulation-validation.md).

These documents describe the current local working tree, including changes that may not yet be pushed to GitHub. Development takes place on `develop`. The repository owner merges production changes into `main`. Follow [AGENTS.md](AGENTS.md) and keep credentials, local databases and generated reports out of Git.
