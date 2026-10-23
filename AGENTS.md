# Relay Hub workflow

- Make changes only on `develop`.
- Push development work to `develop`; do not commit to, push to, or merge into `main`.
- The user handles merges into production `main`.
- Work in this repository, not in the existing installed WhatsApp Hub.
- Keep the Hub runtime in Go; business logic and AI belong to Relay Core.
- Never commit credentials, site configurations or production data.

- Validate the integrated behavior and relevant load/failure cases locally before pushing. UI builds and synthetic component tests alone are not release validation. Keep incomplete integration work local.
