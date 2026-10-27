# Relay Hub

Relay Hub is the Go transport service for [Relay Core](https://github.com/andrometiq/frappe_relay_core), a Frappe app. One Hub serves multiple accounts; each account links one Core site to its WhatsApp, Telegram and email connections.

The Hub provides a Frappe-style administration UI, PostgreSQL-backed users and sessions, account-scoped MCP tools, provider adapters, event journals and Core callbacks. Core owns contacts, business permissions, conversations and AI automation.

**Start with the [quickstart](docs/quickstart.md).** It covers PostgreSQL 18, private configuration, the first administrator, HTTPS browser access and a readiness check. Installation alone does not provision provider accounts or prove live delivery.

| Task | Documentation |
| --- | --- |
| Use the admin pages (owner and administrators) | [User guide](docs/user-guide.md) |
| Understand the system | [Architecture](docs/architecture.md) |
| Install and configure | [Quickstart](docs/quickstart.md), [configuration](docs/configuration.md) |
| Deploy a server, update or restart | [Deployment](docs/deployment.md) |
| Manage users and tokens | [Authentication](docs/authentication.md) |
| Integrate an application | [MCP and message reference](docs/api-reference.md) |
| Check channel support | [Provider support matrix](docs/provider-support.md) |
| Build, restart or migrate | [Operations](docs/operations.md) |
| Run local simulations | [Testing](docs/testing.md) |
| Browse everything | [Documentation index](docs/README.md) |

On a server, `./relay setup` installs the Hub as a systemd user service. After that, the daily commands are:

```sh
./relay update    # pull, build, migrate and restart
./relay restart   # rebuild and restart
./relay logs
```

The connected release checkpoint passes 10/10 WhatsApp, 11/11 Telegram and 13/13 email cases. All three channels support durable sends and incoming callbacks. Telegram reports bot acceptance; email reports SMTP acceptance and trusted DSNs. Core updates use Socket.IO. See [provider support](docs/provider-support.md) for channel limits and [release evidence](docs/simulation-validation.md) for measurements and rerun commands.

The drivers use local fake providers with real Hub/Core services. They do not test browser rendering, live provider eligibility or production capacity. Bulk campaigns are outside this release evidence.

These documents describe the current local working tree, including changes that may not yet be pushed to GitHub. Development takes place on `develop`. The repository owner merges production changes into `main`. Follow [AGENTS.md](AGENTS.md) and keep credentials, local databases and generated reports out of Git.
