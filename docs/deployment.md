# Deploy Relay Hub

This guide installs the Hub on a Linux server as a systemd user service. Run every command as the user that owns the checkout, from the repository root.

## Requirements

- Go 1.26 or later. Install it from <https://go.dev/dl/> into `/usr/local/go`. The launcher also finds `/usr/local/go/bin/go` when it is not on `PATH`.
- Node.js 22 or 24 with npm, `git`, `curl` and systemd.
- A PostgreSQL 18 database and a login that owns it.
- An HTTPS reverse proxy for the public address. The Hub listens on loopback only.

## Fresh install

```sh
git clone -b develop git@github.com:andrometiq/relay_hub.git ~/services/relay_hub
cd ~/services/relay_hub
./relay setup
./relay create-admin
```

`./relay setup` does these steps:

1. Checks Go, Node.js and npm.
2. Asks for the PostgreSQL URL, or for host, port, database, user and password. It tests the connection and asks again when the test fails.
3. Writes the values to `.env` (mode 600). It generates missing secrets and asks for the public HTTPS address when it is missing.
4. Builds the Hub and the UI into a new release in `.runtime/releases`.
5. Applies database migrations.
6. Writes and enables the `relay-hub` systemd user unit, starts it and waits for `/readyz`.

Setup keeps every value that is already in `.env`. It only fills missing values, so you can run it again at any time. When the unit file is different from the expected unit, setup replaces it and keeps the old file as `relay-hub.service.bak`.

The service starts at boot only when linger is on for the user. Setup tries to turn it on. If it cannot, it prints this command for an administrator:

```sh
sudo loginctl enable-linger <user>
```

For scripts, set the values in the environment before setup, for example `DATABASE_URL`, `RELAY_PUBLIC_ORIGIN` and `RELAY_ADDR`. Setup saves them to `.env` and does not ask. `RELAY_SERVICE_NAME` changes the unit name (default `relay-hub`).

## Daily commands

| Command | Result |
| --- | --- |
| `./relay update` | `git pull --ff-only`, build, stop, migrate, start and wait for `/readyz`. It stops at the first failed step. If the build fails, the running Hub is not changed. If the migration fails, the old release starts again. |
| `./relay restart` | Build the Hub and the UI, then restart the service and wait for `/readyz`. If the build fails, the old Hub keeps running. If the new release does not become ready, the previous release starts again. |
| `./relay status` | Show the service state and the `/readyz` result. |
| `./relay logs` | Follow the service log (`journalctl --user -u relay-hub -f`). |
| `./relay stop` | Stop the service. Start it again with `./relay restart`. |

`./relay restart` does not apply migrations. Use `./relay update` or `./relay migrate` when the code has new migrations.

The unit runs the built binary `.runtime/current/relay-hub serve`. `.runtime/current` points to the active release. The launcher keeps the three newest releases.

## Back up `.env`

`.env` holds the database URL and the credential key. Without the credential key, saved channel credentials cannot be read. Keep a private copy after setup and after each change:

```sh
cp .env ~/relay-hub-env.backup && chmod 600 ~/relay-hub-env.backup
```

Also keep a copy off the server. Back up the database and `.env` together. See [operations](operations.md) for key rotation.

## Switch from the old service

An older install has a unit that runs `./relay restart` and `./relay stop`. To switch it to the new unit, run:

```sh
cd ~/services/relay_hub
cp .env ~/relay-hub-env.backup && chmod 600 ~/relay-hub-env.backup
git pull --ff-only
./relay setup
```

Setup builds first, then stops the old service, migrates, replaces the unit and starts the new one. The Hub is down only during the migration and the start.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| `Go 1.26+ is required` | Install Go into `/usr/local/go` or add it to `PATH`. |
| `database connection failed` | Host, port, user and password in `DATABASE_URL`. Test the server with `psql`. The Hub needs PostgreSQL 18. |
| `Failed to connect to bus` | Run as the service user in a login shell, for example `sudo -iu <user>`. Setup sets `XDG_RUNTIME_DIR` when it is empty. |
| `Database is not ready for this code` on restart | The code has new migrations. Run `./relay update`. |
| Hub does not become ready | `./relay logs`. Fix the error, then run `./relay restart`. |
| `./relay logs` shows no lines | The user journal is not kept on this server. Use `sudo journalctl _SYSTEMD_USER_UNIT=relay-hub.service -f`. |
| Hub does not start after reboot | `loginctl show-user <user> -p Linger` must show `yes`. |
| `Another Relay operation is running` | Wait for the other `./relay` command to finish. |
