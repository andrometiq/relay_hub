# Relay Hub documentation

Use the [quickstart](quickstart.md) for a new installation. The guides and references below describe the current code reviewed on **2026-09-23**. Dated reports retain the evidence and limitations of their own checkpoints.

## Guides

- [Quickstart](quickstart.md): prerequisites, PostgreSQL, configuration, first login and expected readiness output.
- [Authentication](authentication.md): users, sessions, account grants and API tokens.
- [Deployment](deployment.md): server setup, updates, restarts, logs and `.env` backup.
- [Operations](operations.md): UI builds, migrations, restarts, logs and troubleshooting.
- [Testing](testing.md): bounded local checks and integrated simulations.
- [Review access](review-access.md): the environment-specific review gateway and preview harness.

## References

- [Architecture](architecture.md): responsibilities, data flow and delivery guarantees.
- [Configuration](configuration.md): `.env`, database modes, channel settings and webhooks.
- [MCP and message API](api-reference.md): tools, typed payloads, replies, reactions and sticker retrieval.
- [Provider support](provider-support.md): implemented operations and explicit channel limits.
- [Database model](database-authentication-model.md): tables, field types and persistence rules.
- [UI components](ui-components.md) and [Frappe virtualization](frappe-virtualization.md): reusable controls and bounded rendering.

## Validation evidence

- [Combined simulation results](simulation-validation.md): reproducible command and recorded results.
- [Group simulation coverage](group-simulation-coverage.md): WhatsApp and Telegram group cases.
- [WhatsApp group details](whatsapp-group-simulation.md) and [rich-message details](whatsapp-rich-message-simulation.md): provider contracts and limits.
- [Scaling validation](scaling-validation.md) and [performance reports](performance/README.md): measured fixture workloads and their scope.

## Historical checkpoints

These documents explain earlier decisions or tests. They are not the installation instructions or current feature matrix:

- [Initial setup validation](setup-validation.md)
- [Initial UI progress](ui-progress.md)
- [Route and API audit](route-api-audit.md)
- [Credential implementation task list](hub-credential-management-tasks.md)
- [Production gap audit](production-gap-audit.md)
- [Initial provider research](provider-adapter-research.md)
- [Initial WhatsApp implementation checkpoint](whatsapp-implementation.md)

## Updating documentation

Update the relevant guide or reference with its code change. Keep measured results in dated reports, state skipped tests and distinguish a provider simulator from a live provider. Link to one canonical description instead of copying configuration or support tables into every report. Examples must use synthetic identities and no working credentials.

This layout follows Google's [documentation guide](https://google.github.io/styleguide/docguide/) and [best practices](https://google.github.io/styleguide/docguide/best_practices.html). The [GoogleTest quickstart](https://google.github.io/googletest/quickstart-cmake.html) provides the model for explicit prerequisites, runnable steps and expected output; Relay itself uses Go and Python tests, not GoogleTest. These references were checked on 2026-09-23.

- [Review configuration fixes and validation, 23 September](review-fixes-2026-09-23.md).
