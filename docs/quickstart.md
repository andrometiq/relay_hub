# Run Relay Hub locally

This guide starts a new Hub with Docker PostgreSQL and no provider accounts. It does not install Relay Core into a Frappe site. Run commands from the repository root on `develop`.

## Prerequisites

- Linux with Bash, `curl`, `flock` and `setsid`.
- Go **1.26 or later**, Node.js **22 or 24**, and npm.
- Docker Engine with Docker Compose and permission to access its daemon.
- Python 3 for the configuration and secure password-input examples below.
- An HTTPS reverse proxy with a certificate trusted by your browser. The Hub binds to loopback; configure the proxy before browser login.

The launcher checks the build tools. It does not install operating-system packages. The Docker helper can use an already permitted `sudo -n docker` fallback; it does not ask for a password. Native PostgreSQL and externally managed databases are described in [configuration](configuration.md).

## 1. Create private configuration

For a **fresh checkout only**, replace `https://relay.example.com` with your HTTPS browser origin, then run:

```sh
python3 - <<'PY'
import os
import secrets

app_password = secrets.token_hex(24)
values = {
    "RELAY_DATABASE_MODE": "docker",
    "RELAY_DB_PORT": "18433",
    "POSTGRES_PASSWORD": secrets.token_hex(24),
    "CHANNEL_DB_PASSWORD": app_password,
    "DATABASE_URL": "postgresql://channel_user:" + app_password
                    + "@127.0.0.1:18433/channel_hub?sslmode=disable",
    "RELAY_ADDR": "127.0.0.1:18081",
    "RELAY_PUBLIC_ORIGIN": "https://relay.example.com",
    "RELAY_PUBLIC_PREFIX": "",
    "RELAY_CREDENTIAL_KEY": secrets.token_hex(32),
    "GOMAXPROCS": "2",
    "GOFLAGS": "-p=2",
}
fd = os.open(".env", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, "w") as config:
    config.write("# Private Relay Hub configuration. Never commit.\n")
    config.writelines(f"{key}={value}\n" for key, value in values.items())
print("Created private .env")
PY
```

An existing `.env` makes this example fail safely with `FileExistsError`. Edit the existing file instead of overwriting its database password or encryption key. The `.env` parser uses literal values; it does not expand `$VARIABLE` references. Environment variables take precedence over the file.

Keep a secure backup of `RELAY_CREDENTIAL_KEY`. Replacing it does not rotate stored ciphertext and can make credentials unreadable. A fresh installer-generated `.env` includes independent random encryption and webhook secrets. It preserves existing files unchanged. Configure the public HTTPS origin before starting authenticated Hub service.

## 2. Install and migrate

```sh
./relay install
```

This starts the [PostgreSQL 18 container](../compose.yaml), downloads locked dependencies, checks the database, applies migrations and builds the UI. The final launcher message is:

```text
Installed. Start with ./relay restart.
```

The container binds only `127.0.0.1:18433`, has a persistent volume, and is limited to one CPU and 256 MiB. Initialization creates `channel_user` owning `channel_hub`, without superuser privileges. Password initialization runs only on an empty volume; editing `.env` does not change an existing database password.

## 3. Create the first administrator

The server has no default username or password. Bootstrap reads two lines from stdin and refuses to run after any Hub user exists. This example prompts without putting the password in shell history or command arguments:

```sh
python3 - <<'PY'
import getpass
import subprocess

with open("/dev/tty") as terminal:
    print("Hub administrator username: ", end="", flush=True)
    username = terminal.readline().strip()
password = getpass.getpass("Hub administrator password: ")
subprocess.run(
    ["go", "run", "./cmd/relay-hub", "bootstrap-user"],
    input=username + "\n" + password + "\n", text=True, check=True,
)
PY
```

Expected output: `Initial Hub administrator created`. Use a password accepted by the [authentication policy](authentication.md). The explicit `DATABASE_URL` written in step 1 also works for this standalone Go command.

## 4. Start and check readiness

```sh
./relay restart
curl --fail --silent http://127.0.0.1:18081/readyz
```

The readiness response contains `"status":"ready"`, `"database":"PostgreSQL 18"` and the current release's `instance` identifier. A successful response proves database/schema readiness, not provider connectivity.

Proxy your configured HTTPS origin to `http://127.0.0.1:18081`, preserving the original `Host`. For a dedicated domain, leave `RELAY_PUBLIC_PREFIX` empty. If using the supported `/hub` mount, set that prefix and strip it before forwarding. Do not expose operational probes through the public proxy.

Open `<RELAY_PUBLIC_ORIGIN>/auth/login`, or `<RELAY_PUBLIC_ORIGIN>/hub/auth/login` when mounted at `/hub`. Use the administrator created above. HTTP-only browser access cannot establish the required secure session cookie.

## 5. Configure a channel

Create an account and channel credentials in **Accounts**, then apply the runtime reload. Account site URLs currently require an HTTPS origin without a path, query or embedded credentials. Grant the integration user access to that account before creating its API token. See [configuration](configuration.md) for channel fields, webhook setup and current onboarding limits.

Use the [MCP reference](api-reference.md) for Core integration. Run the [local simulations](testing.md) before a separately configured live-provider acceptance test.

To stop only this Hub process:

```sh
./relay stop
```

The database remains running and its data is preserved. See [operations](operations.md) for restart, migration and troubleshooting details.
