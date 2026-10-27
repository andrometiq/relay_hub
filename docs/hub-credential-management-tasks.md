# Hub credential management tasks

> Historical checkpoint. Claims and test results below describe that stage of development. Use the [current support matrix](provider-support.md), [API reference](api-reference.md) and [testing guide](testing.md) for current behavior.

1. Persist Relay accounts, one site per account, and channel credential records with encrypted secrets.
2. Add authenticated, CSRF-protected account/channel APIs with admin-only writes and account-scoped reads.
3. Add WhatsApp, Telegram and Email setup forms; validate provider-specific fields and never return secrets.
4. Load enabled accounts into provider adapters without restart, with atomic replacement and safe rollback.
5. Add Telegram webhook/polling and email delivery/receive adapters after credential persistence.
6. Test duplicate accounts, cross-account access, invalid credentials, encryption failure, secret redaction, and reload rollback.

Current slice: tasks 1–3 plus PostgreSQL migrations 0011–0012, authentication/session audit, and the admin operational-events view. Provider runtime reload and Telegram/email transport remain next tasks.

The current UI now has **Accounts** setup with WhatsApp access token, Telegram bot token, and Email credential fields. `GET /api/accounts` returns metadata only; `POST /api/accounts` requires the authenticated administrator session and same-origin request. Secrets are encrypted using `RELAY_CREDENTIAL_KEY` and are never returned. Existing `.env` files must keep a stable private key before restart; the local development environment has one provisioned with mode `0600`.
