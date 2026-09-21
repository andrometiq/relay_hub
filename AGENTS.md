# Relay Hub workflow

- Make changes only on `develop` or a feature branch based on `develop`.
- Push development work to `develop`; do not commit to, push to, or merge into `main`.
- The user handles merges into production `main`.
- Work in this repository, not in the existing installed WhatsApp Hub.
- Keep the Hub runtime in Go; business logic and AI belong to Relay Core.
- Never commit credentials, site configurations or production data.
