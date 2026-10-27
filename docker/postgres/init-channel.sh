#!/bin/sh
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<'SQL'
\getenv channel_password CHANNEL_DB_PASSWORD
CREATE ROLE channel_user LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD :'channel_password';
CREATE DATABASE channel_hub OWNER channel_user;
REVOKE ALL ON DATABASE channel_hub FROM PUBLIC;
GRANT CONNECT ON DATABASE channel_hub TO channel_user;
SQL
